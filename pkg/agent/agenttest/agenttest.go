// Package agenttest provides a test harness for agent loops built on the y
// SDK. It combines a scripted fake provider (no network), an in-memory tool
// registry, and assertion helpers so consumers can write deterministic tests
// of multi-turn agent behavior without an LLM.
//
// # Basic usage
//
//	provider := agenttest.NewScriptProvider(t,
//		agenttest.TextTurn("Let me read that file."),
//		agenttest.ToolTurn("read_file", `{"path":"main.go"}`),
//		agenttest.TextTurn("The file defines main."),
//	)
//	registry := tools.NewRegistry()
//	agenttest.MustRegister(t, registry, tools.ToolDescriptor{Name: "read_file"}, handler)
//	a := agenttest.NewTestAgent(t, provider, registry)
//	result, err := a.Run(ctx, "what does main.go do?")
//	agenttest.AssertToolCalls(t, result, "read_file")
//	agenttest.AssertState(t, result, agent.StateCompleted)
//
// The script is exhaustive: the provider fails the test if a run consumes
// more turns than scripted (see NewScriptProvider).
package agenttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/providers/providertest"
	"github.com/diasYuri/y/pkg/tools"
)

// ScriptProvider is a deterministic provider whose responses are queued in
// order, one per provider request (turn). It never performs network I/O.
type ScriptProvider struct {
	*providertest.FakeProvider
	t *testing.T

	mu       sync.Mutex
	requests []providers.StreamRequest
}

// NewScriptProvider creates a provider that replays the given turns in
// order. At test cleanup, the test fails if any scripted turn was left
// unconsumed — an unconsumed script means the agent loop took a different
// path than the test author expected.
func NewScriptProvider(t *testing.T, turns ...ScriptedTurn) *ScriptProvider {
	t.Helper()
	responses := make([]providertest.FakeResponse, 0, len(turns))
	for _, turn := range turns {
		responses = append(responses, providertest.FakeResponse{Events: turn.events()})
	}
	provider := &ScriptProvider{
		FakeProvider: providertest.NewFakeProvider(providertest.WithFakeResponses(responses...)),
		t:            t,
	}
	t.Cleanup(func() {
		if pending := provider.PendingResponseCount(); pending > 0 {
			t.Errorf("agenttest: %d scripted turn(s) were not consumed; the agent loop diverged from the script", pending)
		}
	})
	return provider
}

// Requests returns the provider requests captured so far, in call order.
func (p *ScriptProvider) Requests() []providers.StreamRequest {
	// FakeProvider does not retain requests; ScriptProvider wraps Stream to
	// capture them. See Stream.
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.StreamRequest(nil), p.requests...)
}

// Stream captures the request and delegates to the scripted fake.
func (p *ScriptProvider) Stream(ctx context.Context, req providers.StreamRequest) (providers.EventStream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, cloneStreamRequest(req))
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

// AppendResponses queues additional turns mid-test (e.g. to steer a run
// that is already in progress).
func (p *ScriptProvider) AppendTurns(turns ...ScriptedTurn) {
	responses := make([]providertest.FakeResponse, 0, len(turns))
	for _, turn := range turns {
		responses = append(responses, providertest.FakeResponse{Events: turn.events()})
	}
	p.AppendResponses(responses...)
}

// ScriptedTurn is one queued provider response: assistant text plus
// optional tool calls, terminated with a stop event.
type ScriptedTurn struct {
	Text      string
	ToolCalls []ScriptedToolCall
	Usage     ai.Usage
	Err       error
}

// ScriptedToolCall is a tool call emitted by a scripted turn.
type ScriptedToolCall struct {
	Name      string
	Arguments string
}

// TextTurn scripts an assistant message with only text.
func TextTurn(text string) ScriptedTurn {
	return ScriptedTurn{Text: text}
}

// ToolTurn scripts an assistant message that requests one tool call.
func ToolTurn(name, arguments string) ScriptedTurn {
	return ScriptedTurn{ToolCalls: []ScriptedToolCall{{Name: name, Arguments: arguments}}}
}

// TextAndToolTurn scripts an assistant message with text and a tool call.
func TextAndToolTurn(text, name, arguments string) ScriptedTurn {
	return ScriptedTurn{Text: text, ToolCalls: []ScriptedToolCall{{Name: name, Arguments: arguments}}}
}

