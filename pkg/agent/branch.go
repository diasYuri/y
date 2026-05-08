package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
)

// BranchID uniquely identifies a conversation branch.
type BranchID string

// Branch is a fork of the agent transcript at a specific point.
// Branches share the prefix of messages up to the fork point,
// then diverge with their own messages.
type Branch struct {
	ID       BranchID
	Parent   BranchID
	Messages []ai.Message
	Created  time.Time
	Label    string
	// ForkLen is the number of messages copied from the parent at fork
	// time. Merge replays only Messages[ForkLen:] back into the parent so
	// that subsequent appends on the parent are preserved correctly.
	// The main branch (no parent) keeps ForkLen == 0.
	ForkLen int
}

// BranchManager manages conversation branches for an agent.
type BranchManager struct {
	mu       sync.RWMutex
	branches map[BranchID]*Branch
	main     BranchID
}

// NewBranchManager creates a new branch manager with a main branch.
func NewBranchManager() *BranchManager {
	main := BranchID("main")
	return &BranchManager{
		branches: map[BranchID]*Branch{
			main: {
				ID:      main,
				Parent:  "",
				Created: time.Now().UTC(),
				Label:   "main",
			},
		},
		main: main,
	}
}

// Fork creates a new branch from an existing branch at the current tip.
func (bm *BranchManager) Fork(from BranchID, label string) (BranchID, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	src, ok := bm.branches[from]
	if !ok {
		return "", fmt.Errorf("branch %q not found", from)
	}

	id := BranchID(fmt.Sprintf("branch-%d", len(bm.branches)))
	branch := &Branch{
		ID:       id,
		Parent:   from,
		Messages: cloneMessages(src.Messages),
		Created:  time.Now().UTC(),
		Label:    label,
		ForkLen:  len(src.Messages),
	}
	bm.branches[id] = branch
	return id, nil
}

// Get returns a branch by ID.
func (bm *BranchManager) Get(id BranchID) (*Branch, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	b, ok := bm.branches[id]
	if !ok {
		return nil, false
	}
	return b, true
}

// List returns all branch IDs.
func (bm *BranchManager) List() []BranchID {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	out := make([]BranchID, 0, len(bm.branches))
	for id := range bm.branches {
		out = append(out, id)
	}
	return out
}

// AppendMessages adds messages to a branch.
func (bm *BranchManager) AppendMessages(id BranchID, msgs ...ai.Message) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	b, ok := bm.branches[id]
	if !ok {
		return fmt.Errorf("branch %q not found", id)
	}
	b.Messages = append(b.Messages, cloneMessages(msgs)...)
	return nil
}

// SetMessages replaces all messages in a branch.
func (bm *BranchManager) SetMessages(id BranchID, msgs []ai.Message) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	b, ok := bm.branches[id]
	if !ok {
		return fmt.Errorf("branch %q not found", id)
	}
	b.Messages = cloneMessages(msgs)
	return nil
}

// Merge merges a child branch back into its parent. Only the messages the
// child appended after the fork point (Messages[ForkLen:]) are replayed onto
// the parent's current tip. Messages the parent appended after the fork are
// preserved.
func (bm *BranchManager) Merge(child BranchID) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	src, ok := bm.branches[child]
	if !ok {
		return fmt.Errorf("branch %q not found", child)
	}
	if src.Parent == "" {
		return fmt.Errorf("branch %q has no parent to merge into", child)
	}
	parent, ok := bm.branches[src.Parent]
	if !ok {
		return fmt.Errorf("parent branch %q not found", src.Parent)
	}

	// Append only the messages added on the child since the fork point.
	if src.ForkLen <= len(src.Messages) {
		appended := src.Messages[src.ForkLen:]
		if len(appended) > 0 {
			parent.Messages = append(parent.Messages, cloneMessages(appended)...)
		}
	}
	return nil
}

// Delete removes a branch. The main branch cannot be deleted.
func (bm *BranchManager) Delete(id BranchID) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if id == bm.main {
		return fmt.Errorf("cannot delete main branch")
	}
	if _, ok := bm.branches[id]; !ok {
		return fmt.Errorf("branch %q not found", id)
	}
	delete(bm.branches, id)
	return nil
}

// Main returns the main branch ID.
func (bm *BranchManager) Main() BranchID {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.main
}
