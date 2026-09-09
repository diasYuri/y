// Package google implements the Google Gemini Generative Language provider.
package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/providers/internal/sdkstream"
	"google.golang.org/genai"
)

const (
	providerID      = "google"
	defaultBaseURL  = "https://generativelanguage.googleapis.com/v1beta"
	defaultModelID  = "gemini-2.5-flash"
	defaultMaxEvent = 1 << 20
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

// Models lists models through the official Google SDK when credentials are
// available, falling back to the generated list for offline use.
func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		p = New()
	}
	if apiKey := p.resolveAPIKey(""); apiKey != "" {
		if client, err := p.sdkClient(ctx, apiKey, p.baseURL, providers.StreamOptions{}); err == nil {
			page, err := client.Models.List(ctx, nil)
			if err == nil {
				models := make([]ai.Model, 0, len(page.Items))
				for {
					for _, model := range page.Items {
						if model == nil {
							continue
						}
						id := strings.TrimPrefix(model.Name, "models/")
						if id == "" {
							continue
						}
						name := model.DisplayName
						if name == "" {
							name = id
						}
						models = append(models, ai.Model{
							ID:            id,
							Name:          name,
							API:           "google-gemini",
							Provider:      providerID,
							BaseURL:       p.baseURL,
							Reasoning:     strings.Contains(strings.ToLower(id), "thinking") || strings.Contains(id, "2.5"),
							Input:         []ai.InputKind{ai.InputText, ai.InputImage},
							ContextWindow: int64(model.InputTokenLimit),
							MaxTokens:     int64(model.OutputTokenLimit),
							Capabilities:  ai.ModelCapabilities{StructuredOutput: true},
						})
					}
					if page.NextPageToken == "" {
						break
					}
					page, err = page.Next(ctx)
					if err != nil {
						break
					}
				}
				if len(models) > 0 {
					return models, nil
				}
			}
		}
	}
	return p.curatedModels(), nil
}

func (p *Provider) curatedModels() []ai.Model {
	out := CuratedModels()
	for i := range out {
		out[i].BaseURL = p.baseURL
		out[i].Capabilities.StructuredOutput = true
	}
	return out
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
	if err := providers.ValidateStructuredOutputRequest(req, providerID, p.Capabilities(req.Model.ID)); err != nil {
		return nil, err
	}

	apiKey := p.resolveAPIKey(req.Options.APIKey)
	if apiKey == "" {
		return nil, errors.New("google API key is required; set GEMINI_API_KEY or pass APIKey")
	}
	contents, config, err := buildSDKRequest(req, p.httpOptions(req.Options, req.Model))
	if err != nil {
		return nil, err
	}
	client, err := p.sdkClient(ctx, apiKey, p.modelBaseURL(req.Model), req.Options)
	if err != nil {
		return nil, err
	}
	if p.dryRun {
		if cancel != nil {
			cancel()
		}
		p.inspectSDKRequest(ctx, req, apiKey, contents, config)
		return providers.SyntheticDryRunStream(), nil
	}
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	upstream := client.Models.GenerateContentStream(ctx, modelID, contents, config)
	return sdkstream.NewIterator(ctx, func(yield func(*genai.GenerateContentResponse, error) bool) {
		for response, iterErr := range upstream {
			if !yield(response, iterErr) {
				return
			}
		}
	}, newGoogleConsumer(), normalizeGoogleError)
}

// client returns the HTTP client with middleware applied.
func (p *Provider) client() *http.Client {
	return providers.ApplyInspector(providers.ApplyCommonClient(p.httpClient, p.middlewares), p.inspector)
}

// CountTokens calls the Gemini countTokens endpoint when an API key is
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
	// The official Gemini Developer API SDK currently rejects system
	// instructions and tools in CountTokensConfig. Do not send a partial
	// request and report a misleading exact count; use the shared estimator
	// until the SDK exposes those fields for this backend.
	if c.SystemPrompt != "" || len(c.Tools) > 0 {
		return estimatedTokens(c)
	}
	streamReq := providers.StreamRequest{
		Model:   ai.Model{ID: modelID},
		Context: c,
	}
	contents, generateConfig, err := buildSDKRequest(streamReq, p.httpOptions(providers.StreamOptions{}, streamReq.Model))
	if err != nil {
		return 0, err
	}
	client, err := p.sdkClient(ctx, apiKey, p.modelBaseURL(streamReq.Model), providers.StreamOptions{})
	if err != nil {
		return estimatedTokens(c)
	}
	if p.dryRun {
		return estimatedTokens(c)
	}
	result, err := client.Models.CountTokens(ctx, modelID, contents, &genai.CountTokensConfig{HTTPOptions: generateConfig.HTTPOptions})
	if err != nil {
		return estimatedTokens(c)
	}
	if result == nil || result.TotalTokens <= 0 {
		return estimatedTokens(c)
	}
	return int64(result.TotalTokens), nil
}

