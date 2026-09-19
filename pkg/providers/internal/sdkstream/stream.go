// Package sdkstream adapts official provider SDK streams to the provider
// package's pull-based EventStream contract.
package sdkstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
)

// ErrEventTooLarge is returned when one SSE frame exceeds the configured
// provider limit. It keeps the public WithMaxEventBytes contract while the
// official SDK owns SSE decoding.
var ErrEventTooLarge = errors.New("sdk stream event exceeds configured limit")

// LimitClient returns a client copy that bounds individual SSE frames in
// response bodies. The wrapper is intentionally transport-level so it works
// with all three official SDKs without replacing their decoders.
func LimitClient(client *http.Client, maxEvent int64) *http.Client {
	if client == nil || maxEvent <= 0 {
		return client
	}
	out := *client
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	out.Transport = limitTransport{next: next, maxEvent: maxEvent}
	return &out
}

type limitTransport struct {
	next     http.RoundTripper
	maxEvent int64
}

func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &limitBody{ReadCloser: resp.Body, maxEvent: t.maxEvent}
	return resp, nil
}

type limitBody struct {
	io.ReadCloser
	maxEvent int64
	frame    int64
	last     [4]byte
	lastN    int
}

func (b *limitBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	for i := 0; i < n; i++ {
		b.frame++
		if b.frame > b.maxEvent {
			return i + 1, ErrEventTooLarge
		}
		if b.lastN < len(b.last) {
			b.last[b.lastN] = p[i]
			b.lastN++
		} else {
			copy(b.last[:], b.last[1:])
			b.last[len(b.last)-1] = p[i]
		}
		if (b.lastN >= 2 && b.last[b.lastN-2] == '\n' && b.last[b.lastN-1] == '\n') ||
			(b.lastN == 4 && b.last == [4]byte{'\r', '\n', '\r', '\n'}) {
			b.frame = 0
		}
	}
	return n, err
}

// New wraps an SDK stream. The SDK's Next method is deliberately consumed in
// one goroutine because the official SDK streams are not safe for concurrent
// reads and do not accept a caller context on each Next call.
func New[T any](
	next func() bool,
	current func() T,
	streamErr func() error,
	closeUpstream func() error,
	convert func(T) []ai.Event,
) providers.EventStream {
	return NewWithNormalize(next, current, streamErr, closeUpstream, convert, nil)
}

// NewWithNormalize wraps an SDK stream and normalizes errors observed after
// the initial response, preserving the provider error taxonomy for transport
// failures and SDK-decoded error events.
func NewWithNormalize[T any](
	next func() bool,
	current func() T,
	streamErr func() error,
	closeUpstream func() error,
	convert func(T) []ai.Event,
	normalize func(error) error,
) providers.EventStream {
	s := newTyped(next, current, streamErr, closeUpstream, convert, normalize)
	go s.readLoop()
	return s
}

type result struct {
	event ai.Event
	err   error
}

type EventStream struct {
	next          func() bool
	current       func() any
	streamErr     func() error
	closeUpstream func() error
	convert       func(any) []ai.Event
	results       chan result
	done          chan struct{}
	once          sync.Once
	normalize     func(error) error
}

// The generic constructor cannot assign a generic function to an any-valued
// function directly, so this small typed wrapper keeps the public constructor
// ergonomic while the implementation remains non-generic.
func newTyped[T any](next func() bool, current func() T, streamErr func() error, closeUpstream func() error, convert func(T) []ai.Event, normalize func(error) error) *EventStream {
	return &EventStream{
		next:          next,
		current:       func() any { return current() },
		streamErr:     streamErr,
		closeUpstream: closeUpstream,
		convert:       func(value any) []ai.Event { return typedConvert(convert, value) },
		results:       make(chan result, 4),
		done:          make(chan struct{}),
		normalize:     normalize,
	}
}

func typedConvert[T any](convert func(T) []ai.Event, value any) []ai.Event {
	typed, ok := value.(T)
	if !ok {
		return []ai.Event{ai.NewErrorEvent("sdk_stream_type", io.ErrUnexpectedEOF)}
	}
	return convert(typed)
}

func (s *EventStream) readLoop() {
	defer close(s.results)
	defer func() { _ = s.closeUpstream() }()
	for s.next() {
		for _, event := range s.convert(s.current()) {
			if !s.emit(event) {
				return
			}
		}
	}
	if err := s.streamErr(); err != nil {
		err = normalizeSDKError(err, s.normalize)
		s.emit(ai.NewErrorEvent("sdk_stream_read", err))
	}
}

func (s *EventStream) emit(event ai.Event) bool {
	select {
	case <-s.done:
		return false
	case s.results <- result{event: event}:
		return true
	}
}

func (s *EventStream) Next(ctx context.Context) (ai.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.done:
		return nil, providers.ErrStreamClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, providers.ErrStreamClosed
	case item, ok := <-s.results:
		if !ok {
			return nil, io.EOF
		}
		if item.err != nil {
			return nil, item.err
		}
		return item.event, nil
	}
}

func (s *EventStream) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.closeUpstream()
	})
	return err
}

// NewIterator adapts an iterator-shaped SDK stream, such as Google's
// iter.Seq2 response stream. It waits until the SDK has either produced its
// first response or reported its initial HTTP error, preserving the provider
// contract that request failures are returned from Stream rather than from
// the first Next call.
func NewIterator[T any](
	ctx context.Context,
	iterate func(func(T, error) bool),
	convert func(T) []ai.Event,
	normalize func(error) error,
) (providers.EventStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	s := &iteratorEventStream{
		results: make(chan result, 4),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	ready := make(chan error, 1)
	go func() {
		defer close(s.results)
		started := false
		signalReady := func(err error) {
			if started {
				return
			}
			started = true
			ready <- err
		}
		emit := func(event ai.Event) bool {
			select {
			case <-s.done:
				return false
			case <-streamCtx.Done():
				return false
			case s.results <- result{event: event}:
				return true
			}
		}
		iterate(func(value T, err error) bool {
			if err != nil {
				err = normalizeSDKError(err, normalize)
				if !started {
					signalReady(err)
					return false
				}
				emit(ai.NewErrorEvent("sdk_stream_read", err))
				return false
			}
			signalReady(nil)
			for _, event := range convert(value) {
				if !emit(event) {
					return false
				}
			}
			return true
		})
		if !started {
			if err := streamCtx.Err(); err != nil {
				signalReady(err)
			} else {
				signalReady(nil)
			}
		}
	}()
	if err := <-ready; err != nil {
		cancel()
		return nil, err
	}
	return s, nil
}

func normalizeSDKError(err error, normalize func(error) error) error {
	if normalize == nil {
		return err
	}
	return normalize(err)
}

type iteratorEventStream struct {
	results chan result
	done    chan struct{}
	cancel  context.CancelFunc
	once    sync.Once
}

func (s *iteratorEventStream) Next(ctx context.Context) (ai.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.done:
		return nil, providers.ErrStreamClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, providers.ErrStreamClosed
	case item, ok := <-s.results:
		if !ok {
			return nil, io.EOF
		}
		return item.event, item.err
	}
}

func (s *iteratorEventStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.cancel()
	})
	return nil
}

var _ providers.EventStream = (*EventStream)(nil)
var _ providers.EventStream = (*iteratorEventStream)(nil)
