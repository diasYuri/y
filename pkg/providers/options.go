package providers

import (
	"context"
	"io"
	"net/http"

	"github.com/yuri/y/pkg/ai"
)

// ApplyMiddlewares wraps base with the supplied middlewares in registration
// order: the first middleware in the slice ends up outermost (it sees the
// request first when travelling outward, and the response last when
// travelling inward). A nil base falls back to [http.DefaultTransport]. Nil
// middlewares are skipped. The function returns base unchanged when there
// are no middlewares to apply.
//
// Providers use this helper from their client() method to centralize the
// transport-stack composition logic.
func ApplyMiddlewares(base http.RoundTripper, mws []Middleware) http.RoundTripper {
	if len(mws) == 0 {
		return base
	}
	transport := base
	if transport == nil {
		transport = http.DefaultTransport
	}
	for i := len(mws) - 1; i >= 0; i-- {
		if mw := mws[i]; mw != nil {
			transport = mw(transport)
		}
	}
	return transport
}

// ApplyCommonClient returns a copy of client with its Transport replaced by
// the result of [ApplyMiddlewares]. A nil client is replaced with
// [http.DefaultClient]. The original client is not mutated.
func ApplyCommonClient(client *http.Client, mws []Middleware) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if len(mws) == 0 {
		return client
	}
	out := *client
	out.Transport = ApplyMiddlewares(client.Transport, mws)
	return &out
}

// CommonOptions is the embeddable options struct shared by every concrete
// provider implementation. Providers compose this with their provider-specific
// fields (apiKey, baseURL, model defaults, etc.) and reuse the helpers below
// to apply middleware, inspectors, dry-run, retry policy, etc.
//
// CommonOptions is part of the public package API so external providers can
// participate in the same option taxonomy.
type CommonOptions struct {
	HTTPClient  *http.Client
	Middlewares []Middleware
	RetryPolicy RetryPolicy
	Inspector   RequestInspector
	DryRun      bool
	MaxEvent    int64
	ProviderID  string // set by the provider; used for typed errors
}

// ApplyMiddleware composes the provider's Middlewares onto the supplied
// transport. The first registered middleware wraps the others (i.e. it is
// invoked first when a request travels outward). Returns a copy of client with
// the wrapped Transport.
func (o *CommonOptions) ApplyMiddleware(client *http.Client) *http.Client {
	if o == nil || len(o.Middlewares) == 0 {
		return client
	}
	if client == nil {
		client = &http.Client{}
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	// Apply in reverse so first registered ends up outermost.
	for i := len(o.Middlewares) - 1; i >= 0; i-- {
		if mw := o.Middlewares[i]; mw != nil {
			transport = mw(transport)
		}
	}
	out := *client
	out.Transport = transport
	return &out
}

// Inspect invokes the configured RequestInspector, if any. Safe on a nil
// receiver.
func (o *CommonOptions) Inspect(req *http.Request) {
	if o == nil || o.Inspector == nil || req == nil {
		return
	}
	o.Inspector(req)
}

// IsDryRun reports whether the provider was constructed with WithDryRun.
func (o *CommonOptions) IsDryRun() bool {
	return o != nil && o.DryRun
}

// SyntheticDryRunStream returns an EventStream that emits a single StopEvent
// (StopReasonStop) and EOF. Providers use it to short-circuit Stream when
// DryRun mode is active.
func SyntheticDryRunStream() EventStream {
	return &syntheticStream{
		events: []ai.Event{ai.StopEvent{Reason: ai.StopReasonStop}},
		done:   make(chan struct{}),
	}
}

type syntheticStream struct {
	events []ai.Event
	idx    int
	done   chan struct{}
}

func (s *syntheticStream) Next(ctx context.Context) (ai.Event, error) {
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
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	return nil, io.EOF
}

func (s *syntheticStream) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}
