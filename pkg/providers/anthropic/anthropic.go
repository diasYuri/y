// Package anthropic implements the Anthropic Messages provider.
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/providers/internal/retryafter"
	"github.com/yuri/y/pkg/providers/internal/sdkstream"
)

const (
	providerID       = "anthropic"
	defaultBaseURL   = "https://api.anthropic.com/v1"
	defaultModelID   = "claude-sonnet-4-5"
	defaultMaxTokens = 4096
	defaultMaxEvent  = 1 << 20
	anthropicVersion = "2023-06-01"
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
	if apiKey := p.resolveAPIKey(""); apiKey != "" {
		client := p.sdkClient(apiKey, p.messageBaseURL(ai.Model{}), providers.StreamOptions{})
		page, err := client.Models.List(ctx, anthropicsdk.ModelListParams{})
		if err == nil && page != nil && len(page.Data) > 0 {
			models := make([]ai.Model, 0, len(page.Data))
			for _, model := range page.Data {
				models = append(models, ai.Model{
					ID:            model.ID,
					Name:          firstNonEmpty(model.DisplayName, model.ID),
					API:           "anthropic-messages",
					Provider:      providerID,
					BaseURL:       p.baseURL,
					Reasoning:     model.Capabilities.Thinking.Supported || model.Capabilities.Effort.Supported,
					Input:         []ai.InputKind{ai.InputText},
					ContextWindow: model.MaxInputTokens,
					MaxTokens:     model.MaxTokens,
				})
				if model.Capabilities.ImageInput.Supported {
					models[len(models)-1].Input = append(models[len(models)-1].Input, ai.InputImage)
				}
			}
			if len(models) > 0 {
				return models, nil
			}
		}
	}
	// Fallback to the curated list (generated from models.json).
	out := CuratedModels()
	for i := range out {
		out[i].BaseURL = p.baseURL
	}
	return out, nil
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
	payload, err := buildSDKMessageRequest(req)
	if err != nil {
		return nil, err
	}
	if p.dryRun {
		p.inspectSDKRequest(ctx, req, payload, apiKey)
		if cancel != nil {
			cancel()
		}
		return providers.SyntheticDryRunStream(), nil
	}

	client := p.sdkClient(apiKey, p.messageBaseURL(req.Model), req.Options)
	upstream := client.Messages.NewStreaming(ctx, payload, p.sdkRequestOptions(req.Options)...)
	if upstream.Err() != nil {
		if cancel != nil {
			cancel()
		}
		_ = upstream.Close()
		return nil, normalizeAnthropicError(upstream.Err())
	}
	return sdkstream.NewWithNormalize(upstream.Next, upstream.Current, upstream.Err, upstream.Close, newAnthropicConsumer(), normalizeAnthropicError), nil
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
	return providers.ApplyInspector(providers.ApplyCommonClient(p.httpClient, p.middlewares), p.inspector)
}

// CountTokens calls the Anthropic count_tokens endpoint when an API key is
// available, and falls back to providers.EstimateTokens otherwise.
func (p *Provider) CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p == nil {
		return estimatedTokens(c)
	}
	apiKey := p.resolveAPIKey("")
	if apiKey == "" {
		return estimatedTokens(c)
	}
	if modelID == "" {
		modelID = defaultModelID
	}
	params, err := buildSDKCountTokensRequest(modelID, c)
	if err != nil {
		return 0, err
	}
	client := p.sdkClient(apiKey, p.messageBaseURL(ai.Model{BaseURL: p.baseURL}), providers.StreamOptions{})
	result, err := client.Messages.CountTokens(ctx, params, option.WithMaxRetries(p.retry.MaxRetries))
	if err != nil || result == nil || result.InputTokens <= 0 {
		return estimatedTokens(c)
	}
	return result.InputTokens, nil
}

func estimatedTokens(c ai.Context) (int64, error) {
	return providers.EstimateTokens(c), nil
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
	return p.messageBaseURL(model) + "/v1/messages"
}

