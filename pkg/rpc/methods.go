//go:build feature_rpc

package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
)

// chatParams is the parameter shape for the "chat" method.
type chatParams struct {
	SessionID string   `json:"session_id,omitempty"`
	Message   string   `json:"message"`
	System    string   `json:"system,omitempty"`
	Model     string   `json:"model,omitempty"`
	Stream    bool     `json:"stream,omitempty"`
	Tools     []string `json:"tools,omitempty"`
}

// chatResult is the result shape for the "chat" method.
type chatResult struct {
	SessionID string        `json:"session_id"`
	Response  string        `json:"response"`
	Usage     *ai.Usage     `json:"usage,omitempty"`
	ToolCalls []ai.ToolCall `json:"tool_calls,omitempty"`
}

// transcriptParams is the parameter shape for the "transcript" method.
type transcriptParams struct {
	SessionID string `json:"session_id,omitempty"`
}

// transcriptResult is the result shape for the "transcript" method.
type transcriptResult struct {
	SessionID    string       `json:"session_id"`
	Messages     []ai.Message `json:"messages"`
	MessageCount int          `json:"message_count"`
}

// clearParams is the parameter shape for the "clear" method.
type clearParams struct {
	SessionID string `json:"session_id,omitempty"`
}

// modelInfo is a lightweight model descriptor for RPC responses.
type modelInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Input    []string `json:"input,omitempty"`
}

// toolInfo is a lightweight tool descriptor for RPC responses.
type toolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (s *Server) handleModels(ctx context.Context) (any, *ErrorObj) {
	if s.cfg.Provider == nil {
		return []modelInfo{}, nil
	}
	models, err := s.cfg.Provider.Models(ctx)
	if err != nil {
		return nil, newError(ErrInternalError, fmt.Sprintf("failed to list models: %v", err))
	}
	out := make([]modelInfo, 0, len(models))
	for _, m := range models {
		inputs := make([]string, 0, len(m.Input))
		for _, ik := range m.Input {
			inputs = append(inputs, string(ik))
		}
		out = append(out, modelInfo{
			ID:       m.ID,
			Name:     m.Name,
			Provider: string(m.Provider),
			Input:    inputs,
		})
	}
	return out, nil
}