func estimatedTokens(c ai.Context) (int64, error) {
	return providers.EstimateTokens(c), nil
}

func (p *Provider) modelBaseURL(model ai.Model) string {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	return strings.TrimRight(baseURL, "/")
}

func googleBaseURL(raw string) (string, string) {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	// The SDK always prepends APIVersion. A dot normalizes away in URL.JoinPath
	// and lets custom proxy endpoints retain their original path, while the
	// public Gemini endpoint uses its explicit v1beta suffix below.
	version := "."
	parsed, err := url.Parse(baseURL)
	if err == nil {
		path := strings.TrimRight(parsed.Path, "/")
		for _, candidate := range []string{"v1alpha", "v1beta", "v1"} {
			if strings.HasSuffix(path, "/"+candidate) || path == candidate {
				version = candidate
				path = strings.TrimSuffix(path, "/"+candidate)
				path = strings.TrimSuffix(path, candidate)
				parsed.Path = strings.TrimRight(path, "/")
				return strings.TrimRight(parsed.String(), "/"), version
			}
		}
	}
	return baseURL, version
}

func (p *Provider) httpOptions(opts providers.StreamOptions, model ai.Model) genai.HTTPOptions {
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = model.BaseURL
	}
	baseURL, version := googleBaseURL(baseURL)
	headers := make(http.Header)
	for key, value := range opts.Headers {
		if strings.TrimSpace(key) != "" && value != "" {
			headers.Set(key, value)
		}
	}
	providers.ApplyRequestMetadata(headers, opts)
	if opts.Timeout > 0 {
		timeout := opts.Timeout
		return genai.HTTPOptions{
			BaseURL:      baseURL,
			APIVersion:   version,
			Headers:      headers,
			Timeout:      &timeout,
			RetryOptions: p.retryOptions(opts),
		}
	}
	return genai.HTTPOptions{
		BaseURL:      baseURL,
		APIVersion:   version,
		Headers:      headers,
		RetryOptions: p.retryOptions(opts),
	}
}

func (p *Provider) retryOptions(opts providers.StreamOptions) *genai.HTTPRetryOptions {
	retries := p.retry.MaxRetries
	if opts.MaxRetries > 0 {
		retries = opts.MaxRetries
	}
	attempts := int32(retries + 1)
	initialDelay := p.retry.InitialDelay.Seconds()
	maxDelay := p.retry.MaxDelay.Seconds()
	expBase := p.retry.BackoffFactor
	if opts.MaxRetryDelay > 0 {
		maxDelay = opts.MaxRetryDelay.Seconds()
	}
	if initialDelay <= 0 {
		initialDelay = 0.5
	}
	if maxDelay <= 0 {
		maxDelay = 30
	}
	if expBase <= 0 {
		expBase = 2
	}
	return &genai.HTTPRetryOptions{
		Attempts:     &attempts,
		InitialDelay: &initialDelay,
		MaxDelay:     &maxDelay,
		ExpBase:      &expBase,
		Jitter:       &p.retry.Jitter,
	}
}

func (p *Provider) sdkClient(ctx context.Context, apiKey, baseURL string, opts providers.StreamOptions) (*genai.Client, error) {
	httpOptions := p.httpOptions(opts, ai.Model{BaseURL: baseURL})
	httpClient := sdkstream.LimitClient(p.client(), p.maxEvent)
	if httpClient != nil {
		copyClient := *httpClient
		next := httpClient.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		copyClient.Transport = googleAPIKeyTransport{apiKey: apiKey, next: next}
		httpClient = &copyClient
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      apiKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  httpClient,
		HTTPOptions: httpOptions,
	})
}

type googleAPIKeyTransport struct {
	apiKey string
	next   http.RoundTripper
}

func (t googleAPIKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiKey == "" {
		return t.next.RoundTrip(req)
	}
	copyReq := req.Clone(req.Context())
	query := copyReq.URL.Query()
	query.Set("key", t.apiKey)
	copyReq.URL.RawQuery = query.Encode()
	return t.next.RoundTrip(copyReq)
}

