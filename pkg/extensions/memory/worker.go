package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

const (
	ModeExplicit  = "explicit"
	ModeSuggest   = "suggest"
	ModeAutomatic = "automatic"
	jobCapture    = "memory.capture"
)

// WorkerOptions configures bounded asynchronous extraction and publication.
type WorkerOptions struct {
	Mode        string
	Queue       pmemory.JobQueue
	Extractor   pmemory.Extractor
	Store       pmemory.Store
	WorkerID    string
	QueueSize   int
	MaxAttempts int
}

// Worker runs capture jobs without blocking the agent response.
type Worker struct {
	queue       pmemory.JobQueue
	extractor   pmemory.Extractor
	store       pmemory.Store
	mode        string
	workerID    string
	maxAttempts int
	localQueue  *LocalJobQueue
	cancel      context.CancelFunc
	done        chan struct{}
	stop        chan struct{}
	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	stopOnce    sync.Once
	doneOnce    sync.Once
}

func NewWorker(opts WorkerOptions) *Worker {
	queue := opts.Queue
	localQueue, _ := queue.(*LocalJobQueue)
	if queue == nil {
		localQueue = NewLocalJobQueue(opts.QueueSize)
		queue = localQueue
	}
	mode := opts.Mode
	if mode != ModeExplicit && mode != ModeSuggest && mode != ModeAutomatic {
		mode = ModeSuggest
	}
	workerID := opts.WorkerID
	if workerID == "" {
		workerID = "memory-worker"
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	return &Worker{queue: queue, extractor: opts.Extractor, store: opts.Store, mode: mode, workerID: workerID, maxAttempts: attempts, localQueue: localQueue, done: make(chan struct{}), stop: make(chan struct{})}
}
func (w *Worker) Start() {
	if w == nil {
		return
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.started || w.closed {
		return
	}
	w.started = true
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.loop(ctx)
}
func (w *Worker) Enqueue(ctx context.Context, request pmemory.ExtractionRequest) error {
	if w == nil || w.queue == nil {
		return nil
	}
	w.lifecycleMu.Lock()
	closed := w.closed
	w.lifecycleMu.Unlock()
	if closed {
		return errors.New("memory worker is closed")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	jobID := request.JobID
	if jobID == "" {
		jobID = stableJobID(request)
	}
	return w.queue.Enqueue(ctx, pmemory.Job{ID: jobID, Kind: jobCapture, IdempotencyKey: jobID, SessionID: request.SessionID, RunID: request.RunID, ProjectID: request.ProjectID, TenantID: request.TenantID, Payload: payload, MaxAttempts: w.maxAttempts})
}
func (w *Worker) loop(ctx context.Context) {
	defer close(w.done)
	for {
		stopping := w.isStopping()
		job, err := w.queue.Claim(ctx, w.workerID)
		if err == nil {
			if processErr := w.process(ctx, job); processErr != nil {
				_ = w.queue.Retry(context.Background(), job.ID, processErr)
			} else {
				_ = w.queue.Complete(context.Background(), job.ID)
			}
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if !errors.Is(err, pmemory.ErrNoJob) {
			if stopping {
				return
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-timer.C:
			case <-w.stop:
				if !timer.Stop() {
					<-timer.C
				}
			}
			continue
		}
		if stopping {
			return
		}
		if w.localQueue != nil {
			select {
			case <-w.localQueue.Notify():
			case <-w.stop:
			case <-ctx.Done():
				return
			}
		} else {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-w.stop:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Worker) isStopping() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}
func (w *Worker) process(ctx context.Context, job pmemory.Job) error {
	if job.Kind != jobCapture {
		return fmt.Errorf("unknown memory job kind %q", job.Kind)
	}
	var request pmemory.ExtractionRequest
	if err := json.Unmarshal(job.Payload, &request); err != nil {
		return fmt.Errorf("decode memory capture: %w", err)
	}
	if w.extractor == nil || w.store == nil || w.mode == ModeExplicit {
		return nil
	}
	request.Transcript = []byte(RedactText(string(request.Transcript)))
	result, err := w.extractor.Extract(ctx, request)
	if err != nil {
		return err
	}
	for _, candidate := range result.Candidates {
		if err := w.publish(ctx, request, candidate); err != nil {
			if errors.Is(err, pmemory.ErrSensitiveContent) {
				continue
			}
			return err
		}
	}
	return nil
}
func (w *Worker) publish(ctx context.Context, request pmemory.ExtractionRequest, candidate pmemory.Candidate) error {
	clean, sensitive := sanitizeCandidate(candidate)
	if sensitive || strings.TrimSpace(normalizedCandidateText(clean)) == "" {
		return pmemory.ErrSensitiveContent
	}
	if clean.Kind == "project_decision" {
		clean.Kind = pmemory.KindDecision
	}
	if clean.Kind == "project_fact" {
		clean.Kind = pmemory.KindProjectFact
	}
	if !clean.Kind.Valid() {
		return pmemory.ErrInvalidMemory
	}
	scope := parseScope(clean.Scope)
	if !pmemory.ValidScope(scope) || scope == ycontext.ScopeRequest {
		return pmemory.ErrInvalidMemory
	}
	value := pmemory.Memory{Kind: clean.Kind, Scope: scope, Summary: clean.Summary, Content: clean.Content, Tags: clean.Tags, Confidence: clean.Confidence, Source: pmemory.SourceRef{SessionID: request.SessionID, RunID: request.RunID, ProjectID: request.ProjectID, TenantID: request.TenantID, WorkspaceID: request.WorkspaceID}, ExpiresAt: clean.ExpiresAt, LastVerified: request.CapturedAt, Status: pmemory.StatusActive}
	value.ID = pmemory.StableID(value)
	if w.mode == ModeSuggest {
		value.Status = pmemory.StatusPending
	}
	existing, err := w.store.Search(ctx, pmemory.Query{TenantID: request.TenantID, WorkspaceID: request.WorkspaceID, ProjectID: request.ProjectID, SessionID: request.SessionID, Terms: strings.Fields(clean.Summary), IncludePending: true, MaxItems: 64})
	if err != nil {
		return err
	}
	for _, prior := range existing {
		if strings.EqualFold(strings.TrimSpace(prior.Summary), strings.TrimSpace(value.Summary)) || strings.EqualFold(strings.TrimSpace(prior.Content), strings.TrimSpace(value.Content)) {
			return nil
		}
	}
	return w.store.Save(ctx, value)
}

// Close stops workers and waits for the current local job to finish or the
// caller's deadline. Durable queues remain recoverable through lease expiry.
func (w *Worker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.lifecycleMu.Lock()
	if !w.closed {
		w.closed = true
		w.stopOnce.Do(func() { close(w.stop) })
	}
	started := w.started
	cancel := w.cancel
	w.lifecycleMu.Unlock()
	if !started {
		w.doneOnce.Do(func() { close(w.done) })
		return nil
	}

	// Closing is graceful: the stop signal prevents new waits, but the loop
	// keeps claiming and processing already queued jobs before it exits.
	// Cancellation is reserved for a caller-provided deadline.
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return ctx.Err()
	}
}
func stableJobID(request pmemory.ExtractionRequest) string {
	data, _ := json.Marshal(request)
	value := pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeSession, Summary: request.SessionID + request.RunID, Content: string(data), Source: pmemory.SourceRef{SessionID: request.SessionID, RunID: request.RunID}}
	return pmemory.StableID(value)
}
