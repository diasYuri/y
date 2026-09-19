package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/tools"
)

const (
	toolSpawn  = "subagent_spawn"
	toolList   = "subagent_list"
	toolStatus = "subagent_status"
	toolWait   = "subagent_wait"
	toolCancel = "subagent_cancel"
)

// Register adds subagent lifecycle tools to a native y tool registry.
func Register(registry *tools.Registry, manager *Manager) error {
	if manager == nil {
		return ErrManagerClosed
	}
	return manager.Register(registry)
}

// Register adds subagent lifecycle tools to a native y tool registry.
func (m *Manager) Register(registry *tools.Registry) error {
	if m == nil {
		return ErrManagerClosed
	}
	if registry == nil {
		return fmt.Errorf("subagent tool registry is nil")
	}
	for _, spec := range []struct {
		descriptor tools.ToolDescriptor
		handler    tools.ToolHandler
	}{
		{spawnDescriptor(), tools.ToolHandlerFunc(m.handleSpawn)},
		{listDescriptor(), tools.ToolHandlerFunc(m.handleList)},
		{statusDescriptor(), tools.ToolHandlerFunc(m.handleStatus)},
		{waitDescriptor(), tools.ToolHandlerFunc(m.handleWait)},
		{cancelDescriptor(), tools.ToolHandlerFunc(m.handleCancel)},
	} {
		if err := registry.Add(spec.descriptor, spec.handler); err != nil {
			return err
		}
	}
	return nil
}

// AgentOption installs the parent completion gate and cancellation behavior.
// It does not add tools; call Register separately on the parent's registry.
func (m *Manager) AgentOption() agent.Option {
	return func(a *agent.Agent) {
		if m == nil || a == nil {
			return
		}
		agent.WithRuntimeHooks(agent.RuntimeHooks{
			OnSession: func(_ context.Context, event agent.SessionEvent) error {
				if event.State == agent.StateCanceled || event.State == agent.StateFailed {
					_ = m.CancelParent(context.Background(), event.RunID)
				}
				return nil
			},
			BeforeComplete: func(ctx context.Context, _ agent.RunResult) (agent.CompletionDecision, error) {
				parentID := a.Snapshot().RunID
				waited, err := m.WaitParent(ctx, parentID)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						// Let the agent loop observe ctx.Err() at the next turn
						// boundary and produce StateCanceled.
						return agent.CompletionContinue, nil
					}
					return agent.CompletionAllow, err
				}
				followUp := m.consumeFollowUp(parentID)
				if waited || followUp {
					a.Steer(ai.Message{
						Role: ai.RoleUser,
						Content: []ai.ContentBlock{{
							Type: ai.ContentText,
							Text: m.completionNotice(parentID),
						}},
					})
					return agent.CompletionContinue, nil
				}
				return agent.CompletionAllow, nil
			},
		})(a)
	}
}