func buildSDKRequest(req providers.StreamRequest, httpOptions genai.HTTPOptions) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	contents := make([]*genai.Content, 0, len(req.Context.Messages))
	for _, message := range req.Context.Messages {
		content, err := convertSDKMessage(message)
		if err != nil {
			return nil, nil, err
		}
		if content != nil && len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}
	config := &genai.GenerateContentConfig{HTTPOptions: &httpOptions}
	if req.Context.SystemPrompt != "" {
		config.SystemInstruction = genai.NewContentFromText(req.Context.SystemPrompt, genai.RoleUser)
		config.SystemInstruction.Role = ""
	}
	if req.Options.Temperature != nil {
		temperature := float32(*req.Options.Temperature)
		config.Temperature = &temperature
	}
	if req.Options.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.Options.MaxTokens)
	}
	if len(req.Context.Tools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, 0, len(req.Context.Tools))
		for _, tool := range req.Context.Tools {
			declaration := &genai.FunctionDeclaration{Name: tool.Name, Description: tool.Description}
			if len(tool.InputSchema) > 0 {
				var schema any
				if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
					return nil, nil, fmt.Errorf("decode tool %q schema: %w", tool.Name, err)
				}
				declaration.ParametersJsonSchema = schema
			}
			declarations = append(declarations, declaration)
		}
		config.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	}
	if req.Options.Reasoning != "" && req.Options.Reasoning != ai.ThinkingOff {
		budget := int32(req.Options.ThinkingBudgets[req.Options.Reasoning])
		config.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true, ThinkingBudget: &budget}
	}
	applyGoogleExtras(config, req.Options.Extras)
	if format := req.Options.ResponseFormat; format != nil && format.Type != ai.ResponseFormatText {
		config.ResponseMIMEType = "application/json"
		if format.Type == ai.ResponseFormatJSONSchema {
			schema, err := format.SchemaMap()
			if err != nil {
				return nil, nil, err
			}
			config.ResponseJsonSchema = schema
		}
	}
	return contents, config, nil
}

func convertSDKMessage(message ai.Message) (*genai.Content, error) {
	role := genai.RoleUser
	if message.Role == ai.RoleAssistant {
		role = genai.RoleModel
	}
	parts, err := convertSDKContent(message.Content)
	if err != nil {
		return nil, err
	}
	if message.Role == ai.RoleAssistant {
		for _, call := range message.ToolCalls {
			args := map[string]any{}
			if len(bytes.TrimSpace(call.Arguments)) > 0 {
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					return nil, fmt.Errorf("decode tool call %q arguments: %w", call.Name, err)
				}
			}
			part := &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Name, Args: args}}
			if call.ThoughtSignature != "" {
				signature, err := base64.StdEncoding.DecodeString(call.ThoughtSignature)
				if err != nil {
					return nil, fmt.Errorf("decode tool call %q thought signature: %w", call.Name, err)
				}
				part.ThoughtSignature = signature
			}
			parts = append(parts, part)
		}
	}
	if message.Role == ai.RoleToolResult {
		if message.ToolResult == nil {
			return nil, nil
		}
		text := toolResultText(message.ToolResult.Content)
		parts = []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID:       message.ToolResult.ToolCallID,
			Name:     message.ToolResult.ToolName,
			Response: map[string]any{"content": text},
		}}}
		role = genai.RoleUser
	}
	return &genai.Content{Role: role, Parts: parts}, nil
}

