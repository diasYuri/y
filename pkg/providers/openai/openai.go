// Package openai implements the initial OpenAI Responses API provider.
package openai

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

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/providers/internal/retryafter"
	"github.com/yuri/y/pkg/providers/internal/sdkstream"
)

const (
	providerID      = "openai"
	defaultBaseURL  = "https://api.openai.com/v1"
	defaultModelID  = "gpt-5.4-mini"
	defaultMaxEvent = 1 << 20
)

// Provider streams OpenAI Responses API events as normalized AI events.
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

// WithAPIKey sets an explicit API key. The value is only used for the
// Authorization header and is never logged by this package.
//
// API key precedence (uniform across all providers):
//  1. StreamRequest.Options.APIKey (per-request override) wins.
//  2. WithAPIKey constructor option wins next.
//  3. Provider env vars via the configured WithEnvLookup (OPENAI_API_KEY for
//     New, or OPENAI_COMPATIBLE_API_KEY / Y_OPENAI_COMPATIBLE_API_KEY for
//     NewCompatible).
func WithAPIKey(apiKey string) Option {
	return func(p *Provider) {
		p.apiKey = apiKey
	}
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

// New creates an OpenAI provider using the Responses API.
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

// NewCompatible creates an OpenAI-compatible Chat Completions provider for the
// given baseURL. This is the supported entry point for local LLM servers and
// hosted compatible APIs (vLLM, llama.cpp/llama-server, Ollama, Together,
// Groq, etc.). The provider ID is "openai-compatible" so callers can route
// based on the source family.
//
// The returned provider honours Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY=true to
// allow local endpoints with no auth. NewCompatible delegates to
// pkg/providers/openai_compatible; that package remains importable for
// backwards compatibility but is documented as deprecated.
func NewCompatible(baseURL string, opts ...Option) providers.Provider {
	return newCompatible(baseURL, opts...)
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

// Models returns the OpenAI model list. It attempts to fetch from the API
// and falls back to a built-in list on failure.
func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		p = New()
	}
	if apiKey := p.resolveAPIKey(""); apiKey != "" {
		client := p.sdkClient(apiKey, p.responseBaseURL(ai.Model{}), providers.StreamOptions{})
		page, err := client.Models.List(ctx)
		if err == nil && page != nil && len(page.Data) > 0 {
			models := make([]ai.Model, 0, len(page.Data))
			for _, model := range page.Data {
				models = append(models, ai.Model{
					ID:       model.ID,
					Name:     model.ID,
					API:      "openai-responses",
					Provider: providerID,
					BaseURL:  p.baseURL,
					Reasoning: strings.HasPrefix(model.ID, "o") ||
						strings.Contains(model.ID, "reasoning") ||
						strings.HasPrefix(model.ID, "gpt-5"),
					Input: []ai.InputKind{ai.InputText, ai.InputImage},
				})
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

// Stream starts a streaming Responses API request (or Chat Completions for
// NewCompatible providers).
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
		return nil, errors.New("openai API key is required; set OPENAI_API_KEY or pass APIKey")
	}

	payload, err := buildSDKRequest(req)
	if err != nil {
		return nil, err
	}

	baseURL := p.responseBaseURL(req.Model)
	client := p.sdkClient(apiKey, baseURL, req.Options)
	sdkOpts := p.sdkRequestOptions(req.Options)
	if p.dryRun {
		p.inspectSDKRequest(ctx, req, payload, apiKey)
		if cancel != nil {
			cancel()
		}
		return providers.SyntheticDryRunStream(), nil
	}

	upstream := client.Responses.NewStreaming(ctx, payload, sdkOpts...)
	if upstream.Err() != nil {
		if cancel != nil {
			cancel()
		}
		_ = upstream.Close()
		return nil, normalizeOpenAIError(upstream.Err())
	}
	return sdkstream.NewWithNormalize(
		upstream.Next,
		upstream.Current,
		upstream.Err,
		upstream.Close,
		newOpenAIConsumer(),
		normalizeOpenAIError,
	), nil
}
func (p *Provider) client() *http.Client {
	return providers.ApplyInspector(providers.ApplyCommonClient(p.httpClient, p.middlewares), p.inspector)
}

// CountTokens uses OpenAI's official Responses input-token endpoint and falls
// back to the shared estimator only when credentials or the endpoint are not
// available.
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
	input, err := buildSDKInput(c, false)
	if err != nil {
		return 0, err
	}
	params := responses.InputTokenCountParams{
		Model: param.NewOpt(modelID),
		Input: responses.InputTokenCountParamsInputUnion{OfResponseInputItemArray: input},
	}
	client := p.sdkClient(apiKey, p.responseBaseURL(ai.Model{BaseURL: p.baseURL}), providers.StreamOptions{})
	result, err := client.Responses.InputTokens.Count(ctx, params, option.WithMaxRetries(p.retry.MaxRetries))
	if err != nil || result == nil || result.InputTokens <= 0 {
		return estimatedTokens(c)
	}
	return result.InputTokens, nil
}

func estimatedTokens(c ai.Context) (int64, error) {
	return providers.EstimateTokens(c), nil
}

// Capabilities returns the feature set supported by the named OpenAI model.
// All current GPT-5/4 family models support vision, tools, prompt caching,
// reasoning (encrypted_content), and JSON mode.
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

func (p *Provider) responseURL(model ai.Model) string {
	return p.responseBaseURL(model) + "/responses"
}

func (p *Provider) responseBaseURL(model ai.Model) string {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return baseURL
}

func (p *Provider) sdkClient(apiKey, baseURL string, opts providers.StreamOptions) openaisdk.Client {
	requestOptions := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(strings.TrimRight(baseURL, "/") + "/"),
		option.WithHTTPClient(sdkstream.LimitClient(p.client(), p.maxEvent)),
		option.WithMaxRetries(p.retry.MaxRetries),
	}
	if opts.MaxRetries > 0 {
		requestOptions = append(requestOptions, option.WithMaxRetries(opts.MaxRetries))
	}
	return openaisdk.NewClient(requestOptions...)
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

func (p *Provider) inspectSDKRequest(ctx context.Context, req providers.StreamRequest, payload responses.ResponseNewParams, apiKey string) {
	if p.inspector == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.responseURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	for key, value := range req.Options.Headers {
		if strings.TrimSpace(key) != "" && value != "" {
			httpReq.Header.Set(key, value)
		}
	}
	p.inspector(httpReq)
}

func buildSDKRequest(req providers.StreamRequest) (responses.ResponseNewParams, error) {
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	input, err := buildSDKInput(req.Context, req.Model.Reasoning)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	out := responses.ResponseNewParams{
		Model: shared.ResponsesModel(modelID),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Store: param.NewOpt(false),
	}
	if req.Options.MaxTokens > 0 {
		out.MaxOutputTokens = param.NewOpt(req.Options.MaxTokens)
	}
	if req.Options.Temperature != nil {
		out.Temperature = param.NewOpt(*req.Options.Temperature)
	}
	if req.Options.CacheRetention != ai.CacheRetentionNone && req.Options.SessionID != "" {
		out.PromptCacheKey = param.NewOpt(req.Options.SessionID)
		if req.Options.CacheRetention == ai.CacheRetentionLong {
			out.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetention24h
		}
	}
	if req.Options.Reasoning != "" && req.Model.Reasoning {
		out.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(string(req.Options.Reasoning))}
		out.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}
	for _, tool := range req.Context.Tools {
		var schema map[string]any
		if len(tool.InputSchema) > 0 {
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				return responses.ResponseNewParams{}, fmt.Errorf("decode tool %q schema: %w", tool.Name, err)
			}
		}
		out.Tools = append(out.Tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name:        tool.Name,
			Description: param.NewOpt(tool.Description),
			Parameters:  schema,
			Strict:      param.NewOpt(false),
		}})
	}
	return out, nil
}

