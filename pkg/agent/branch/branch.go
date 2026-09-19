package branch

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/ai"
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
	Name     string
	// ForkLen is the number of messages copied from the parent at fork
	// time. Merge replays only Messages[ForkLen:] back into the parent so
	// that subsequent appends on the parent are preserved correctly.
	// The main branch (no parent) keeps ForkLen == 0.
	ForkLen int
}

// Entry is a transport- and storage-neutral description of one branch in a
// session tree. Messages are deliberately excluded: callers can fetch just
// the branch they need with GetMessagesAt.
type Entry struct {
	ID           BranchID  `json:"id"`
	Parent       BranchID  `json:"parent,omitempty"`
	Label        string    `json:"label,omitempty"`
	Name         string    `json:"name,omitempty"`
	Created      time.Time `json:"created"`
	MessageCount int       `json:"message_count"`
	ForkLen      int       `json:"fork_len,omitempty"`
}

// BranchManager manages conversation branches for an agent.
type BranchManager struct {
	mu       sync.RWMutex
	branches map[BranchID]*Branch
	main     BranchID
	next     uint64
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
				Name:    "main",
			},
		},
		main: main,
	}
}

// Clone creates an independent root branch from from. Unlike Fork, a clone
// does not retain a parent relationship and can therefore be persisted or
// resumed as a separate session without carrying the source tree with it.
func (bm *BranchManager) Clone(from BranchID, name string) (BranchID, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	src, ok := bm.branches[from]
	if !ok {
		return "", fmt.Errorf("branch %q not found", from)
	}
	id := bm.nextIDLocked()
	if name == "" {
		name = string(id)
	}
	bm.branches[id] = &Branch{
		ID:       id,
		Messages: cloneMessages(src.Messages),
		Created:  time.Now().UTC(),
		Label:    src.Label,
		Name:     name,
	}
	return id, nil
}

// Fork creates a new branch from an existing branch at the current tip.
func (bm *BranchManager) Fork(from BranchID, label string) (BranchID, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	src, ok := bm.branches[from]
	if !ok {
		return "", fmt.Errorf("branch %q not found", from)
	}

	id := bm.nextIDLocked()
	branch := &Branch{
		ID:       id,
		Parent:   from,
		Messages: cloneMessages(src.Messages),
		Created:  time.Now().UTC(),
		Label:    label,
		Name:     label,
		ForkLen:  len(src.Messages),
	}
	bm.branches[id] = branch
	return id, nil
}

func (bm *BranchManager) nextIDLocked() BranchID {
	for {
		bm.next++
		id := BranchID(fmt.Sprintf("branch-%d", bm.next))
		if _, exists := bm.branches[id]; !exists {
			return id
		}
	}
}

// Get returns a branch by ID.
func (bm *BranchManager) Get(id BranchID) (*Branch, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	b, ok := bm.branches[id]
	if !ok {
		return nil, false
	}
	copy := *b
	copy.Messages = cloneMessages(b.Messages)
	return &copy, true
}

// List returns all branch IDs.
func (bm *BranchManager) List() []BranchID {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	out := make([]BranchID, 0, len(bm.branches))
	for id := range bm.branches {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// GetTree returns a stable, metadata-only view of the session branch tree.
func (bm *BranchManager) GetTree() []Entry {
	return bm.GetEntries()
}

// GetEntries lists branches in stable ID order. It is safe for callers to
// retain the returned values after the manager is mutated.
func (bm *BranchManager) GetEntries() []Entry {
	if bm == nil {
		return nil
	}
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	out := make([]Entry, 0, len(bm.branches))
	for _, branch := range bm.branches {
		out = append(out, Entry{
			ID: branch.ID, Parent: branch.Parent, Label: branch.Label, Name: branch.Name,
			Created: branch.Created, MessageCount: len(branch.Messages), ForkLen: branch.ForkLen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetMessagesAt returns a deep copy of the messages through index. Passing a
// negative index returns the entire branch; index zero includes the first
// message. This lets stores expose a cursor without leaking mutable state.
func (bm *BranchManager) GetMessagesAt(id BranchID, index int) ([]ai.Message, error) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	branch, ok := bm.branches[id]
	if !ok {
		return nil, fmt.Errorf("branch %q not found", id)
	}
	end := len(branch.Messages)
	if index >= 0 {
		end = index + 1
		if end > len(branch.Messages) {
			end = len(branch.Messages)
		}
	}
	return cloneMessages(branch.Messages[:end]), nil
}

// SetLabel changes the human-facing label of a branch.
func (bm *BranchManager) SetLabel(id BranchID, label string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	branch, ok := bm.branches[id]
	if !ok {
		return fmt.Errorf("branch %q not found", id)
	}
	branch.Label = label
	return nil
}

// SetName changes the stable display name of a branch.
func (bm *BranchManager) SetName(id BranchID, name string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	branch, ok := bm.branches[id]
	if !ok {
		return fmt.Errorf("branch %q not found", id)
	}
	branch.Name = name
	return nil
}

// ResumeFrom returns a deep-copy transcript suitable for a fresh Agent. It
// is intentionally an operation on data, not a live-agent handoff.
func (bm *BranchManager) ResumeFrom(id BranchID) ([]ai.Message, error) {
	return bm.GetMessagesAt(id, -1)
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
