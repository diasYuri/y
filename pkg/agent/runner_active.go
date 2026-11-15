package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/diasYuri/y/pkg/ai"
)

var ErrRunnerClosed = errors.New("agent: runner is closed")
var ErrRunNotActive = errors.New("agent: run is not active")

type activeExecution struct {
	agent   *Agent
	settled bool
	cancel  context.CancelFunc
}

func (r *AgentRunner) beginExecution(ctx context.Context, id string) (context.Context, func(), error) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.closed {
		return nil, nil, ErrRunnerClosed
	}
	if r.active == nil {
		r.active = make(map[string]*activeExecution)
	}
	ctx, cancel := context.WithCancel(ctx)
	r.active[id] = &activeExecution{cancel: cancel}
	r.activeWG.Add(1)
	return ctx, func() {
		cancel()
		r.activeMu.Lock()
		delete(r.active, id)
		r.activeMu.Unlock()
		r.activeWG.Done()
	}, nil
}

func (r *AgentRunner) activate(id string, instance *Agent, cancel context.CancelFunc) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if active := r.active[id]; active != nil {
		active.agent = instance
	}
}

// Steer queues messages for the active run's next steering safe point.
// It never exposes the Agent owned by the runner.
func (r *AgentRunner) Steer(id string, messages ...ai.Message) error {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	active := r.active[id]
	if active == nil || active.agent == nil || active.settled {
		return ErrRunNotActive
	}
	active.agent.Steer(messages...)
	return nil
}

// FollowUp queues messages using the Agent's follow-up semantics. Queued messages
// are included in the runner's final checkpoint, including on cancellation.
func (r *AgentRunner) FollowUp(id string, messages ...ai.Message) error {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	active := r.active[id]
	if active == nil || active.agent == nil || active.settled {
		return ErrRunNotActive
	}
	active.agent.FollowUp(messages...)
	return nil
}

// CancelRun cancels the active execution context and all context-bound children.
func (r *AgentRunner) CancelRun(id string) error {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	active := r.active[id]
	if active == nil || active.settled {
		return ErrRunNotActive
	}
	active.cancel()
	return nil
}

// Subscribe observes canonical envelopes after they have been durably appended.
// Callbacks execute synchronously, in per-run sequence order, and must return
// promptly. They may use active controls and unsubscribe, but must not call Run
// recursively or block waiting for that run to finish. Different runs can call a
// sink concurrently. The returned function is safe to call more than once.
func (r *AgentRunner) Subscribe(sink func(EventEnvelope)) func() {
	if sink == nil {
		return func() {}
	}
	r.subsMu.Lock()
	if r.subs == nil {
		r.subs = make(map[uint64]func(EventEnvelope))
	}
	r.nextSub++
	id := r.nextSub
	r.subs[id] = sink
	r.subsMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.subsMu.Lock(); delete(r.subs, id); r.subsMu.Unlock() }) }
}

func (r *AgentRunner) publish(event EventEnvelope) {
	r.subsMu.RLock()
	sinks := make([]func(EventEnvelope), 0, len(r.subs))
	for _, sink := range r.subs {
		sinks = append(sinks, sink)
	}
	r.subsMu.RUnlock()
	for _, sink := range sinks {
		copy := event
		copy.Payload = append([]byte(nil), event.Payload...)
		if event.Error != nil {
			e := *event.Error
			copy.Error = &e
		}
		sink(copy)
	}
}

// Close rejects new executions, cancels active ones and waits for final durable
// checkpoints. Resource registries/providers remain owned by the caller's scope.
func (r *AgentRunner) Close(ctx context.Context) error {
	r.activeMu.Lock()
	r.closed = true
	for _, active := range r.active {
		active.cancel()
	}
	r.activeMu.Unlock()
	done := make(chan struct{})
	go func() { r.activeWG.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (r *AgentRunner) settle(id string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if active := r.active[id]; active != nil {
		active.settled = true
	}
}
