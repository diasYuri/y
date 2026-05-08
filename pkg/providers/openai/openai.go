// Package openai implements the initial OpenAI Responses API provider.
package openai

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
)

const (
	providerID        = "openai"
	defaultBaseURL    = "https://api.openai.com/v1"
	defaultModelID    = "gpt-5"
	defaultMaxEvent   = 1 << 20
	maxErrorBodyBytes = 4 << 10
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

type openaiModel struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

type openaiModelsResponse struct {
	Models []openaiModel `json:"data"`
}

func (p *Provider) fetchModels(ctx context.Context) ([]ai.Model, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = p.envLookup("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("no API key available")
	}

	cfg := retry.DefaultConfig()
	var result openaiModelsResponse

	err := retry.Do(ctx, cfg, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := p.httpClient.Do(req)
		if err != nil {
			return err
		}
		if retry.IsRetryableHTTPStatus(resp.StatusCode) {
			resp.Body.Close()
			return fmt.Errorf("models API returned %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("models API returned %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(&result)
	})
	if err != nil {
		return nil, err
	}

	models := make([]ai.Model, 0, len(result.Models))
	for _, m := range result.Models {
		id := m.ID
		if m.Root != "" {
			id = m.Root
		}
		models = append(models, ai.Model{
			ID:        id,
			Name:      id,
			API:       "openai-responses",
			Provider:  providerID,
			BaseURL:   p.baseURL,
			Reasoning: true,
			Input:     []ai.InputKind{ai.InputText, ai.InputImage},
		})
	}
	return models, nil
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

	payload, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.responseURL(req.Model), bytes.NewReader(body))
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
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

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
		return nil, providers.ClassifyHTTPError(p.ID(), resp.StatusCode, retryafter.Parse(resp.Header.Get("Retry-After")), body, nil)
	}

	return newStream(resp.Body, p.maxEvent, cancel), nil
}
func (p *Provider) client() *http.Client {
	return providers.ApplyCommonClient(p.httpClient, p.middlewares)
}

// CountTokens returns a token estimate. OpenAI does not expose a generally
// available token-count endpoint for the Responses API, so this falls back to
// the shared providers.EstimateTokens heuristic.
func (p *Provider) CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
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
	baseURL := p.baseURL
	if model.BaseURL != "" {
		baseURL = strings.TrimRight(model.BaseURL, "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return baseURL + "/responses"
}

type responseRequest struct {
	Model            string          `json:"model"`
	Input            []inputItem     `json:"input"`
	Stream           bool            `json:"stream"`
	Store            bool            `json:"store"`
	MaxOutputTokens  int64           `json:"max_output_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	PromptCacheKey   string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRet   string          `json:"prompt_cache_retention,omitempty"`
	Tools            []toolParam     `json:"tools,omitempty"`
	Reasoning        *reasoningParam `json:"reasoning,omitempty"`
	Include          []string        `json:"include,omitempty"`
	IncludeObfuscate bool            `json:"include_obfuscation"`
}

type reasoningParam struct {
	Effort string `json:"effort"`
}

type inputItem struct {
	Type    string         `json:"type,omitempty"`
	Role    string         `json:"role,omitempty"`
	Content []inputContent `json:"content,omitempty"`
	CallID  string         `json:"call_id,omitempty"`
	Output  any            `json:"output,omitempty"`
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name,omitempty"`
	Args    string         `json:"arguments,omitempty"`
	Status  string         `json:"status,omitempty"`
}

type inputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type toolParam struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict"`
}

