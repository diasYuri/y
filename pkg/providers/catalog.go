package providers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/ai"
)

var (
	ErrModelNotFound      = errors.New("provider model not found")
	ErrProviderRegistered = errors.New("provider already registered")
)

// ModelCatalog is the stable lookup surface between request selection and a
// concrete provider. A catalog may be backed by static models, remote refresh,
// or a tenant-specific service.
type ModelCatalog interface {
	List(context.Context) ([]ai.Model, error)
	Lookup(context.Context, ai.ProviderID, string) (ai.Model, error)
	Refresh(context.Context, ai.ProviderID) error
}

// Catalog combines the model lists of registered providers and caches them
// for a bounded interval. Registration and refresh are safe concurrently with
// request lookups.
type Catalog struct {
	mu        sync.RWMutex
	sources   map[ai.ProviderID]Provider
	models    map[ai.ProviderID][]ai.Model
	refreshed map[ai.ProviderID]time.Time
	ttl       time.Duration
	now       func() time.Time
}

// CatalogOption configures a Catalog.
type CatalogOption func(*Catalog)

func WithCatalogTTL(ttl time.Duration) CatalogOption {
	return func(c *Catalog) { c.ttl = ttl }
}

func NewCatalog(options ...CatalogOption) *Catalog {
	c := &Catalog{
		sources:   make(map[ai.ProviderID]Provider),
		models:    make(map[ai.ProviderID][]ai.Model),
		refreshed: make(map[ai.ProviderID]time.Time),
		ttl:       5 * time.Minute,
		now:       time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	return c
}

// Register adds a provider source. It does not perform network I/O.
func (c *Catalog) Register(provider Provider) error {
	if provider == nil {
		return errors.New("provider is nil")
	}
	id := ai.ProviderID(provider.ID())
	if id == "" {
		return errors.New("provider ID is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.sources[id]; exists {
		return fmt.Errorf("%w: %s", ErrProviderRegistered, id)
	}
	c.sources[id] = provider
	return nil
}

// List returns a stable, deduplicated model list. Stale cache entries are
// refreshed lazily; callers that need explicit freshness can call Refresh.
func (c *Catalog) List(ctx context.Context) ([]ai.Model, error) {
	c.mu.RLock()
	ids := make([]ai.ProviderID, 0, len(c.sources))
	for id := range c.sources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	c.mu.RUnlock()

	var out []ai.Model
	for _, id := range ids {
		models, err := c.modelsFor(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, models...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].ID < out[j].ID
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

func (c *Catalog) modelsFor(ctx context.Context, id ai.ProviderID) ([]ai.Model, error) {
	c.mu.RLock()
	models, refreshed, exists := c.models[id], c.refreshed[id], c.sources[id]
	stale := c.ttl > 0 && c.clock().Sub(refreshed) >= c.ttl
	if !stale && refreshed.IsZero() {
		stale = true
	}
	if !stale {
		out := cloneModels(models)
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()
	if exists == nil {
		return nil, fmt.Errorf("provider %q is not registered", id)
	}
	if err := c.refresh(ctx, id, exists); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneModels(c.models[id]), nil
}

func (c *Catalog) refresh(ctx context.Context, id ai.ProviderID, provider Provider) error {
	models, err := provider.Models(ctx)
	if err != nil {
		return fmt.Errorf("refresh models for %s: %w", id, err)
	}
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = id
		}
		if models[i].Provider != id {
			return fmt.Errorf("provider %s returned model %s owned by %s", id, models[i].ID, models[i].Provider)
		}
		providerCaps := provider.Capabilities(models[i].ID)
		if models[i].Capabilities == (ai.ModelCapabilities{}) {
			models[i].Capabilities = ai.ModelCapabilities{
				Vision:           providerCaps.Vision || containsInput(models[i].Input, ai.InputImage),
				Tools:            providerCaps.Tools,
				Reasoning:        providerCaps.Reasoning || models[i].Reasoning,
				PromptCache:      providerCaps.PromptCache,
				JSONMode:         providerCaps.JSONMode,
				StructuredOutput: providerCaps.StructuredOutput,
				Streaming:        providerCaps.Streaming,
			}
		} else {
			models[i].Capabilities.Vision = models[i].Capabilities.Vision || providerCaps.Vision
			models[i].Capabilities.Tools = models[i].Capabilities.Tools || providerCaps.Tools
			models[i].Capabilities.Reasoning = models[i].Capabilities.Reasoning || providerCaps.Reasoning
			models[i].Capabilities.PromptCache = models[i].Capabilities.PromptCache || providerCaps.PromptCache
			models[i].Capabilities.JSONMode = models[i].Capabilities.JSONMode || providerCaps.JSONMode
			models[i].Capabilities.StructuredOutput = models[i].Capabilities.StructuredOutput || providerCaps.StructuredOutput
			models[i].Capabilities.Streaming = models[i].Capabilities.Streaming || providerCaps.Streaming
		}
	}
	c.mu.Lock()
	c.models[id] = cloneModels(models)
	c.refreshed[id] = c.clock()
	c.mu.Unlock()
	return nil
}

func containsInput(inputs []ai.InputKind, wanted ai.InputKind) bool {
	for _, input := range inputs {
		if input == wanted {
			return true
		}
	}
	return false
}

// Refresh invalidates and reloads one provider. An empty provider ID refreshes
// all sources.
func (c *Catalog) Refresh(ctx context.Context, providerID ai.ProviderID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	c.mu.RLock()
	ids := []ai.ProviderID{providerID}
	if providerID == "" {
		ids = ids[:0]
		for id := range c.sources {
			ids = append(ids, id)
		}
	}
	sources := make(map[ai.ProviderID]Provider, len(ids))
	for _, id := range ids {
		sources[id] = c.sources[id]
	}
	c.mu.RUnlock()
	for _, id := range ids {
		if sources[id] == nil {
			return fmt.Errorf("provider %q is not registered", id)
		}
		if err := c.refresh(ctx, id, sources[id]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) Lookup(ctx context.Context, providerID ai.ProviderID, modelID string) (ai.Model, error) {
	if modelID == "" {
		return ai.Model{}, fmt.Errorf("%w: model ID is empty", ErrModelNotFound)
	}
	models, err := c.modelsFor(ctx, providerID)
	if err != nil {
		return ai.Model{}, err
	}
	for _, model := range models {
		if model.ID == modelID {
			return model, nil
		}
	}
	return ai.Model{}, fmt.Errorf("%w: %s/%s", ErrModelNotFound, providerID, modelID)
}

// Provider returns the registered implementation for a model selection.
func (c *Catalog) Provider(providerID ai.ProviderID) (Provider, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	provider, ok := c.sources[providerID]
	return provider, ok
}

// ResolveProvider implements the request-scoped provider resolver used by
// the distributed runner.
func (c *Catalog) ResolveProvider(ctx context.Context, providerID ai.ProviderID) (Provider, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	provider, ok := c.Provider(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", providerID)
	}
	return provider, nil
}

func (c *Catalog) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func cloneModels(models []ai.Model) []ai.Model {
	if len(models) == 0 {
		return nil
	}
	out := make([]ai.Model, len(models))
	for i, model := range models {
		out[i] = model
		out[i].Input = append([]ai.InputKind(nil), model.Input...)
		out[i].Headers = make(map[string]string, len(model.Headers))
		for key, value := range model.Headers {
			out[i].Headers[key] = value
		}
		out[i].Metadata = append([]byte(nil), model.Metadata...)
	}
	return out
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