type spawnInput struct {
	Name         string `json:"name,omitempty"`
	Prompt       string `json:"prompt"`
	Mode         Mode   `json:"mode,omitempty"`
	TimeoutMS    int64  `json:"timeout_ms,omitempty"`
	ModelID      string `json:"model_id,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

type idInput struct {
	ID string `json:"id"`
}

type listInput struct {
	Status Status `json:"status,omitempty"`
}

type waitInput struct {
	ID        string `json:"id"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

func (m *Manager) handleSpawn(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input spawnInput
	if err := decodeArguments(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	if input.TimeoutMS < 0 {
		return tools.ToolResponse{}, invalidArguments("timeout_ms cannot be negative")
	}
	spawn := SpawnRequest{
		ParentID:               strings.TrimSpace(request.RunID),
		Name:                   input.Name,
		Prompt:                 input.Prompt,
		Mode:                   input.Mode,
		Model:                  ai.Model{ID: strings.TrimSpace(input.ModelID), Provider: ai.ProviderID(strings.TrimSpace(input.ProviderID))},
		ProviderID:             ai.ProviderID(strings.TrimSpace(input.ProviderID)),
		SystemPrompt:           input.SystemPrompt,
		WorkspaceRoot:          request.WorkspaceRoot,
		WorkspaceID:            request.Identity.WorkspaceID,
		TenantID:               request.Identity.TenantID,
		ProjectID:              request.ProjectID,
		Identity:               request.Identity,
		PolicyVersion:          request.PolicyVersion,
		AuthorizationExpiresAt: request.AuthorizationExpiresAt,
		RequestID:              request.RequestID,
	}
	if input.TimeoutMS > 0 {
		spawn.Timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	record, err := m.Spawn(ctx, spawn)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(publicRecord(record))
}

func (m *Manager) handleList(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input listInput
	if len(strings.TrimSpace(string(request.Arguments))) > 0 && string(request.Arguments) != "{}" {
		if err := decodeArguments(request.Arguments, &input); err != nil {
			return tools.ToolResponse{}, err
		}
	}
	records, err := m.List(ctx, request.RunID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	public := make([]Record, 0, len(records))
	for _, record := range records {
		if input.Status != "" && record.Status != input.Status {
			continue
		}
		public = append(public, publicRecord(record))
	}
	return jsonResponse(struct {
		Subagents []Record `json:"subagents"`
	}{public})
}

func (m *Manager) handleStatus(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input idInput
	if err := decodeArguments(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	record, err := m.Get(ctx, request.RunID, input.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(publicRecord(record))
}

func (m *Manager) handleWait(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input waitInput
	if err := decodeArguments(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	waitCtx := ctx
	var cancel context.CancelFunc
	if input.TimeoutMS < 0 {
		return tools.ToolResponse{}, invalidArguments("timeout_ms cannot be negative")
	}
	if input.TimeoutMS > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(input.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	record, err := m.Wait(waitCtx, request.RunID, input.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(publicRecord(record))
}

func (m *Manager) handleCancel(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	var input idInput
	if err := decodeArguments(request.Arguments, &input); err != nil {
		return tools.ToolResponse{}, err
	}
	if err := m.Cancel(ctx, request.RunID, input.ID); err != nil {
		return tools.ToolResponse{}, err
	}
	record, err := m.Get(ctx, request.RunID, input.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(publicRecord(record))
}

func (m *Manager) completionNotice(parentID string) string {
	records, err := m.List(context.Background(), parentID)
	if err != nil {
		return "Subagent Manager: all child executions have settled. Inspect their results with subagent_status before giving the final answer."
	}
	var builder strings.Builder
	builder.WriteString("Subagent Manager: all child executions have settled. Review their results with subagent_status or subagent_wait, then integrate them before finishing.\n")
	for _, record := range records {
		fmt.Fprintf(&builder, "- %s (%s): %s\n", record.ID, record.Name, record.Status)
	}
	return limitText(builder.String(), defaultCompletionTextBytes)
}

func decodeArguments(raw json.RawMessage, target any) error {
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

func jsonResponse(value any) (tools.ToolResponse, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("marshal subagent response: %w", err)
	}
	return tools.ToolResponse{
		Content: []tools.ContentBlock{{Type: tools.ContentText, Text: string(raw)}},
		Details: raw,
	}, nil
}

func publicRecord(record Record) Record {
	public := cloneRecord(record)
	public.Request.Prompt = ""
	public.Request.SystemPrompt = ""
	public.Request.Identity.Capabilities = nil
	public.Request.WorkspaceRoot = ""
	public.Request.Model.Headers = nil
	public.Request.Model.Metadata = nil
	return public
}

func limitText(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func sequentialDescriptor(name, description string, schema json.RawMessage) tools.ToolDescriptor {
	return tools.ToolDescriptor{
		Name:          name,
		Description:   description,
		InputSchema:   schema,
		ExecutionMode: tools.ExecutionSequential,
	}
}

func spawnDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolSpawn, "Start an isolated subagent for a focused task. The parent must collect its result before finishing.", json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"prompt":{"type":"string"},"mode":{"type":"string","enum":["ephemeral","durable"]},"timeout_ms":{"type":"integer","minimum":1},"model_id":{"type":"string"},"provider_id":{"type":"string"},"system_prompt":{"type":"string"}},"required":["prompt"],"additionalProperties":false}`))
}

func listDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolList, "List subagents created by the current parent session.", json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["queued","running","completed","failed","canceled","waiting_approval"]}},"additionalProperties":false}`))
}

func statusDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolStatus, "Read the status and bounded result of one child subagent.", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`))
}

func waitDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolWait, "Wait for one child subagent to finish and return its bounded result.", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"timeout_ms":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`))
}

func cancelDescriptor() tools.ToolDescriptor {
	return sequentialDescriptor(toolCancel, "Cancel one child subagent owned by the current parent session.", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`))
}
