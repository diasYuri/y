package subagents_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/extensions/subagents"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/providertest"
	"github.com/yuri/y/pkg/tools"
)

func TestManager_ExecutesChildrenWithBoundedConcurrency(t *testing.T) {
	provider := newControlledProvider()
	manager, err := subagents.New(subagents.ManagerConfig{
		Provider:      provider,
		Registry:      tools.NewRegistry(),
		MaxConcurrent: 2,
		MaxQueued:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	first, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "two"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, provider.started)
	waitForSignal(t, provider.started)

	close(provider.release)
	for _, child := range []subagents.Record{first, second} {
		result, err := manager.Wait(context.Background(), "parent", child.ID)
		if err != nil {
			t.Fatalf("Wait(%s): %v", child.ID, err)
		}
		if result.Status != subagents.StatusCompleted || result.Result.Text != "done" {
			t.Fatalf("child result = %#v, want completed done", result)
		}
	}
	if got := provider.maxActive.Load(); got > 2 {
		t.Fatalf("max active streams = %d, want <= 2", got)
	}
	if got := provider.maxActive.Load(); got != 2 {
		t.Fatalf("max active streams = %d, want 2", got)
	}
}

func TestManager_RejectsQueueOverflowAndNestedSpawn(t *testing.T) {
	provider := newControlledProvider()
	manager, err := subagents.New(subagents.ManagerConfig{
		Provider:             provider,
		Registry:             tools.NewRegistry(),
		MaxConcurrent:        1,
		MaxQueued:            1,
		MaxChildrenPerParent: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	first, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, provider.started)
	if _, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "two"}); err != nil {
		t.Fatalf("second spawn returned %v", err)
	}
	if _, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "three"}); !errors.Is(err, subagents.ErrQueueFull) {
		t.Fatalf("third spawn error = %v, want ErrQueueFull", err)
	}
	if _, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: first.ID, Prompt: "nested"}); !errors.Is(err, subagents.ErrNestedDisabled) {
		t.Fatalf("nested spawn error = %v, want ErrNestedDisabled", err)
	}
	close(provider.release)
}

func TestManager_CancelStopsChildAndWaitsForTerminalState(t *testing.T) {
	provider := newControlledProvider()
	manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "cancel me"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, provider.started)
	if err := manager.Cancel(context.Background(), "parent", child.ID); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), "parent", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
}

