package taskmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

const (
	toolCreate   = "task_create"
	toolList     = "task_list"
	toolStart    = "task_start"
	toolComplete = "task_complete"
	toolRefine   = "task_refine"
)

// Register adds the task-management tools to a native y tool registry.
func Register(registry *tools.Registry, manager *Manager) error {
	if manager == nil {
		return ErrManagerNil
	}
	return manager.Register(registry)
}

// Register adds the task-management tools to a native y tool registry.
func (m *Manager) Register(registry *tools.Registry) error {
	if m == nil {
		return ErrManagerNil
	}
	if registry == nil {
		return fmt.Errorf("task: tool registry is nil")
	}
	for _, spec := range []struct {
		descriptor tools.ToolDescriptor
		handler    tools.ToolHandler
	}{
		{createDescriptor(), tools.ToolHandlerFunc(m.handleCreate)},
		{listDescriptor(), tools.ToolHandlerFunc(m.handleList)},
		{startDescriptor(), tools.ToolHandlerFunc(m.handleStart)},
		{completeDescriptor(), tools.ToolHandlerFunc(m.handleComplete)},
		{refineDescriptor(), tools.ToolHandlerFunc(m.handleRefine)},
	} {
		if err := registry.Add(spec.descriptor, spec.handler); err != nil {
			return err
		}
	}
	return nil
}

// AgentOption installs the task prompt, completion gate, and session reset
// behavior on an agent. Register the manager's tools separately with Register.
func (m *Manager) AgentOption() agent.Option {
	return func(a *agent.Agent) {
		if m == nil || a == nil {
			return
		}
		agent.WithRuntimeHooks(agent.RuntimeHooks{
			OnSession: func(_ context.Context, event agent.SessionEvent) error {
				m.observeSession(event.State)
				return nil
			},
			BeforeRequest: func(_ context.Context, request *providers.StreamRequest) (*agent.HookedResponse, error) {
				if request != nil {
					request.Context.SystemPrompt = appendPrompt(request.Context.SystemPrompt, m.prompt())
				}
				return nil, nil
			},
			BeforeComplete: func(_ context.Context, _ agent.RunResult) (agent.CompletionDecision, error) {
				if m.AllComplete() {
					return agent.CompletionAllow, nil
				}
				a.Steer(ai.Message{
					Role: ai.RoleUser,
					Content: []ai.ContentBlock{{
						Type: ai.ContentText,
						Text: m.completionReminder(),
					}},
				})
				return agent.CompletionContinue, nil
			},
		})(a)
	}
}

func (m *Manager) observeSession(state agent.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state == agent.StateIdle && (m.lastState == string(agent.StateCompleted) || m.lastState == string(agent.StateCanceled)) {
		m.tasks = make(map[string]Task)
		m.nextID = 0
	}
	m.lastState = string(state)
}

