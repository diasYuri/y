package subagents_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/extensions/subagents"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers/providertest"
	"github.com/diasYuri/y/pkg/tools"
)

func TestManager_DurableChildRebindsInheritedIdentity(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	stateStore := agent.NewInMemoryStateStore()
	runner := agent.NewAgentRunner(provider, tools.NewRegistry(), agent.WithRunnerStateStore(stateStore))
	manager, err := subagents.New(subagents.ManagerConfig{
		DefaultMode:   subagents.ModeDurable,
		DurableRunner: runner,
		Store:         subagents.NewMemoryStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{
		ParentID: "parent-run",
		Prompt:   "run with child identity",
		Identity: policy.Identity{
			RunID:     "parent-run",
			SessionID: "parent-session",
			TenantID:  "tenant-1",
		},
		TenantID: "tenant-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), "parent-run", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCompleted {
		t.Fatalf("result = %#v, want completed", result)
	}
	snapshot, err := stateStore.Load(context.Background(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Agent.PolicyIdentity.RunID != child.ID || snapshot.Agent.PolicyIdentity.SessionID != child.ID {
		t.Fatalf("child identity = %#v, want run/session %q", snapshot.Agent.PolicyIdentity, child.ID)
	}
}

func TestManager_CompletedChildStillForcesParentFollowUp(t *testing.T) {
	childProvider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "child done"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	manager, err := subagents.New(subagents.ManagerConfig{
		Provider: childProvider,
		Registry: tools.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	parentProvider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "first"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "integrated"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	parent := agent.New(parentProvider, tools.NewRegistry(), agent.WithRunID("parent-run"), manager.AgentOption())
	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent-run", Prompt: "finish quickly"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Wait(context.Background(), "parent-run", child.ID); err != nil || result.Status != subagents.StatusCompleted {
		t.Fatalf("child result = %#v, err %v", result, err)
	}

	result, err := parent.Run(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 2 || result.State != agent.StateCompleted {
		t.Fatalf("parent result = %#v, want two completed turns", result)
	}
}

func TestManager_ResumeEphemeralApproval(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "danger-call", Name: "danger", Arguments: json.RawMessage(`{"ok":true}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "approved"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	registry := tools.NewRegistry()
	var calls atomic.Int32
	if err := registry.Add(tools.ToolDescriptor{Name: "danger", Sensitive: true}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		calls.Add(1)
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "done"}}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "need approval"})
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForStatus(t, manager, "parent", child.ID, subagents.StatusWaitingApproval)
	if pending.Result.Approval == nil {
		t.Fatalf("pending result = %#v, want approval request", pending.Result)
	}
	if err := manager.Resume(context.Background(), "parent", child.ID, policy.ApprovalResolution{
		ApprovalID: pending.Result.Approval.ApprovalID,
		State:      policy.ApprovalApproved,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), "parent", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("resumed result = %#v, calls = %d", result, calls.Load())
	}
}

func TestManager_RetriesTerminalPersistence(t *testing.T) {
	provider := newControlledProvider()
	store := &faultStore{Store: subagents.NewMemoryStore()}
	manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: tools.NewRegistry(), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	child, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "persist result"})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, provider.started)
	store.saveFailures.Store(1)
	close(provider.release)

	result, err := manager.Wait(context.Background(), "parent", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != subagents.StatusCompleted || !strings.Contains(result.Error, subagents.ErrPersistence.Error()) {
		t.Fatalf("in-memory result = %#v, want completed with persistence error", result)
	}
	stored, err := store.Load(context.Background(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == subagents.StatusCompleted {
		t.Fatalf("store unexpectedly persisted terminal result: %#v", stored)
	}
	if err := manager.RetryPersistence(context.Background(), "parent", child.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Load(context.Background(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != subagents.StatusCompleted {
		t.Fatalf("stored result = %#v, want completed", stored)
	}
}

func TestManager_PropagatesProjectIDFromToolRequest(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	projectCh := make(chan string, 1)
	registry := tools.NewRegistry()
	manager, err := subagents.New(subagents.ManagerConfig{
		Provider: provider,
		Registry: registry,
		ChildOptions: func(child subagents.ChildContext) []agent.Option {
			projectCh <- child.Request.ProjectID
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	if err := manager.Register(registry); err != nil {
		t.Fatal(err)
	}
	response, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "subagent_spawn",
		RunID:     "parent",
		ProjectID: "project-1",
		Arguments: json.RawMessage(`{"prompt":"scoped child"}`),
	})
	if err != nil || len(response.Content) != 1 {
		t.Fatalf("spawn response = %#v, err %v", response, err)
	}
	select {
	case projectID := <-projectCh:
		if projectID != "project-1" {
			t.Fatalf("project ID = %q, want project-1", projectID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child options")
	}
}

func TestManager_ReturnsStoreListErrorBeforeChildLimit(t *testing.T) {
	store := &faultStore{Store: subagents.NewMemoryStore()}
	store.listFailures.Store(1)
	manager, err := subagents.New(subagents.ManagerConfig{
		Provider: providerForRegression(),
		Registry: tools.NewRegistry(),
		Store:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	if _, err := manager.Spawn(context.Background(), subagents.SpawnRequest{ParentID: "parent", Prompt: "must fail"}); !errors.Is(err, errListUnavailable) {
		t.Fatalf("spawn error = %v, want list error", err)
	}
}

func waitForStatus(t *testing.T, manager *subagents.Manager, parentID, childID string, status subagents.Status) subagents.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		record, err := manager.Get(context.Background(), parentID, childID)
		if err == nil && record.Status == status {
			return record
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q", status)
	return subagents.Record{}
}

type faultStore struct {
	subagents.Store
	saveFailures atomic.Int32
	listFailures atomic.Int32
}

var errListUnavailable = errors.New("list unavailable")

func (s *faultStore) Save(ctx context.Context, id string, version uint64, record subagents.Record) error {
	if s.saveFailures.CompareAndSwap(1, 0) {
		return errors.New("save unavailable")
	}
	return s.Store.Save(ctx, id, version, record)
}

func (s *faultStore) List(ctx context.Context, parentID string) ([]subagents.Record, error) {
	if s.listFailures.CompareAndSwap(1, 0) {
		return nil, errListUnavailable
	}
	return s.Store.List(ctx, parentID)
}

func providerForRegression() agent.Provider {
	return providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
}
