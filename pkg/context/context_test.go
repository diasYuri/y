package context

import (
	"context"
	"strings"
	"testing"
)

func TestResolverPrecedenceDeduplicationAndVersionedCache(t *testing.T) {
	global := NewMemorySource("global", Item{ID: "policy", Content: "global", Scope: ScopeGlobal, Priority: 1})
	request := NewMemorySource("request", Item{ID: "policy", Content: "request", Scope: ScopeRequest, Priority: 2}, Item{ID: "extra", Content: "extra", Scope: ScopeRequest, Priority: 1})
	resolver := NewResolver(global, request)
	result, err := resolver.Resolve(context.Background(), Request{BasePrompt: "base", AppendPrompt: "answer", MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Content != "request" {
		t.Fatalf("items = %#v, want request precedence and one duplicate", result.Items)
	}
	if !strings.Contains(result.SystemPrompt, `source="request"`) || result.Fingerprint == "" {
		t.Fatalf("prompt/fingerprint = %q / %q", result.SystemPrompt, result.Fingerprint)
	}

	cached, err := resolver.Resolve(context.Background(), Request{BasePrompt: "base", AppendPrompt: "answer", MaxTokens: 100})
	if err != nil || cached.Fingerprint != result.Fingerprint {
		t.Fatalf("cached result = %#v, err=%v", cached, err)
	}
	request.Set(Item{ID: "policy", Content: "changed", Scope: ScopeRequest, Priority: 2})
	changed, err := resolver.Resolve(context.Background(), Request{BasePrompt: "base", AppendPrompt: "answer", MaxTokens: 100})
	if err != nil || changed.Fingerprint == result.Fingerprint || !strings.Contains(changed.SystemPrompt, "changed") {
		t.Fatalf("changed result = %#v, err=%v", changed, err)
	}
}

func TestResolverItemsMatchTokenBudgetSelection(t *testing.T) {
	resolver := NewResolver(NewMemorySource("global", Item{ID: "large", Content: "this item is intentionally larger than the budget", Scope: ScopeGlobal}))
	result, err := resolver.Resolve(context.Background(), Request{MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || strings.Contains(result.SystemPrompt, "large") {
		t.Fatalf("budget result = %#v, want skipped item omitted", result)
	}
}

func TestResolverCacheKeyIncludesQuery(t *testing.T) {
	source := &querySource{}
	resolver := NewResolver(source)
	first, err := resolver.Resolve(context.Background(), Request{Query: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve(context.Background(), Request{Query: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint || first.Items[0].Content == second.Items[0].Content {
		t.Fatalf("query results were cached together: %#v / %#v", first, second)
	}
}

type querySource struct{}

func (*querySource) Load(_ context.Context, request Request) ([]Item, error) {
	return []Item{{ID: "query", Content: request.Query, Scope: ScopeRequest}}, nil
}
