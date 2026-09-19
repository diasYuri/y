package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	ycontext "github.com/diasYuri/y/pkg/context"
)

// Kind classifies a durable memory item.
type Kind string

const (
	KindPreference  Kind = "preference"
	KindProjectFact Kind = "project_fact"
	KindDecision    Kind = "decision"
	KindCorrection  Kind = "correction"
	KindReference   Kind = "reference"
)

// Status controls whether a memory participates in recall.
type Status string

const (
	StatusActive    Status = "active"
	StatusPending   Status = "pending"
	StatusForgotten Status = "forgotten"
)

// SourceRef records non-sensitive provenance and the isolation boundary that
// owns a memory. It must never contain a transcript or credential.
type SourceRef struct {
	SessionID   string `json:"source_session,omitempty"`
	RunID       string `json:"source_run,omitempty"`
	ProjectID   string `json:"source_project,omitempty"`
	TenantID    string `json:"source_tenant,omitempty"`
	WorkspaceID string `json:"source_workspace,omitempty"`
}

// Memory is a durable historical fact, preference, decision, correction, or
// reference. Content is untrusted data and is never a policy instruction.
type Memory struct {
	ID           string         `json:"id"`
	Kind         Kind           `json:"kind"`
	Scope        ycontext.Scope `json:"scope"`
	Summary      string         `json:"summary"`
	Content      string         `json:"content"`
	Tags         []string       `json:"tags,omitempty"`
	Source       SourceRef      `json:"source"`
	Confidence   float32        `json:"confidence,omitempty"`
	Sensitive    bool           `json:"sensitive,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	LastVerified time.Time      `json:"last_verified_at,omitempty"`
	LastUsedAt   time.Time      `json:"last_used_at,omitempty"`
	UsageCount   int            `json:"usage_count,omitempty"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	Status       Status         `json:"status"`
}

// Query describes a bounded, isolation-aware memory lookup.
type Query struct {
	TenantID       string           `json:"tenant_id,omitempty"`
	WorkspaceID    string           `json:"workspace_id,omitempty"`
	ProjectID      string           `json:"project_id,omitempty"`
	SessionID      string           `json:"session_id,omitempty"`
	Scopes         []ycontext.Scope `json:"scopes,omitempty"`
	Terms          []string         `json:"terms,omitempty"`
	MaxItems       int              `json:"max_items,omitempty"`
	MaxBytes       int64            `json:"max_bytes,omitempty"`
	IncludePending bool             `json:"include_pending,omitempty"`
	IncludeExpired bool             `json:"include_expired,omitempty"`
	Cursor         string           `json:"cursor,omitempty"`
	PageSize       int              `json:"page_size,omitempty"`
	Now            time.Time        `json:"now,omitempty"`
}

// Job is a durable, idempotent unit of asynchronous memory work.
type Job struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	ProjectID      string    `json:"project_id,omitempty"`
	TenantID       string    `json:"tenant_id,omitempty"`
	Payload        []byte    `json:"payload,omitempty"`
	Attempt        int       `json:"attempt,omitempty"`
	MaxAttempts    int       `json:"max_attempts,omitempty"`
	AvailableAt    time.Time `json:"available_at,omitempty"`
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

// Store is the source-of-truth boundary for memory. Implementations may be
// local, remote, durable, or intentionally ephemeral.
type Store interface {
	Search(context.Context, Query) ([]Memory, error)
	Read(context.Context, string) (Memory, error)
	Save(context.Context, Memory) error
	Forget(context.Context, string) error
	Version(context.Context, ycontext.Request) (string, error)
}

// UsageTracker is an optional store capability used by context sources to
// record which memories were surfaced without coupling Store to write-heavy
// usage accounting.
type UsageTracker interface {
	MarkSurfaced(context.Context, []string) error
}

// JobQueue is the deployment-neutral queue contract. Claim implementations
// must make expired leases visible again and all operations must be idempotent.
type JobQueue interface {
	Enqueue(context.Context, Job) error
	Claim(context.Context, string) (Job, error)
	Complete(context.Context, string) error
	Retry(context.Context, string, error) error
}

// LeaseManager coordinates work that may be executed by more than one worker.
type LeaseManager interface {
	Acquire(context.Context, string, string, time.Duration) error
	Renew(context.Context, string, string, time.Duration) error
	Release(context.Context, string, string) error
}

var (
	ErrNotFound         = errors.New("memory not found")
	ErrAlreadyForgotten = errors.New("memory already forgotten")
	ErrQueueFull        = errors.New("memory job queue is full")
	ErrNoJob            = errors.New("no memory job available")
	ErrLeaseConflict    = errors.New("memory lease conflict")
	ErrSensitiveContent = errors.New("memory contains sensitive content")
	ErrInvalidMemory    = errors.New("invalid memory")
)

// Valid reports whether k is a supported memory kind.
func (k Kind) Valid() bool {
	switch k {
	case KindPreference, KindProjectFact, KindDecision, KindCorrection, KindReference:
		return true
	default:
		return false
	}
}

// ValidScope reports whether scope is a supported context boundary.
func ValidScope(scope ycontext.Scope) bool {
	switch scope {
	case ycontext.ScopeGlobal, ycontext.ScopeTenant, ycontext.ScopeWorkspace,
		ycontext.ScopeProject, ycontext.ScopeSession:
		return true
	default:
		return false
	}
}

// Validate checks the invariant that every backend must enforce before write.
func Validate(value Memory) error {
	if !value.Kind.Valid() {
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidMemory, value.Kind)
	}
	if !ValidScope(value.Scope) {
		return fmt.Errorf("%w: unsupported scope %q", ErrInvalidMemory, value.Scope)
	}
	if strings.TrimSpace(value.Summary) == "" {
		return fmt.Errorf("%w: summary is required", ErrInvalidMemory)
	}
	if strings.TrimSpace(value.Content) == "" {
		return fmt.Errorf("%w: content is required", ErrInvalidMemory)
	}
	if len([]rune(value.Summary)) > 512 {
		return fmt.Errorf("%w: summary is too long", ErrInvalidMemory)
	}
	if len([]byte(value.Content)) > 1<<20 {
		return fmt.Errorf("%w: content exceeds 1 MiB", ErrInvalidMemory)
	}
	if value.Confidence < 0 || value.Confidence > 1 {
		return fmt.Errorf("%w: confidence must be between 0 and 1", ErrInvalidMemory)
	}
	return nil
}

// StableID returns the deterministic identity used for idempotent saves.
func StableID(value Memory) string {
	parts := []string{string(value.Kind), string(value.Scope), value.Source.TenantID,
		value.Source.WorkspaceID, value.Source.ProjectID, value.Source.SessionID,
		strings.TrimSpace(value.Summary), strings.TrimSpace(value.Content)}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}
