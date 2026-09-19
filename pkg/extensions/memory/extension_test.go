package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	ycontext "github.com/diasYuri/y/pkg/context"
	runtimeextensions "github.com/diasYuri/y/pkg/extensions"
	pmemory "github.com/diasYuri/y/pkg/memory"
	"github.com/diasYuri/y/pkg/tools"
)

type testStore struct {
	mu      sync.Mutex
	values  map[string]pmemory.Memory
	version int
}

func newTestStore() *testStore { return &testStore{values: make(map[string]pmemory.Memory)} }
func (s *testStore) Search(ctx context.Context, query pmemory.Query) ([]pmemory.Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pmemory.Memory
	for _, value := range s.values {
		if value.Status == pmemory.StatusForgotten {
			continue
		}
		if len(query.Terms) == 0 || containsAny(value, query.Terms) {
			out = append(out, value)
		}
	}
	return out, nil
}
func (s *testStore) Read(ctx context.Context, id string) (pmemory.Memory, error) {
	if err := ctx.Err(); err != nil {
		return pmemory.Memory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return pmemory.Memory{}, pmemory.ErrNotFound
	}
	return value, nil
}
func (s *testStore) Save(ctx context.Context, value pmemory.Memory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID = pmemory.StableID(value)
	}
	s.mu.Lock()
	s.values[value.ID] = value
	s.version++
	s.mu.Unlock()
	return nil
}
func (s *testStore) Forget(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.values, id)
	s.version++
	s.mu.Unlock()
	return nil
}
func (s *testStore) Version(ctx context.Context, _ ycontext.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(rune(s.version)), nil
}
func containsAny(value pmemory.Memory, terms []string) bool {
	text := value.Summary + " " + value.Content
	for _, term := range terms {
		if len(term) > 0 && containsFold(text, term) {
			return true
		}
	}
	return false
}
func containsFold(value, term string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(term))
}