func (turn ScriptedTurn) events() []ai.Event {
	if turn.Err != nil {
		return []ai.Event{
			ai.NewErrorEvent("agenttest_script", turn.Err),
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	}
	var events []ai.Event
	if turn.Text != "" {
		events = append(events, ai.TextDelta{Text: turn.Text})
	}
	for i, call := range turn.ToolCalls {
		arguments := json.RawMessage(call.Arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		events = append(events, ai.ToolCallEvent{
			ContentIndex: i,
			ToolCall: ai.ToolCall{
				ID:        fmt.Sprintf("agenttest-call-%d", i),
				Name:      call.Name,
				Arguments: arguments,
			},
			Complete: true,
		})
	}
	if turn.Usage != (ai.Usage{}) {
		events = append(events, ai.UsageEvent{Usage: turn.Usage})
	}
	events = append(events, ai.StopEvent{Reason: ai.StopReasonStop})
	return events
}

// NewTestAgent creates an agent wired to the given provider and registry
// with a deterministic model. Extra opts are applied after the defaults.
func NewTestAgent(t *testing.T, provider agent.Provider, registry agent.ToolRegistry, opts ...agent.Option) *agent.Agent {
	t.Helper()
	models, err := provider.Models(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("agenttest: provider Models() failed: %v", err)
	}
	base := []agent.Option{
		agent.WithModel(models[0]),
		agent.WithSystemPrompt("agenttest"),
		agent.WithMaxTurns(16),
		agent.WithLogger(agent.DiscardLogger),
	}
	return agent.New(provider, registry, append(base, opts...)...)
}

// MustRegister adds a tool to the registry and fails the test on error.
func MustRegister(t *testing.T, registry *tools.Registry, desc tools.ToolDescriptor, handler tools.ToolHandler) {
	t.Helper()
	if err := registry.Add(desc, handler); err != nil {
		t.Fatalf("agenttest: register tool %q: %v", desc.Name, err)
	}
}

// FakeTool returns a permissive handler that returns a fixed text result.
func FakeTool(result string) tools.ToolHandler {
	return tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: result}}}, nil
	})
}

// RecordingTool returns a handler that appends each received request to the
// returned slice (safe for concurrent use) and replies with result.
func RecordingTool(result string) (tools.ToolHandler, func() []tools.ToolRequest) {
	var mu sync.Mutex
	var requests []tools.ToolRequest
	handler := tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: result}}}, nil
	})
	return handler, func() []tools.ToolRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]tools.ToolRequest(nil), requests...)
	}
}

// AssertToolCalls fails the test unless the run's transcript contains tool
// results for exactly the given tool names, in order.
func AssertToolCalls(t *testing.T, result agent.RunResult, names ...string) {
	t.Helper()
	var got []string
	for _, message := range result.Messages {
		if message.ToolResult != nil && message.ToolResult.ToolName != "" {
			got = append(got, message.ToolResult.ToolName)
		}
	}
	if len(got) != len(names) {
		t.Fatalf("agenttest: tool calls = %v, want %v", got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("agenttest: tool calls = %v, want %v", got, names)
		}
	}
}

// AssertState fails the test unless the run finished in the given state.
func AssertState(t *testing.T, result agent.RunResult, state agent.State) {
	t.Helper()
	if result.State != state {
		t.Fatalf("agenttest: run state = %s, want %s", result.State, state)
	}
}

// AssertTranscriptRoles fails the test unless the transcript roles match
// exactly, in order. Tool result messages report role "tool".
func AssertTranscriptRoles(t *testing.T, a *agent.Agent, roles ...string) {
	t.Helper()
	var got []string
	for _, message := range a.Transcript() {
		role := string(message.Role)
		if message.ToolResult != nil {
			role = "tool"
		}
		got = append(got, role)
	}
	if len(got) != len(roles) {
		t.Fatalf("agenttest: transcript roles = %v, want %v", got, roles)
	}
	for i := range roles {
		if got[i] != roles[i] {
			t.Fatalf("agenttest: transcript roles = %v, want %v", got, roles)
		}
	}
}

// EventCollector returns an agent.EventSink that appends every event to the
// returned slice (safe for concurrent use).
func EventCollector() (agent.EventSink, func() []agent.Event) {
	var mu sync.Mutex
	var events []agent.Event
	sink := func(event agent.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	return sink, func() []agent.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]agent.Event(nil), events...)
	}
}

// EventKinds projects a collected event slice to its kinds, in order.
func EventKinds(events []agent.Event) []agent.EventKind {
	kinds := make([]agent.EventKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func cloneStreamRequest(req providers.StreamRequest) providers.StreamRequest {
	clone := req
	clone.Context.Messages = append([]ai.Message(nil), req.Context.Messages...)
	clone.Context.Tools = append([]ai.Tool(nil), req.Context.Tools...)
	return clone
}
