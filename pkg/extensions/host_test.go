package extensions

import (
	"context"
	"testing"

	"github.com/yuri/y/pkg/agent"
	ycontext "github.com/yuri/y/pkg/context"
	"github.com/yuri/y/pkg/tools"
)

type testExtension struct {
	id     string
	closed *[]string
}

func (e testExtension) ID() string { return e.id }

func (e testExtension) Install(host *Host) error {
	if err := host.AddSource(ycontext.NewMemorySource(e.id)); err != nil {
		return err
	}
	host.AddHooks(agent.RuntimeHooks{})
	return nil
}

func (e testExtension) Close(context.Context) error {
	*e.closed = append(*e.closed, e.id)
	return nil
}

func TestHostComposesSourcesAndClosesInReverseOrder(t *testing.T) {
	closed := []string{}
	host := NewHost(tools.NewRegistry())
	if err := host.Install(testExtension{id: "first", closed: &closed}); err != nil {
		t.Fatal(err)
	}
	if err := host.Install(testExtension{id: "second", closed: &closed}); err != nil {
		t.Fatal(err)
	}
	if got := len(host.sources); got != 2 {
		t.Fatalf("sources = %d, want 2", got)
	}
	if got := len(host.AgentOptions()); got != 3 {
		t.Fatalf("agent options = %d, want resolver plus two hook options", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || closed[0] != "second" || closed[1] != "first" {
		t.Fatalf("close order = %v", closed)
	}
}
