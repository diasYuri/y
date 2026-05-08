// Package openai_compatible implements OpenAI Chat Completions compatible APIs.
//
// Deprecated: prefer pkg/providers/openai.NewCompatible(baseURL, opts...) for
// new code. The implementation continues to live in this package and remains
// the canonical Chat Completions provider for local LLM endpoints; the
// deprecation notice is to encourage import-site convergence on the openai
// package.
package openai_compatible

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
	"github.com/yuri/y/pkg/providers/internal/retryafter"
	providerstream "github.com/yuri/y/pkg/providers/internal/stream"
)

const (
	providerID        = "openai-compatible"
	defaultBaseURL    = "http://localhost:11434/v1"
	defaultModelID    = "local-model"
	defaultMaxEvent   = 1 << 20
	maxErrorBodyBytes = 4 << 10
)

// Provider streams OpenAI-compatible Chat Completions events as normalized AI
// events. It covers local and hosted compatible endpoints without tying the
// core runtime to Node/Bun SDKs.
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

// WithBaseURL sets the OpenAI-compatible API base URL.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) {
		if strings.TrimSpace(baseURL) != "" {
			p.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		}
	}
}

// WithAPIKey sets an explicit API key. Empty keys are allowed for local
// endpoints if Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY is true.
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

// New creates an OpenAI-compatible provider.
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

// Models returns the default compatible model descriptor (curated list).
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

// Stream starts a streaming Chat Completions request.
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
	if apiKey == "" && !p.allowEmptyKey() {
		return nil, errors.New("openai-compatible API key is required; set OPENAI_COMPATIBLE_API_KEY, Y_OPENAI_COMPATIBLE_API_KEY, or allow empty local auth")
	}
	payload, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai-compatible request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for key, value := range req.Options.Headers {
		if strings.TrimSpace(key) == "" || value == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
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

	state := &streamState{tools: make(map[int]*toolState)}
	return providerstream.New(resp.Body, p.maxEvent, cancel, "openai_compatible_stream_read", state.consume), nil
}

// client returns the HTTP client with middleware applied.
func (p *Provider) client() *http.Client {
	return providers.ApplyCommonClient(p.httpClient, p.middlewares)
}

// CountTokens returns a token estimate. Compatible endpoints generally do not
// expose a token-count endpoint, so this falls back to the shared heuristic.
func (p *Provider) CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return providers.EstimateTokens(c), nil
}

// Capabilities returns the feature set assumed for the named compatible
// model. Local LLM endpoints vary widely; this returns conservative defaults
// (text + tool-calls + streaming). Vision is enabled only when the model
// descriptor declares image support.
func (p *Provider) Capabilities(modelID string) providers.Capabilities {
	if strings.TrimSpace(modelID) == "" {
		return providers.Capabilities{}
	}
	return providers.Capabilities{
		Tools:     true,
		Streaming: true,
	}
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

func (p *Provider) allowEmptyKey() bool {
	if p != nil && p.envLookup != nil {
		return strings.EqualFold(p.envLookup("Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY"), "true")
	}
	return strings.EqualFold(os.Getenv("Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY"), "true")
}

func (p *Provider) chatURL(model ai.Model) string {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return baseURL + "/chat/completions"
}

type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []messageParam `json:"messages"`
	Stream           bool           `json:"stream"`
	StreamOptions    *streamOpts    `json:"stream_options,omitempty"`
	MaxTokens        int64          `json:"max_tokens,omitempty"`
	MaxCompletionTok int64          `json:"max_completion_tokens,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	Tools            []toolParam    `json:"tools,omitempty"`
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type messageParam struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []toolCallParam `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type toolCallParam struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Function functionParam `json:"function"`
}

type functionParam struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolParam struct {
	Type     string            `json:"type"`
	Function toolFunctionParam `json:"function"`
}

