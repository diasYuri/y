// Package anthropic implements the Anthropic Messages provider.
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/providers/internal/retry"
	"github.com/yuri/y/pkg/providers/internal/retryafter"
	providerstream "github.com/yuri/y/pkg/providers/internal/stream"
)

const (
	providerID        = "anthropic"
	defaultBaseURL    = "https://api.anthropic.com/v1"
	defaultModelID    = "claude-sonnet-4-5"
	defaultMaxTokens  = 4096
	defaultMaxEvent   = 1 << 20
	maxErrorBodyBytes = 4 << 10
	anthropicVersion  = "2023-06-01"
)

// Provider streams Anthropic Messages events as normalized AI events.
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

// WithBaseURL sets the Anthropic API base URL.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) {
		if strings.TrimSpace(baseURL) != "" {
			p.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		}
	}
}

// WithAPIKey sets an explicit API key or OAuth token.
//
// API key precedence (uniform across all providers):
//  1. StreamRequest.Options.APIKey (per-request override) wins.
//  2. WithAPIKey constructor option wins next.
//  3. Provider env vars via the configured WithEnvLookup
//     (ANTHROPIC_OAUTH_TOKEN, ANTHROPIC_API_KEY).
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

// WithMiddleware appends a middleware to the HTTP transport stack. Multiple
// middlewares are applied in registration order (first registered → outermost).
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

// New creates an Anthropic provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		httpClient: newProxyClient(),
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

func newProxyClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return providerID }

// Models returns Anthropic model list. It attempts to fetch from the API
// and falls back to a built-in list on failure.
func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		p = New()
	}
	models, err := p.fetchModels(ctx)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	// Fallback to the curated list (generated from models.json).
	out := CuratedModels()
	for i := range out {
		out[i].BaseURL = p.baseURL
	}
	return out, nil
}

type anthropicModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type anthropicModelsResponse struct {
	Models []anthropicModel `json:"data"`
}

func (p *Provider) fetchModels(ctx context.Context) ([]ai.Model, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = p.envLookup("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("no API key available")
	}

	cfg := retry.DefaultConfig()
	var result anthropicModelsResponse
	var resp *http.Response

	err := retry.Do(ctx, cfg, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		resp, err = p.httpClient.Do(req)
		if err != nil {
			return err
		}
		if retry.IsRetryableHTTPStatus(resp.StatusCode) {
			resp.Body.Close()
			return fmt.Errorf("models API returned %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return retry.Do(ctx, retry.Config{}, func() error {
				return fmt.Errorf("models API returned %d", resp.StatusCode)
			})
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(&result)
	})
	if err != nil {
		return nil, err
	}

	models := make([]ai.Model, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, ai.Model{
			ID:        m.ID,
			Name:      firstNonEmpty(m.DisplayName, m.ID),
			API:       "anthropic-messages",
			Provider:  providerID,
			BaseURL:   p.baseURL,
			Reasoning: true,
			Input:     []ai.InputKind{ai.InputText, ai.InputImage},
		})
	}
	return models, nil
}