func (p *Provider) messageBaseURL(model ai.Model) string {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	return baseURL
}

func (p *Provider) sdkClient(apiKey, baseURL string, opts providers.StreamOptions) anthropicsdk.Client {
	requestOptions := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(strings.TrimRight(baseURL, "/") + "/"),
		option.WithHTTPClient(sdkstream.LimitClient(p.client(), p.maxEvent)),
		option.WithMaxRetries(p.retry.MaxRetries),
	}
	if strings.HasPrefix(apiKey, "Bearer ") {
		requestOptions = append(requestOptions, option.WithAuthToken(strings.TrimPrefix(apiKey, "Bearer ")))
	} else {
		requestOptions = append(requestOptions, option.WithAPIKey(apiKey))
	}
	if opts.MaxRetries > 0 {
		requestOptions = append(requestOptions, option.WithMaxRetries(opts.MaxRetries))
	}
	return anthropicsdk.NewClient(requestOptions...)
}

func (p *Provider) sdkRequestOptions(opts providers.StreamOptions) []option.RequestOption {
	out := make([]option.RequestOption, 0, len(opts.Headers)+1)
	for key, value := range opts.Headers {
		if strings.TrimSpace(key) != "" && value != "" {
			out = append(out, option.WithHeader(key, value))
		}
	}
	if opts.Timeout > 0 {
		out = append(out, option.WithRequestTimeout(opts.Timeout))
	}
	return out
}

func (p *Provider) inspectSDKRequest(ctx context.Context, req providers.StreamRequest, payload anthropicsdk.MessageNewParams, apiKey string) {
	if p.inspector == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messageURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Anthropic-Version", anthropicVersion)
	if strings.HasPrefix(apiKey, "Bearer ") {
		httpReq.Header.Set("Authorization", apiKey)
	} else {
		httpReq.Header.Set("X-API-Key", apiKey)
	}
	for key, value := range req.Options.Headers {
		if strings.TrimSpace(key) != "" && value != "" {
			httpReq.Header.Set(key, value)
		}
	}
	p.inspector(httpReq)
}

func buildSDKMessageRequest(req providers.StreamRequest) (anthropicsdk.MessageNewParams, error) {
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	messages, err := buildSDKMessages(req.Context.Messages)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, err
	}
	out := anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model(modelID),
		MaxTokens: defaultMaxTokens,
		Messages:  messages,
	}
	if req.Options.MaxTokens > 0 {
		out.MaxTokens = req.Options.MaxTokens
	}
	if req.Options.Temperature != nil {
		out.Temperature = param.NewOpt(*req.Options.Temperature)
	}
	if req.Context.SystemPrompt != "" {
		out.System = []anthropicsdk.TextBlockParam{{Text: req.Context.SystemPrompt}}
	}
	if req.Options.Reasoning != "" && req.Options.Reasoning != ai.ThinkingMinimal {
		budget := req.Options.ThinkingBudgets[req.Options.Reasoning]
		if budget < 1024 {
			budget = 1024
		}
		out.Thinking = anthropicsdk.ThinkingConfigParamOfEnabled(budget)
	}
	if req.Options.CacheRetention != ai.CacheRetentionNone {
		cache := anthropicsdk.NewCacheControlEphemeralParam()
		if req.Options.CacheRetention == ai.CacheRetentionLong {
			cache.TTL = anthropicsdk.CacheControlEphemeralTTL("1h")
		} else {
			cache.TTL = anthropicsdk.CacheControlEphemeralTTL("5m")
		}
		out.CacheControl = cache
	}
	if req.Options.SessionID != "" {
		out.Metadata.UserID = param.NewOpt(req.Options.SessionID)
	}
	for _, tool := range req.Context.Tools {
		out.Tools = append(out.Tools, anthropicsdk.ToolUnionParam{OfTool: &anthropicsdk.ToolParam{
			Name:        tool.Name,
			Description: param.NewOpt(tool.Description),
			InputSchema: sdkToolSchema(tool.InputSchema),
		}})
	}
	return out, nil
}

