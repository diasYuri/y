package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	ycontext "github.com/yuri/y/pkg/context"
	pmemory "github.com/yuri/y/pkg/memory"
	"github.com/yuri/y/pkg/tools"
)

const (
	capabilityMemoryRead  tools.Capability = "memory.read"
	capabilityMemoryWrite tools.Capability = "memory.write"
)

type searchArgs struct {
	Query    string `json:"query,omitempty"`
	Scope    string `json:"scope,omitempty"`
	MaxItems int    `json:"max_items,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}
type readArgs struct {
	ID string `json:"id"`
}
type saveArgs struct {
	ID         string       `json:"id,omitempty"`
	Type       pmemory.Kind `json:"type"`
	Scope      string       `json:"scope"`
	Summary    string       `json:"summary"`
	Content    string       `json:"content"`
	Tags       []string     `json:"tags,omitempty"`
	Confidence float32      `json:"confidence,omitempty"`
	Explicit   bool         `json:"explicit,omitempty"`
}
type forgetArgs struct {
	ID       string `json:"id"`
	Explicit bool   `json:"explicit,omitempty"`
}

func registerTools(reg *tools.Registry, ext *Extension) error {
	for _, item := range []struct {
		desc    tools.ToolDescriptor
		handler tools.ToolHandler
	}{
		{tools.ToolDescriptor{Name: "memory.search", Description: "Search historical memory relevant to the current request.", InputSchema: schema(`{"type":"object","properties":{"query":{"type":"string"},"scope":{"type":"string"},"max_items":{"type":"integer"},"max_bytes":{"type":"integer"}}}`), Capabilities: []tools.Capability{capabilityMemoryRead}}, toolHandlerFunc(ext.search)},
		{tools.ToolDescriptor{Name: "memory.read", Description: "Read one historical memory item by ID.", InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`), Capabilities: []tools.Capability{capabilityMemoryRead}}, toolHandlerFunc(ext.read)},
		{tools.ToolDescriptor{Name: "memory.list", Description: "List active historical memory items.", InputSchema: schema(`{"type":"object","properties":{"scope":{"type":"string"},"max_items":{"type":"integer"}}}`), Capabilities: []tools.Capability{capabilityMemoryRead}}, toolHandlerFunc(ext.list)},
		{tools.ToolDescriptor{Name: "memory.save", Description: "Save a memory only after the user explicitly requests it.", InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"},"type":{"type":"string"},"scope":{"type":"string"},"summary":{"type":"string"},"content":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"confidence":{"type":"number"},"explicit":{"type":"boolean"}},"required":["type","scope","summary","content"]}`), Capabilities: []tools.Capability{capabilityMemoryWrite}}, toolHandlerFunc(ext.save)},
		{tools.ToolDescriptor{Name: "memory.forget", Description: "Forget one memory item and invalidate its recall.", InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"},"explicit":{"type":"boolean"}},"required":["id"]}`), Capabilities: []tools.Capability{capabilityMemoryWrite}}, toolHandlerFunc(ext.forget)},
	} {
		if err := reg.Add(item.desc, item.handler); err != nil {
			return err
		}
	}
	return nil
}

type toolHandlerFunc func(context.Context, tools.ToolRequest) (tools.ToolResponse, error)

func (f toolHandlerFunc) Handle(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	return f(ctx, req)
}
func schema(value string) json.RawMessage { return json.RawMessage(value) }

func (e *Extension) search(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	var args searchArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return tools.ToolResponse{}, err
	}
	query := e.query(req, args.Query, args.Scope, args.MaxItems, args.MaxBytes)
	values, err := e.store.Search(ctx, query)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(values)
}
func (e *Extension) list(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	var args searchArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return tools.ToolResponse{}, err
		}
	}
	values, err := e.store.Search(ctx, e.query(req, "", args.Scope, args.MaxItems, args.MaxBytes))
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(values)
}
func (e *Extension) read(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	var args readArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return tools.ToolResponse{}, err
	}
	value, err := e.store.Read(ctx, args.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	if !e.authorized(value, req) {
		return tools.ToolResponse{}, pmemory.ErrNotFound
	}
	return jsonResponse(value)
}
func (e *Extension) save(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	var args saveArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return tools.ToolResponse{}, err
	}
	if !e.explicitRequest(req.RunID, args.Explicit) {
		return tools.ToolResponse{}, errors.New("memory.save requires an explicit user request")
	}
	scope := parseScope(args.Scope)
	query := e.query(req, "", "", 0, 0)
	value := pmemory.Memory{ID: args.ID, Kind: args.Type, Scope: scope, Summary: args.Summary, Content: args.Content, Tags: args.Tags, Confidence: args.Confidence, Source: pmemory.SourceRef{RunID: req.RunID, ProjectID: query.ProjectID, TenantID: query.TenantID, WorkspaceID: query.WorkspaceID, SessionID: query.SessionID}, Status: pmemory.StatusActive}
	if err := e.store.Save(ctx, value); err != nil {
		return tools.ToolResponse{}, err
	}
	if value.ID == "" {
		value.ID = pmemory.StableID(value)
	}
	return jsonResponse(map[string]any{"id": value.ID, "saved": true, "scope": value.Scope})
}
func (e *Extension) forget(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
	var args forgetArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return tools.ToolResponse{}, err
	}
	if !e.explicitRequest(req.RunID, args.Explicit) {
		return tools.ToolResponse{}, errors.New("memory.forget requires an explicit user request")
	}
	value, err := e.store.Read(ctx, args.ID)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	if !e.authorized(value, req) {
		return tools.ToolResponse{}, pmemory.ErrNotFound
	}
	if err := e.store.Forget(ctx, args.ID); err != nil {
		return tools.ToolResponse{}, err
	}
	return jsonResponse(map[string]any{"id": args.ID, "forgotten": true})
}

