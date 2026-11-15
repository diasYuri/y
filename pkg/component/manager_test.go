package component

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type testManagerService interface{ Name() string }

type testManagerServiceValue struct{ name string }

func (s testManagerServiceValue) Name() string { return s.name }

var testManagerServiceKey = NewKey[testManagerService]("example.store", "1.0.0")

func TestManagerLeavesMissingRequirementsInactiveAndActivatesLater(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	var mu sync.Mutex
	var starts []string
	consumer := managerConsumer("consumer", "provider", &starts, &mu, nil)

	if err := m.Reconcile(context.Background(), []Definition{consumer}); err != nil {
		t.Fatalf("reconcile without provider: %v", err)
	}
	status, ok := m.Status("consumer")
	if !ok || status.State != ComponentInactive || len(status.Missing) != 1 {
		t.Fatalf("inactive status = %#v, %v", status, ok)
	}
	if len(starts) != 0 {
		t.Fatalf("started without provider: %v", starts)
	}

	provider := managerProvider("provider", "1.4.2", "first", false, &starts, &mu)
	if err := m.Reconcile(context.Background(), []Definition{consumer, provider}); err != nil {
		t.Fatalf("reconcile with provider: %v", err)
	}
	if !reflect.DeepEqual(starts, []string{"provider:first", "consumer:first"}) {
		t.Fatalf("start order = %v", starts)
	}
	status, ok = m.Status("consumer")
	if !ok || status.State != ComponentActive {
		t.Fatalf("active status = %#v, %v", status, ok)
	}
	service, err := ResolveService[testManagerService](m, "provider", testManagerServiceKey, testServiceRange())
	if err != nil || service.Name() != "first" {
		t.Fatalf("resolved service = %v, %v", service, err)
	}
}

func TestManagerRequiresExplicitProviderWhenSeveralAreAvailable(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	var events []string
	var mu sync.Mutex
	consumer := managerConsumer("consumer", "provider-b", &events, &mu, nil)
	providerA := managerProvider("provider-a", "1.2.0", "a", false, &events, &mu)
	providerB := managerProvider("provider-b", "1.8.0", "b", false, &events, &mu)
	if err := m.Reconcile(context.Background(), []Definition{consumer, providerA, providerB}); err != nil {
		t.Fatalf("reconcile multiple providers: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"provider:a", "provider:b", "consumer:b"}) {
		t.Fatalf("activation order/value = %v", events)
	}
	for providerID, expected := range map[string]string{"provider-a": "a", "provider-b": "b"} {
		service, err := ResolveService[testManagerService](m, providerID, testManagerServiceKey, testServiceRange())
		if err != nil || service.Name() != expected {
			t.Fatalf("resolve from %q = %v, %v", providerID, service, err)
		}
	}
}

func TestManagerFailedReplacementPreservesCommittedGeneration(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	var mu sync.Mutex
	var events []string
	oldProvider := managerProvider("provider", "1.4.0", "old", false, &events, &mu)
	consumer := managerConsumer("consumer", "provider", &events, &mu, nil)
	if err := m.Reconcile(context.Background(), []Definition{consumer, oldProvider}); err != nil {
		t.Fatal(err)
	}
	oldConsumerStatus, _ := m.Status("consumer")

	failedProvider := managerProvider("provider", "1.5.0", "new", true, &events, &mu)
	failedProvider.Descriptor.Revision = "revision-2"
	err := m.Reconcile(context.Background(), []Definition{consumer, failedProvider})
	var reconcileErr *ReconcileError
	if !errors.As(err, &reconcileErr) || reconcileErr.Committed {
		t.Fatalf("replacement error = %#v, want uncommitted ReconcileError", err)
	}
	service, resolveErr := ResolveService[testManagerService](m, "provider", testManagerServiceKey, testServiceRange())
	if resolveErr != nil || service.Name() != "old" {
		t.Fatalf("committed provider after failure = %v, %v", service, resolveErr)
	}
	newConsumerStatus, _ := m.Status("consumer")
	if newConsumerStatus.Generation != oldConsumerStatus.Generation || newConsumerStatus.State != ComponentActive {
		t.Fatalf("consumer changed after failed replacement: before=%#v after=%#v", oldConsumerStatus, newConsumerStatus)
	}
	if !containsString(events, "rollback:new") {
		t.Fatalf("failed provider cleanup missing: %v", events)
	}
}

func TestManagerReplacesProviderAfterStagingDependentsAndClosesOldDependentsFirst(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	var mu sync.Mutex
	var events []string
	oldProvider := managerProvider("provider", "1.4.0", "old", false, &events, &mu)
	consumer := managerConsumer("consumer", "provider", &events, &mu, &events)
	if err := m.Reconcile(context.Background(), []Definition{consumer, oldProvider}); err != nil {
		t.Fatal(err)
	}
	events = nil

	newProvider := managerProvider("provider", "1.5.0", "new", false, &events, &mu)
	newProvider.Descriptor.Revision = "revision-2"
	if err := m.Reconcile(context.Background(), []Definition{consumer, newProvider}); err != nil {
		t.Fatalf("replace provider: %v", err)
	}
	want := []string{
		"provider:new", "consumer:new",
		"consumer-close:old", "provider-close:old",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("replacement events = %v, want %v", events, want)
	}
}

func TestManagerRejectsCyclesAndIncompatibleVersionsBeforeStarting(t *testing.T) {
	cases := []struct {
		name string
		defs []Definition
		want error
	}{
		{
			name: "incompatible version",
			defs: []Definition{
				managerProvider("provider", "2.0.0", "value", false, nil, nil),
				managerConsumer("consumer", "provider", nil, nil, nil),
			},
			want: ErrVersionMismatch,
		},
		{
			name: "cycle",
			defs: []Definition{
				cycleDefinition("left", "right", "service.left", "service.right"),
				cycleDefinition("right", "left", "service.right", "service.left"),
			},
			want: ErrDependencyCycle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			defer m.Close(context.Background())
			if err := m.Reconcile(context.Background(), tc.defs); !errors.Is(err, tc.want) {
				t.Fatalf("reconcile error = %v, want %v", err, tc.want)
			}
			for _, status := range m.Statuses() {
				if status.State == ComponentActive || status.State == ComponentStarting {
					t.Fatalf("component started before validation failed: %#v", status)
				}
			}
		})
	}
}

