package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/diasYuri/y/pkg/search"
)

const (
	// CapabilityWebSearch identifies a backend web search operation.
	CapabilityWebSearch Capability = "web.search"
	WebSearchToolName              = "web_search"
	maxSearchQueryBytes            = 512
	maxSearchResults               = 20
)

var safeSearchMetadataKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// NewWebSearchTool creates the canonical registry descriptor and handler for a
// SearchProvider. Optional input fields and their schema are derived from the
// provider's declared capabilities.
func NewWebSearchTool(provider search.Provider) (ToolDescriptor, ToolHandler, error) {
	if provider == nil {
		return ToolDescriptor{}, nil, fmt.Errorf("web search provider is nil: %w", ErrInvalidTool)
	}
	caps := provider.Capabilities()
	properties := map[string]any{
		"query": map[string]any{"type": "string", "description": "Search query"},
	}
	if caps.AllowedDomains {
		properties["allowed_domains"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Restrict results to these domains"}
	}
	if caps.Location {
		properties["location"] = map[string]any{"type": "string", "description": "Search location or locale context"}
	}
	if caps.ContextSize {
		properties["context_size"] = map[string]any{
			"type": "string", "enum": []string{string(search.ContextSizeLow), string(search.ContextSizeMedium), string(search.ContextSizeHigh)},
			"description": "Amount of supporting result context to return; this does not include user prompt text",
		}
	}
	if caps.MaxResults {
		properties["max_results"] = map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchResults, "description": "Maximum number of results"}
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": []string{"query"}, "additionalProperties": false,
	})
	if err != nil {
		return ToolDescriptor{}, nil, fmt.Errorf("build web search schema: %w", err)
	}
	desc := ToolDescriptor{
		Name:          WebSearchToolName,
		Description:   "Search the web and return citeable results with titles, URLs, and snippets.",
		InputSchema:   schema,
		Capabilities:  []Capability{CapabilityWebSearch},
		Sensitive:     true,
		ExecutionMode: ExecutionSequential,
	}
	handler := ToolHandlerFunc(func(ctx context.Context, toolReq ToolRequest) (ToolResponse, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return ToolResponse{}, err
		}
		if err := validateSearchArgumentFields(toolReq.Arguments, caps); err != nil {
			return ToolResponse{}, err
		}
		var req search.Request
		if err := json.Unmarshal(toolReq.Arguments, &req); err != nil {
			return ToolResponse{}, searchToolError("invalid_search_request", "decode web search arguments", errors.Join(search.ErrInvalidRequest, err))
		}
		if err := validateSearchRequest(req, caps); err != nil {
			return ToolResponse{}, err
		}
		response, err := provider.Search(ctx, req)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("web search: %w", err)
		}
		if err := validateSearchResponse(response); err != nil {
			return ToolResponse{}, err
		}
		details, err := json.Marshal(response)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("encode web search results: %w", err)
		}
		return ToolResponse{
			Content: []ContentBlock{{Type: ContentText, Text: formatSearchResults(response)}},
			Details: details,
		}, nil
	})
	return desc, handler, nil
}

func validateSearchArgumentFields(arguments json.RawMessage, caps search.Capabilities) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return searchToolError("invalid_search_request", "decode web search arguments", errors.Join(search.ErrInvalidRequest, err))
	}
	for field := range fields {
		supported := true
		switch field {
		case "query":
		case "allowed_domains":
			supported = caps.AllowedDomains
		case "location":
			supported = caps.Location
		case "context_size":
			supported = caps.ContextSize
		case "max_results":
			supported = caps.MaxResults
		default:
			return searchToolError("invalid_search_request", fmt.Sprintf("unknown search field %q", field), search.ErrInvalidRequest)
		}
		if !supported {
			return searchToolError("unsupported_search_control", fmt.Sprintf("backend does not support %s", field), search.ErrUnsupportedControl)
		}
	}
	if raw, ok := fields["max_results"]; ok {
		var limit int
		if err := json.Unmarshal(raw, &limit); err != nil || limit < 1 || limit > maxSearchResults {
			return searchToolError("invalid_search_request", "max_results must be an integer from 1 to 20", search.ErrInvalidRequest)
		}
	}
	if raw, ok := fields["context_size"]; ok {
		var size search.ContextSize
		if err := json.Unmarshal(raw, &size); err != nil || !validSearchContextSize(size) {
			return searchToolError("invalid_search_request", "context_size must be low, medium, or high", search.ErrInvalidRequest)
		}
	}
	return nil
}