func buildSDKInput(c ai.Context, reasoning bool) (responses.ResponseInputParam, error) {
	var input responses.ResponseInputParam
	if c.SystemPrompt != "" {
		content := responses.ResponseInputMessageContentListParam{
			responses.ResponseInputContentParamOfInputText(c.SystemPrompt),
		}
		role := responses.EasyInputMessageRoleSystem
		if reasoning {
			role = responses.EasyInputMessageRoleDeveloper
		}
		input = append(input, responses.ResponseInputItemParamOfMessage(content, role))
	}
	for _, msg := range c.Messages {
		items, err := buildSDKMessage(msg)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	return input, nil
}

func buildSDKMessage(msg ai.Message) ([]responses.ResponseInputItemUnionParam, error) {
	switch msg.Role {
	case ai.RoleUser, ai.RoleAssistant:
		content, err := buildSDKContent(msg.Content)
		if err != nil {
			return nil, err
		}
		if len(content) > 0 {
			role := responses.EasyInputMessageRoleUser
			if msg.Role == ai.RoleAssistant {
				role = responses.EasyInputMessageRoleAssistant
			}
			items := []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage(content, role),
			}
			for _, call := range msg.ToolCalls {
				items = append(items, buildSDKFunctionCall(call))
			}
			return items, nil
		}
		return buildSDKToolCalls(msg.ToolCalls), nil
	case ai.RoleToolResult:
		if msg.ToolResult == nil {
			return nil, nil
		}
		callID, _ := splitToolCallID(msg.ToolResult.ToolCallID)
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfFunctionCallOutput(callID, toolResultText(msg.ToolResult.Content)),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

func toolResultText(blocks []ai.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type != ai.ContentText {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block.Text)
	}
	return b.String()
}

func buildSDKToolCalls(calls []ai.ToolCall) []responses.ResponseInputItemUnionParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(calls))
	for _, call := range calls {
		items = append(items, buildSDKFunctionCall(call))
	}
	return items
}