func TestVersionConstraintUsesSemVerPrecedenceAndBounds(t *testing.T) {
	constraint := VersionConstraint{MinInclusive: "1.2.0", MaxExclusive: "2.0.0"}
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"1.2.0", true},
		{"1.9.9", true},
		{"2.0.0", false},
		{"1.2.0-rc.1", false},
		{"1.2.0+build.4", true},
		{"v1.5.0", false},
		{"01.2.0", false},
	} {
		if got := constraint.Matches(tc.version); got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
	if err := (VersionConstraint{MinInclusive: "2.0.0", MaxExclusive: "2.0.0"}).Validate(); err == nil {
		t.Fatal("expected an empty range to be rejected")
	}
}

func TestManagerCloseContinuesAfterCallerDeadline(t *testing.T) {
	m := NewManager()
	started := make(chan struct{})
	release := make(chan struct{})
	definition := Define(Descriptor{ID: "worker", Version: "1"}, func() (Component, error) {
		return ComponentFunc(func(_ context.Context, scope *Scope) error {
			return scope.Track(func(context.Context) error {
				close(started)
				<-release
				return nil
			})
		}), nil
	})
	if err := m.Reconcile(context.Background(), []Definition{definition}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", err)
	}
	<-started
	close(release)
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func managerProvider(id, serviceVersion, name string, fail bool, events *[]string, mu *sync.Mutex) Definition {
	descriptor := Descriptor{
		ID:       id,
		Version:  "1",
		Provides: []ServiceRef{{ID: testManagerServiceKey.ref.ID, Version: serviceVersion}},
	}
	return Define(descriptor, func() (Component, error) {
		return ComponentFunc(func(_ context.Context, scope *Scope) error {
			if events != nil {
				if err := scope.Track(func(context.Context) error {
					mu.Lock()
					defer mu.Unlock()
					if fail {
						*events = append(*events, "rollback:"+name)
					} else {
						*events = append(*events, "provider-close:"+name)
					}
					return nil
				}); err != nil {
					return err
				}
			}
			if fail {
				return errors.New("provider start failed")
			}
			if err := Provide[testManagerService](scope, NewKey[testManagerService](testManagerServiceKey.ref.ID, serviceVersion), testManagerServiceValue{name: name}); err != nil {
				return err
			}
			if events != nil {
				mu.Lock()
				*events = append(*events, "provider:"+name)
				mu.Unlock()
			}
			return nil
		}), nil
	})
}

func managerConsumer(id, providerID string, events *[]string, mu *sync.Mutex, cleanupEvents *[]string) Definition {
	descriptor := Descriptor{
		ID:      id,
		Version: "1",
		Requirements: []ServiceRequirement{{
			ServiceID:   testManagerServiceKey.ref.ID,
			ProviderID:  providerID,
			Version:     testServiceRange(),
			Cardinality: CardinalityOne,
		}},
	}
	return Define(descriptor, func() (Component, error) {
		return ComponentFunc(func(_ context.Context, scope *Scope) error {
			service, err := Require(scope, testManagerServiceKey)
			if err != nil {
				return err
			}
			if events != nil {
				mu.Lock()
				*events = append(*events, "consumer:"+service.Name())
				mu.Unlock()
			}
			if cleanupEvents != nil {
				return scope.Track(func(context.Context) error {
					serviceDuringClose, err := Require(scope, testManagerServiceKey)
					if err != nil {
						return err
					}
					mu.Lock()
					*cleanupEvents = append(*cleanupEvents, "consumer-close:"+serviceDuringClose.Name())
					mu.Unlock()
					return nil
				})
			}
			return nil
		}), nil
	})
}

func testServiceRange() VersionConstraint {
	return VersionConstraint{MinInclusive: "1.0.0", MaxExclusive: "2.0.0"}
}

func cycleDefinition(id, providerID, providedID, requiredID string) Definition {
	return Define(Descriptor{
		ID:       id,
		Version:  "1",
		Provides: []ServiceRef{{ID: providedID, Version: "1.0.0"}},
		Requirements: []ServiceRequirement{{
			ServiceID:   requiredID,
			ProviderID:  providerID,
			Version:     VersionConstraint{Exact: "1.0.0"},
			Cardinality: CardinalityOne,
		}},
	}, func() (Component, error) {
		return ComponentFunc(func(context.Context, *Scope) error { return nil }), nil
	})
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
