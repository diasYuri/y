package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scope identifies the ownership boundary of a context item.
type Scope string

const (
	ScopeGlobal    Scope = "global"
	ScopeTenant    Scope = "tenant"
	ScopeWorkspace Scope = "workspace"
	ScopeProject   Scope = "project"
	ScopeSession   Scope = "session"
	ScopeRequest   Scope = "request"
)

// Request contains stable identity and budgeting information supplied to
// every source. Sources decide which scopes they can answer.
type Request struct {
	TenantID     string `json:"tenant_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	MaxTokens    int64  `json:"max_tokens,omitempty"`
	BasePrompt   string `json:"base_prompt,omitempty"`
	AppendPrompt string `json:"append_prompt,omitempty"`
}

// Item is a context fragment with enough provenance to reproduce the final
// prompt and audit where an instruction came from.
type Item struct {
	ID        string            `json:"id"`
	Content   string            `json:"content"`
	Source    string            `json:"source"`
	Version   string            `json:"version,omitempty"`
	Scope     Scope             `json:"scope"`
	Priority  int               `json:"priority,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ContextSource supplies context without coupling the resolver to a storage
// technology. Name and Version are optional companion interfaces.
type ContextSource interface {
	Load(context.Context, Request) ([]Item, error)
}

type NamedSource interface{ Name() string }
type VersionedSource interface {
	Version(context.Context, Request) (string, error)
}

// SourceInfo records source versions used for a resolution.
type SourceInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Count   int    `json:"count"`
}

// Result is deterministic for a fixed request, source version set, and source
// output. Fingerprint can be persisted with a run for replay diagnostics.
type Result struct {
	Items           []Item       `json:"items,omitempty"`
	SystemPrompt    string       `json:"system_prompt"`
	EstimatedTokens int64        `json:"estimated_tokens"`
	Fingerprint     string       `json:"fingerprint"`
	Sources         []SourceInfo `json:"sources,omitempty"`
	ResolvedAt      time.Time    `json:"resolved_at"`
}

// Resolver composes sources in deterministic precedence order and caches by
// request plus source versions. It is safe for concurrent requests.
type Resolver struct {
	mu      sync.RWMutex
	sources []ContextSource
	cache   map[string]Result
}

func NewResolver(sources ...ContextSource) *Resolver {
	r := &Resolver{cache: make(map[string]Result)}
	for _, source := range sources {
		if source != nil {
			r.sources = append(r.sources, source)
		}
	}
	return r
}

func (r *Resolver) Add(source ContextSource) error {
	if source == nil {
		return errors.New("context source is nil")
	}
	r.mu.Lock()
	r.sources = append(r.sources, source)
	r.cache = make(map[string]Result)
	r.mu.Unlock()
	return nil
}

func (r *Resolver) Sources() []ContextSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ContextSource(nil), r.sources...)
}

func (r *Resolver) Resolve(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sources := r.Sources()
	cacheKey, versions, err := r.cacheKey(ctx, request, sources)
	if err != nil {
		return Result{}, err
	}
	r.mu.RLock()
	if cached, ok := r.cache[cacheKey]; ok {
		r.mu.RUnlock()
		return cloneResult(cached), nil
	}
	r.mu.RUnlock()

	var items []Item
	infos := make([]SourceInfo, 0, len(sources))
	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		loaded, err := source.Load(ctx, request)
		if err != nil {
			return Result{}, fmt.Errorf("load context source %s: %w", sourceName(source, i), err)
		}
		name := sourceName(source, i)
		infos = append(infos, SourceInfo{Name: name, Version: versions[i], Count: len(loaded)})
		for j := range loaded {
			item := loaded[j]
			if item.Source == "" {
				item.Source = name
			}
			items = append(items, cloneItem(item))
		}
	}
	items = deduplicate(items)
	result := buildResult(request, items, infos)
	result.ResolvedAt = time.Now().UTC()
	result.Fingerprint = fingerprint(result)
	r.mu.Lock()
	r.cache[cacheKey] = cloneResult(result)
	r.mu.Unlock()
	return result, nil
}

// Invalidate removes all cached resolutions. Source adapters should call this
// after a mutation when they do not expose a versioned view.
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cache = make(map[string]Result)
	r.mu.Unlock()
}

func (r *Resolver) cacheKey(ctx context.Context, request Request, sources []ContextSource) (string, []string, error) {
	versions := make([]string, len(sources))
	parts := []string{request.TenantID, request.WorkspaceID, request.ProjectID, request.SessionID, request.RequestID, fmt.Sprint(request.MaxTokens), request.BasePrompt, request.AppendPrompt}
	for i, source := range sources {
		name := sourceName(source, i)
		version := ""
		if versioned, ok := source.(VersionedSource); ok {
			var err error
			version, err = versioned.Version(ctx, request)
			if err != nil {
				return "", nil, fmt.Errorf("version context source %s: %w", name, err)
			}
		}
		versions[i] = version
		parts = append(parts, name, version)
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:]), versions, nil
}