func buildSDKCountTokensRequest(modelID string, c ai.Context) (anthropicsdk.MessageCountTokensParams, error) {
	messages, err := buildSDKMessages(c.Messages)
	if err != nil {
		return anthropicsdk.MessageCountTokensParams{}, err
	}
	out := anthropicsdk.MessageCountTokensParams{Model: anthropicsdk.Model(modelID), Messages: messages}
	if c.SystemPrompt != "" {
		out.System = anthropicsdk.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: []anthropicsdk.TextBlockParam{{Text: c.SystemPrompt}},
		}
	}
	for _, tool := range c.Tools {
		out.Tools = append(out.Tools, anthropicsdk.MessageCountTokensToolUnionParam{OfTool: &anthropicsdk.ToolParam{
			Name: tool.Name, Description: param.NewOpt(tool.Description), InputSchema: sdkToolSchema(tool.InputSchema),
		}})
	}
	return out, nil
}

func sdkToolSchema(raw json.RawMessage) anthropicsdk.ToolInputSchemaParam {
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	required := make([]string, 0)
	if values, ok := schema["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required = append(required, name)
			}
		}
	}
	return anthropicsdk.ToolInputSchemaParam{Properties: schema["properties"], Required: required}
}

func buildSDKMessages(messages []ai.Message) ([]anthropicsdk.MessageParam, error) {
	out := make([]anthropicsdk.MessageParam, 0, len(messages))
	for _, msg := range messages {
		blocks, err := buildSDKBlocks(msg.Content, msg.ToolCalls, msg.ToolResult)
		if err != nil {
			return nil, err
		}
		if msg.Role == ai.RoleToolResult {
			// Anthropic represents tool results as user content blocks.
			out = append(out, anthropicsdk.NewUserMessage(blocks...))
			continue
		}
		if len(blocks) == 0 {
			continue
		}
		switch msg.Role {
		case ai.RoleAssistant:
			out = append(out, anthropicsdk.NewAssistantMessage(blocks...))
		case ai.RoleUser:
			out = append(out, anthropicsdk.NewUserMessage(blocks...))
		default:
			return nil, fmt.Errorf("unsupported message role %q", msg.Role)
		}
	}
	return out, nil
}

func buildSDKBlocks(content []ai.ContentBlock, calls []ai.ToolCall, result *ai.ToolResult) ([]anthropicsdk.ContentBlockParamUnion, error) {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(content)+len(calls)+1)
	for _, block := range content {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				blocks = append(blocks, anthropicsdk.ContentBlockParamUnion{OfText: &anthropicsdk.TextBlockParam{Text: block.Text}})
			}
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			blocks = append(blocks, anthropicsdk.ContentBlockParamUnion{OfImage: &anthropicsdk.ImageBlockParam{Source: anthropicsdk.ImageBlockParamSourceUnion{OfBase64: &anthropicsdk.Base64ImageSourceParam{Data: base64.StdEncoding.EncodeToString(block.ImageData), MediaType: anthropicsdk.Base64ImageSourceMediaType(block.ImageMIMEType)}}}})
		case ai.ContentThinking:
			blocks = append(blocks, anthropicsdk.ContentBlockParamUnion{OfThinking: &anthropicsdk.ThinkingBlockParam{Thinking: block.Thinking, Signature: block.Signature}})
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	for _, call := range calls {
		var input any
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &input)
		}
		if input == nil {
			input = map[string]any{}
		}
		blocks = append(blocks, anthropicsdk.ContentBlockParamUnion{OfToolUse: &anthropicsdk.ToolUseBlockParam{ID: call.ID, Name: call.Name, Input: input}})
	}
	if result != nil {
		var contentBlocks []anthropicsdk.ToolResultBlockParamContentUnion
		for _, block := range result.Content {
			if block.Type == ai.ContentText && block.Text != "" {
				contentBlocks = append(contentBlocks, anthropicsdk.ToolResultBlockParamContentUnion{OfText: &anthropicsdk.TextBlockParam{Text: block.Text}})
			}
		}
		blocks = append(blocks, anthropicsdk.ContentBlockParamUnion{OfToolResult: &anthropicsdk.ToolResultBlockParam{ToolUseID: result.ToolCallID, IsError: param.NewOpt(result.IsError), Content: contentBlocks}})
	}
	return blocks, nil
}