func TestExtensionRegistersExplicitToolsAndEnforcesSaveIntent(t *testing.T) {
	store := newTestStore()
	ext, err := New(WithStore(store), WithConfig(Config{Enabled: true, DedicatedTools: true, AutoExtract: false, FailOpen: false}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ext.Close(context.Background()) }()
	registry := tools.NewRegistry()
	if err := ext.RegisterTools(registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != 5 {
		t.Fatalf("registered tools = %d", len(registry.List()))
	}
	args, _ := json.Marshal(saveArgs{Type: pmemory.KindPreference, Scope: "global", Summary: "language", Content: "Use Go", Explicit: false})
	_, err = registry.Handle(context.Background(), tools.ToolRequest{Name: "memory.save", Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Fatalf("implicit save err = %v", err)
	}
	if err := ext.beforeMessage(agent.WithRuntimeIdentity(context.Background(), agent.RuntimeIdentity{RunID: "run-1"}), &ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "remember this"}}}); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(saveArgs{Type: pmemory.KindPreference, Scope: "global", Summary: "language", Content: "Use Go", Explicit: true})
	response, err := registry.Handle(context.Background(), tools.ToolRequest{Name: "memory.save", Arguments: args, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Content[0].Text, "saved") {
		t.Fatalf("save response = %#v", response)
	}
	values, err := store.Search(context.Background(), pmemory.Query{})
	if err != nil || len(values) != 1 {
		t.Fatalf("saved values = %#v, err=%v", values, err)
	}
	forgetArgs, _ := json.Marshal(forgetArgs{ID: values[0].ID, Explicit: true})
	if _, err := registry.Handle(context.Background(), tools.ToolRequest{Name: "memory.forget", Arguments: forgetArgs, RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), values[0].ID); !errors.Is(err, pmemory.ErrNotFound) {
		t.Fatalf("read after forget err = %v", err)
	}
}

func TestConfigFromSettingsOwnsMemorySchema(t *testing.T) {
	config, err := ConfigFromSettings(map[string]string{
		"enabled":            "false",
		"mode":               "automatic",
		"profile":            "stateless",
		"backend":            "noop",
		"max_items":          "9",
		"max_context_tokens": "800",
	}, func(key string) string {
		if key == "Y_EXTENSION_MEMORY" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.Mode != ModeAutomatic || config.Profile != "stateless" || config.MaxItems != 9 || config.MaxContextTokens != 800 {
		t.Fatalf("config = %#v", config)
	}
	if _, err := ConfigFromSettings(map[string]string{"unknown": "true"}, func(string) string { return "" }); err == nil {
		t.Fatal("unknown extension setting was accepted")
	}
}

func TestExtensionInstallsThroughGenericHost(t *testing.T) {
	ext, err := New(WithStore(newTestStore()), WithConfig(Config{Enabled: true, DedicatedTools: true, AutoExtract: false, FailOpen: true}))
	if err != nil {
		t.Fatal(err)
	}
	host := runtimeextensions.NewHost(tools.NewRegistry())
	if err := host.Install(ext); err != nil {
		t.Fatal(err)
	}
	if got := len(host.Registry.List()); got != 5 {
		t.Fatalf("registered tools = %d", got)
	}
	if got := len(host.AgentOptions()); got != 2 {
		t.Fatalf("agent options = %d, want resolver and hooks", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitConsentIsIsolatedByRun(t *testing.T) {
	ext, err := New(WithConfig(Config{Enabled: true, AutoExtract: false}))
	if err != nil {
		t.Fatal(err)
	}
	remember := agent.WithRuntimeIdentity(context.Background(), agent.RuntimeIdentity{RunID: "run-remember"})
	other := agent.WithRuntimeIdentity(context.Background(), agent.RuntimeIdentity{RunID: "run-other"})
	message := func(text string) *ai.Message {
		return &ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: text}}}
	}
	if err := ext.beforeMessage(remember, message("remember this")); err != nil {
		t.Fatal(err)
	}
	if err := ext.beforeMessage(other, message("just answer this")); err != nil {
		t.Fatal(err)
	}
	if !ext.explicitRequest("run-remember", false) {
		t.Fatal("remember request lost its consent")
	}
	if ext.explicitRequest("run-other", false) {
		t.Fatal("consent leaked between runs")
	}
}

func TestDirectReadAndForgetAreScoped(t *testing.T) {
	store := newTestStore()
	foreign := pmemory.Memory{ID: "foreign", Kind: pmemory.KindReference, Scope: ycontext.ScopeProject, Summary: "private", Content: "other project", Source: pmemory.SourceRef{ProjectID: "project-a"}}
	if err := store.Save(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	ext, err := New(WithStore(store), WithConfig(Config{Enabled: true, ProjectID: "project-b", DedicatedTools: true, AutoExtract: false}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ext.Close(context.Background()) }()
	registry := tools.NewRegistry()
	if err := ext.RegisterTools(registry); err != nil {
		t.Fatal(err)
	}
	readArgs, _ := json.Marshal(readArgs{ID: foreign.ID})
	request := tools.ToolRequest{Name: "memory.read", Arguments: readArgs, ProjectID: "project-b", RunID: "run-1"}
	if _, err := registry.Handle(context.Background(), request); !errors.Is(err, pmemory.ErrNotFound) {
		t.Fatalf("cross-project read err = %v", err)
	}
	if err := ext.beforeMessage(agent.WithRuntimeIdentity(context.Background(), agent.RuntimeIdentity{RunID: "run-1"}), &ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "forget this"}}}); err != nil {
		t.Fatal(err)
	}
	forgetArgs, _ := json.Marshal(forgetArgs{ID: foreign.ID, Explicit: true})
	request.Name = "memory.forget"
	request.Arguments = forgetArgs
	if _, err := registry.Handle(context.Background(), request); !errors.Is(err, pmemory.ErrNotFound) {
		t.Fatalf("cross-project forget err = %v", err)
	}
	if _, err := store.Read(context.Background(), foreign.ID); err != nil {
		t.Fatalf("foreign memory was deleted: %v", err)
	}
}