func (m *Manager) prompt() string {
	if m == nil {
		return ""
	}
	summary := m.Summary()
	var builder strings.Builder
	builder.WriteString("Task management is mandatory for this activity. ")
	builder.WriteString("Before implementing, create a task plan with task_create. ")
	builder.WriteString("If a task becomes larger or changes scope, use task_refine to split it into ordered child tasks. ")
	builder.WriteString("Use task_start while working and task_complete only after the work is actually finished. ")
	builder.WriteString("Do not give the final answer while any task remains open.\n")
	builder.WriteString(fmt.Sprintf("Current task summary: %d total, %d completed, %d open.\n", summary.Total, summary.Completed, summary.Open))
	if tasks := m.List(); len(tasks) > 0 {
		builder.WriteString("Current tasks:\n")
		for _, task := range tasks {
			builder.WriteString(fmt.Sprintf("- [%s] %s: %s", task.Status, task.ID, task.Name))
			if task.ParentID != "" {
				builder.WriteString(fmt.Sprintf(" (parent %s)", task.ParentID))
			}
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func (m *Manager) completionReminder() string {
	summary := m.Summary()
	if summary.Total == 0 {
		return "Task Manager: a task plan is required before completion. Use task_create now, then work through the tasks."
	}
	return fmt.Sprintf("Task Manager: do not finish yet. %d task(s) remain open. Use task_list, task_start, task_refine, or task_complete as appropriate, then continue the implementation.", summary.Open)
}

func appendPrompt(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + addition
}

type taskInput struct {
	ID          string `json:"id,omitempty"`
	ParentID    string `json:"parentId,omitempty"`
	ParentIDAlt string `json:"parent_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order,omitempty"`
}

func (in taskInput) task() Task {
	parentID := in.ParentID
	if parentID == "" {
		parentID = in.ParentIDAlt
	}
	return Task{
		ID:          in.ID,
		ParentID:    parentID,
		Name:        in.Name,
		Description: in.Description,
		Order:       in.Order,
	}
}

type createInput struct {
	Tasks []taskInput `json:"tasks"`
}

type taskIDInput struct {
	ID string `json:"id"`
}

type refineInput struct {
	ParentID    string      `json:"parentId"`
	ParentIDAlt string      `json:"parent_id,omitempty"`
	Tasks       []taskInput `json:"tasks"`
}

func (in refineInput) parentID() string {
	if in.ParentID != "" {
		return in.ParentID
	}
	return in.ParentIDAlt
}

func (m *Manager) handleCreate(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input createInput
	if err := decode(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	if len(input.Tasks) == 0 {
		return tools.ToolResponse{}, invalidArguments("tasks must contain at least one task")
	}
	requested := make([]Task, 0, len(input.Tasks))
	for _, inputTask := range input.Tasks {
		requested = append(requested, inputTask.task())
	}
	created, err := m.CreateMany(requested)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return taskResponse(struct {
		Created []Task  `json:"created"`
		Summary Summary `json:"summary"`
	}{created, m.Summary()})
}

func (m *Manager) handleList(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	if len(strings.TrimSpace(string(request.Arguments))) > 0 && string(request.Arguments) != "{}" {
		return tools.ToolResponse{}, invalidArguments("task_list does not accept arguments")
	}
	return taskResponse(struct {
		Tasks   []Task  `json:"tasks"`
		Summary Summary `json:"summary"`
	}{m.List(), m.Summary()})
}

func (m *Manager) handleStart(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input taskIDInput
	if err := decode(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	task, err := m.Start(input.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return taskResponse(task)
}

func (m *Manager) handleComplete(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input taskIDInput
	if err := decode(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	task, err := m.Complete(input.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return taskResponse(struct {
		Task    Task    `json:"task"`
		Summary Summary `json:"summary"`
	}{task, m.Summary()})
}

func (m *Manager) handleRefine(_ context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input refineInput
	if err := decode(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	children := make([]Task, 0, len(input.Tasks))
	for _, child := range input.Tasks {
		children = append(children, child.task())
	}
	created, err := m.Refine(input.parentID(), children)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return taskResponse(struct {
		Created []Task  `json:"created"`
		Summary Summary `json:"summary"`
	}{created, m.Summary()})
}

func decode(raw json.RawMessage, target any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return invalidArguments("arguments are required")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalidArguments("arguments must be valid JSON")
	}
	return nil
}

func invalidArguments(message string) error {
	return &tools.Error{Code: "invalid_arguments", Message: message, Cause: tools.ErrInvalidTool}
}

func taskResponse(value any) (tools.ToolResponse, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("marshal task response: %w", err)
	}
	return tools.ToolResponse{
		Content: []tools.ContentBlock{{Type: tools.ContentText, Text: string(raw)}},
		Details: raw,
	}, nil
}

func sequentialDescriptor(name, description string, schema json.RawMessage) tools.ToolDescriptor {
	return tools.ToolDescriptor{
		Name:          name,
		Description:   description,
		InputSchema:   schema,
		ExecutionMode: tools.ExecutionSequential,
	}
}

func createDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolCreate, "Create one or more tasks for the current activity before implementation.", json.RawMessage(`{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object"}}},"required":["tasks"],"additionalProperties":false}`))
}

func listDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolList, "List the current task plan and progress.", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`))
}

func startDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolStart, "Mark a task as in progress.", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`))
}

func completeDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolComplete, "Mark a task complete after its work is finished.", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`))
}

func refineDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolRefine, "Split one task into an ordered sequence of child tasks.", json.RawMessage(`{"type":"object","properties":{"parentId":{"type":"string"},"parent_id":{"type":"string"},"tasks":{"type":"array","items":{"type":"object"}}},"required":["tasks"],"additionalProperties":false}`))
}