type anthropicToolState struct{ id, name, args, signature string }

func newAnthropicConsumer() func(anthropicsdk.MessageStreamEventUnion) []ai.Event {
	state := &struct {
		blocks  map[int64]*anthropicToolState
		stopped bool
	}{blocks: make(map[int64]*anthropicToolState)}
	return func(event anthropicsdk.MessageStreamEventUnion) []ai.Event {
		switch event.Type {
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				block := &anthropicToolState{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
				if raw, err := json.Marshal(event.ContentBlock.Input); err == nil && string(raw) != "null" && string(raw) != "{}" {
					block.args = string(raw)
				}
				state.blocks[event.Index] = block
			}
		case "content_block_delta":
			block := state.blocks[event.Index]
			switch event.Delta.Type {
			case "text_delta":
				return []ai.Event{ai.TextDelta{ContentIndex: int(event.Index), Text: event.Delta.Text}}
			case "thinking_delta":
				return []ai.Event{ai.TextDelta{ContentIndex: int(event.Index), Text: event.Delta.Thinking}}
			case "signature_delta":
				if block != nil {
					block.signature += event.Delta.Signature
				}
			case "input_json_delta":
				if block == nil {
					block = &anthropicToolState{}
					state.blocks[event.Index] = block
				}
				block.args += event.Delta.PartialJSON
				return []ai.Event{ai.ToolCallEvent{ContentIndex: int(event.Index), ToolCall: ai.ToolCall{ID: block.id, Name: block.name}, ArgumentsDelta: json.RawMessage(event.Delta.PartialJSON)}}
			}
		case "content_block_stop":
			if block := state.blocks[event.Index]; block != nil {
				args := block.args
				if args == "" {
					args = "{}"
				}
				return []ai.Event{ai.ToolCallEvent{ContentIndex: int(event.Index), ToolCall: ai.ToolCall{ID: block.id, Name: block.name, Arguments: json.RawMessage(args), ThoughtSignature: block.signature}, Complete: true}}
			}
		case "message_delta":
			usage := event.Usage
			reason := anthropicStopReason(string(event.Delta.StopReason))
			state.stopped = true
			return []ai.Event{
				ai.UsageEvent{Usage: ai.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadInputTokens, CacheWriteTokens: usage.CacheCreationInputTokens, TotalTokens: usage.InputTokens + usage.OutputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens}},
				ai.StopEvent{Reason: reason},
			}
		case "message_stop":
			if !state.stopped {
				state.stopped = true
				return []ai.Event{ai.StopEvent{Reason: ai.StopReasonStop}}
			}
		}
		return nil
	}
}

func anthropicStopReason(reason string) ai.StopReason {
	switch reason {
	case "max_tokens":
		return ai.StopReasonLength
	case "tool_use":
		return ai.StopReasonToolUse
	case "model_context_window_exceeded":
		return ai.StopReasonLength
	case "end_turn", "stop_sequence", "":
		return ai.StopReasonStop
	default:
		return ai.StopReasonError
	}
}

func normalizeAnthropicError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *anthropicsdk.Error
	if errors.As(err, &apiErr) {
		var retryAfter time.Duration
		if apiErr.Response != nil {
			retryAfter = retryafter.Parse(apiErr.Response.Header.Get("Retry-After"))
		}
		return providers.ClassifyHTTPError(providerID, apiErr.StatusCode, retryAfter, apiErr.Error(), err)
	}
	return &providers.NetworkError{Provider: providerID, Message: err.Error(), Err: err}
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
