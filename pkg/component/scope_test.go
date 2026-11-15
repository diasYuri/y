package component

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestScopeClosesWorkersBeforeTrackedResources(t *testing.T) {
	workerStarted := make(chan struct{})
	workerRelease := make(chan struct{})
	var cleanupOrder []int
	scopeCtx, cancel := context.WithCancel(context.Background())
	scope := &Scope{accepting: true, ctx: scopeCtx, cancel: cancel}
	if err := scope.Go(func(ctx context.Context) error {
		close(workerStarted)
		<-ctx.Done()
		<-workerRelease
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := scope.Track(func(context.Context) error { cleanupOrder = append(cleanupOrder, 1); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := scope.Track(func(context.Context) error { cleanupOrder = append(cleanupOrder, 2); return nil }); err != nil {
		t.Fatal(err)
	}
	scope.seal()
	<-workerStarted

	ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if err := scope.close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", err)
	}
	if len(cleanupOrder) != 0 {
		t.Fatalf("cleanups ran before worker exit: %v", cleanupOrder)
	}
	close(workerRelease)
	if err := scope.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cleanupOrder, []int{2, 1}) {
		t.Fatalf("cleanup order = %v, want [2 1]", cleanupOrder)
	}
}

func TestScopeAttemptsAllCleanupsAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first cleanup")
	secondErr := errors.New("second cleanup")
	var calls []int
	scope := &Scope{accepting: true}
	_ = scope.Track(func(context.Context) error { calls = append(calls, 1); return firstErr })
	_ = scope.Track(func(context.Context) error { calls = append(calls, 2); return secondErr })
	err := scope.close(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("close error = %v, want both cleanup errors", err)
	}
	if !reflect.DeepEqual(calls, []int{2, 1}) {
		t.Fatalf("cleanup calls = %v, want [2 1]", calls)
	}
}