func (e *Extension) explicitRequest(runID string, argument bool) bool {
	_ = argument // Tool arguments are model-controlled and cannot establish consent.
	e.mu.Lock()
	observed := e.explicitByRun[runID]
	if runID == "" {
		// The empty key preserves direct extension tests and non-agent callers.
		observed = e.explicit
	}
	e.mu.Unlock()
	return observed
}
func (e *Extension) query(req tools.ToolRequest, text, scope string, maxItems int, maxBytes int64) pmemory.Query {
	e.mu.Lock()
	config := e.config
	sessionID := ""
	if req.RunID != "" {
		sessionID = e.lastRunByRun[req.RunID].SessionID
	}
	e.mu.Unlock()
	query := pmemory.Query{TenantID: req.Identity.TenantID, WorkspaceID: req.Identity.WorkspaceID, ProjectID: req.ProjectID, SessionID: req.Identity.SessionID, Terms: strings.Fields(text), MaxItems: maxItems, MaxBytes: maxBytes}
	if query.TenantID == "" {
		query.TenantID = config.TenantID
	}
	if query.WorkspaceID == "" {
		query.WorkspaceID = config.WorkspaceID
	}
	if query.ProjectID == "" {
		query.ProjectID = config.ProjectID
	}
	if query.SessionID == "" {
		query.SessionID = sessionID
	}
	if scope != "" {
		query.Scopes = []ycontext.Scope{parseScope(scope)}
	}
	return query
}

func (e *Extension) authorized(value pmemory.Memory, req tools.ToolRequest) bool {
	query := e.query(req, "", "", 0, 0)
	switch value.Scope {
	case ycontext.ScopeGlobal:
		return true
	case ycontext.ScopeTenant:
		return query.TenantID != "" && value.Source.TenantID == query.TenantID
	case ycontext.ScopeWorkspace:
		return query.WorkspaceID != "" && value.Source.WorkspaceID == query.WorkspaceID
	case ycontext.ScopeProject:
		return query.ProjectID != "" && value.Source.ProjectID == query.ProjectID
	case ycontext.ScopeSession:
		return query.SessionID != "" && value.Source.SessionID == query.SessionID
	default:
		return false
	}
}
func jsonResponse(value any) (tools.ToolResponse, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tools.ToolResponse{}, err
	}
	return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: string(raw)}}}, nil
}
func parseScope(value string) ycontext.Scope {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "global":
		return ycontext.ScopeGlobal
	case "tenant":
		return ycontext.ScopeTenant
	case "workspace":
		return ycontext.ScopeWorkspace
	case "project":
		return ycontext.ScopeProject
	case "session":
		return ycontext.ScopeSession
	default:
		return ""
	}
}
