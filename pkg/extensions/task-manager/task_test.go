package taskmanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	taskext "github.com/yuri/y/pkg/extensions/task-manager"
	"github.com/yuri/y/pkg/providers/providertest"
	"github.com/yuri/y/pkg/tools"
)

func TestManagerRefineAndCompleteParent(t *testing.T) {
	m := taskext.New()
	root, err := m.Create(taskext.Task{Name: "Implement feature"})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	children, err := m.Refine(root.ID, []taskext.Task{
		{Name: "Design API"},
		{Name: "Implement code"},
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if len(children) != 2 || children[0].ParentID != root.ID || children[1].Order != 2 {
		t.Fatalf("unexpected children: %+v", children)
	}

	if _, err := m.Complete(root.ID); !errors.Is(err, taskext.ErrTaskIncomplete) {
		t.Fatalf("Complete parent error = %v, want ErrTaskIncomplete", err)
	}
	if _, err := m.Complete(children[0].ID); err != nil {
		t.Fatalf("Complete first child: %v", err)
	}
	if m.AllComplete() {
		t.Fatal("plan completed before the last child")
	}
	if _, err := m.Complete(children[1].ID); err != nil {
		t.Fatalf("Complete second child: %v", err)
	}
	if !m.AllComplete() {
		t.Fatal("plan should be complete after all children")
	}
	completedRoot, ok := m.Get(root.ID)
	if !ok || completedRoot.Status != taskext.StatusCompleted {
		t.Fatalf("root = %+v, want completed", completedRoot)
	}
}

func TestCreateManyIsAtomic(t *testing.T) {
	m := taskext.New()
	_, err := m.CreateMany([]taskext.Task{
		{Name: "valid"},
		{Name: ""},
	})
	if err == nil {
		t.Fatal("CreateMany should reject an invalid task")
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("task count after failed batch = %d, want 0", got)
	}
}

func TestRegisterToolsManagesTasks(t *testing.T) {
	m := taskext.New()
	registry := tools.NewRegistry()
	if err := taskext.Register(registry, m); err != nil {
		t.Fatalf("Register: %v", err)
	}

	response, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "task_create",
		Arguments: json.RawMessage(`{"tasks":[{"name":"Write tests"}]}`),
	})
	if err != nil {
		t.Fatalf("task_create: %v", err)
	}
	if !strings.Contains(response.Content[0].Text, "Write tests") {
		t.Fatalf("create response = %q", response.Content[0].Text)
	}

	if _, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "task_complete",
		Arguments: json.RawMessage(`{"id":"task-1"}`),
	}); err != nil {
		t.Fatalf("task_complete: %v", err)
	}
	if !m.AllComplete() {
		t.Fatal("task tool should complete the plan")
	}
}

func TestAgentOptionRequiresTaskPlanBeforeCompletion(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.TextDelta{Text: "I am done"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		}},
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{
				ID:        "create",
				Name:      "task_create",
				Arguments: json.RawMessage(`{"tasks":[{"name":"Do the work"}]}`),
			}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ToolCall: ai.ToolCall{
				ID:        "complete",
				Name:      "task_complete",
				Arguments: json.RawMessage(`{"id":"task-1"}`),
			}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{
			ai.TextDelta{Text: "done"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		}},
	))

	m := taskext.New()
	registry := tools.NewRegistry()
	if err := taskext.Register(registry, m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a := agent.New(provider, registry, m.AgentOption())
	result, err := a.Run(context.Background(), "Implement the feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.State != agent.StateCompleted {
		t.Fatalf("state = %q, want completed", result.State)
	}
	if result.Turns != 4 {
		t.Fatalf("turns = %d, want 4", result.Turns)
	}
	if !m.AllComplete() {
		t.Fatal("task manager should be complete")
	}
	if len(result.Messages) < 2 || result.Messages[1].Role != ai.RoleAssistant {
		t.Fatalf("expected the first assistant response to be preserved: %+v", result.Messages)
	}
	foundReminder := false
	for _, message := range result.Messages {
		if message.Role == ai.RoleUser && len(message.Content) > 0 && strings.Contains(message.Content[0].Text, "Task Manager") {
			foundReminder = true
			break
		}
	}
	if !foundReminder {
		t.Fatal("completion gate did not steer the agent with a task reminder")
	}
}
