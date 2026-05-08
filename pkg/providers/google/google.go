// Package google implements the Google Gemini Generative Language provider.
package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/providers/internal/retryafter"
	providerstream "github.com/yuri/y/pkg/providers/internal/stream"
)

const (
	providerID        = "google"
	defaultBaseURL    = "https://generativelanguage.googleapis.com/v1beta"
	defaultModelID    = "gemini-2.5-flash"
	defaultMaxEvent   = 1 << 20
	maxErrorBodyBytes = 4 << 10
)

var toolIDCounter uint64

// Provider streams Google Gemini events as normalized AI events.
type Provider struct {
	httpClient  *http.Client
	baseURL     string
	apiKey      string
	envLookup   func(string) string
	maxEvent    int64
	middlewares []providers.Middleware
	retry       providers.RetryPolicy
	inspector   providers.RequestInspector
	dryRun      bool
}

// Option configures Provider.
type Option func(*Provider)

// WithHTTPClient sets the HTTP client. A nil client is ignored.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.httpClient = client
		}
	}
}

// WithBaseURL sets the Gemini API base URL.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) {
		if strings.TrimSpace(baseURL) != "" {
			p.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		}
	}
}

// WithAPIKey sets an explicit API key.
//
// API key precedence (uniform across all providers):
//  1. StreamRequest.Options.APIKey (per-request override) wins.
//  2. WithAPIKey constructor option wins next.
//  3. Provider env vars (GEMINI_API_KEY, GOOGLE_API_KEY) via the configured
//     WithEnvLookup.
func WithAPIKey(apiKey string) Option {
	return func(p *Provider) { p.apiKey = apiKey }
}

// WithEnvLookup overrides environment lookup for tests.
func WithEnvLookup(lookup func(string) string) Option {
	return func(p *Provider) {
		if lookup != nil {
			p.envLookup = lookup
		}
	}
}

// WithMaxEventBytes limits one decoded SSE event payload.
func WithMaxEventBytes(limit int64) Option {
	return func(p *Provider) {
		if limit > 0 {
			p.maxEvent = limit
		}
	}
}

// WithRetryPolicy sets the retry policy for transient HTTP failures.
func WithRetryPolicy(policy providers.RetryPolicy) Option {
	return func(p *Provider) { p.retry = policy }
}

// WithMiddleware appends a middleware to the HTTP transport stack.
func WithMiddleware(mw providers.Middleware) Option {
	return func(p *Provider) {
		if mw != nil {
			p.middlewares = append(p.middlewares, mw)
		}
	}
}

// WithRequestInspector installs a callback invoked with the fully-built
// http.Request immediately before it would be sent.
func WithRequestInspector(fn providers.RequestInspector) Option {
	return func(p *Provider) { p.inspector = fn }
}

// WithDryRun enables dry-run mode: the provider builds and inspects the
// request but does not send it, returning an empty synthetic stream.
func WithDryRun() Option {
	return func(p *Provider) { p.dryRun = true }
}

// New creates a Google Gemini provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		httpClient: http.DefaultClient,
		baseURL:    defaultBaseURL,
		envLookup:  os.Getenv,
		maxEvent:   defaultMaxEvent,
		retry:      providers.DefaultRetryPolicy(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return providerID }

// Models returns a built-in Google model list available without network. The
// list is generated from models.json by `go generate ./pkg/providers/...`.
func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := CuratedModels()
	for i := range out {
		out[i].BaseURL = p.baseURL
	}
	return out, nil
}

