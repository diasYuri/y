package memory

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	pmemory "github.com/diasYuri/y/pkg/memory"
)

const defaultJobLease = 30 * time.Second

// LocalJobQueue is a bounded, idempotent in-process queue.
type LocalJobQueue struct {
	mu       sync.Mutex
	capacity int
	pending  map[string]pmemory.Job
	claimed  map[string]pmemory.Job
	notify   chan struct{}
	now      func() time.Time
}

func NewLocalJobQueue(capacity int) *LocalJobQueue {
	if capacity <= 0 {
		capacity = 128
	}
	return &LocalJobQueue{capacity: capacity, pending: make(map[string]pmemory.Job), claimed: make(map[string]pmemory.Job), notify: make(chan struct{}, 1), now: time.Now}
}
func (q *LocalJobQueue) Enqueue(ctx context.Context, job pmemory.Job) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if job.ID == "" {
		return errors.New("memory job id is required")
	}
	now := q.currentTime()
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[job.ID]; ok {
		return nil
	}
	if _, ok := q.claimed[job.ID]; ok {
		return nil
	}
	if len(q.pending)+len(q.claimed) >= q.capacity {
		return pmemory.ErrQueueFull
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.AvailableAt.IsZero() {
		job.AvailableAt = now
	}
	job.UpdatedAt = now
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 5
	}
	q.pending[job.ID] = cloneJob(job)
	q.signal()
	return nil
}
func (q *LocalJobQueue) Claim(ctx context.Context, workerID string) (pmemory.Job, error) {
	if err := ctxErr(ctx); err != nil {
		return pmemory.Job{}, err
	}
	if workerID == "" {
		return pmemory.Job{}, errors.New("memory worker id is required")
	}
	now := q.currentTime()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requeueExpiredLocked(now)
	ids := make([]string, 0, len(q.pending))
	for id, job := range q.pending {
		if !job.AvailableAt.After(now) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return pmemory.Job{}, pmemory.ErrNoJob
	}
	sort.Strings(ids)
	job := q.pending[ids[0]]
	delete(q.pending, job.ID)
	job.Attempt++
	job.LeaseOwner = workerID
	job.LeaseExpiresAt = now.Add(defaultJobLease)
	job.UpdatedAt = now
	q.claimed[job.ID] = cloneJob(job)
	return cloneJob(job), nil
}
func (q *LocalJobQueue) Complete(ctx context.Context, id string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	q.mu.Lock()
	delete(q.claimed, id)
	delete(q.pending, id)
	q.mu.Unlock()
	return nil
}
func (q *LocalJobQueue) Retry(ctx context.Context, id string, _ error) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	now := q.currentTime()
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.claimed[id]
	if !ok {
		if _, pending := q.pending[id]; pending {
			return nil
		}
		return pmemory.ErrNotFound
	}
	delete(q.claimed, id)
	if job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		return nil
	}
	job.LeaseOwner = ""
	job.LeaseExpiresAt = time.Time{}
	job.AvailableAt = now.Add(time.Duration(job.Attempt*job.Attempt) * 100 * time.Millisecond)
	job.UpdatedAt = now
	q.pending[id] = cloneJob(job)
	q.signal()
	return nil
}
func (q *LocalJobQueue) Notify() <-chan struct{} { return q.notify }
func (q *LocalJobQueue) requeueExpiredLocked(now time.Time) {
	for id, job := range q.claimed {
		if !job.LeaseExpiresAt.IsZero() && !job.LeaseExpiresAt.After(now) {
			delete(q.claimed, id)
			job.LeaseOwner = ""
			job.LeaseExpiresAt = time.Time{}
			job.AvailableAt = now
			q.pending[id] = job
		}
	}
}
func (q *LocalJobQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
func (q *LocalJobQueue) currentTime() time.Time {
	now := time.Now
	if q != nil && q.now != nil {
		now = q.now
	}
	return now().UTC()
}
func cloneJob(job pmemory.Job) pmemory.Job {
	job.Payload = append([]byte(nil), job.Payload...)
	return job
}

// LocalLeaseManager provides expiring leases for local deployments and tests.
type LocalLeaseManager struct {
	mu     sync.Mutex
	leases map[string]localLease
	now    func() time.Time
}
type localLease struct {
	owner     string
	expiresAt time.Time
}

func NewLocalLeaseManager() *LocalLeaseManager {
	return &LocalLeaseManager{leases: make(map[string]localLease), now: time.Now}
}
func (m *LocalLeaseManager) Acquire(ctx context.Context, key, owner string, ttl time.Duration) error {
	return m.change(ctx, key, owner, ttl, false)
}
func (m *LocalLeaseManager) Renew(ctx context.Context, key, owner string, ttl time.Duration) error {
	return m.change(ctx, key, owner, ttl, true)
}
func (m *LocalLeaseManager) Release(ctx context.Context, key, owner string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.leases[key]
	if !ok {
		return nil
	}
	if current.owner != owner {
		return pmemory.ErrLeaseConflict
	}
	delete(m.leases, key)
	return nil
}
func (m *LocalLeaseManager) change(ctx context.Context, key, owner string, ttl time.Duration, renew bool) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if key == "" || owner == "" {
		return errors.New("lease key and owner are required")
	}
	if ttl <= 0 {
		return errors.New("lease ttl must be positive")
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.leases[key]
	if ok && current.expiresAt.After(now) && current.owner != owner {
		return pmemory.ErrLeaseConflict
	}
	if renew && (!ok || current.owner != owner) {
		return pmemory.ErrLeaseConflict
	}
	m.leases[key] = localLease{owner: owner, expiresAt: now.Add(ttl)}
	return nil
}

var _ pmemory.JobQueue = (*LocalJobQueue)(nil)
var _ pmemory.LeaseManager = (*LocalLeaseManager)(nil)