func convertSDKContent(blocks []ai.ContentBlock) ([]*genai.Part, error) {
	parts := make([]*genai.Part, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				parts = append(parts, genai.NewPartFromText(block.Text))
			}
		case ai.ContentImage:
			if len(block.ImageData) > 0 && block.ImageMIMEType != "" {
				parts = append(parts, genai.NewPartFromBytes(block.ImageData, block.ImageMIMEType))
			}
		case ai.ContentThinking:
			part := &genai.Part{Text: block.Thinking, Thought: true}
			if block.Signature != "" {
				signature, err := base64.StdEncoding.DecodeString(block.Signature)
				if err != nil {
					return nil, fmt.Errorf("decode thinking signature: %w", err)
				}
				part.ThoughtSignature = signature
			}
			parts = append(parts, part)
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
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

func applyGoogleExtras(config *genai.GenerateContentConfig, extras providers.ProviderExtras) {
	for key, value := range extras {
		switch key {
		case "cached_content", "cachedContent":
			if cached, ok := value.(string); ok {
				config.CachedContent = cached
			}
		case "response_mime_type", "responseMimeType":
			if mime, ok := value.(string); ok {
				config.ResponseMIMEType = mime
			}
		case "response_json_schema", "responseJsonSchema":
			config.ResponseJsonSchema = value
		}
	}
}

func (p *Provider) inspectSDKRequest(ctx context.Context, req providers.StreamRequest, apiKey string, contents []*genai.Content, config *genai.GenerateContentConfig) {
	if p.inspector == nil {
		return
	}
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	baseURL, version := googleBaseURL(p.modelBaseURL(req.Model))
	versionPath := ""
	if version != "." {
		versionPath = "/" + version
	}
	u := strings.TrimRight(baseURL, "/") + versionPath + "/models/" + url.PathEscape(modelID) + ":streamGenerateContent?alt=sse"
	body, _ := json.Marshal(map[string]any{
		"contents":          contents,
		"systemInstruction": config.SystemInstruction,
		"generationConfig":  config,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Goog-Api-Key", apiKey)
	for key, values := range config.HTTPOptions.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	providers.ApplyRequestMetadata(httpReq.Header, req.Options)
	p.inspector(httpReq)
}

func newGoogleConsumer() func(*genai.GenerateContentResponse) []ai.Event {
	state := &streamState{}
	return func(response *genai.GenerateContentResponse) []ai.Event {
		if response == nil {
			return nil
		}
		events := make([]ai.Event, 0, 4)
		for _, candidate := range response.Candidates {
			if candidate == nil || candidate.Content == nil {
				continue
			}
			for _, part := range candidate.Content.Parts {
				if part == nil {
					continue
				}
				if part.Thought && part.Text != "" {
					events = append(events, ai.ThinkingDelta{ContentIndex: 0, Thinking: part.Text, Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature)})
				} else if part.Text != "" {
					events = append(events, ai.TextDelta{ContentIndex: 0, Text: part.Text})
				}
				if part.InlineData != nil && len(part.InlineData.Data) > 0 {
					events = append(events, ai.ImageEvent{ContentIndex: 0, Data: append([]byte(nil), part.InlineData.Data...), MIMEType: part.InlineData.MIMEType})
				}
				if part.FunctionCall != nil {
					state.toolUse = true
					args, _ := json.Marshal(part.FunctionCall.Args)
					if len(bytes.TrimSpace(args)) == 0 || string(args) == "null" {
						args = json.RawMessage(`{}`)
					}
					id := part.FunctionCall.ID
					if id == "" {
						id = fmt.Sprintf("%s_%d", part.FunctionCall.Name, atomic.AddUint64(&toolIDCounter, 1))
					}
					toolCall := ai.ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: args, ThoughtSignature: base64.StdEncoding.EncodeToString(part.ThoughtSignature)}
					events = append(events, ai.ToolCallEvent{
						ContentIndex: 0,
						ToolCall:     toolCall,
						Complete:     true,
					})
				}
			}
			if candidate.FinishReason != "" {
				events = append(events, ai.StopEvent{Reason: mapSDKFinishReason(candidate.FinishReason, state.toolUse)})
			}
		}
		if usage := sdkUsage(response.UsageMetadata); usage.TotalTokens != 0 || usage.InputTokens != 0 || usage.OutputTokens != 0 {
			events = append(events, ai.UsageEvent{Usage: usage})
		}
		return events
	}
}

func sdkUsage(metadata *genai.GenerateContentResponseUsageMetadata) ai.Usage {
	if metadata == nil {
		return ai.Usage{}
	}
	return (usageMetadata{
		PromptTokenCount:        int64(metadata.PromptTokenCount),
		CandidatesTokenCount:    int64(metadata.CandidatesTokenCount),
		ThoughtsTokenCount:      int64(metadata.ThoughtsTokenCount),
		CachedContentTokenCount: int64(metadata.CachedContentTokenCount),
		TotalTokenCount:         int64(metadata.TotalTokenCount),
	}).normalized()
}

func mapSDKFinishReason(reason genai.FinishReason, toolUse bool) ai.StopReason {
	if toolUse {
		return ai.StopReasonToolUse
	}
	switch reason {
	case genai.FinishReasonMaxTokens:
		return ai.StopReasonLength
	case genai.FinishReasonSafety, genai.FinishReasonMalformedFunctionCall:
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

func normalizeGoogleError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return providers.ClassifyHTTPError(providerID, apiErr.Code, 0, apiErr.Message, err)
	}
	var apiErrPtr *genai.APIError
	if errors.As(err, &apiErrPtr) && apiErrPtr != nil {
		return providers.ClassifyHTTPError(providerID, apiErrPtr.Code, 0, apiErrPtr.Message, err)
	}
	return &providers.NetworkError{Provider: providerID, Err: err}
}

// Capabilities returns the feature set supported by the named Gemini model.
// Gemini 1.5+ supports vision, tools, and reasoning via thinking budgets.
func (p *Provider) Capabilities(modelID string) providers.Capabilities {
	if strings.TrimSpace(modelID) == "" {
		return providers.Capabilities{}
	}
	caps := providers.Capabilities{
		Vision:           true,
		Tools:            true,
		JSONMode:         true,
		StructuredOutput: true,
		Streaming:        true,
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

type streamState struct {
	toolUse bool
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

var _ providers.Provider = (*Provider)(nil)
