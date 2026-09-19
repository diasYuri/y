package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

type extractorFunc func(context.Context, pmemory.ExtractionRequest) (pmemory.ExtractionResult, error)

func (f extractorFunc) Extract(ctx context.Context, request pmemory.ExtractionRequest) (pmemory.ExtractionResult, error) {
	return f(ctx, request)
}

func TestMemorySourceRanksAndBoundsRecall(t *testing.T) {
	store := newTestStore()
	for i, content := range []string{"Go modules and Go tests", "Go formatting", "unrelated"} {
		if err := store.Save(context.Background(), pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeProject, Summary: "item", Content: content, Source: pmemory.SourceRef{ProjectID: "p"}, Confidence: float32(i) / 3}); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(store, SourceOptions{MaxItems: 5, MaxContextTokens: 120, MaxReadBytes: 1024, FailOpen: false})
	items, err := source.Load(context.Background(), ycontext.Request{ProjectID: "p", Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || len(items) > 2 {
		t.Fatalf("source items = %d", len(items))
	}
	if !strings.Contains(items[0].Content, `<memory source="y"`) {
		t.Fatalf("memory delimiter = %q", items[0].Content)
	}
	if !strings.Contains(items[0].Content, "historical memory") {
		t.Fatalf("memory warning missing = %q", items[0].Content)
	}
}

func TestLocalJobQueueIdempotenceRetryAndLeaseRecovery(t *testing.T) {
	queue := NewLocalJobQueue(2)
	if err := queue.Enqueue(context.Background(), pmemory.Job{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), pmemory.Job{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Claim(context.Background(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempt != 1 {
		t.Fatalf("attempt = %d", job.Attempt)
	}
	if err := queue.Retry(context.Background(), job.ID, errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(context.Background(), "worker-b"); !errors.Is(err, pmemory.ErrNoJob) {
		t.Fatalf("immediate retry claim = %v", err)
	}
	queue.mu.Lock()
	retried := queue.pending[job.ID]
	retried.AvailableAt = time.Time{}
	queue.pending[job.ID] = retried
	queue.mu.Unlock()
	if _, err := queue.Claim(context.Background(), "worker-b"); err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	if err := queue.Enqueue(context.Background(), pmemory.Job{ID: "two"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.Claim(context.Background(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	claimed.LeaseExpiresAt = time.Now().Add(-time.Second)
	queue.claimed[claimed.ID] = claimed
	queue.mu.Unlock()
	if _, err := queue.Claim(context.Background(), "worker-b"); err != nil {
		t.Fatal(err)
	}
}

func TestRedactionRejectsSecrets(t *testing.T) {
	candidate := pmemory.Candidate{Kind: pmemory.KindReference, Scope: "global", Summary: "credentials", Content: "Bearer abcdefghijklmnop"}
	_, sensitive := sanitizeCandidate(candidate)
	if !sensitive {
		t.Fatal("secret candidate was not marked sensitive")
	}
	redacted, changed := redactText("api_key=secret")
	if !changed || strings.Contains(redacted, "secret") {
		t.Fatalf("redacted = %q, changed=%v", redacted, changed)
	}
	_, sensitive = sanitizeCandidate(pmemory.Candidate{Kind: pmemory.KindReference, Scope: "global", Summary: "metadata", Content: "safe", Tags: []string{"api_key=secret-value"}})
	if !sensitive {
		t.Fatal("secret candidate tag was not marked sensitive")
	}
}

func TestWorkerCloseDrainsQueuedCapture(t *testing.T) {
	store := newTestStore()
	worker := NewWorker(WorkerOptions{
		Mode: ModeAutomatic,
		Extractor: extractorFunc(func(_ context.Context, request pmemory.ExtractionRequest) (pmemory.ExtractionResult, error) {
			return pmemory.ExtractionResult{Candidates: []pmemory.Candidate{{Kind: pmemory.KindReference, Scope: "global", Summary: "captured", Content: string(request.Transcript)}}}, nil
		}),
		Store: store,
	})
	worker.Start()
	if err := worker.Enqueue(context.Background(), pmemory.ExtractionRequest{JobID: "capture:one", Transcript: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	values, err := store.Search(context.Background(), pmemory.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Content != "one" {
		t.Fatalf("drained values = %#v", values)
	}
}

func TestAfterRunUsesUniqueCaptureJobsForRepeatedRunID(t *testing.T) {
	store := newTestStore()
	ext, err := New(
		WithStore(store),
		WithExtractor(extractorFunc(func(_ context.Context, request pmemory.ExtractionRequest) (pmemory.ExtractionResult, error) {
			return pmemory.ExtractionResult{Candidates: []pmemory.Candidate{{Kind: pmemory.KindReference, Scope: "global", Summary: request.JobID, Content: "captured"}}}, nil
		})),
		WithConfig(Config{Enabled: true, Mode: ModeAutomatic, AutoExtract: true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRuntimeIdentity(context.Background(), agent.RuntimeIdentity{RunID: "reused-run", SessionID: "session"})
	turn := agent.TurnContext{RunID: "reused-run", SessionID: "session"}
	if err := ext.beforeTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	result := agent.RunResult{State: agent.StateCompleted, Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "capture"}}}}}
	if err := ext.afterRun(ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := ext.afterRun(ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := ext.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	values, err := store.Search(context.Background(), pmemory.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("repeated capture values = %#v", values)
	}
}
