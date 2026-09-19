package memory

import (
	"context"

	ycontext "github.com/yuri/y/pkg/context"
	pmemory "github.com/yuri/y/pkg/memory"
)

// NoopStore implements stateless memory. It never touches disk and treats
// writes as successful so callers do not need a special execution path.
type NoopStore struct{}

func (NoopStore) Search(ctx context.Context, _ pmemory.Query) ([]pmemory.Memory, error) {
	return nil, ctxErr(ctx)
}
func (NoopStore) Read(ctx context.Context, _ string) (pmemory.Memory, error) {
	if err := ctxErr(ctx); err != nil {
		return pmemory.Memory{}, err
	}
	return pmemory.Memory{}, pmemory.ErrNotFound
}
func (NoopStore) Save(ctx context.Context, value pmemory.Memory) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return pmemory.Validate(value)
}
func (NoopStore) Forget(ctx context.Context, _ string) error { return ctxErr(ctx) }
func (NoopStore) Version(ctx context.Context, _ ycontext.Request) (string, error) {
	if err := ctxErr(ctx); err != nil {
		return "", err
	}
	return "noop", nil
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

var _ pmemory.Store = NoopStore{}
