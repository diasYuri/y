package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

// TestSnapshotPreservesRecoverableErr drives the agent into StateFailed with a
// recoverable error, snapshots through JSON, restores onto a fresh agent, and
// confirms Continue() routes through Recover() (not RunMessages directly).
func TestSnapshotPreservesRecoverableErr(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		// First response fails with a transient error so the agent enters
		// StateFailed with recoverableErr set.
		providers.FakeResponse{
			Err: &providers.RateLimitError{Provider: "fake", StatusCode: 429},
		},
	))
	a := New(provider, tools.NewRegistry())
	if _, err := a.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected first run to fail")
	}
	if a.State() != StateFailed {
		t.Fatalf("state = %q, want failed", a.State())
	}

	snap := a.Snapshot()
	if snap.RecoverableErrMsg == "" {
		t.Fatal("snapshot RecoverableErrMsg empty after recoverable failure")
	}

	// Round-trip through JSON.
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AgentSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.RecoverableErrMsg == "" {
		t.Fatal("decoded RecoverableErrMsg empty after JSON round-trip")
	}

	// Build a fresh provider whose next response succeeds — the restored
	// agent should call it via Recover (which Continue dispatches to).
	var afterCount int32
	provider2 := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "recovered"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	a2, err := RestoreAgent(provider2, tools.NewRegistry(), decoded,
		WithAfterRequest(AfterRequestHook(func(ctx context.Context, req providers.StreamRequest, msg ai.Message, usage ai.Usage, err error) error {
			atomic.AddInt32(&afterCount, 1)
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("RestoreAgent: %v", err)
	}
	if a2.State() != StateFailed {
		t.Fatalf("restored state = %q, want failed", a2.State())
	}

	// Continue should route to Recover (because recoverableErr is set on the
	// restored agent), making a fresh provider call.
	res, err := a2.Continue(context.Background())
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if res.State != StateCompleted {
		t.Fatalf("state after Continue = %q, want completed", res.State)
	}
	if got := atomic.LoadInt32(&afterCount); got == 0 {
		t.Fatal("AfterRequest hook never fired; Continue did not route to Recover")
	}
	if provider2.CallCount() != 1 {
		t.Fatalf("provider2 calls = %d, want 1", provider2.CallCount())
	}
}

// TestSnapshotPreservesExecutionOptions verifies that toolMode,
// toolConcurrency, toolTimeout, maxRetries, maxRetryDelay, compactionEnabled,
// and the JSON-friendly subset of streamDefaults round-trip through Snapshot.
func TestSnapshotPreservesExecutionOptions(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "ok"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	temp := 0.42
	a := New(provider, tools.NewRegistry(),
		WithToolExecutionMode(ToolExecutionSequential),
		WithToolConcurrency(3),
		WithToolTimeout(7*time.Second),
		WithMaxRetries(4),
		WithMaxRetryDelay(11*time.Second),
		WithCompaction(true),
		WithStreamDefaults(providers.StreamOptions{
			Temperature:   &temp,
			MaxTokens:     2048,
			Timeout:       5 * time.Second,
			MaxRetries:    2,
			MaxRetryDelay: 3 * time.Second,
			Reasoning:     ai.ThinkingHigh,
			ThinkingBudgets: map[ai.ThinkingLevel]int64{
				ai.ThinkingHigh: 4096,
			},
			CacheRetention: ai.CacheRetentionLong,
		}),
	)

	snap := a.Snapshot()
	if snap.ToolMode != ToolExecutionSequential {
		t.Fatalf("snap.ToolMode = %q", snap.ToolMode)
	}
	if snap.ToolConcurrency != 3 {
		t.Fatalf("snap.ToolConcurrency = %d", snap.ToolConcurrency)
	}
	if snap.ToolTimeout != 7*time.Second {
		t.Fatalf("snap.ToolTimeout = %v", snap.ToolTimeout)
	}
	if snap.MaxRetries != 4 {
		t.Fatalf("snap.MaxRetries = %d", snap.MaxRetries)
	}
	if snap.MaxRetryDelay != 11*time.Second {
		t.Fatalf("snap.MaxRetryDelay = %v", snap.MaxRetryDelay)
	}
	if !snap.CompactionEnabled {
		t.Fatal("snap.CompactionEnabled = false, want true")
	}
	if snap.StreamDefaults.Temperature == nil || *snap.StreamDefaults.Temperature != 0.42 {
		t.Fatalf("snap.StreamDefaults.Temperature = %v", snap.StreamDefaults.Temperature)
	}
	if snap.StreamDefaults.MaxTokens != 2048 {
		t.Fatalf("snap.StreamDefaults.MaxTokens = %d", snap.StreamDefaults.MaxTokens)
	}

	// JSON round-trip
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AgentSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	a2, err := RestoreAgent(provider, tools.NewRegistry(), decoded)
	if err != nil {
		t.Fatalf("RestoreAgent: %v", err)
	}

	a2.mu.Lock()
	defer a2.mu.Unlock()
	if a2.toolMode != ToolExecutionSequential {
		t.Fatalf("restored toolMode = %q", a2.toolMode)
	}
	if a2.toolConcurrency != 3 {
		t.Fatalf("restored toolConcurrency = %d", a2.toolConcurrency)
	}
	if a2.toolTimeout != 7*time.Second {
		t.Fatalf("restored toolTimeout = %v", a2.toolTimeout)
	}
	if a2.maxRetries != 4 {
		t.Fatalf("restored maxRetries = %d", a2.maxRetries)
	}
	if a2.maxRetryDelay != 11*time.Second {
		t.Fatalf("restored maxRetryDelay = %v", a2.maxRetryDelay)
	}
	if !a2.compactionEnabled {
		t.Fatal("restored compactionEnabled = false")
	}
	if a2.streamDefaults.Temperature == nil || *a2.streamDefaults.Temperature != 0.42 {
		t.Fatalf("restored streamDefaults.Temperature = %v", a2.streamDefaults.Temperature)
	}
	if a2.streamDefaults.MaxTokens != 2048 {
		t.Fatalf("restored streamDefaults.MaxTokens = %d", a2.streamDefaults.MaxTokens)
	}
	if a2.streamDefaults.Timeout != 5*time.Second {
		t.Fatalf("restored streamDefaults.Timeout = %v", a2.streamDefaults.Timeout)
	}
	if a2.streamDefaults.Reasoning != ai.ThinkingHigh {
		t.Fatalf("restored streamDefaults.Reasoning = %q", a2.streamDefaults.Reasoning)
	}
	if got := a2.streamDefaults.ThinkingBudgets[ai.ThinkingHigh]; got != 4096 {
		t.Fatalf("restored thinkingBudget[high] = %d", got)
	}
	if a2.streamDefaults.CacheRetention != ai.CacheRetentionLong {
		t.Fatalf("restored cacheRetention = %q", a2.streamDefaults.CacheRetention)
	}
}

// TestRestoreOverwritesUnconditionally verifies that Restore replaces fields
// even when the snapshot has zero/empty values (P1-12: no merge-only behaviour).
func TestRestoreOverwritesUnconditionally(t *testing.T) {
	provider := providers.NewFakeProvider()
	a := New(provider, tools.NewRegistry(),
		WithSystemPrompt("original"),
		WithSessionID("session-1"),
		WithWorkspaceRoot("/old"),
	)

	// Empty snapshot — Restore must clear, not merge.
	if err := a.Restore(AgentSnapshot{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.systemPrompt != "" {
		t.Fatalf("systemPrompt = %q, want empty after Restore", a.systemPrompt)
	}
	if a.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty after Restore", a.sessionID)
	}
	if a.workspaceRoot != "" {
		t.Fatalf("workspaceRoot = %q, want empty after Restore", a.workspaceRoot)
	}
	if a.recoverableErr != nil {
		t.Fatalf("recoverableErr = %v, want nil", a.recoverableErr)
	}
}
