package memory

import (
	"context"
	"encoding/json"
	"time"
)

// ExtractionRequest is the redaction boundary for asynchronous extraction.
// Payload should contain a transcript and non-sensitive run metadata only.
type ExtractionRequest struct {
	JobID       string    `json:"job_id"`
	SessionID   string    `json:"session_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	ProjectID   string    `json:"project_id,omitempty"`
	TenantID    string    `json:"tenant_id,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Transcript  []byte    `json:"transcript,omitempty"`
	CapturedAt  time.Time `json:"captured_at"`
}

// Candidate is the structured output of an extractor before validation and
// publication. It intentionally resembles Memory without operational fields.
type Candidate struct {
	Kind       Kind       `json:"type"`
	Scope      string     `json:"scope"`
	Summary    string     `json:"summary"`
	Content    string     `json:"content"`
	Tags       []string   `json:"tags,omitempty"`
	Confidence float32    `json:"confidence,omitempty"`
	Sensitive  bool       `json:"sensitive,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// ExtractionResult is a structured, provider-neutral extractor response.
type ExtractionResult struct {
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Extractor produces memory candidates from a completed transcript.
type Extractor interface {
	Extract(context.Context, ExtractionRequest) (ExtractionResult, error)
}

// CandidatePayload encodes a result for durable job handoff.
func CandidatePayload(result ExtractionResult) ([]byte, error) { return json.Marshal(result) }