// RegisterWebSearchProvider registers the canonical web search tool and
// returns its owner handle. For component-owned lifecycle, pass the descriptor
// and handler from NewWebSearchTool to Host.RegisterTool instead.
func RegisterWebSearchProvider(registry *Registry, provider search.Provider) (*ToolRegistration, error) {
	if registry == nil {
		return nil, fmt.Errorf("web search registry is nil: %w", ErrInvalidTool)
	}
	desc, handler, err := NewWebSearchTool(provider)
	if err != nil {
		return nil, err
	}
	return registry.Register(desc, handler)
}

func validateSearchRequest(req search.Request, caps search.Capabilities) error {
	if strings.TrimSpace(req.Query) == "" || len(req.Query) > maxSearchQueryBytes {
		return searchToolError("invalid_search_request", "query must contain 1 to 512 bytes", search.ErrInvalidRequest)
	}
	if len(req.AllowedDomains) > 0 && !caps.AllowedDomains {
		return searchToolError("unsupported_search_control", "backend does not support allowed_domains", search.ErrUnsupportedControl)
	}
	if req.Location != "" && !caps.Location {
		return searchToolError("unsupported_search_control", "backend does not support location", search.ErrUnsupportedControl)
	}
	if req.ContextSize != "" && !caps.ContextSize {
		return searchToolError("unsupported_search_control", "backend does not support context_size", search.ErrUnsupportedControl)
	}
	if req.ContextSize != "" && !validSearchContextSize(req.ContextSize) {
		return searchToolError("invalid_search_request", "context_size must be low, medium, or high", search.ErrInvalidRequest)
	}
	if req.MaxResults != 0 && !caps.MaxResults {
		return searchToolError("unsupported_search_control", "backend does not support max_results", search.ErrUnsupportedControl)
	}
	if req.MaxResults < 0 || req.MaxResults > maxSearchResults {
		return searchToolError("invalid_search_request", "max_results must be between 1 and 20 when set", search.ErrInvalidRequest)
	}
	if len(req.AllowedDomains) > 20 {
		return searchToolError("invalid_search_request", "allowed_domains supports at most 20 domains", search.ErrInvalidRequest)
	}
	for _, domain := range req.AllowedDomains {
		if !validSearchDomain(domain) {
			return searchToolError("invalid_search_request", fmt.Sprintf("invalid allowed domain %q", domain), search.ErrInvalidRequest)
		}
	}
	if len(req.Location) > 200 {
		return searchToolError("invalid_search_request", "location exceeds its size limit", search.ErrInvalidRequest)
	}
	return nil
}

func validSearchContextSize(size search.ContextSize) bool {
	switch size {
	case search.ContextSizeLow, search.ContextSizeMedium, search.ContextSizeHigh:
		return true
	default:
		return false
	}
}

func validSearchDomain(domain string) bool {
	if domain == "" || strings.TrimSpace(domain) != domain || strings.ContainsAny(domain, "/:@?#* \t\r\n") {
		return false
	}
	u, err := url.Parse("https://" + domain)
	return err == nil && u.Hostname() == domain && !strings.Contains(domain, "..")
}

func validateSearchResponse(response search.Response) error {
	if len(response.Results) > maxSearchResults {
		return searchToolError("invalid_search_result", "backend returned more than 20 results", search.ErrInvalidResult)
	}
	for i, result := range response.Results {
		parsed, err := url.Parse(result.URL)
		if strings.TrimSpace(result.Title) == "" || len(result.Title) > 1000 || len(result.Snippet) > 5000 || len(result.URL) > 4096 || err != nil || parsed == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
			return searchToolError("invalid_search_result", fmt.Sprintf("result %d has invalid title, URL, or snippet", i), search.ErrInvalidResult)
		}
		if len(result.Metadata) > 16 {
			return searchToolError("invalid_search_result", fmt.Sprintf("result %d has too much metadata", i), search.ErrInvalidResult)
		}
		for key, value := range result.Metadata {
			if !safeSearchMetadataKey.MatchString(key) || len(value) > 512 {
				return searchToolError("invalid_search_result", fmt.Sprintf("result %d has invalid safe metadata", i), search.ErrInvalidResult)
			}
		}
	}
	return nil
}

func formatSearchResults(response search.Response) string {
	var out strings.Builder
	for i, result := range response.Results {
		fmt.Fprintf(&out, "%d. %s\n   %s\n   %s", i+1, result.Title, result.URL, result.Snippet)
		if i+1 < len(response.Results) {
			out.WriteString("\n")
		}
	}
	return out.String()
}

func searchToolError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
