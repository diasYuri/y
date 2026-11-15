package extensions

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/component"
	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/tools"
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
	if got := len(host.resolver.Sources()); got != 2 {
		t.Fatalf("sources = %d, want 2", got)
	}
	if got := len(host.AgentOptions()); got != 1 {
		t.Fatalf("agent options = %d, want runtime view source", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || closed[0] != "second" || closed[1] != "first" {
		t.Fatalf("close order = %v", closed)
	}
}

type scopedTestExtension struct {
	id      string
	closed  *[]string
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (e *scopedTestExtension) ID() string          { return e.id }
func (e *scopedTestExtension) Install(*Host) error { return nil }
func (e *scopedTestExtension) Close(context.Context) error {
	if e.closed != nil {
		*e.closed = append(*e.closed, e.id)
	}
	return nil
}
func (e *scopedTestExtension) InstallScoped(_ context.Context, host *Host, scope *component.Scope) error {
	if _, err := host.RegisterSource(scope, ycontext.NewMemorySource(e.id)); err != nil {
		return err
	}
	if _, err := host.RegisterHooks(scope, agent.RuntimeHooks{BeforeTurn: func(context.Context, agent.TurnContext) error { return nil }}); err != nil {
		return err
	}
	handler := tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		if e.entered != nil {
			close(e.entered)
			<-e.release
		}
		return tools.ToolResponse{}, nil
	})
	if _, err := host.RegisterTool(scope, tools.ToolDescriptor{Name: e.id + ".tool"}, handler); err != nil {
		return err
	}
	if e.fail {
		return errors.New("install failed after registration")
	}
	return nil
}

func TestHostRollsBackScopedInstallFailure(t *testing.T) {
	closed := []string{}
	host := NewHost(tools.NewRegistry())
	ext := &scopedTestExtension{id: "broken", closed: &closed, fail: true}
	if err := host.Install(ext); err == nil {
		t.Fatal("expected install failure")
	}
	if got := host.Registry.List(); len(got) != 0 {
		t.Fatalf("tools after failed install = %v", got)
	}
	if got := host.resolver.Sources(); len(got) != 0 {
		t.Fatalf("sources after failed install = %d", len(got))
	}
	if len(closed) != 1 || closed[0] != "broken" {
		t.Fatalf("closed extensions = %v", closed)
	}
}

