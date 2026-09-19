package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/providers/providertest"
	"github.com/diasYuri/y/pkg/telemetry"
	"github.com/diasYuri/y/pkg/tools"
)

func TestAgentRunnerPersistsCanonicalEventsAndIsIdempotent(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hello"},
			ai.UsageEvent{Usage: ai.Usage{InputTokens: 2, OutputTokens: 1, ReasoningTokens: 3, TotalTokens: 6}},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	stateStore := NewInMemoryStateStore()
	eventStore := NewInMemoryEventStore()
	runner := NewAgentRunner(provider, tools.NewRegistry(),
		WithRunnerStateStore(stateStore), WithRunnerEventStore(eventStore), WithRunnerOwner("worker-a"))

	response, err := runner.Run(context.Background(), RunRequest{
		RunID:          "run-1",
		SessionID:      "session-1",
		TenantID:       "tenant-1",
		IdempotencyKey: "request-1",
		Prompt:         "say hello",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if response.Snapshot.Status != RunCompleted || response.Snapshot.Version != 3 {
		t.Fatalf("snapshot = %#v, want completed version 3", response.Snapshot)
	}
	if response.Result.Usage.ReasoningTokens != 3 {
		t.Fatalf("reasoning usage = %d, want 3", response.Result.Usage.ReasoningTokens)
	}

	seen := make(map[EventKind]bool)
	for i, event := range response.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if event.SchemaVersion != CurrentEventSchemaVersion || event.RunID != "run-1" {
			t.Fatalf("event envelope = %#v", event)
		}
		seen[event.Type] = true
	}
	for _, required := range []EventKind{EventAgentStarted, EventMessageStarted, EventMessageDelta, EventMessageCompleted, EventTurnStarted, EventTurnCompleted, EventStateCheckpointed, EventAgentCompleted, EventAgentSettled} {
		if !seen[required] {
			t.Errorf("missing canonical event %q", required)
		}
	}
	for _, event := range response.Events {
		if event.Type == EventAgentStarted && event.IdempotencyKey != "request-1:agent-start" {
			t.Fatalf("agent start idempotency key = %q, want request-1:agent-start", event.IdempotencyKey)
		}
	}
	stored, err := eventStore.Read(context.Background(), "run-1", 0)
	if err != nil || len(stored) != len(response.Events) {
		t.Fatalf("stored events = %d, response events = %d, err=%v", len(stored), len(response.Events), err)
	}

	second, err := runner.Run(context.Background(), RunRequest{RunID: "run-1", TenantID: "tenant-1", IdempotencyKey: "request-1", Prompt: "say hello"})
	if err != nil {
		t.Fatalf("idempotent Run error: %v", err)
	}
	if second.Result.Messages == nil || len(second.Events) != 0 || provider.CallCount() != 1 {
		t.Fatalf("idempotent response = %#v, provider calls = %d", second, provider.CallCount())
	}
}

func TestAgentRunnerTracesStateLoadAndSave(t *testing.T) {
	tracer := telemetry.NewMemoryTracer()
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}}}))
	runner := NewAgentRunner(provider, tools.NewRegistry(), WithRunnerStateStore(NewInMemoryStateStore()), WithRunnerEventStore(NewInMemoryEventStore()), WithRunnerTracer(tracer))
	if _, err := runner.Run(context.Background(), RunRequest{RunID: "traced-run", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	seenLoad, seenSave := false, false
	for _, span := range tracer.Spans() {
		seenLoad = seenLoad || span.Name == "state.load"
		seenSave = seenSave || span.Name == "state.save"
	}
	if !seenLoad || !seenSave {
		t.Fatalf("spans = %#v", tracer.Spans())
	}
}

func TestAgentRunnerRecoverableRetryDoesNotUseTerminalIdempotencyFastPath(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Err: &providers.NetworkError{Message: "temporary outage"}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "recovered"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	stateStore := NewInMemoryStateStore()
	runner := NewAgentRunner(provider, tools.NewRegistry(),
		WithRunnerStateStore(stateStore), WithRunnerEventStore(NewInMemoryEventStore()),
		WithRunnerAgentOptions(WithMaxRetries(0)))
	first, err := runner.Run(context.Background(), RunRequest{RunID: "recoverable", TenantID: "tenant-1", IdempotencyKey: "request-1", Prompt: "hello"})
	if err == nil || first.Snapshot.Status != RunRecoverable {
		t.Fatalf("first run = status %q, err %v; want recoverable", first.Snapshot.Status, err)
	}
	second, err := runner.Run(context.Background(), RunRequest{RunID: "recoverable", TenantID: "tenant-1", IdempotencyKey: "request-1"})
	if err != nil || second.Snapshot.Status != RunCompleted {
		t.Fatalf("recovery = status %q, err %v; want completed", second.Snapshot.Status, err)
	}
	if provider.CallCount() != 2 || !strings.Contains(second.Result.Messages[len(second.Result.Messages)-1].Content[0].Text, "recovered") {
		t.Fatalf("recovery result/provider calls = %#v/%d", second.Result, provider.CallCount())
	}
}

func TestAgentRunnerRemoteApprovalResumesDurableTool(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "call-1", Name: "danger", Arguments: json.RawMessage(`{"ok":true}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "approved"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	called := 0
	registry := tools.NewRegistry(tools.WithPolicy(policy.NewDistributedEngine(policy.DistributedConfig{
		Config: policy.Config{RequireApprovalForSensitive: true},
	})))
	if err := registry.Add(tools.ToolDescriptor{Name: "danger", Sensitive: true}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		called++
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "done"}}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentRunner(provider, registry, WithRunnerStateStore(NewInMemoryStateStore()), WithRunnerEventStore(NewInMemoryEventStore()))
	first, err := runner.Run(context.Background(), RunRequest{RunID: "approval-run", TenantID: "tenant-1", IdempotencyKey: "request-1", Prompt: "do it"})
	if !errors.Is(err, ErrApprovalPending) || first.Snapshot.Status != RunWaitingApproval || first.Result.Approval == nil {
		t.Fatalf("pending approval = status %q, result %#v, err %v", first.Snapshot.Status, first.Result, err)
	}
	second, err := runner.Run(context.Background(), RunRequest{
		RunID: "approval-run", TenantID: "tenant-1", IdempotencyKey: "request-1",
		Approval: &policy.ApprovalResolution{ApprovalID: first.Result.Approval.ApprovalID, State: policy.ApprovalApproved},
	})
	if err != nil || second.Snapshot.Status != RunCompleted || called != 1 {
		t.Fatalf("approved run = status %q, called %d, err %v", second.Snapshot.Status, called, err)
	}
}

func TestJSONLStorePersistsLeaseAndOperationOwnership(t *testing.T) {
	root := t.TempDir()
	first := NewJSONLStore(root)
	second := NewJSONLStore(root)
	lease, err := first.AcquireLease(context.Background(), "run-1", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.AcquireLease(context.Background(), "run-1", "worker-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second lease error = %v, want ErrLeaseHeld", err)
	}
	if err := first.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	operation := OperationRecord{IdempotencyKey: "op-1", RunID: "run-1", TurnID: "turn-1", ToolCallID: "call-1", ToolName: "write", ArgumentsHash: "hash"}
	if _, acquired, err := first.BeginOperation(context.Background(), operation); err != nil || !acquired {
		t.Fatalf("begin operation = acquired %v, err %v", acquired, err)
	}
	if _, acquired, err := second.BeginOperation(context.Background(), operation); err != nil || acquired {
		t.Fatalf("duplicate operation = acquired %v, err %v", acquired, err)
	}
	response := tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "ok"}}}
	if err := first.CompleteOperation(context.Background(), operation.IdempotencyKey, response, nil); err != nil {
		t.Fatal(err)
	}
	replayed, acquired, err := second.BeginOperation(context.Background(), operation)
	if err != nil || acquired || replayed.Response == nil || replayed.Response.Content[0].Text != "ok" {
		t.Fatalf("replayed operation = %#v, acquired %v, err %v", replayed, acquired, err)
	}
}

func TestAgentRunnerResolvesProviderPerRequest(t *testing.T) {
	defaultProvider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "wrong"}, ai.StopEvent{Reason: ai.StopReasonStop}}}))
	selectedProvider := providertest.NewFakeProvider(providertest.WithFakeID("selected"), providertest.WithFakeModels(ai.Model{ID: "selected-model", Provider: "selected"}), providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "selected"}, ai.StopEvent{Reason: ai.StopReasonStop}}}))
	catalog := providers.NewCatalog()
	if err := catalog.Register(selectedProvider); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentRunner(defaultProvider, tools.NewRegistry(), WithRunnerProviderResolver(catalog))
	response, err := runner.Run(context.Background(), RunRequest{RunID: "provider-run", TenantID: "tenant-1", ProviderID: "selected", Model: ai.Model{ID: "selected-model", Provider: "selected"}, Prompt: "hello"})
	if err != nil || response.Result.Messages[len(response.Result.Messages)-1].Content[0].Text != "selected" || defaultProvider.CallCount() != 0 || selectedProvider.CallCount() != 1 {
		t.Fatalf("provider selection = %#v, err %v, default/selected calls %d/%d", response.Result, err, defaultProvider.CallCount(), selectedProvider.CallCount())
	}
}

func TestSnapshotScopeIncludesContextIdentity(t *testing.T) {
	snapshot := NewSnapshot("scope-run", AgentSnapshot{ContextRequest: ycontext.Request{TenantID: "tenant-1", WorkspaceID: "workspace-1"}})
	if snapshot.TenantID != "tenant-1" || snapshot.WorkspaceID != "workspace-1" {
		t.Fatalf("snapshot scope = tenant %q/workspace %q", snapshot.TenantID, snapshot.WorkspaceID)
	}
	runner := NewAgentRunner(providertest.NewFakeProvider(), tools.NewRegistry())
	_, err := runner.Run(context.Background(), RunRequest{RunID: "scope-run", TenantID: "tenant-2", InitialState: &snapshot})
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("cross-tenant resume error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestAgentRunnerFreshRunPreservesRequestOptions(t *testing.T) {
	provider := &capturingProvider{FakeProvider: providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "ok"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))}
	identity := policy.Identity{
		CallerID:     "caller-1",
		TenantID:     "tenant-1",
		WorkspaceID:  "workspace-1",
		SessionID:    "session-1",
		Capabilities: []string{"filesystem.read"},
	}
	model := ai.Model{ID: "requested-model", Provider: "fake"}
	runner := NewAgentRunner(provider, tools.NewRegistry(), WithRunnerStateStore(NewInMemoryStateStore()))
	response, err := runner.Run(context.Background(), RunRequest{
		RunID:          "fresh-run",
		SessionID:      "session-1",
		TenantID:       "tenant-1",
		WorkspaceID:    "workspace-1",
		ProjectID:      "project-1",
		Identity:       identity,
		PolicyVersion:  "policy-v1",
		IdempotencyKey: "request-1",
		RequestID:      "request-1",
		Model:          model,
		APIKey:         "request-secret",
		Prompt:         "hello",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Model.ID != model.ID || request.Options.APIKey != "request-secret" {
		t.Fatalf("request model/key = %q/%q, want %q/request-secret", request.Model.ID, request.Options.APIKey, model.ID)
	}
	if request.Options.RunID != "fresh-run" || request.Options.SessionID != "session-1" {
		t.Fatalf("request scope options = %#v", request.Options)
	}
	if response.Snapshot.Agent.PolicyIdentity.TenantID != "tenant-1" || response.Snapshot.Agent.PolicyIdentity.CallerID != "caller-1" {
		t.Fatalf("snapshot identity = %#v", response.Snapshot.Agent.PolicyIdentity)
	}
	if response.Snapshot.Agent.ContextRequest.ProjectID != "project-1" || response.Snapshot.Agent.PolicyVersion != "policy-v1" {
		t.Fatalf("snapshot request configuration = %#v", response.Snapshot.Agent)
	}
}

func TestAgentRunnerValidatesStoredScopeBeforeTerminalFastPath(t *testing.T) {
	store := NewInMemoryStateStore()
	state := NewSnapshot("scoped-run", AgentSnapshot{
		RunID: "scoped-run",
		PolicyIdentity: policy.Identity{
			TenantID:    "tenant-a",
			WorkspaceID: "workspace-a",
		},
		ContextRequest: ycontext.Request{ProjectID: "project-a"},
	})
	state.Status = RunCompleted
	state.Result = &RunResult{State: StateCompleted}
	if err := store.Save(context.Background(), state.RunID, 0, state); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	runner := NewAgentRunner(providertest.NewFakeProvider(), tools.NewRegistry(), WithRunnerStateStore(store))
	for name, request := range map[string]RunRequest{
		"tenant":    {RunID: "scoped-run", TenantID: "tenant-b"},
		"workspace": {RunID: "scoped-run", TenantID: "tenant-a", WorkspaceID: "workspace-b"},
		"project":   {RunID: "scoped-run", TenantID: "tenant-a", WorkspaceID: "workspace-a", ProjectID: "project-b"},
		"omitted":   {RunID: "scoped-run"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runner.Run(context.Background(), request)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("Run error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestJSONLStoreUsesCollisionFreeSafeRunDirectories(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ids := []string{"..", "run/a", "run_a"}
	paths := make(map[string]string, len(ids))
	for _, runID := range ids {
		paths[runID] = store.runDir(runID)
		state := NewSnapshot(runID, AgentSnapshot{RunID: runID})
		if err := store.Save(context.Background(), runID, 0, state); err != nil {
			t.Fatalf("Save(%q): %v", runID, err)
		}
		loaded, err := store.Load(context.Background(), runID)
		if err != nil {
			t.Fatalf("Load(%q): %v", runID, err)
		}
		if loaded.RunID != runID {
			t.Fatalf("Load(%q) returned run ID %q", runID, loaded.RunID)
		}
		cleanRoot := filepath.Join(root, "runs") + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(paths[runID])+string(os.PathSeparator), cleanRoot) {
			t.Fatalf("run %q escaped store root: %q", runID, paths[runID])
		}
	}
	if paths[ids[0]] == paths[ids[1]] || paths[ids[1]] == paths[ids[2]] || paths[ids[0]] == paths[ids[2]] {
		t.Fatalf("run IDs collided: %#v", paths)
	}
}

func TestAgentRunnerDurablyReplaysCompletedToolResult(t *testing.T) {
	events := NewInMemoryEventStore()
	const operationKey = "run-1-run-1-turn-1-tool-call-1"
	envelope, err := NewEventEnvelope(EventToolCompleted, "run-1", 1, map[string]any{
		"tool_result": ai.ToolResult{
			ToolCallID: "call-1",
			ToolName:   "side_effect",
			Content:    []ai.ContentBlock{{Type: ai.ContentText, Text: "already done"}},
		},
	})
	if err != nil {
		t.Fatalf("NewEventEnvelope: %v", err)
	}
	envelope.IdempotencyKey = operationKey
	if err := events.Append(context.Background(), "run-1", []EventEnvelope{envelope}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	calls := 0
	base := tools.NewRegistry()
	if err := base.Add(tools.ToolDescriptor{Name: "side_effect"}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		calls++
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "executed"}}}, nil
	})); err != nil {
		t.Fatalf("Add tool: %v", err)
	}
	replaying := &eventBackedToolRegistry{base: base, events: events}
	response, err := replaying.Handle(context.Background(), tools.ToolRequest{
		ID:             "call-1",
		Name:           "side_effect",
		RunID:          "run-1",
		IdempotencyKey: operationKey,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls != 0 || len(response.Content) != 1 || response.Content[0].Text != "already done" {
		t.Fatalf("replayed response = %#v, handler calls = %d", response, calls)
	}
}

func TestAgentRunnerRecoveryReconcilesToolResultFromEventStore(t *testing.T) {
	const (
		runID = "recover-run"
		nonce = "worker-generation-1"
	)
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "call-1", Name: "side_effect", Arguments: json.RawMessage(`{}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "recovered"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	registry := tools.NewRegistry()
	calls := 0
	if err := registry.Add(tools.ToolDescriptor{Name: "side_effect"}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		calls++
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "executed"}}}, nil
	})); err != nil {
		t.Fatalf("Add tool: %v", err)
	}
	events := NewInMemoryEventStore()
	operationKey := fmt.Sprintf("%s-run-%s-turn-1-tool-call-1", runID, nonce)
	completed, err := NewEventEnvelope(EventToolCompleted, runID, 1, map[string]any{
		"tool_result": ai.ToolResult{
			ToolCallID: "call-1",
			ToolName:   "side_effect",
			Content:    []ai.ContentBlock{{Type: ai.ContentText, Text: "durably executed"}},
		},
	})
	if err != nil {
		t.Fatalf("NewEventEnvelope: %v", err)
	}
	completed.IdempotencyKey = operationKey
	if err := events.Append(context.Background(), runID, []EventEnvelope{completed}); err != nil {
		t.Fatalf("Append completed event: %v", err)
	}

	stateStore := NewInMemoryStateStore()
	state := NewSnapshot(runID, AgentSnapshot{
		RunID:    runID,
		RunNonce: nonce,
		Model:    ai.Model{ID: "fake", Provider: "fake"},
		State:    StateExecutingTools,
		Transcript: []ai.Message{
			{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "start"}}},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "side_effect", Arguments: json.RawMessage(`{}`)}}},
		},
	})
	state.Status = RunRunning
	if err := stateStore.Save(context.Background(), runID, 0, state); err != nil {
		t.Fatalf("Save recovery state: %v", err)
	}
	runner := NewAgentRunner(provider, registry,
		WithRunnerStateStore(stateStore), WithRunnerEventStore(events))
	response, err := runner.Run(context.Background(), RunRequest{RunID: runID})
	if err != nil {
		t.Fatalf("recovery Run: %v", err)
	}
	if calls != 0 || response.Result.State != StateCompleted || provider.CallCount() != 2 {
		t.Fatalf("recovery result = %#v, handler calls = %d, provider calls = %d", response.Result, calls, provider.CallCount())
	}
}