func buildRequest(req providers.StreamRequest) (responseRequest, error) {
	modelID := req.Model.ID
	if modelID == "" {
		modelID = defaultModelID
	}
	out := responseRequest{
		Model:            modelID,
		Stream:           true,
		Store:            false,
		IncludeObfuscate: false,
	}
	if req.Options.MaxTokens > 0 {
		out.MaxOutputTokens = req.Options.MaxTokens
	}
	if req.Options.Temperature != nil {
		out.Temperature = req.Options.Temperature
	}
	if req.Options.CacheRetention != ai.CacheRetentionNone && req.Options.SessionID != "" {
		out.PromptCacheKey = req.Options.SessionID
		if req.Options.CacheRetention == ai.CacheRetentionLong {
			out.PromptCacheRet = "24h"
		}
	}
	if req.Model.Reasoning && req.Options.Reasoning != "" {
		out.Reasoning = &reasoningParam{Effort: string(req.Options.Reasoning)}
		out.Include = []string{"reasoning.encrypted_content"}
	}

	if req.Context.SystemPrompt != "" {
		role := "system"
		if req.Model.Reasoning {
			role = "developer"
		}
		out.Input = append(out.Input, inputItem{
			Role: role,
			Content: []inputContent{{
				Type: "input_text",
				Text: req.Context.SystemPrompt,
			}},
		})
	}
	for _, msg := range req.Context.Messages {
		items, err := convertMessage(msg)
		if err != nil {
			return responseRequest{}, err
		}
		out.Input = append(out.Input, items...)
	}
	for _, tool := range req.Context.Tools {
		out.Tools = append(out.Tools, toolParam{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.InputSchema,
			Strict:      false,
		})
	}
	return out, nil
}

func convertMessage(msg ai.Message) ([]inputItem, error) {
	switch msg.Role {
	case ai.RoleUser:
		content, err := convertContent(msg.Content, "input_text")
		if err != nil {
			return nil, err
		}
		if len(content) == 0 {
			return nil, nil
		}
		return []inputItem{{Role: "user", Content: content}}, nil
	case ai.RoleAssistant:
		items := make([]inputItem, 0, 1+len(msg.ToolCalls))
		content, err := convertContent(msg.Content, "output_text")
		if err != nil {
			return nil, err
		}
		if len(content) > 0 {
			items = append(items, inputItem{Role: "assistant", Content: content, Status: "completed"})
		}
		for _, call := range msg.ToolCalls {
			args := string(call.Arguments)
			if args == "" {
				args = "{}"
			}
			callID, itemID := splitToolCallID(call.ID)
			items = append(items, inputItem{
				Type:   "function_call",
				ID:     itemID,
				CallID: callID,
				Name:   call.Name,
				Args:   args,
			})
		}
		return items, nil
	case ai.RoleToolResult:
		if msg.ToolResult == nil {
			return nil, nil
		}
		callID, _ := splitToolCallID(msg.ToolResult.ToolCallID)
		output := toolResultOutput(msg.ToolResult.Content)
		return []inputItem{{
			Type:   "function_call_output",
			CallID: callID,
			Output: output,
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported message role %q", msg.Role)
	}
}

func convertContent(blocks []ai.ContentBlock, textType string) ([]inputContent, error) {
	content := make([]inputContent, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if block.Text != "" {
				content = append(content, inputContent{Type: textType, Text: block.Text})
			}
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			content = append(content, inputContent{
				Type:     "input_image",
				ImageURL: "data:" + block.ImageMIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.ImageData),
				Detail:   "auto",
			})
		case ai.ContentThinking:
			continue
		default:
			return nil, fmt.Errorf("unsupported content type %q", block.Type)
		}
	}
	return content, nil
}

func toolResultOutput(blocks []ai.ContentBlock) any {
	text := strings.Builder{}
	var imageParts []inputContent
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentText:
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
		case ai.ContentImage:
			if len(block.ImageData) == 0 || block.ImageMIMEType == "" {
				continue
			}
			imageParts = append(imageParts, inputContent{
				Type:     "input_image",
				ImageURL: "data:" + block.ImageMIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.ImageData),
				Detail:   "auto",
			})
		}
	}
	if len(imageParts) == 0 {
		if text.Len() == 0 {
			return ""
		}
		return text.String()
	}
	parts := make([]inputContent, 0, len(imageParts)+1)
	if text.Len() > 0 {
		parts = append(parts, inputContent{Type: "input_text", Text: text.String()})
	}
	parts = append(parts, imageParts...)
	return parts
}

func splitToolCallID(id string) (callID, itemID string) {
	callID, itemID, ok := strings.Cut(id, "|")
	if !ok {
		return id, ""
	}
	return callID, itemID
}

var _ providers.Provider = (*Provider)(nil)
