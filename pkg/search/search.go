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

// SafeSearch is the requested level of explicit-content filtering.
// An empty value leaves the provider's configured default unchanged.
type SafeSearch string

const (
	// SafeSearchOff disables explicit-content filtering.
	SafeSearchOff SafeSearch = "off"
	// SafeSearchModerate enables the provider's moderate explicit-content filter.
	SafeSearchModerate SafeSearch = "moderate"
	// SafeSearchStrict enables the provider's strict explicit-content filter.
	SafeSearchStrict SafeSearch = "strict"
)

// DateRange constrains publication dates to inclusive calendar dates in
// YYYY-MM-DD form. At least one bound must be set. Backends without a
// publication-date filter must not advertise the DateRange capability.
type DateRange struct {
	// Start is the inclusive first date, or empty for an open lower bound.
	Start string `json:"start,omitempty"`
	// End is the inclusive last date, or empty for an open upper bound.
	End string `json:"end,omitempty"`
}

// Capabilities reports which optional controls a backend can enforce. A true
// flag means the provider applies the full control semantics. Tool adapters
// should advertise only controls reported here.
type Capabilities struct {
	AllowedDomains  bool // Restrict result hosts to listed domains and their subdomains.
	ExcludedDomains bool // Exclude result hosts matching listed domains or subdomains.
	Location        bool // Geographic or locale hint with provider-defined mapping.
	ContextSize     bool // Selection of the amount of supporting result context.
	MaxResults      bool // Caller-provided upper bound on the result count.
	Country         bool // Search country using an ISO 3166-1 alpha-2 code.
	Language        bool // Result language using an ISO 639-1 code.
	DateRange       bool // Inclusive publication-date bounds in YYYY-MM-DD form.
	SafeSearch      bool // Off, moderate, and strict explicit-content filtering levels.
}

// Request is a provider-neutral web search. A backend must enforce every
// requested control it accepts; unsupported controls must return
// ErrUnsupportedControl instead of being silently ignored. Country uses an
// ISO 3166-1 alpha-2 code, Language uses an ISO 639-1 code, and SafeSearch may
// be left empty to retain the backend's configured default. Domain filters
// match hostnames case-insensitively, including subdomains; exclusions take
// precedence when a host matches both lists.
type Request struct {
	Query           string      `json:"query"`
	AllowedDomains  []string    `json:"allowed_domains,omitempty"`  // Restrict results to these domains and their subdomains.
	ExcludedDomains []string    `json:"excluded_domains,omitempty"` // Exclude these domains and their subdomains; takes precedence.
	Location        string      `json:"location,omitempty"`         // Free-form geographic or locale hint.
	ContextSize     ContextSize `json:"context_size,omitempty"`     // Amount of supporting context to return.
	MaxResults      int         `json:"max_results,omitempty"`      // Maximum result count requested.
	Country         string      `json:"country,omitempty"`          // ISO 3166-1 alpha-2 target country.
	Language        string      `json:"language,omitempty"`         // ISO 639-1 content language.
	DateRange       *DateRange  `json:"date_range,omitempty"`       // Inclusive publication-date bounds.
	SafeSearch      SafeSearch  `json:"safe_search,omitempty"`      // Explicit-content filtering level.
}

// Result contains bounded citation evidence callers may safely expose to an
// agent. PageDate is the provider's most relevant reported page date, which may
// represent publication or modification; it uses YYYY-MM-DD. Highlights are
// additional excerpts from the result page. Metadata is limited to
// non-sensitive facts and may identify the search provider.
type Result struct {
	Title      string            `json:"title"`
	URL        string            `json:"url"`
	Snippet    string            `json:"snippet"`
	PageDate   string            `json:"page_date,omitempty"`
	Highlights []string          `json:"highlights,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
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