func buildResult(request Request, items []Item, infos []SourceInfo) Result {
	var builder strings.Builder
	if request.BasePrompt != "" {
		builder.WriteString(request.BasePrompt)
	}
	var tokens int64
	included := make([]Item, 0, len(items))
	if builder.Len() > 0 {
		tokens = estimateTokens(builder.String())
	}
	for _, item := range items {
		block := fmt.Sprintf("\n\n<context source=%q scope=%q id=%q version=%q>\n%s\n</context>", item.Source, item.Scope, item.ID, item.Version, item.Content)
		blockTokens := estimateTokens(block)
		if request.MaxTokens > 0 && tokens+blockTokens > request.MaxTokens {
			continue
		}
		builder.WriteString(block)
		tokens += blockTokens
		included = append(included, item)
	}
	if request.AppendPrompt != "" {
		blockTokens := estimateTokens(request.AppendPrompt)
		if request.MaxTokens <= 0 || tokens+blockTokens <= request.MaxTokens {
			builder.WriteString("\n\n")
			builder.WriteString(request.AppendPrompt)
			tokens += blockTokens
		}
	}
	return Result{Items: included, SystemPrompt: builder.String(), EstimatedTokens: tokens, Sources: infos}
}

func deduplicate(items []Item) []Item {
	best := make(map[string]Item, len(items))
	for i, item := range items {
		if item.ID == "" {
			item.ID = fmt.Sprintf("%s:%d", item.Source, i)
		}
		key := item.ID
		current, ok := best[key]
		if !ok || item.Priority > current.Priority || item.Priority == current.Priority && scopeRank(item.Scope) > scopeRank(current.Scope) || item.Priority == current.Priority && item.Scope == current.Scope && item.Source < current.Source {
			best[key] = item
		}
	}
	out := make([]Item, 0, len(best))
	for _, item := range best {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if scopeRank(out[i].Scope) != scopeRank(out[j].Scope) {
			return scopeRank(out[i].Scope) > scopeRank(out[j].Scope)
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func scopeRank(scope Scope) int {
	switch scope {
	case ScopeRequest:
		return 6
	case ScopeSession:
		return 5
	case ScopeProject:
		return 4
	case ScopeWorkspace:
		return 3
	case ScopeTenant:
		return 2
	case ScopeGlobal:
		return 1
	default:
		return 0
	}
}

func sourceName(source ContextSource, index int) string {
	if named, ok := source.(NamedSource); ok && strings.TrimSpace(named.Name()) != "" {
		return named.Name()
	}
	return fmt.Sprintf("source-%d", index)
}

func estimateTokens(value string) int64 {
	if value == "" {
		return 0
	}
	return int64((len([]rune(value)) + 3) / 4)
}

func cloneItem(item Item) Item {
	item.Metadata = cloneStringMap(item.Metadata)
	return item
}

func cloneResult(result Result) Result {
	result.Items = make([]Item, len(result.Items))
	for i, item := range result.Items {
		result.Items[i] = cloneItem(item)
	}
	result.Sources = append([]SourceInfo(nil), result.Sources...)
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func fingerprint(result Result) string {
	var builder strings.Builder
	builder.WriteString(result.SystemPrompt)
	for _, item := range result.Items {
		builder.WriteString("\x00")
		builder.WriteString(item.Source)
		builder.WriteString("\x00")
		builder.WriteString(item.Version)
		builder.WriteString("\x00")
		builder.WriteString(item.Content)
	}
	hash := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(hash[:])
}

// MemorySource is a mutable, versioned source useful for tests and request or
// tenant configuration. Set replaces the complete source atomically.
type MemorySource struct {
	mu      sync.RWMutex
	name    string
	version uint64
	items   []Item
}

func NewMemorySource(name string, items ...Item) *MemorySource {
	return &MemorySource{name: name, items: cloneItems(items)}
}
func (s *MemorySource) Name() string { return s.name }
func (s *MemorySource) Load(ctx context.Context, _ Request) ([]Item, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneItems(s.items), nil
}
func (s *MemorySource) Version(ctx context.Context, _ Request) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprint(s.version), nil
}
func (s *MemorySource) Set(items ...Item) {
	s.mu.Lock()
	s.items = cloneItems(items)
	s.version++
	s.mu.Unlock()
}

func cloneItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, item := range items {
		out[i] = cloneItem(item)
	}
	return out
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
