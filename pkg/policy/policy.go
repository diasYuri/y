package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrQuotaExceeded = fmt.Errorf("policy tenant quota exceeded")

// DecisionKind classifies the outcome of a policy evaluation.
type DecisionKind string

const (
	DecisionAllow           DecisionKind = "allow"
	DecisionDeny            DecisionKind = "deny"
	DecisionRequireApproval DecisionKind = "require_approval"
)

// ApprovalMode identifies how an approval request will be surfaced.
type ApprovalMode string

const (
	ApprovalModeHeadless ApprovalMode = "headless"
)

// ApprovalState describes the current status of an approval resolution.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
)

// ApprovalResolution captures an approval outcome supplied by the caller.
type ApprovalResolution struct {
	ApprovalID string        `json:"approval_id,omitempty"`
	Mode       ApprovalMode  `json:"mode,omitempty"`
	State      ApprovalState `json:"state"`
	Reason     string        `json:"reason,omitempty"`
	Actor      string        `json:"actor,omitempty"`
	ExpiresAt  time.Time     `json:"expires_at,omitempty"`
}

// Identity is the caller and data-isolation boundary carried with a request.
type Identity struct {
	CallerID     string   `json:"caller_id,omitempty"`
	TenantID     string   `json:"tenant_id"`
	WorkspaceID  string   `json:"workspace_id,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	RunID        string   `json:"run_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// ApprovalRequest is returned when a decision requires user approval.
type ApprovalRequest struct {
	ApprovalID    string       `json:"approval_id,omitempty"`
	RequestID     string       `json:"request_id,omitempty"`
	RunID         string       `json:"run_id,omitempty"`
	TurnID        string       `json:"turn_id,omitempty"`
	ToolCallID    string       `json:"tool_call_id,omitempty"`
	Mode          ApprovalMode `json:"mode"`
	ToolName      string       `json:"tool_name,omitempty"`
	Capability    string       `json:"capability,omitempty"`
	WorkspaceRoot string       `json:"workspace_root,omitempty"`
	Path          string       `json:"path,omitempty"`
	ResolvedPath  string       `json:"resolved_path,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	CallerID      string       `json:"caller_id,omitempty"`
	TenantID      string       `json:"tenant_id,omitempty"`
	WorkspaceID   string       `json:"workspace_id,omitempty"`
	ExpiresAt     time.Time    `json:"expires_at,omitempty"`
}

// Decision is the typed authorization result for a concrete operation.
type Decision struct {
	Kind     DecisionKind     `json:"kind"`
	Reason   string           `json:"reason,omitempty"`
	Approval *ApprovalRequest `json:"approval,omitempty"`
}

// Request describes a concrete operation requiring authorization.
type Request struct {
	ToolName               string              `json:"tool_name,omitempty"`
	Capability             string              `json:"capability,omitempty"`
	WorkspaceRoot          string              `json:"workspace_root,omitempty"`
	Path                   string              `json:"path,omitempty"`
	ResolvedPath           string              `json:"resolved_path,omitempty"`
	EscapesWorkspace       bool                `json:"escapes_workspace,omitempty"`
	Sensitive              bool                `json:"sensitive,omitempty"`
	Approval               *ApprovalResolution `json:"approval,omitempty"`
	Identity               Identity            `json:"identity,omitempty"`
	RequestID              string              `json:"request_id,omitempty"`
	RunID                  string              `json:"run_id,omitempty"`
	TurnID                 string              `json:"turn_id,omitempty"`
	ToolCallID             string              `json:"tool_call_id,omitempty"`
	Arguments              json.RawMessage     `json:"arguments,omitempty"`
	RequiredCapabilities   []string            `json:"required_capabilities,omitempty"`
	PolicyVersion          string              `json:"policy_version,omitempty"`
	AuthorizationExpiresAt time.Time           `json:"authorization_expires_at,omitempty"`
}

// Config controls the default rules used by the engine.
type Config struct {
	ApprovalMode                ApprovalMode `json:"approval_mode,omitempty"`
	DenyEscapesWorkspace        bool         `json:"deny_escaped_workspace,omitempty"`
	RequireApprovalForSensitive bool         `json:"require_approval_for_sensitive,omitempty"`
	PolicyVersion               string       `json:"policy_version,omitempty"`
	RequireTenant               bool         `json:"require_tenant,omitempty"`
}

// DefaultConfig returns the baseline headless configuration.
func DefaultConfig() Config {
	return Config{
		ApprovalMode:                ApprovalModeHeadless,
		DenyEscapesWorkspace:        true,
		RequireApprovalForSensitive: true,
	}
}

// Engine evaluates policy requests.
type Engine interface {
	Decide(ctx context.Context, req Request) (Decision, error)
}

// AuditEvent is the durable record of an authorization decision. Arguments
// are redacted before they are stored; raw secrets must never enter this type.
type AuditEvent struct {
	SchemaVersion int             `json:"schema_version"`
	EventID       string          `json:"event_id"`
	Timestamp     time.Time       `json:"timestamp"`
	RequestID     string          `json:"request_id,omitempty"`
	Identity      Identity        `json:"identity"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	Decision      DecisionKind    `json:"decision"`
	Reason        string          `json:"reason,omitempty"`
	PolicyVersion string          `json:"policy_version,omitempty"`
	ArgumentsHash string          `json:"arguments_hash,omitempty"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
}

type AuditSink interface {
	Append(context.Context, AuditEvent) error
}

type AuditSinkFunc func(context.Context, AuditEvent) error

func (f AuditSinkFunc) Append(ctx context.Context, event AuditEvent) error { return f(ctx, event) }

// QuotaStore is the atomic counter boundary for distributed tenant limits.
// The operation key makes policy retries idempotent; a SQL/Redis adapter can
// implement the same contract with a conditional increment.
type QuotaStore interface {
	Consume(context.Context, string, string, int) error
}

// InMemoryQuotaStore is a concurrency-safe quota adapter for local runs and
// tests. Production workers should share an external implementation.
type InMemoryQuotaStore struct {
	mu   sync.Mutex
	used map[string]int
	seen map[string]struct{}
}

func NewInMemoryQuotaStore() *InMemoryQuotaStore {
	return &InMemoryQuotaStore{used: make(map[string]int), seen: make(map[string]struct{})}
}

func (s *InMemoryQuotaStore) Consume(ctx context.Context, tenantID, operationKey string, limit int) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used == nil {
		s.used = make(map[string]int)
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	seenKey := tenantID + "\x00" + operationKey
	if _, ok := s.seen[seenKey]; ok {
		return nil
	}
	if s.used[tenantID] >= limit {
		return fmt.Errorf("%w: tenant %q", ErrQuotaExceeded, tenantID)
	}
	s.used[tenantID]++
	s.seen[seenKey] = struct{}{}
	return nil
}

// InMemoryAuditSink is a concurrency-safe audit adapter for tests and local
// deployments. Events are copied on read.
type InMemoryAuditSink struct {
	mu     sync.Mutex
	events []AuditEvent
}

func NewInMemoryAuditSink() *InMemoryAuditSink { return &InMemoryAuditSink{} }

func (s *InMemoryAuditSink) Append(ctx context.Context, event AuditEvent) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = 1
	}
	if event.EventID == "" {
		event.EventID = eventID(event.RequestID + event.ToolCallID + event.Timestamp.String())
	}
	event.Arguments = Redact(event.Arguments)
	s.mu.Lock()
	s.events = append(s.events, cloneAuditEvent(event))
	s.mu.Unlock()
	return nil
}

func (s *InMemoryAuditSink) Events() []AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditEvent, len(s.events))
	for i, event := range s.events {
		out[i] = cloneAuditEvent(event)
	}
	return out
}

// DistributedConfig adds identity, audit and capability rules around the
// ordinary engine without tying policy to an interactive approval UI.
type DistributedConfig struct {
	Config
	Audit               AuditSink           `json:"-"`
	AllowedCapabilities map[string][]string `json:"allowed_capabilities,omitempty"`
	TenantLimits        map[string]int      `json:"tenant_limits,omitempty"`
	Quota               QuotaStore          `json:"-"`
}

// NewDistributedEngine creates a policy engine with multi-tenant checks and
// audit emission. Existing callers can keep using NewEngine for local use.
func NewDistributedEngine(cfg DistributedConfig) Engine {
	if cfg.Quota == nil {
		cfg.Quota = NewInMemoryQuotaStore()
	}
	return &distributedEngine{base: NewEngine(cfg.Config), cfg: cfg, used: make(map[string]int)}
}

type distributedEngine struct {
	base Engine
	cfg  DistributedConfig
	mu   sync.Mutex
	used map[string]int
}

// AuditAllTools marks this engine for registries that otherwise optimize
// policy evaluation for only sensitive/capability-bound tools.
func (*distributedEngine) AuditAllTools() bool { return true }

func (e *distributedEngine) Decide(ctx context.Context, req Request) (Decision, error) {
	if err := contextError(ctx); err != nil {
		return Decision{}, err
	}
	decision := Decision{Kind: DecisionAllow}
	var err error
	capabilityErr := e.capabilityError(req)
	switch {
	case e.cfg.RequireTenant && strings.TrimSpace(req.Identity.TenantID) == "":
		decision = Decision{Kind: DecisionDeny, Reason: "tenant identity is required"}
	case !req.AuthorizationExpiresAt.IsZero() && !time.Now().Before(req.AuthorizationExpiresAt):
		decision = Decision{Kind: DecisionDeny, Reason: "authorization expired"}
	case req.Approval != nil && !req.Approval.ExpiresAt.IsZero() && !time.Now().Before(req.Approval.ExpiresAt):
		decision = Decision{Kind: DecisionDeny, Reason: "approval expired"}
	case capabilityErr != nil:
		decision = Decision{Kind: DecisionDeny, Reason: capabilityErr.Error()}
	default:
		decision, err = e.base.Decide(ctx, req)
	}
	if err == nil && decision.Kind == DecisionAllow {
		if limitErr := e.consumeTenantLimit(ctx, req); limitErr != nil {
			decision = Decision{Kind: DecisionDeny, Reason: limitErr.Error()}
		}
	}
	if err == nil && e.cfg.Audit != nil {
		if auditErr := e.cfg.Audit.Append(ctx, auditFromRequest(req, decision, e.cfg.PolicyVersion)); auditErr != nil {
			return Decision{}, fmt.Errorf("append policy audit: %w", auditErr)
		}
	}
	return decision, err
}

func (e *distributedEngine) consumeTenantLimit(ctx context.Context, req Request) error {
	tenantID := req.Identity.TenantID
	limit := e.cfg.TenantLimits[tenantID]
	if limit <= 0 {
		return nil
	}
	operationKey := req.RequestID + "|" + req.RunID + "|" + req.TurnID + "|" + req.ToolCallID + "|" + req.ToolName + "|" + hash(req.Arguments)
	if e.cfg.Quota != nil {
		return e.cfg.Quota.Consume(ctx, tenantID, operationKey, limit)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.used[tenantID] >= limit {
		return fmt.Errorf("tenant %q execution limit exceeded", tenantID)
	}
	e.used[tenantID]++
	return nil
}

func (e *distributedEngine) capabilityError(req Request) error {
	if len(req.RequiredCapabilities) == 0 {
		return nil
	}
	granted := make(map[string]struct{}, len(req.Identity.Capabilities))
	for _, capability := range req.Identity.Capabilities {
		granted[capability] = struct{}{}
	}
	allowed := make(map[string]struct{})
	for _, capability := range e.cfg.AllowedCapabilities[req.Identity.TenantID] {
		allowed[capability] = struct{}{}
	}
	for _, capability := range req.RequiredCapabilities {
		if _, ok := granted[capability]; !ok {
			return fmt.Errorf("capability %q is not authorized", capability)
		}
		if len(allowed) > 0 {
			if _, ok := allowed[capability]; !ok {
				return fmt.Errorf("capability %q is not allowed for tenant", capability)
			}
		}
	}
	return nil
}

func auditFromRequest(req Request, decision Decision, policyVersion string) AuditEvent {
	arguments, argumentsHash := auditArguments(req.Arguments)
	if policyVersion == "" {
		policyVersion = req.PolicyVersion
	}
	return AuditEvent{
		SchemaVersion: 1,
		EventID:       eventID(req.RequestID + req.RunID + req.ToolCallID),
		Timestamp:     time.Now().UTC(),
		RequestID:     req.RequestID,
		Identity:      req.Identity,
		ToolName:      req.ToolName,
		ToolCallID:    req.ToolCallID,
		Decision:      decision.Kind,
		Reason:        decision.Reason,
		PolicyVersion: policyVersion,
		ArgumentsHash: argumentsHash,
		Arguments:     arguments,
	}
}

func auditArguments(raw []byte) (json.RawMessage, string) {
	if len(raw) == 0 {
		return nil, hash(raw)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		// Invalid JSON may be arbitrary bytes, so retain only its digest.
		return nil, hash(raw)
	}
	redactValue(value)
	redacted, err := json.Marshal(value)
	if err != nil {
		return nil, hash(raw)
	}
	return redacted, hash(redacted)
}

// Redact removes common secret-bearing fields from JSON before audit/logging.
// Invalid JSON returns nil because it may be arbitrary secret-bearing bytes;
// callers that need correlation should store only a hash of the original.
func Redact(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	redactValue(value)
	out, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return out
}

func redactValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || lower == "authorization" {
				typed[key] = "[REDACTED]"
				continue
			}
			redactValue(child)
		}
	case []any:
		for _, child := range typed {
			redactValue(child)
		}
	}
}

func eventID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func hash(raw []byte) string { return eventID(string(raw)) }

func cloneAuditEvent(event AuditEvent) AuditEvent {
	event.Arguments = append(json.RawMessage(nil), event.Arguments...)
	event.Identity.Capabilities = append([]string(nil), event.Identity.Capabilities...)
	return event
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Func adapts a function to Engine.
type Func func(ctx context.Context, req Request) (Decision, error)

// Decide calls f.
func (f Func) Decide(ctx context.Context, req Request) (Decision, error) {
	return f(ctx, req)
}

type engine struct{ cfg Config }

// NewEngine creates a rule-based policy engine with the given config.
func NewEngine(cfg Config) Engine {
	base := DefaultConfig()
	if cfg.ApprovalMode != "" {
		base.ApprovalMode = cfg.ApprovalMode
	}
	if cfg.DenyEscapesWorkspace {
		base.DenyEscapesWorkspace = true
	}
	if cfg.RequireApprovalForSensitive {
		base.RequireApprovalForSensitive = true
	}
	return &engine{cfg: base}
}

// Decide applies the default workspace and approval rules.
func (e *engine) Decide(ctx context.Context, req Request) (Decision, error) {
	if err := contextError(ctx); err != nil {
		return Decision{}, err
	}
	if req.EscapesWorkspace && e.cfg.DenyEscapesWorkspace {
		return Decision{Kind: DecisionDeny, Reason: "workspace escape is denied"}, nil
	}
	if req.Approval != nil {
		switch req.Approval.State {
		case ApprovalApproved:
			if req.Approval.ApprovalID != "" {
				expected := approvalRequiredDecision(req, approvalMode(req.Approval.Mode, e.cfg.ApprovalMode)).Approval.ApprovalID
				if req.Approval.ApprovalID != expected {
					return Decision{Kind: DecisionDeny, Reason: "approval does not match operation"}, nil
				}
			}
			return Decision{Kind: DecisionAllow}, nil
		case ApprovalDenied:
			reason := req.Approval.Reason
			if reason == "" {
				reason = "approval denied"
			}
			return Decision{Kind: DecisionDeny, Reason: reason}, nil
		case ApprovalPending, "":
			return approvalRequiredDecision(req, approvalMode(req.Approval.Mode, e.cfg.ApprovalMode)), nil
		default:
			return Decision{Kind: DecisionDeny, Reason: fmt.Sprintf("unsupported approval state %q", req.Approval.State)}, nil
		}
	}
	if req.Sensitive && e.cfg.RequireApprovalForSensitive {
		return approvalRequiredDecision(req, e.cfg.ApprovalMode), nil
	}
	return Decision{Kind: DecisionAllow}, nil
}

func approvalMode(requested, fallback ApprovalMode) ApprovalMode {
	if requested != "" {
		return requested
	}
	if fallback != "" {
		return fallback
	}
	return ApprovalModeHeadless
}

func approvalRequiredDecision(req Request, mode ApprovalMode) Decision {
	return Decision{
		Kind: DecisionRequireApproval,
		Approval: &ApprovalRequest{
			ApprovalID:    eventID(req.RequestID + "|" + req.RunID + "|" + req.TurnID + "|" + req.ToolCallID + "|" + req.ToolName),
			RequestID:     req.RequestID,
			RunID:         req.RunID,
			TurnID:        req.TurnID,
			ToolCallID:    req.ToolCallID,
			Mode:          approvalMode(mode, ApprovalModeHeadless),
			ToolName:      req.ToolName,
			Capability:    req.Capability,
			WorkspaceRoot: req.WorkspaceRoot,
			Path:          req.Path,
			ResolvedPath:  req.ResolvedPath,
			Reason:        "approval required before executing a sensitive operation",
			CallerID:      req.Identity.CallerID,
			TenantID:      req.Identity.TenantID,
			WorkspaceID:   req.Identity.WorkspaceID,
			ExpiresAt:     req.AuthorizationExpiresAt,
		},
	}
}
