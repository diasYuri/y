package memory

import (
	"context"

	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

// RemoteBackend is implemented by a deployment adapter backed by a
// transactional metadata store and durable document/object storage.
type RemoteBackend interface{ pmemory.Store }

// RemoteStore adds the memory subsystem's fail-open read semantics around a
// remote backend. Writes are deliberately returned to the caller so a durable
// queue can retry them instead of silently dropping them.
type RemoteStore struct {
	Backend  RemoteBackend
	FailOpen bool
}

func (s *RemoteStore) Search(ctx context.Context, query pmemory.Query) ([]pmemory.Memory, error) {
	if s == nil || s.Backend == nil {
		return nil, nil
	}
	values, err := s.Backend.Search(ctx, query)
	if err != nil && s.FailOpen {
		return nil, nil
	}
	return values, err
}
func (s *RemoteStore) Read(ctx context.Context, id string) (pmemory.Memory, error) {
	if s == nil || s.Backend == nil {
		return pmemory.Memory{}, pmemory.ErrNotFound
	}
	value, err := s.Backend.Read(ctx, id)
	if err != nil && s.FailOpen {
		return pmemory.Memory{}, pmemory.ErrNotFound
	}
	return value, err
}
func (s *RemoteStore) Save(ctx context.Context, value pmemory.Memory) error {
	if s == nil || s.Backend == nil {
		return pmemory.ErrNotFound
	}
	return s.Backend.Save(ctx, value)
}
func (s *RemoteStore) Forget(ctx context.Context, id string) error {
	if s == nil || s.Backend == nil {
		return pmemory.ErrNotFound
	}
	return s.Backend.Forget(ctx, id)
}
func (s *RemoteStore) Version(ctx context.Context, request ycontext.Request) (string, error) {
	if s == nil || s.Backend == nil {
		return "unavailable", nil
	}
	version, err := s.Backend.Version(ctx, request)
	if err != nil && s.FailOpen {
		return "unavailable", nil
	}
	return version, err
}

var _ pmemory.Store = (*RemoteStore)(nil)