func TestHostUninstallRemovesOnlyOneOwner(t *testing.T) {
	closed := []string{}
	host := NewHost(tools.NewRegistry())
	for _, id := range []string{"first", "second"} {
		if err := host.Install(&scopedTestExtension{id: id, closed: &closed}); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.Uninstall(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	listed := host.Registry.List()
	if len(listed) != 1 || listed[0].Name != "second.tool" {
		t.Fatalf("tools after uninstall = %#v", listed)
	}
	if got := host.resolver.Sources(); len(got) != 1 || got[0].(ycontext.NamedSource).Name() != "second" {
		t.Fatalf("sources after uninstall = %#v", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || closed[0] != "first" || closed[1] != "second" {
		t.Fatalf("closed extensions = %v", closed)
	}
}

func TestHostUninstallDrainsInflightToolCall(t *testing.T) {
	host := NewHost(tools.NewRegistry())
	ext := &scopedTestExtension{id: "drain", entered: make(chan struct{}), release: make(chan struct{})}
	if err := host.Install(ext); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := host.Registry.Handle(context.Background(), tools.ToolRequest{Name: "drain.tool"})
		callDone <- err
	}()
	<-ext.entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := host.Uninstall(ctx, "drain"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uninstall error = %v, want deadline exceeded", err)
	}
	if got := host.Registry.List(); len(got) != 0 {
		t.Fatalf("tool remained visible while draining: %#v", got)
	}
	if _, err := host.Registry.Handle(context.Background(), tools.ToolRequest{Name: "drain.tool"}); !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("new call error = %v, want tool not found", err)
	}
	close(ext.release)
	if err := <-callDone; err != nil {
		t.Fatalf("in-flight call error = %v", err)
	}
	if err := host.Uninstall(context.Background(), "drain"); err != nil {
		t.Fatalf("second uninstall = %v", err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type reactiveHostService interface{ Name() string }

type reactiveHostServiceValue struct{ name string }

func (s reactiveHostServiceValue) Name() string { return s.name }

var reactiveHostServiceKey = component.NewKey[reactiveHostService]("example.host.service", "1.0.0")

type reactiveTestExtension struct {
	id            string
	version       string
	revision      string
	service       string
	activeService string
	providerID    string
	provides      bool
	requires      bool
	starts        *[]string
	closes        *[]string
	fail          bool
}

func (e *reactiveTestExtension) ID() string        { return e.id }
func (*reactiveTestExtension) Install(*Host) error { return nil }
func (e *reactiveTestExtension) NewExtension() (Extension, error) {
	copy := *e
	return &copy, nil
}
func (e *reactiveTestExtension) Close(context.Context) error {
	if e.closes != nil {
		service := e.activeService
		if service == "" {
			service = e.service
		}
		*e.closes = append(*e.closes, "close:"+e.id+":"+service)
	}
	return nil
}
func (e *reactiveTestExtension) ComponentDescriptor() component.Descriptor {
	descriptor := component.Descriptor{
		ID:       "extensions/" + e.id,
		Version:  e.version,
		Revision: e.revision,
	}
	if e.provides {
		descriptor.Provides = []component.ServiceRef{{ID: reactiveHostServiceKeyRef().ID, Version: e.version}}
	}
	if e.requires {
		descriptor.Requirements = []component.ServiceRequirement{{
			ServiceID:   reactiveHostServiceKeyRef().ID,
			ProviderID:  "extensions/" + e.providerID,
			Version:     component.VersionConstraint{MinInclusive: "1.0.0", MaxExclusive: "2.0.0"},
			Cardinality: component.CardinalityOne,
		}}
	}
	return descriptor
}
func (e *reactiveTestExtension) InstallScoped(_ context.Context, _ *Host, scope *component.Scope) error {
	if e.fail {
		return errors.New("reactive extension start failed")
	}
	name := e.service
	if e.provides {
		key := component.NewKey[reactiveHostService](reactiveHostServiceKeyRef().ID, e.version)
		if err := component.Provide[reactiveHostService](scope, key, reactiveHostServiceValue{name: name}); err != nil {
			return err
		}
	}
	if e.requires {
		service, err := component.Require(scope, reactiveHostServiceKey)
		if err != nil {
			return err
		}
		name = service.Name()
	}
	e.activeService = name
	if e.starts != nil {
		*e.starts = append(*e.starts, "start:"+e.id+":"+name)
	}
	return nil
}

func reactiveHostServiceKeyRef() component.ServiceRef {
	return component.Reference(reactiveHostServiceKey)
}

func reactiveHostDefinition(extension *reactiveTestExtension) ExtensionDefinition {
	return ExtensionDefinition{
		ID:         extension.id,
		Descriptor: extension.ComponentDescriptor(),
		Factory:    extension.NewExtension,
	}
}

func TestHostKeepsExtensionInactiveUntilProviderArrives(t *testing.T) {
	host := NewHost(tools.NewRegistry())
	defer host.Close(context.Background())
	var starts []string
	consumer := &reactiveTestExtension{id: "consumer", version: "1", providerID: "provider", requires: true, starts: &starts}
	if err := host.Install(consumer); err != nil {
		t.Fatalf("install consumer: %v", err)
	}
	status, ok := host.Status("consumer")
	if !ok || status.State != component.ComponentInactive {
		t.Fatalf("consumer status = %#v, %v", status, ok)
	}
	if len(starts) != 0 {
		t.Fatalf("consumer started without provider: %v", starts)
	}
	provider := &reactiveTestExtension{id: "provider", version: "1.4.0", service: "stored", provides: true, starts: &starts}
	if err := host.Install(provider); err != nil {
		t.Fatalf("install provider: %v", err)
	}
	if !reflect.DeepEqual(starts, []string{"start:provider:stored", "start:consumer:stored"}) {
		t.Fatalf("activation order = %v", starts)
	}
}

func TestHostReconcileRestartsDependentAndUninstallsProviderLast(t *testing.T) {
	host := NewHost(tools.NewRegistry())
	defer host.Close(context.Background())
	var starts []string
	var closes []string
	provider := &reactiveTestExtension{id: "provider", version: "1.4.0", revision: "r1", service: "old", provides: true, starts: &starts, closes: &closes}
	consumer := &reactiveTestExtension{id: "consumer", version: "1", providerID: "provider", requires: true, starts: &starts, closes: &closes}
	if err := host.Reconcile(context.Background(), []ExtensionDefinition{reactiveHostDefinition(consumer), reactiveHostDefinition(provider)}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	starts, closes = nil, nil

	providerNext := &reactiveTestExtension{id: "provider", version: "1.5.0", revision: "r2", service: "new", provides: true, starts: &starts, closes: &closes}
	if err := host.Reconcile(context.Background(), []ExtensionDefinition{reactiveHostDefinition(consumer), reactiveHostDefinition(providerNext)}); err != nil {
		t.Fatalf("provider replacement: %v", err)
	}
	if !reflect.DeepEqual(starts, []string{"start:provider:new", "start:consumer:new"}) {
		t.Fatalf("replacement activation = %v", starts)
	}
	if !reflect.DeepEqual(closes, []string{"close:consumer:old", "close:provider:old"}) {
		t.Fatalf("old-generation teardown = %v", closes)
	}

	if err := host.Uninstall(context.Background(), "provider"); err != nil {
		t.Fatalf("provider uninstall: %v", err)
	}
	status, ok := host.Status("consumer")
	if !ok || status.State != component.ComponentInactive {
		t.Fatalf("consumer status after provider removal = %#v, %v", status, ok)
	}
}