func TestManager_TimeoutCancelsChild(t *testing.T) {
	provider := newControlledProvider()
	manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{
		ParentID: "parent",
		Prompt:   "time out",
		Timeout:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, provider.started)
	result, err := manager.Wait(context.Background(), "parent", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
}

func TestManager_ParentCannotCompleteBeforeChildren(t *testing.T) {
	childProvider := newControlledProvider()
	manager, err := subagents.New(subagents.ManagerConfig{Provider: childProvider, Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	parentProvider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "premature"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "final"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	parent := agent.New(parentProvider, tools.NewRegistry(), agent.WithRunID("parent-run"), manager.AgentOption())
	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent-run", Prompt: "slow child"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, childProvider.started)

	start := time.Now()
	go func() {
		time.Sleep(40 * time.Millisecond)
		close(childProvider.release)
	}()
	result, err := parent.Run(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != agent.StateCompleted || result.Turns != 2 {
		t.Fatalf("parent result = %#v, want completed after two turns", result)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("parent completed after %s before child settled", elapsed)
	}
	childResult, err := manager.Wait(context.Background(), "parent-run", child.ID)
	if err != nil || childResult.Status != subagents.StatusCompleted {
		t.Fatalf("child result = %#v, err %v", childResult, err)
	}
}

func TestManager_DurableChildUsesRunnerStateAndEvents(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "durable result"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	stateStore := agent.NewInMemoryStateStore()
	eventStore := agent.NewInMemoryEventStore()
	runner := agent.NewAgentRunner(provider, tools.NewRegistry(), agent.WithRunnerStateStore(stateStore), agent.WithRunnerEventStore(eventStore))
	manager, err := subagents.New(subagents.ManagerConfig{
		DefaultMode:   subagents.ModeDurable,
		DurableRunner: runner,
		Store:         subagents.NewMemoryStore(),
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{
		ParentID:      "parent",
		Prompt:        "persist this",
		SystemPrompt:  "child only",
		WorkspaceRoot: "/workspace/child",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), "parent", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCompleted || result.Result.Text != "durable result" {
		t.Fatalf("result = %#v", result)
	}
	snapshot, err := stateStore.Load(context.Background(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != agent.RunCompleted || snapshot.Agent.SystemPrompt != "child only" || snapshot.Agent.WorkspaceRoot != "/workspace/child" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	events, err := eventStore.Read(context.Background(), child.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("events = %d, err %v", len(events), err)
	}
}

func TestManager_RecoverRequeuesDurableRecords(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "recovered"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	stateStore := agent.NewInMemoryStateStore()
	eventStore := agent.NewInMemoryEventStore()
	runner := agent.NewAgentRunner(provider, tools.NewRegistry(), agent.WithRunnerStateStore(stateStore), agent.WithRunnerEventStore(eventStore))
	store := subagents.NewMemoryStore()
	if err := store.Create(context.Background(), subagents.Record{
		ID:       "durable-recovery",
		ParentID: "parent",
		Name:     "recovery",
		Mode:     subagents.ModeDurable,
		Status:   subagents.StatusQueued,
		Request: subagents.SpawnRequest{
			ParentID: "parent",
			Prompt:   "recover this child",
			Mode:     subagents.ModeDurable,
		},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := subagents.New(subagents.ManagerConfig{
		DefaultMode:   subagents.ModeDurable,
		DurableRunner: runner,
		Store:         store,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), "parent", "durable-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCompleted || result.Result.Text != "recovered" {
		t.Fatalf("recovered result = %#v", result)
	}
}

func TestManager_ToolsAreScopedToParent(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	registry := tools.NewRegistry()
	manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	if err := manager.Register(registry); err != nil {
		t.Fatal(err)
	}
	response, err := registry.Handle(context.Background(), tools.ToolRequest{
		ID:        "call-1",
		Name:      "subagent_spawn",
		RunID:     "parent",
		Arguments: []byte(`{"prompt":"tool child"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Content) != 1 {
		t.Fatalf("response = %#v", response)
	}
	records, err := manager.List(context.Background(), "parent")
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, err %v", records, err)
	}
	if _, err := manager.Get(context.Background(), "other-parent", records[0].ID); !errors.Is(err, subagents.ErrNotOwned) {
		t.Fatalf("cross-parent access error = %v, want ErrNotOwned", err)
	}
}

func closeManager(t *testing.T, manager *subagents.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child execution")
	}
}

type controlledProvider struct {
	active    atomic.Int64
	maxActive atomic.Int64
	started   chan struct{}
	release   chan struct{}
}

func newControlledProvider() *controlledProvider {
	return &controlledProvider{started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (p *controlledProvider) ID() string { return "controlled" }

func (p *controlledProvider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []ai.Model{{ID: "controlled-model", Provider: "controlled"}}, nil
}

func (p *controlledProvider) Stream(ctx context.Context, _ providers.StreamRequest) (providers.EventStream, error) {
	active := p.active.Add(1)
	for {
		old := p.maxActive.Load()
		if active <= old || p.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case p.started <- struct{}{}:
	default:
	}
	return &controlledStream{provider: p}, nil
}

type controlledStream struct {
	provider *controlledProvider
	step     int
	once     sync.Once
}

func (s *controlledStream) Next(ctx context.Context) (ai.Event, error) {
	if s.step == 0 {
		s.step++
		select {
		case <-s.provider.release:
			return ai.TextDelta{Text: "done"}, nil
		case <-ctx.Done():
			s.finish()
			return nil, ctx.Err()
		}
	}
	if s.step == 1 {
		s.step++
		s.finish()
		return ai.StopEvent{Reason: ai.StopReasonStop}, nil
	}
	return nil, io.EOF
}

func (s *controlledStream) Close() error {
	s.finish()
	return nil
}

func (s *controlledStream) finish() {
	s.once.Do(func() { s.provider.active.Add(-1) })
}