func TestAgentRunnerCheckpointsToolResultsBeforeTurn(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "call-1", Name: "side_effect", Arguments: json.RawMessage(`{}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "done"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{Name: "side_effect"}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "result"}}}, nil
	})); err != nil {
		t.Fatalf("Add tool: %v", err)
	}
	events := NewInMemoryEventStore()
	runner := NewAgentRunner(provider, registry, WithRunnerStateStore(NewInMemoryStateStore()), WithRunnerEventStore(events))
	if _, err := runner.Run(context.Background(), RunRequest{RunID: "checkpoint-run", Prompt: "start"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	stored, err := events.Read(context.Background(), "checkpoint-run", 0)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	toolCompleted := uint64(0)
	firstCheckpoint := uint64(0)
	for _, event := range stored {
		if event.Type == EventToolCompleted && toolCompleted == 0 {
			toolCompleted = event.Sequence
		}
		if event.Type == EventStateCheckpointed && firstCheckpoint == 0 {
			firstCheckpoint = event.Sequence
		}
	}
	if toolCompleted == 0 || firstCheckpoint == 0 || firstCheckpoint <= toolCompleted {
		t.Fatalf("tool/checkpoint event order = tool %d, checkpoint %d", toolCompleted, firstCheckpoint)
	}
}

func TestEventEnvelopeRedactsNestedArgumentsAndPreservesPayload(t *testing.T) {
	envelope, err := EventEnvelopeFromEvent(Event{
		Kind: EventTurnCompleted,
		Message: ai.Message{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{
			ID: "call-1", Name: "send", Arguments: json.RawMessage(`{"token":"nested-secret"}`),
		}}},
		ToolCall: ai.ToolCall{ID: "call-1", Name: "send", Arguments: json.RawMessage(`{"password":"direct-secret"}`)},
		Payload:  json.RawMessage(`{"custom":"value"}`),
	}, "run-1", 1)
	if err != nil {
		t.Fatalf("EventEnvelopeFromEvent: %v", err)
	}
	raw := string(envelope.Payload)
	if strings.Contains(raw, "nested-secret") || strings.Contains(raw, "direct-secret") {
		t.Fatalf("event payload leaked tool arguments: %s", raw)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode envelope payload: %v", err)
	}
	if string(payload["payload"]) != `{"custom":"value"}` {
		t.Fatalf("custom payload = %s", payload["payload"])
	}
}

func TestAgentUsesNewToolIdempotencyGenerationForEachPersistentRun(t *testing.T) {
	provider := &capturingProvider{FakeProvider: providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "same-call", Name: "side_effect", Arguments: json.RawMessage(`{}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "first"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{ID: "same-call", Name: "side_effect", Arguments: json.RawMessage(`{}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "second"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))}
	registry := tools.NewRegistry()
	var keys []string
	if err := registry.Add(tools.ToolDescriptor{Name: "side_effect"}, tools.ToolHandlerFunc(func(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
		keys = append(keys, request.IdempotencyKey)
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "ok"}}}, nil
	})); err != nil {
		t.Fatalf("Add tool: %v", err)
	}
	agent := New(provider, registry)
	if _, err := agent.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := agent.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("tool idempotency keys = %#v, want two distinct keys", keys)
	}
}

func TestAgentRunnerRemoteAbortIsConfirmed(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Delay: 500 * time.Millisecond,
		Events: []ai.Event{
			ai.TextDelta{Text: "late"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	control := NewInMemoryRunControl()
	runner := NewAgentRunner(provider, tools.NewRegistry(),
		WithRunnerStateStore(NewInMemoryStateStore()),
		WithRunnerEventStore(NewInMemoryEventStore()),
		WithRunnerControl(control), WithRunnerPollInterval(5*time.Millisecond))

	type result struct {
		response RunResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := runner.Run(context.Background(), RunRequest{RunID: "abort-1", Prompt: "wait"})
		done <- result{response: response, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	if err := control.RequestAbort(context.Background(), "abort-1"); err != nil {
		t.Fatalf("RequestAbort error: %v", err)
	}

	select {
	case outcome := <-done:
		if outcome.err == nil || !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("run error = %v, want context canceled", outcome.err)
		}
		if outcome.response.Snapshot.Status != RunAborted {
			t.Fatalf("status = %q, want aborted", outcome.response.Snapshot.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not observe remote abort")
	}
	state, err := control.GetControl(context.Background(), "abort-1")
	if err != nil || state.Status != ControlCompleted {
		t.Fatalf("control state = %#v, err=%v", state, err)
	}
}

func TestAgentSnapshotDoesNotSerializeCredentialsAndPreservesRichMessage(t *testing.T) {
	apiKey := "do-not-persist"
	a := New(nil, tools.NewRegistry(),
		WithModel(ai.Model{ID: "m", Headers: map[string]string{"Authorization": "secret"}}),
		WithStreamDefaults(providers.StreamOptions{APIKey: apiKey}))
	a.Reset(ai.Message{
		Role: ai.RoleAssistant,
		Content: []ai.ContentBlock{{Type: ai.ContentThinking, Thinking: "reason", Signature: "sig", Details: json.RawMessage(`{"step":1}`)}, {
			Type: ai.ContentImage, ImageData: []byte{1, 2}, ImageMIMEType: "image/png",
		}},
		ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{"x":1}`), Details: json.RawMessage(`{"trusted":true}`)}},
	})
	snapshot := a.Snapshot()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), apiKey) || strings.Contains(string(raw), "Authorization") || strings.Contains(string(raw), "secret") {
		t.Fatalf("snapshot contains credential material: %s", raw)
	}
	if len(snapshot.Transcript[0].Content[1].ImageData) != 2 || snapshot.Transcript[0].ToolCalls[0].Details == nil {
		t.Fatalf("rich snapshot fields were lost: %#v", snapshot.Transcript[0])
	}
}