// Stream starts a streaming Anthropic Messages request.
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
		return nil, errors.New("anthropic API key is required; set ANTHROPIC_OAUTH_TOKEN, ANTHROPIC_API_KEY, or pass APIKey")
	}
	payload, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messageURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Anthropic-Version", anthropicVersion)
	for key, value := range req.Options.Headers {
		if strings.TrimSpace(key) == "" || value == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	if strings.HasPrefix(apiKey, "Bearer ") {
		httpReq.Header.Set("Authorization", apiKey)
	} else {
		httpReq.Header.Set("X-API-Key", apiKey)
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
		retryAfter := retryafter.Parse(resp.Header.Get("Retry-After"))
		return nil, providers.ClassifyHTTPError(providerID, resp.StatusCode, retryAfter, body, nil)
	}

	state := &streamState{}
	return providerstream.New(resp.Body, p.maxEvent, cancel, "anthropic_stream_read", state.consume), nil
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

// client returns the HTTP client with middleware applied.
func (p *Provider) client() *http.Client {
	return providers.ApplyCommonClient(p.httpClient, p.middlewares)
}

// CountTokens calls the Anthropic count_tokens endpoint when an API key is
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
	// Build a small Messages request payload; the count endpoint accepts the
	// same shape minus stream/max_tokens.
	streamReq := providers.StreamRequest{
		Model:   ai.Model{ID: modelID},
		Context: c,
	}
	payload, err := buildRequest(streamReq)
	if err != nil {
		return 0, err
	}
	payload.Stream = false
	payload.MaxTokens = 0
	body, err := json.Marshal(struct {
		Model    string         `json:"model"`
		System   string         `json:"system,omitempty"`
		Messages []messageParam `json:"messages"`
		Tools    []toolParam    `json:"tools,omitempty"`
	}{Model: payload.Model, System: payload.System, Messages: payload.Messages, Tools: payload.Tools})
	if err != nil {
		return providers.EstimateTokens(c), nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages/count_tokens", bytes.NewReader(body))
	if err != nil {
		return providers.EstimateTokens(c), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Anthropic-Version", anthropicVersion)
	if strings.HasPrefix(apiKey, "Bearer ") {
		httpReq.Header.Set("Authorization", apiKey)
	} else {
		httpReq.Header.Set("X-API-Key", apiKey)
	}

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
		InputTokens int64 `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return providers.EstimateTokens(c), nil
	}
	if out.InputTokens <= 0 {
		return providers.EstimateTokens(c), nil
	}
	return out.InputTokens, nil
}

// Capabilities returns the feature set supported by the named Anthropic model.
// All Claude family models support vision, tools, prompt caching, and
// reasoning.
func (p *Provider) Capabilities(modelID string) providers.Capabilities {
	if strings.TrimSpace(modelID) == "" {
		return providers.Capabilities{}
	}
	return providers.Capabilities{
		Vision:      true,
		Tools:       true,
		Reasoning:   true,
		PromptCache: true,
		JSONMode:    true,
		Streaming:   true,
	}
}

// Close releases idle HTTP connections held by the underlying transport. It
// is idempotent and safe to call on a nil receiver.
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

func (p *Provider) messageURL(model ai.Model) string {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return baseURL + "/v1/messages"
}

type messageRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int64          `json:"max_tokens"`
	Stream      bool           `json:"stream"`
	System      string         `json:"system,omitempty"`
	Messages    []messageParam `json:"messages"`
	Tools       []toolParam    `json:"tools,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Thinking    *thinkingParam `json:"thinking,omitempty"`
}

type thinkingParam struct {
	Type         string `json:"type"`
	BudgetTokens int64  `json:"budget_tokens,omitempty"`
}

type messageParam struct {
	Role    string         `json:"role"`
	Content []contentParam `json:"content"`
}

type contentParam struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   []contentParam  `json:"content,omitempty"`
	Source    *imageSource    `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type toolParam struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func buildRequest(req providers.StreamRequest) (messageRequest, error) {
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	maxTokens := req.Options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	out := messageRequest{
		Model:     modelID,
		MaxTokens: maxTokens,
		Stream:    true,
		System:    req.Context.SystemPrompt,
	}
	if req.Options.Temperature != nil {
		out.Temperature = req.Options.Temperature
	}
	if req.Options.Reasoning != "" && req.Options.Reasoning != ai.ThinkingMinimal {
		budget := req.Options.ThinkingBudgets[req.Options.Reasoning]
		out.Thinking = &thinkingParam{Type: "enabled", BudgetTokens: budget}
	}
	for _, msg := range req.Context.Messages {
		converted, err := convertMessage(msg)
		if err != nil {
			return messageRequest{}, err
		}
		if len(converted.Content) > 0 {
			out.Messages = append(out.Messages, converted)
		}
	}
	for _, tool := range req.Context.Tools {
		out.Tools = append(out.Tools, toolParam{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return out, nil
}

func convertMessage(msg ai.Message) (messageParam, error) {
	switch msg.Role {
	case ai.RoleUser:
		content, err := convertContent(msg.Content)
		if err != nil {
			return messageParam{}, err
		}
		return messageParam{Role: "user", Content: content}, nil
	case ai.RoleAssistant:
		content, err := convertContent(msg.Content)
		if err != nil {
			return messageParam{}, err
		}
		for _, call := range msg.ToolCalls {
			args := call.Arguments
			if len(bytes.TrimSpace(args)) == 0 {
				args = json.RawMessage(`{}`)
			}
			content = append(content, contentParam{
				Type:  "tool_use",
				ID:    call.ID,
				Name:  call.Name,
				Input: args,
			})
		}
		return messageParam{Role: "assistant", Content: content}, nil
	case ai.RoleToolResult:
		if msg.ToolResult == nil {
			return messageParam{}, nil
		}
		return messageParam{Role: "user", Content: []contentParam{{
			Type:      "tool_result",
			ToolUseID: msg.ToolResult.ToolCallID,
			Content:   toolResultContent(msg.ToolResult.Content),
		}}}, nil
	default:
		return messageParam{}, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

func convertContent(blocks []ai.ContentBlock) ([]contentParam, error) {
	content := make([]contentParam, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				content = append(content, contentParam{Type: "text", Text: block.Text})
			}
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			content = append(content, contentParam{
				Type: "image",
				Source: &imageSource{
					Type:      "base64",
					MediaType: block.ImageMIMEType,
					Data:      base64.StdEncoding.EncodeToString(block.ImageData),
				},
			})
		case ai.ContentThinking:
			continue
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	return content, nil
}

func toolResultContent(blocks []ai.ContentBlock) []contentParam {
	content, _ := convertContent(blocks)
	if len(content) == 0 {
		return []contentParam{{Type: "text", Text: ""}}
	}
	return content
}

type streamState struct {
	tools   map[int]*toolState
	toolUse bool
}

type toolState struct {
	id        string
	name      string
	arguments strings.Builder
}

func (s *streamState) consume(data []byte) []ai.Event {
	if s.tools == nil {
		s.tools = make(map[int]*toolState)
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
	}

	switch header.Type {
	case "content_block_start":
		var event struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
		}
		if event.ContentBlock.Type != "tool_use" {
			return nil
		}
		tool := s.tool(event.Index)
		tool.id = event.ContentBlock.ID
		tool.name = event.ContentBlock.Name
		if len(bytes.TrimSpace(event.ContentBlock.Input)) > 0 && string(event.ContentBlock.Input) != "{}" {
			tool.arguments.Write(event.ContentBlock.Input)
		}
	case "content_block_delta":
		var event struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
		}
		if event.Delta.Type == "input_json_delta" {
			tool := s.tool(event.Index)
			tool.arguments.WriteString(event.Delta.PartialJSON)
			return []ai.Event{ai.ToolCallEvent{
				ContentIndex:   event.Index,
				ToolCall:       ai.ToolCall{ID: tool.id, Name: tool.name},
				ArgumentsDelta: json.RawMessage(event.Delta.PartialJSON),
				Complete:       false,
			}}
		}
		text := event.Delta.Text
		if text == "" {
			text = event.Delta.Thinking
		}
		if text == "" {
			return nil
		}
		return []ai.Event{ai.TextDelta{ContentIndex: event.Index, Text: text}}
	case "content_block_stop":
		var event struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
		}
		tool := s.tools[event.Index]
		if event.ContentBlock.Type == "tool_use" {
			tool = s.tool(event.Index)
			tool.id = event.ContentBlock.ID
			tool.name = event.ContentBlock.Name
			if len(bytes.TrimSpace(event.ContentBlock.Input)) > 0 {
				tool.arguments.Reset()
				tool.arguments.Write(event.ContentBlock.Input)
			}
		}
		if tool == nil || (tool.id == "" && tool.name == "" && tool.arguments.Len() == 0) {
			return nil
		}
		s.toolUse = true
		args := tool.arguments.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		return []ai.Event{ai.ToolCallEvent{
			ContentIndex: event.Index,
			ToolCall: ai.ToolCall{
				ID:        tool.id,
				Name:      tool.name,
				Arguments: json.RawMessage(args),
			},
			Complete: true,
		}}
	case "message_delta":
		var event struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage usagePayload `json:"usage"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
		}
		events := usageEvents(event.Usage)
		if event.Delta.StopReason != "" {
			reason := mapStopReason(event.Delta.StopReason)
			if s.toolUse && reason == ai.StopReasonStop {
				reason = ai.StopReasonToolUse
			}
			events = append(events, ai.StopEvent{Reason: reason})
		}
		return events
	case "message_stop":
		reason := ai.StopReasonStop
		if s.toolUse {
			reason = ai.StopReasonToolUse
		}
		return []ai.Event{ai.StopEvent{Reason: reason}}
	case "error":
		var event struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("anthropic_stream_json", err)}
		}
		return []ai.Event{
			ai.ErrorEvent{Code: event.Error.Type, Message: event.Error.Message},
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	}
	return nil
}

func (s *streamState) tool(index int) *toolState {
	tool := s.tools[index]
	if tool == nil {
		tool = &toolState{}
		s.tools[index] = tool
	}
	return tool
}

type usagePayload struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func usageEvents(usage usagePayload) []ai.Event {
	total := usage.InputTokens + usage.OutputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	if total == 0 {
		return nil
	}
	return []ai.Event{ai.UsageEvent{Usage: ai.Usage{
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
		TotalTokens:      total,
	}}}
}

func mapStopReason(reason string) ai.StopReason {
	switch reason {
	case "max_tokens":
		return ai.StopReasonLength
	case "tool_use":
		return ai.StopReasonToolUse
	case "error":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var _ providers.Provider = (*Provider)(nil)
