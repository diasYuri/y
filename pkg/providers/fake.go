package providers

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
)

const (
	defaultFakeProviderID = "fake"
	defaultFakeModelID    = "fake-1"
)

// FakeProvider is an in-memory provider for unit tests. It returns queued
// responses in FIFO order and never performs network or subprocess work.
//
// Deprecated: prefer pkg/providers/providertest.FakeProvider, which is a
// re-export of this type. The canonical location is providertest; the type
// remains in pkg/providers to avoid an import cycle (providertest depends on
// providers via the Provider interface).
type FakeProvider struct {
	mu           sync.Mutex
	id           string
	models       []ai.Model
	responses    []FakeResponse
	callCount    int
	closed       bool
	capabilities Capabilities
	tokensFn     func(modelID string, c ai.Context) (int64, error)
}

// FakeResponse is one queued streaming response.
type FakeResponse struct {
	Events []ai.Event
	Delay  time.Duration
	Err    error
}

// FakeOption configures a FakeProvider.
type FakeOption func(*FakeProvider)

// NewFakeProvider creates a fake provider with one default text-capable model.
func NewFakeProvider(opts ...FakeOption) *FakeProvider {
	p := &FakeProvider{
		id: defaultFakeProviderID,
		models: []ai.Model{{
			ID:            defaultFakeModelID,
			Name:          "Fake Model",
			API:           "fake",
			Provider:      defaultFakeProviderID,
			BaseURL:       "http://localhost:0",
			Input:         []ai.InputKind{ai.InputText, ai.InputImage},
			ContextWindow: 128000,
			MaxTokens:     16384,
		}},
		capabilities: Capabilities{Vision: true, Tools: true, Streaming: true},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithFakeID sets the provider ID.
func WithFakeID(id string) FakeOption {
	return func(p *FakeProvider) {
		if id != "" {
			p.id = id
		}
	}
}

// WithFakeModels replaces the fake model list.
func WithFakeModels(models ...ai.Model) FakeOption {
	return func(p *FakeProvider) {
		if len(models) == 0 {
			return
		}
		p.models = append([]ai.Model(nil), models...)
	}
}

// WithFakeResponses queues responses returned by Stream.
func WithFakeResponses(responses ...FakeResponse) FakeOption {
	return func(p *FakeProvider) {
		p.responses = append([]FakeResponse(nil), responses...)
	}
}

// WithFakeCapabilities overrides the capabilities returned by Capabilities.
func WithFakeCapabilities(c Capabilities) FakeOption {
	return func(p *FakeProvider) { p.capabilities = c }
}

// WithFakeCountTokens overrides the CountTokens implementation. The default
// returns EstimateTokens(c).
func WithFakeCountTokens(fn func(modelID string, c ai.Context) (int64, error)) FakeOption {
	return func(p *FakeProvider) { p.tokensFn = fn }
}

// ID returns the provider identifier.
func (p *FakeProvider) ID() string {
	if p == nil || p.id == "" {
		return defaultFakeProviderID
	}
	return p.id
}

// Models returns the fake models.
func (p *FakeProvider) Models(ctx context.Context) ([]ai.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.Model(nil), p.models...), nil
}

// CountTokens returns a token estimate via EstimateTokens by default. Override
// with WithFakeCountTokens.
func (p *FakeProvider) CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p != nil && p.tokensFn != nil {
		return p.tokensFn(modelID, c)
	}
	return EstimateTokens(c), nil
}

// Capabilities returns the configured capabilities.
func (p *FakeProvider) Capabilities(modelID string) Capabilities {
	if p == nil {
		return Capabilities{}
	}
	return p.capabilities
}

// Close marks the provider closed. Idempotent.
func (p *FakeProvider) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// IsClosed reports whether Close has been called.
func (p *FakeProvider) IsClosed() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Stream returns the next queued fake response.
func (p *FakeProvider) Stream(ctx context.Context, _ StreamRequest) (EventStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.callCount++
	if len(p.responses) == 0 {
		err := errors.New("no fake provider responses queued")
		return newFakeEventStream(FakeResponse{
			Events: []ai.Event{
				ai.NewErrorEvent("fake_empty", err),
				ai.StopEvent{Reason: ai.StopReasonError},
			},
		}), nil
	}

	response := p.responses[0]
	copy(p.responses, p.responses[1:])
	p.responses[len(p.responses)-1] = FakeResponse{}
	p.responses = p.responses[:len(p.responses)-1]
	return newFakeEventStream(response), nil
}

// AppendResponses appends responses to the pending fake queue.
func (p *FakeProvider) AppendResponses(responses ...FakeResponse) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, responses...)
}

// PendingResponseCount returns the number of queued fake responses.
func (p *FakeProvider) PendingResponseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.responses)
}

// CallCount returns how many times Stream has been called.
func (p *FakeProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

type fakeEventStream struct {
	mu      sync.Mutex
	events  []ai.Event
	delay   time.Duration
	err     error
	index   int
	errSent bool
	done    chan struct{}
	once    sync.Once
}

func newFakeEventStream(response FakeResponse) *fakeEventStream {
	return &fakeEventStream{
		events: append([]ai.Event(nil), response.Events...),
		delay:  response.Delay,
		err:    response.Err,
		done:   make(chan struct{}),
	}
}

func (s *fakeEventStream) Next(ctx context.Context) (ai.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.done:
		return nil, ErrStreamClosed
	default:
	}

	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.done:
			return nil, ErrStreamClosed
		case <-timer.C:
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-s.done:
		return nil, ErrStreamClosed
	default:
	}
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.err != nil && !s.errSent {
		s.errSent = true
		return nil, s.err
	}
	return nil, io.EOF
}

func (s *fakeEventStream) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}

var _ Provider = (*FakeProvider)(nil)
