package mom

import (
	"bytes"
	"context"
	"io"
	"time"
)

// FakeClock is a deterministic Clock implementation used in tests.
type FakeClock struct {
	Current time.Time
}

// Now returns FakeClock.Current.
func (f *FakeClock) Now() time.Time {
	if f == nil || f.Current.IsZero() {
		return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	}
	return f.Current.UTC()
}

// Advance moves the fake clock forward.
func (f *FakeClock) Advance(d time.Duration) {
	f.Current = f.Now().Add(d)
}

// FakeDownloader returns canned bytes for given URLs. Used by store tests.
type FakeDownloader struct {
	Files map[string][]byte
	Calls []string
	Err   error
}

// Download returns the bytes mapped to url.
func (f *FakeDownloader) Download(_ context.Context, url string) (io.ReadCloser, error) {
	f.Calls = append(f.Calls, url)
	if f.Err != nil {
		return nil, f.Err
	}
	data, ok := f.Files[url]
	if !ok {
		data = []byte("fake")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