func (s *Server) handleTools(ctx context.Context) (any, *ErrorObj) {
	if s.cfg.ToolRegistry == nil {
		return []toolInfo{}, nil
	}
	tools := s.cfg.ToolRegistry.List()
	out := make([]toolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolInfo{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return out, nil
}

func (s *Server) handleChat(ctx context.Context, params json.RawMessage) (any, *ErrorObj) {
	var p chatParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Message) == "" {
		return nil, newError(ErrInvalidParams, "message is required")
	}
	if s.cfg.Provider == nil {
		return nil, newError(ErrInternalError, "no provider configured")
	}

	session := s.getOrCreateSession(p.SessionID)
	sink := func(ev agent.Event) {
		switch ev.Kind {
		case agent.EventStateChanged:
			s.events.Emit(newStreamEvent("state_changed", session.id, map[string]string{"state": string(ev.State)}))
		case agent.EventTextDelta:
			s.events.Emit(newStreamEvent("text_delta", session.id, map[string]string{"text": ev.TextDelta}))
		case agent.EventToolStarted:
			s.events.Emit(newStreamEvent("tool_started", session.id, map[string]string{"name": ev.ToolCall.Name}))
		case agent.EventToolEnded:
			s.events.Emit(newStreamEvent("tool_ended", session.id, map[string]string{"name": ev.ToolResult.ToolName}))
		case agent.EventTurnStarted:
			s.events.Emit(newStreamEvent("turn_started", session.id, map[string]any{"turn": ev.Turn}))
		case agent.EventTurnEnded:
			s.events.Emit(newStreamEvent("turn_ended", session.id, map[string]any{"turn": ev.Turn, "usage": ev.Usage}))
		case agent.EventCompleted:
			s.events.Emit(newStreamEvent("completed", session.id, map[string]string{"state": string(ev.State)}))
		}
	}
	ag := s.sessionAgent(session, sink)

	s.events.Emit(newStreamEvent("chat_started", session.id, map[string]string{"message": p.Message}))

	// Run the agent.
	result, err := ag.Run(ctx, p.Message)
	if err != nil {
		s.events.Emit(newStreamEvent("chat_error", session.id, map[string]string{"error": err.Error()}))
		return nil, newError(ErrInternalError, fmt.Sprintf("agent run failed: %v", err))
	}

	s.events.Emit(newStreamEvent("chat_completed", session.id, map[string]any{"turns": result.Turns, "state": string(result.State)}))

	// Update session transcript from agent.
	session.mu.Lock()
	session.transcript = ag.Transcript()
	session.updatedAt = time.Now()
	session.mu.Unlock()

	// Extract assistant response text.
	var responseText string
	var toolCalls []ai.ToolCall
	for _, msg := range result.Messages {
		if msg.Role == ai.RoleAssistant {
			for _, block := range msg.Content {
				if block.Type == ai.ContentText {
					responseText = block.Text
				}
			}
			toolCalls = msg.ToolCalls
		}
	}

	return chatResult{
		SessionID: session.id,
		Response:  responseText,
		Usage:     &result.Usage,
		ToolCalls: toolCalls,
	}, nil
}

// continueParams is the parameter shape for the "continue" method.
type continueParams struct {
	SessionID string `json:"session_id,omitempty"`
}

// continueResult is the result shape for the "continue" method.
type continueResult struct {
	SessionID string    `json:"session_id"`
	Response  string    `json:"response"`
	Usage     *ai.Usage `json:"usage,omitempty"`
}

// steerParams is the parameter shape for the "steer" method.
type steerParams struct {
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

// abortParams is the parameter shape for the "abort" method.
type abortParams struct {
	SessionID string `json:"session_id,omitempty"`
}

func (s *Server) handleContinue(ctx context.Context, params json.RawMessage) (any, *ErrorObj) {
	var p continueParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}
	if s.cfg.Provider == nil {
		return nil, newError(ErrInternalError, "no provider configured")
	}

	session := s.getOrCreateSession(p.SessionID)
	if len(session.transcript) == 0 {
		return nil, newError(ErrInvalidRequest, "no transcript to continue from")
	}

	sink := func(ev agent.Event) {
		if ev.Kind == agent.EventTextDelta {
			s.events.Emit(newStreamEvent("text_delta", session.id, map[string]string{"text": ev.TextDelta}))
		}
	}
	ag := s.sessionAgent(session, sink)

	s.events.Emit(newStreamEvent("continue_started", session.id, nil))

	result, err := ag.Continue(ctx)
	if err != nil {
		s.events.Emit(newStreamEvent("continue_error", session.id, map[string]string{"error": err.Error()}))
		return nil, newError(ErrInternalError, fmt.Sprintf("continue failed: %v", err))
	}

	s.events.Emit(newStreamEvent("continue_completed", session.id, map[string]any{"turns": result.Turns}))

	session.mu.Lock()
	session.transcript = ag.Transcript()
	session.updatedAt = time.Now()
	session.mu.Unlock()

	var responseText string
	for _, msg := range result.Messages {
		if msg.Role == ai.RoleAssistant {
			for _, block := range msg.Content {
				if block.Type == ai.ContentText {
					responseText = block.Text
				}
			}
		}
	}

	return continueResult{
		SessionID: session.id,
		Response:  responseText,
		Usage:     &result.Usage,
	}, nil
}

func (s *Server) handleSteer(params json.RawMessage) (any, *ErrorObj) {
	var p steerParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Message) == "" {
		return nil, newError(ErrInvalidParams, "message is required")
	}

	session := s.getOrCreateSession(p.SessionID)

	// For now, append the steering message to the transcript so the next
	// chat/continue will pick it up. Full mid-run steering requires a
	// persistent agent instance per session.
	session.mu.Lock()
	session.transcript = append(session.transcript, ai.Message{
		Role:      ai.RoleUser,
		Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: p.Message}},
		Timestamp: time.Now(),
	})
	session.updatedAt = time.Now()
	session.mu.Unlock()

	s.events.Emit(newStreamEvent("steer_queued", session.id, map[string]string{"message": p.Message}))

	return map[string]string{"status": "queued", "session_id": session.id}, nil
}

func (s *Server) handleAbort(params json.RawMessage) (any, *ErrorObj) {
	var p abortParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}

	session := s.getOrCreateSession(p.SessionID)

	// Abort requires a persistent agent instance. Since we rebuild the agent
	// per call, we emit an event but cannot interrupt an in-flight request.
	// Future improvement: keep a persistent agent per session with Abort support.
	s.events.Emit(newStreamEvent("abort_requested", session.id, nil))

	return map[string]string{"status": "requested", "session_id": session.id}, nil
}

func (s *Server) handleTranscript(params json.RawMessage) (any, *ErrorObj) {
	var p transcriptParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}
	session := s.getOrCreateSession(p.SessionID)
	session.mu.RLock()
	msgs := make([]ai.Message, len(session.transcript))
	copy(msgs, session.transcript)
	session.mu.RUnlock()

	return transcriptResult{
		SessionID:    session.id,
		Messages:     msgs,
		MessageCount: len(msgs),
	}, nil
}

func (s *Server) handleClear(params json.RawMessage) (any, *ErrorObj) {
	var p clearParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, newError(ErrInvalidParams, err.Error())
		}
	}
	session := s.getOrCreateSession(p.SessionID)
	session.mu.Lock()
	session.transcript = session.transcript[:0]
	session.updatedAt = time.Now()
	session.mu.Unlock()

	return map[string]bool{"cleared": true}, nil
}

func (s *Server) getOrCreateSession(id string) *session {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()

	if id != "" {
		if sess, ok := s.sessions[id]; ok {
			return sess
		}
	}
	if id == "" {
		id = generateSessionID()
	}
	sess := &session{
		id:        id,
		createdAt: time.Now(),
		updatedAt: time.Now(),
	}
	s.sessions[id] = sess
	return sess
}

var sessionCounter int

func generateSessionID() string {
	sessionCounter++
	return fmt.Sprintf("sess-%d-%d", time.Now().Unix(), sessionCounter)
}
