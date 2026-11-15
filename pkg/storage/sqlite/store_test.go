package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/storage/sqlite"
	"github.com/diasYuri/y/pkg/tools"
)

func TestDurableContractsAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	state := agent.NewSnapshot("run", agent.AgentSnapshot{RunID: "run"})
	state.Agent.Transcript = []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}}}
	if err := s.Save(ctx, "run", 0, state); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "run", 0, state); !errors.Is(err, agent.ErrVersionConflict) {
		t.Fatalf("stale write: %v", err)
	}
	if err := s.PutMetadata(ctx, "run", json.RawMessage(`{"workspace":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	event, err := agent.NewEventEnvelope(agent.EventAgentStarted, "run", 0, map[string]string{"test": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "run", []agent.EventEnvelope{event, event}); err != nil {
		t.Fatal(err)
	}
	op := agent.OperationRecord{RunID: "run", IdempotencyKey: "key", ToolName: "write", ToolCallID: "call", ArgumentsHash: "hash"}
	if _, got, err := s.BeginOperation(ctx, op); err != nil || !got {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	loaded, err := s.Load(ctx, "run")
	if err != nil || loaded.Version != 1 || len(loaded.Agent.Transcript) != 1 {
		t.Fatalf("checkpoint: %#v %v", loaded, err)
	}
	transcript, err := s.LoadTranscript(ctx, "run")
	if err != nil || len(transcript) != 1 {
		t.Fatal("transcript was not atomic with state", err)
	}
	events, err := s.Read(ctx, "run", 0)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatal("event dedup/sequence", err)
	}
	existing, got, err := s.BeginOperation(ctx, op)
	if err != nil || got || existing.State != agent.OperationPending {
		t.Fatal("uncertain operation replayed", err)
	}
	op.ArgumentsHash = "different"
	if _, _, err := s.BeginOperation(ctx, op); !errors.Is(err, agent.ErrIdempotencyConflict) {
		t.Fatal("identity conflict accepted", err)
	}
	if err := s.CompleteOperation(ctx, "key", tools.ToolResponse{}, nil); err != nil {
		t.Fatal(err)
	}
	records, err := s.Operations(ctx, "run")
	if err != nil || len(records) != 1 || records[0].State != agent.OperationCompleted {
		t.Fatal("explicit resolution failed", err)
	}
	if value, err := s.GetMetadata(ctx, "run"); err != nil || !json.Valid(value) {
		t.Fatal("metadata missing", err)
	}
}

func TestConcurrentClaimAndSessionBranches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	one, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = one.Close() }()
	two, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = two.Close() }()
	var acquired atomic.Int32
	var wg sync.WaitGroup
	for _, store := range []*sqlite.Store{one, two} {
		wg.Go(func() {
			_, claimed, err := store.BeginOperation(ctx, agent.OperationRecord{RunID: "run", ToolName: "write", IdempotencyKey: "key", ArgumentsHash: "hash"})
			if err != nil {
				t.Error(err)
			}
			if claimed {
				acquired.Add(1)
			}
		})
	}
	wg.Wait()
	if acquired.Load() != 1 {
		t.Fatal("operation claimed twice")
	}
	sessions := one.Sessions()
	summary, err := sessions.SaveTranscript(ctx, "workspace", []ai.Message{{Role: ai.RoleUser}}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	child, err := sessions.Fork(ctx, summary.ID, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	clone, err := sessions.Clone(ctx, summary.ID, child, "independent")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetName(ctx, summary.ID, clone, "renamed"); err != nil {
		t.Fatal(err)
	}
	tree, err := two.Sessions().GetTree(ctx, summary.ID)
	if err != nil || len(tree) != 3 {
		t.Fatal("branches not persisted", err)
	}
	for _, entry := range tree {
		if entry.ID == clone && entry.Parent != "" {
			t.Fatal("clone retained parent")
		}
	}
	if messages, err := sessions.ResumeFrom(ctx, summary.ID, child); err != nil || len(messages) != 1 {
		t.Fatal("branch messages missing", err)
	}
	if id, err := sessions.Resolve(ctx, "workspace", summary.ID[:8]); err != nil || id != summary.ID {
		t.Fatal("resolve failed", err)
	}
}