type toolFunctionParam struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildRequest(req providers.StreamRequest) (chatRequest, error) {
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	out := chatRequest{
		Model:         modelID,
		Stream:        true,
		StreamOptions: &streamOpts{IncludeUsage: true},
		Temperature:   req.Options.Temperature,
	}
	if req.Options.MaxTokens > 0 {
		out.MaxTokens = req.Options.MaxTokens
	}
	if req.Model.Reasoning && req.Options.Reasoning != "" {
		out.ReasoningEffort = string(req.Options.Reasoning)
		out.MaxCompletionTok = req.Options.MaxTokens
		out.MaxTokens = 0
	}
	if req.Context.SystemPrompt != "" {
		role := "system"
		if req.Model.Reasoning {
			role = "developer"
		}
		out.Messages = append(out.Messages, messageParam{Role: role, Content: req.Context.SystemPrompt})
	}
	for _, msg := range req.Context.Messages {
		converted, err := convertMessage(msg)
		if err != nil {
			return chatRequest{}, err
		}
		if converted.Content != nil || len(converted.ToolCalls) > 0 || converted.ToolCallID != "" {
			out.Messages = append(out.Messages, converted)
		}
	}
	for _, tool := range req.Context.Tools {
		out.Tools = append(out.Tools, toolParam{
			Type: "function",
			Function: toolFunctionParam{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return out, nil
}

func convertMessage(msg ai.Message) (messageParam, error) {
	switch msg.Role {
	case ai.RoleUser:
		content, err := convertContent(msg.Content)
		return messageParam{Role: "user", Content: content}, err
	case ai.RoleAssistant:
		content, err := convertContent(msg.Content)
		if err != nil {
			return messageParam{}, err
		}
		out := messageParam{Role: "assistant", Content: content}
		for _, call := range msg.ToolCalls {
			args := string(call.Arguments)
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, toolCallParam{
				ID:   call.ID,
				Type: "function",
				Function: functionParam{
					Name:      call.Name,
					Arguments: args,
				},
			})
		}
		return out, nil
	case ai.RoleToolResult:
		if msg.ToolResult == nil {
			return messageParam{}, nil
		}
		return messageParam{
			Role:       "tool",
			ToolCallID: msg.ToolResult.ToolCallID,
			Name:       msg.ToolResult.ToolName,
			Content:    toolResultText(msg.ToolResult.Content),
		}, nil
	default:
		return messageParam{}, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

func convertContent(blocks []ai.ContentBlock) (any, error) {
	parts := make([]contentPart, 0, len(blocks))
	onlyText := true
	var text strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text == "" {
				continue
			}
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
			parts = append(parts, contentPart{Type: "text", Text: block.Text})
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			onlyText = false
			parts = append(parts, contentPart{
				Type: "image_url",
				ImageURL: &imageURL{
					URL:    "data:" + block.ImageMIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.ImageData),
					Detail: "auto",
				},
			})
		case ai.ContentThinking:
			continue
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	if onlyText {
		return text.String(), nil
	}
	return parts, nil
}

func toolResultText(blocks []ai.ContentBlock) string {
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
	return text.String()
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
	var event completionChunk
	if err := json.Unmarshal(data, &event); err != nil {
		return []ai.Event{ai.NewErrorEvent("openai_compatible_stream_json", err)}
	}
	if event.Error.Message != "" || event.Error.Code != "" {
		return []ai.Event{
			ai.ErrorEvent{Code: event.Error.Code, Message: event.Error.Message},
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	}
	events := make([]ai.Event, 0, 3)
	usage := event.Usage.normalized()
	hasUsage := usage.TotalTokens != 0 || usage.InputTokens != 0 || usage.OutputTokens != 0
	for _, choice := range event.Choices {
		if choice.Delta.Content != "" {
			events = append(events, ai.TextDelta{ContentIndex: choice.Index, Text: choice.Delta.Content})
		}
		for _, call := range choice.Delta.ToolCalls {
			tool := s.tool(call.Index)
			if call.ID != "" {
				tool.id = call.ID
			}
			if call.Function.Name != "" {
				tool.name = call.Function.Name
			}
			if call.Function.Arguments != "" {
				tool.arguments.WriteString(call.Function.Arguments)
				events = append(events, ai.ToolCallEvent{
					ContentIndex:   choice.Index,
					ToolCall:       ai.ToolCall{ID: tool.id, Name: tool.name},
					ArgumentsDelta: json.RawMessage(call.Function.Arguments),
					Complete:       false,
				})
			}
		}
		if choice.FinishReason != "" {
			for _, tool := range s.tools {
				if tool.id == "" && tool.name == "" && tool.arguments.Len() == 0 {
					continue
				}
				s.toolUse = true
				args := tool.arguments.String()
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				events = append(events, ai.ToolCallEvent{
					ContentIndex: choice.Index,
					ToolCall: ai.ToolCall{
						ID:        tool.id,
						Name:      tool.name,
						Arguments: json.RawMessage(args),
					},
					Complete: true,
				})
			}
			if hasUsage {
				events = append(events, ai.UsageEvent{Usage: usage})
				hasUsage = false
			}
			events = append(events, ai.StopEvent{Reason: mapFinishReason(choice.FinishReason, s.toolUse)})
		}
	}
	if hasUsage {
		events = append(events, ai.UsageEvent{Usage: usage})
	}
	return events
}

func (s *streamState) tool(index int) *toolState {
	tool := s.tools[index]
	if tool == nil {
		tool = &toolState{}
		s.tools[index] = tool
	}
	return tool
}

type completionChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usagePayload `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type usagePayload struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u usagePayload) normalized() ai.Usage {
	input := u.PromptTokens - u.PromptTokensDetails.CachedTokens
	if input < 0 {
		input = 0
	}
	output := u.CompletionTokens
	total := u.TotalTokens
	if total == 0 {
		total = input + output + u.PromptTokensDetails.CachedTokens
	}
	return ai.Usage{
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: u.PromptTokensDetails.CachedTokens,
		TotalTokens:     total,
	}
}

func mapFinishReason(reason string, toolUse bool) ai.StopReason {
	if toolUse || reason == "tool_calls" || reason == "function_call" {
		return ai.StopReasonToolUse
	}
	switch reason {
	case "length":
		return ai.StopReasonLength
	case "stop", "":
		return ai.StopReasonStop
	case "content_filter":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

var _ providers.Provider = (*Provider)(nil)
