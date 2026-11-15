package agent

import (
	"context"
	"testing"
)

func TestAcquireRuntimeViewWithoutContextResolver(t *testing.T) {
	instance := New(nil, nil)
	view, err := instance.acquireRuntimeView(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeView returned error: %v", err)
	}
	if resolver := view.ContextResolver(); resolver != nil {
		t.Fatal("runtime view should not contain a context resolver when none was configured")
	}
	if err := view.Close(); err != nil {
		t.Fatalf("close runtime view: %v", err)
	}
}