func buildSDKFunctionCall(call ai.ToolCall) responses.ResponseInputItemUnionParam {
	args := string(call.Arguments)
	if args == "" {
		args = "{}"
	}
	callID, itemID := splitToolCallID(call.ID)
	item := responses.ResponseInputItemParamOfFunctionCall(args, callID, call.Name)
	if item.OfFunctionCall != nil && itemID != "" {
		item.OfFunctionCall.ID = param.NewOpt(itemID)
	}
	return item
}

func buildSDKContent(blocks []ai.ContentBlock) (responses.ResponseInputMessageContentListParam, error) {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				content = append(content, responses.ResponseInputContentParamOfInputText(block.Text))
			}
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			part := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
			part.OfInputImage.ImageURL = param.NewOpt("data:" + block.ImageMIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.ImageData))
			content = append(content, part)
		case ai.ContentThinking:
			// OpenAI reasoning items require encrypted provider content. The
			// normalized transcript does not expose that payload.
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	return content, nil
}

type openAIStreamState struct {
	toolIDs map[string]string
	sawTool bool
}

func newOpenAIConsumer() func(responses.ResponseStreamEventUnion) []ai.Event {
	state := &openAIStreamState{toolIDs: make(map[string]string)}
	return func(event responses.ResponseStreamEventUnion) []ai.Event {
		switch event.Type {
		case "response.output_text.delta":
			return []ai.Event{ai.TextDelta{ContentIndex: int(event.ContentIndex), Text: event.Delta}}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				call := event.Item.AsFunctionCall()
				state.toolIDs[call.ID] = call.CallID
				state.sawTool = true
			}
		case "response.function_call_arguments.delta":
			state.sawTool = true
			callID := state.toolIDs[event.ItemID]
			return []ai.Event{ai.ToolCallEvent{ContentIndex: int(event.OutputIndex), ToolCall: ai.ToolCall{ID: callID + "|" + event.ItemID}, ArgumentsDelta: json.RawMessage(event.Delta), Complete: false}}
		case "response.function_call_arguments.done":
			state.sawTool = true
			callID := state.toolIDs[event.ItemID]
			return []ai.Event{ai.ToolCallEvent{ContentIndex: int(event.OutputIndex), ToolCall: ai.ToolCall{ID: callID + "|" + event.ItemID, Name: event.Name, Arguments: json.RawMessage(event.Arguments)}, Complete: true}}
		case "response.completed", "response.incomplete":
			response := event.Response
			var events []ai.Event
			if response.Usage.TotalTokens != 0 || response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 0 {
				events = append(events, ai.UsageEvent{Usage: ai.Usage{InputTokens: response.Usage.InputTokens - response.Usage.InputTokensDetails.CachedTokens, OutputTokens: response.Usage.OutputTokens, CacheReadTokens: response.Usage.InputTokensDetails.CachedTokens, CacheWriteTokens: response.Usage.InputTokensDetails.CacheWriteTokens, TotalTokens: response.Usage.TotalTokens}})
			}
			reason := ai.StopReasonStop
			if state.sawTool {
				reason = ai.StopReasonToolUse
			}
			if event.Type == "response.incomplete" {
				reason = ai.StopReasonLength
			}
			return append(events, ai.StopEvent{Reason: reason})
		case "response.failed", "error":
			message := event.Message
			if message == "" {
				message = event.Code
			}
			return []ai.Event{ai.NewErrorEvent("openai_stream_error", errors.New(message))}
		}
		return nil
	}
}

func normalizeOpenAIError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openaisdk.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		var retryAfter time.Duration
		if apiErr.Response != nil {
			retryAfter = retryafter.Parse(apiErr.Response.Header.Get("Retry-After"))
		}
		return providers.ClassifyHTTPError(providerID, status, retryAfter, apiErr.Error(), err)
	}
	return &providers.NetworkError{Provider: providerID, Message: err.Error(), Err: err}
}

func splitToolCallID(id string) (callID, itemID string) {
	callID, itemID, ok := strings.Cut(id, "|")
	if !ok {
		return id, ""
	}
	return callID, itemID
}

var _ providers.Provider = (*Provider)(nil)
