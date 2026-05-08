// Package stream adapts provider SSE readers to the shared pull stream
// interface used by pkg/providers.
package stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/internal/sse"
)

// Consumer converts one decoded SSE data payload to zero or more normalized
// events. It must not retain data beyond the call unless it copies it.
type Consumer func(data []byte) []ai.Event

type result struct {
	event ai.Event
	err   error
}

type EventStream struct {
	body          io.ReadCloser
	cancel        context.CancelFunc
	maxEventBytes int64
	readCode      string
	consumer      Consumer
	results       chan result
	done          chan struct{}
	once          sync.Once
}

// New starts an SSE reader goroutine and returns a pull-based event stream.
func New(body io.ReadCloser, maxEventBytes int64, cancel context.CancelFunc, readCode string, consumer Consumer) providers.EventStream {
	s := &EventStream{
		body:          body,
		cancel:        cancel,
		maxEventBytes: maxEventBytes,
		readCode:      readCode,
		consumer:      consumer,
		results:       make(chan result, 4),
		done:          make(chan struct{}),
	}
	go s.readLoop()
	return s
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
	case result, ok := <-s.results:
		if !ok {
			return nil, io.EOF
		}
		if result.err != nil {
			return nil, result.err
		}
		return result.event, nil
	}
}

func (s *EventStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
		_ = s.body.Close()
	})
	return nil
}

func (s *EventStream) readLoop() {
	defer close(s.results)
	defer s.body.Close()
	defer func() {
		if s.cancel != nil {
			s.cancel()
		}
	}()

	reader := bufio.NewReader(s.body)
	for {
		data, err := sse.ReadData(reader, s.maxEventBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			s.emit(ai.NewErrorEvent(s.readCode, err))
			return
		}
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			continue
		}
		for _, event := range s.consumer(data) {
			if !s.emit(event) {
				return
			}
		}
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
