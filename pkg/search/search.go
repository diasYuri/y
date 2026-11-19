// Package search defines provider-neutral contracts for web search backends.
// It deliberately contains no transport, network client, or vendor adapter.
package search

import (
	"context"
	"errors"
)

var (
	// ErrInvalidRequest identifies invalid, provider-independent search input.
	ErrInvalidRequest = errors.New("invalid search request")
	// ErrUnsupportedControl identifies a requested control the backend cannot apply.
	ErrUnsupportedControl = errors.New("unsupported search control")
	// ErrInvalidResult identifies a result that cannot be safely cited.
	ErrInvalidResult = errors.New("invalid search result")
)

// ContextSize describes the requested amount of supporting result context.
// It never carries user prompt text.
type ContextSize string

const (
	ContextSizeLow    ContextSize = "low"
	ContextSizeMedium ContextSize = "medium"
	ContextSizeHigh   ContextSize = "high"
)

// Capabilities reports which optional controls a backend can apply. Tool
// adapters should advertise only controls reported here.
type Capabilities struct {
	AllowedDomains bool
	Location       bool
	ContextSize    bool
	MaxResults     bool
}

// Request is a provider-neutral web search. A backend must enforce every
// requested control it accepts; unsupported controls must return
// ErrUnsupportedControl instead of being silently ignored.
type Request struct {
	Query          string      `json:"query"`
	AllowedDomains []string    `json:"allowed_domains,omitempty"`
	Location       string      `json:"location,omitempty"`
	ContextSize    ContextSize `json:"context_size,omitempty"`
	MaxResults     int         `json:"max_results,omitempty"`
}

// Result contains the citation fields callers may safely expose to an agent.
// Metadata is limited to non-sensitive, provider-neutral facts.
type Result struct {
	Title    string            `json:"title"`
	URL      string            `json:"url"`
	Snippet  string            `json:"snippet"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Response contains ordered, citeable results from one search.
type Response struct {
	Results []Result `json:"results"`
}

// Provider performs web search without prescribing transport or vendor.
// Implementations must honor context cancellation and preserve error causes
// with %w when wrapping them.
type Provider interface {
	Capabilities() Capabilities
	Search(context.Context, Request) (Response, error)
}
