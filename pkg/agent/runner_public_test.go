package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/agent/agenttest"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/providers/providertest"
	"github.com/diasYuri/y/pkg/tools"
)

type capturedSource struct{ registry *tools.Registry }

func (s capturedSource) AcquireRuntimeView(context.Context) (*agent.RuntimeView, error) {
	snap, err := s.registry.Snapshot(nil)
	if err != nil {
		return nil, err
	}
	return agent.NewRuntimeView(agent.RuntimeViewInfo{ID: "fixture-generation-1"}, snap, nil, nil, snap.Close)
}

type heldProvider struct {
	*providertest.FakeProvider
	entered chan struct{}
}

func (p *heldProvider) Stream(ctx context.Context, _ providers.StreamRequest) (providers.EventStream, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunnerActiveControlsAndClose(t *testing.T) {
	provider := &heldProvider{FakeProvider: providertest.NewFakeProvider(), entered: make(chan struct{})}
	runner := agent.NewAgentRunner(provider, tools.NewRegistry(), agent.WithRunnerStateStore(agent.NewInMemoryStateStore()))
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), agent.RunRequest{RunID: "active", Prompt: "start"})
		done <- err
	}()
	<-provider.entered
	message := ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "follow up"}}}
	if err := runner.Steer("active", message); err != nil {
		t.Fatal(err)
	}
	if err := runner.FollowUp("active", message); err != nil {
		t.Fatal(err)
	}
	if err := runner.CancelRun("active"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancel did not reach provider", err)
	}
	if err := runner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), agent.RunRequest{RunID: "new"}); !errors.Is(err, agent.ErrRunnerClosed) {
		t.Fatal("closed runner accepted execution", err)
	}
}

type operationSpy struct {
	base              *agent.InMemoryEventStore
	begins, completes atomic.Int32
	uncertain         bool
}

func (s *operationSpy) BeginOperation(ctx context.Context, op agent.OperationRecord) (agent.OperationRecord, bool, error) {
	s.begins.Add(1)
	if s.uncertain {
		op.State = agent.OperationPending
		return op, false, nil
	}
	return s.base.BeginOperation(ctx, op)
}
func (s *operationSpy) CompleteOperation(ctx context.Context, key string, response tools.ToolResponse, err error) error {
	s.completes.Add(1)
	return s.base.CompleteOperation(ctx, key, response, err)
}

func TestRunnerRuntimeViewCannotBypassOperationStore(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "in_doubt"}[uncertain], func(t *testing.T) {
			ctx := context.Background()
			registry := tools.NewRegistry(tools.WithPolicy(policy.Func(func(context.Context, policy.Request) (policy.Decision, error) {
				return policy.Decision{Kind: policy.DecisionAllow}, nil
			})))
			defer func() { _ = registry.Close(ctx) }()
			var effects atomic.Int32
			if err := registry.Add(tools.ToolDescriptor{Name: "mutate", InputSchema: []byte(`{"type":"object"}`)}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
				effects.Add(1)
				return tools.ToolResponse{}, nil
			})); err != nil {
				t.Fatal(err)
			}
			turns := []agenttest.ScriptedTurn{agenttest.ToolTurn("mutate", `{}`)}
			if !uncertain {
				turns = append(turns, agenttest.TextTurn("done"))
			}
			provider := agenttest.NewScriptProvider(t, turns...)
			events := agent.NewInMemoryEventStore()
			operations := &operationSpy{base: events, uncertain: uncertain}
			runner := agent.NewAgentRunner(provider, registry, agent.WithRunnerStateStore(agent.NewInMemoryStateStore()), agent.WithRunnerEventStore(events), agent.WithRunnerOperationStore(operations), agent.WithRunnerAgentOptions(agent.WithRuntimeViewSource(capturedSource{registry})))
			var seen atomic.Int32
			unsubscribe := runner.Subscribe(func(e agent.EventEnvelope) {
				if e.RunID != "run" {
					t.Error("wrong correlation")
				}
				seen.Add(1)
			})
			defer unsubscribe()
			_, err := runner.Run(ctx, agent.RunRequest{RunID: "run", Prompt: "do it"})
			if uncertain {
				if !errors.Is(err, agent.ErrOperationInDoubt) || effects.Load() != 0 {
					t.Fatalf("uncertain effect repeated: %v", err)
				}
			} else if err != nil || effects.Load() != 1 || operations.completes.Load() != 1 {
				t.Fatalf("durable completion bypassed: %v", err)
			}
			if operations.begins.Load() != 1 || seen.Load() == 0 {
				t.Fatal("view bypassed operation store or event stream")
			}
			if err := runner.Steer("run", ai.Message{Role: ai.RoleUser}); !errors.Is(err, agent.ErrRunNotActive) {
				t.Fatal("finished execution accepted steering")
			}
		})
	}
}