// Stream starts a streaming Gemini generateContent request.
func (p *Provider) Stream(ctx context.Context, req providers.StreamRequest) (stream providers.EventStream, err error) {
	if p == nil {
		p = New()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if req.Options.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
		defer func() {
			if err != nil {
				cancel()
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	apiKey := p.resolveAPIKey(req.Options.APIKey)
	if apiKey == "" {
		return nil, errors.New("google API key is required; set GEMINI_API_KEY or pass APIKey")
	}
	payload, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode google request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.streamURL(req.Model, apiKey), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Goog-Api-Key", apiKey)
	for key, value := range req.Options.Headers {
		if strings.TrimSpace(key) == "" || value == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}

	if p.inspector != nil {
		p.inspector(httpReq)
	}
	if p.dryRun {
		if cancel != nil {
			cancel()
		}
		return providers.SyntheticDryRunStream(), nil
	}

	client := p.client()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &providers.NetworkError{Provider: providerID, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		body := strings.TrimSpace(string(limited))
		return nil, providers.ClassifyHTTPError(providerID, resp.StatusCode, retryafter.Parse(resp.Header.Get("Retry-After")), body, nil)
	}

	state := &streamState{}
	return providerstream.New(resp.Body, p.maxEvent, cancel, "google_stream_read", state.consume), nil
}

// client returns the HTTP client with middleware applied.
func (p *Provider) client() *http.Client {
	return providers.ApplyCommonClient(p.httpClient, p.middlewares)
}

// CountTokens calls the Gemini countTokens endpoint when an API key is
// available, and falls back to providers.EstimateTokens otherwise.
func (p *Provider) CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p == nil {
		return providers.EstimateTokens(c), nil
	}
	apiKey := p.resolveAPIKey("")
	if apiKey == "" {
		return providers.EstimateTokens(c), nil
	}
	if modelID == "" {
		modelID = defaultModelID
	}
	streamReq := providers.StreamRequest{
		Model:   ai.Model{ID: modelID},
		Context: c,
	}
	payload, err := buildRequest(streamReq)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(struct {
		Contents          []contentParam `json:"contents"`
		SystemInstruction *contentParam  `json:"systemInstruction,omitempty"`
	}{Contents: payload.Contents, SystemInstruction: payload.SystemInstruction})
	if err != nil {
		return providers.EstimateTokens(c), nil
	}
	u, _ := url.Parse(p.baseURL + "/models/" + url.PathEscape(modelID) + ":countTokens")
	q := u.Query()
	q.Set("key", apiKey)
	u.RawQuery = q.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return providers.EstimateTokens(c), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Goog-Api-Key", apiKey)

	if p.inspector != nil {
		p.inspector(httpReq)
	}
	if p.dryRun {
		return providers.EstimateTokens(c), nil
	}
	resp, err := p.client().Do(httpReq)
	if err != nil {
		return providers.EstimateTokens(c), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return providers.EstimateTokens(c), nil
	}
	var out struct {
		TotalTokens int64 `json:"totalTokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return providers.EstimateTokens(c), nil
	}
	if out.TotalTokens <= 0 {
		return providers.EstimateTokens(c), nil
	}
	return out.TotalTokens, nil
}

// Capabilities returns the feature set supported by the named Gemini model.
// Gemini 1.5+ supports vision, tools, and reasoning via thinking budgets.
func (p *Provider) Capabilities(modelID string) providers.Capabilities {
	if strings.TrimSpace(modelID) == "" {
		return providers.Capabilities{}
	}
	caps := providers.Capabilities{
		Vision:    true,
		Tools:     true,
		JSONMode:  true,
		Streaming: true,
	}
	lc := strings.ToLower(modelID)
	if strings.Contains(lc, "2.5") || strings.Contains(lc, "thinking") {
		caps.Reasoning = true
	}
	return caps
}

// Close releases idle HTTP connections.
func (p *Provider) Close() error {
	if p == nil {
		return nil
	}
	if p.httpClient != nil {
		if t, ok := p.httpClient.Transport.(*http.Transport); ok && t != nil {
			t.CloseIdleConnections()
		}
	}
	return nil
}

func (p *Provider) resolveAPIKey(requestKey string) string {
	if requestKey != "" {
		return requestKey
	}
	if p != nil && p.apiKey != "" {
		return p.apiKey
	}
	lookup := os.Getenv
	if p != nil && p.envLookup != nil {
		lookup = p.envLookup
	}
	src := &auth.EnvSource{Lookup: lookup}
	key, _ := src.Resolve(context.Background(), providerID)
	return key
}

func (p *Provider) streamURL(model ai.Model, apiKey string) string {
	modelID := model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, _ := url.Parse(baseURL + "/models/" + url.PathEscape(modelID) + ":streamGenerateContent")
	q := u.Query()
	q.Set("alt", "sse")
	q.Set("key", apiKey)
	u.RawQuery = q.Encode()
	return u.String()
}

type generateRequest struct {
	SystemInstruction *contentParam     `json:"systemInstruction,omitempty"`
	Contents          []contentParam    `json:"contents"`
	Tools             []toolSetParam    `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int64    `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ThinkingConfig  *struct {
		ThinkingBudget int64 `json:"thinkingBudget,omitempty"`
	} `json:"thinkingConfig,omitempty"`
}

type contentParam struct {
	Role  string      `json:"role,omitempty"`
	Parts []partParam `json:"parts"`
}

type partParam struct {
	Text             string                 `json:"text,omitempty"`
	InlineData       *inlineDataParam       `json:"inlineData,omitempty"`
	FunctionCall     *functionCallParam     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponseParam `json:"functionResponse,omitempty"`
}

type inlineDataParam struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type functionCallParam struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponseParam struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type toolSetParam struct {
	FunctionDeclarations []functionDeclarationParam `json:"functionDeclarations,omitempty"`
}

type functionDeclarationParam struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildRequest(req providers.StreamRequest) (generateRequest, error) {
	out := generateRequest{}
	if req.Context.SystemPrompt != "" {
		out.SystemInstruction = &contentParam{Parts: []partParam{{Text: req.Context.SystemPrompt}}}
	}
	for _, msg := range req.Context.Messages {
		converted, err := convertMessage(msg)
		if err != nil {
			return generateRequest{}, err
		}
		if len(converted.Parts) > 0 {
			out.Contents = append(out.Contents, converted)
		}
	}
	if len(req.Context.Tools) > 0 {
		toolSet := toolSetParam{FunctionDeclarations: make([]functionDeclarationParam, 0, len(req.Context.Tools))}
		for _, tool := range req.Context.Tools {
			toolSet.FunctionDeclarations = append(toolSet.FunctionDeclarations, functionDeclarationParam{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			})
		}
		out.Tools = []toolSetParam{toolSet}
	}
	if req.Options.MaxTokens > 0 || req.Options.Temperature != nil || req.Options.Reasoning != "" {
		cfg := &generationConfig{MaxOutputTokens: req.Options.MaxTokens, Temperature: req.Options.Temperature}
		if req.Options.Reasoning != "" {
			budget := req.Options.ThinkingBudgets[req.Options.Reasoning]
			cfg.ThinkingConfig = &struct {
				ThinkingBudget int64 `json:"thinkingBudget,omitempty"`
			}{ThinkingBudget: budget}
		}
		out.GenerationConfig = cfg
	}
	return out, nil
}

func convertMessage(msg ai.Message) (contentParam, error) {
	switch msg.Role {
	case ai.RoleUser:
		parts, err := convertContent(msg.Content)
		return contentParam{Role: "user", Parts: parts}, err
	case ai.RoleAssistant:
		parts, err := convertContent(msg.Content)
		if err != nil {
			return contentParam{}, err
		}
		for _, call := range msg.ToolCalls {
			args := call.Arguments
			if len(bytes.TrimSpace(args)) == 0 {
				args = json.RawMessage(`{}`)
			}
			parts = append(parts, partParam{FunctionCall: &functionCallParam{ID: call.ID, Name: call.Name, Args: args}})
		}
		return contentParam{Role: "model", Parts: parts}, nil
	case ai.RoleToolResult:
		if msg.ToolResult == nil {
			return contentParam{}, nil
		}
		response := toolResultJSON(msg.ToolResult.Content)
		return contentParam{Role: "user", Parts: []partParam{{
			FunctionResponse: &functionResponseParam{
				ID:       msg.ToolResult.ToolCallID,
				Name:     msg.ToolResult.ToolName,
				Response: response,
			},
		}}}, nil
	default:
		return contentParam{}, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

func convertContent(blocks []ai.ContentBlock) ([]partParam, error) {
	parts := make([]partParam, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				parts = append(parts, partParam{Text: block.Text})
			}
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			parts = append(parts, partParam{InlineData: &inlineDataParam{
				MIMEType: block.ImageMIMEType,
				Data:     base64.StdEncoding.EncodeToString(block.ImageData),
			}})
		case ai.ContentThinking:
			continue
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	return parts, nil
}

func toolResultJSON(blocks []ai.ContentBlock) json.RawMessage {
	var text strings.Builder
	for _, block := range blocks {
		if block.Type != ai.ContentText {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(block.Text)
	}
	if text.Len() == 0 {
		return json.RawMessage(`{"content":""}`)
	}
	encoded, _ := json.Marshal(map[string]string{"content": text.String()})
	return encoded
}

type streamState struct {
	toolUse bool
}

func (s *streamState) consume(data []byte) []ai.Event {
	var event generateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return []ai.Event{ai.NewErrorEvent("google_stream_json", err)}
	}
	if event.Error.Message != "" || event.Error.Code != 0 {
		return []ai.Event{
			ai.ErrorEvent{Code: event.Error.Status, Message: event.Error.Message},
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	}
	events := make([]ai.Event, 0, 3)
	for _, candidate := range event.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				events = append(events, ai.TextDelta{ContentIndex: 0, Text: part.Text})
			}
			if part.FunctionCall != nil {
				s.toolUse = true
				id := part.FunctionCall.ID
				if id == "" {
					id = fmt.Sprintf("%s_%d", part.FunctionCall.Name, atomic.AddUint64(&toolIDCounter, 1))
				}
				args := part.FunctionCall.Args
				if len(bytes.TrimSpace(args)) == 0 {
					args = json.RawMessage(`{}`)
				}
				events = append(events, ai.ToolCallEvent{
					ContentIndex: 0,
					ToolCall: ai.ToolCall{
						ID:        id,
						Name:      part.FunctionCall.Name,
						Arguments: args,
					},
					Complete: true,
				})
			}
		}
		if candidate.FinishReason != "" {
			events = append(events, ai.StopEvent{Reason: mapFinishReason(candidate.FinishReason, s.toolUse)})
		}
	}
	if usage := event.UsageMetadata.normalized(); usage.TotalTokens != 0 || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		events = append(events, ai.UsageEvent{Usage: usage})
	}
	return events
}

type generateEvent struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string             `json:"text"`
				FunctionCall *functionCallParam `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
	Error         struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

type usageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

func (u usageMetadata) normalized() ai.Usage {
	output := u.CandidatesTokenCount + u.ThoughtsTokenCount
	input := u.PromptTokenCount - u.CachedContentTokenCount
	if input < 0 {
		input = 0
	}
	total := u.TotalTokenCount
	if total == 0 {
		total = input + output + u.CachedContentTokenCount
	}
	return ai.Usage{
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: u.CachedContentTokenCount,
		TotalTokens:     total,
	}
}

func mapFinishReason(reason string, toolUse bool) ai.StopReason {
	if toolUse {
		return ai.StopReasonToolUse
	}
	switch reason {
	case "MAX_TOKENS":
		return ai.StopReasonLength
	case "STOP", "FINISH_REASON_UNSPECIFIED":
		return ai.StopReasonStop
	case "MALFORMED_FUNCTION_CALL":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

var _ providers.Provider = (*Provider)(nil)
