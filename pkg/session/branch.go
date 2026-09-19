package session

import (
	"context"

	"github.com/diasYuri/y/pkg/agent/branch"
	"github.com/diasYuri/y/pkg/ai"
)

// BranchStore defines backend-neutral session branching. Implementations may
// use append-only trees, snapshots, event sourcing, object storage or a
// database; callers receive the same semantics in every case.
type BranchStore interface {
	Fork(context.Context, string, branch.BranchID, string) (branch.BranchID, error)
	Clone(context.Context, string, branch.BranchID, string) (branch.BranchID, error)
	GetTree(context.Context, string) ([]branch.Entry, error)
	GetEntries(context.Context, string) ([]branch.Entry, error)
	GetMessagesAt(context.Context, string, branch.BranchID, int) ([]ai.Message, error)
	SetLabel(context.Context, string, branch.BranchID, string) error
	SetName(context.Context, string, branch.BranchID, string) error
	ResumeFrom(context.Context, string, branch.BranchID) ([]ai.Message, error)
}
