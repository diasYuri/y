package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/agent/branch"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/session"
)

// Sessions is separate because session.Store and agent.TranscriptStore have
// different SaveTranscript contracts. They share the same database lifecycle.
type Sessions struct{ store *Store }

func (s *Store) Sessions() *Sessions { return &Sessions{store: s} }

type sessionData struct {
	Summary  session.SessionSummary
	Branches map[branch.BranchID]branch.Branch
}

func (s *Sessions) SaveTranscript(ctx context.Context, cwd string, messages []ai.Message, maxBytes int64) (session.SessionSummary, error) {
	raw, err := json.Marshal(messages)
	if err != nil {
		return session.SessionSummary{}, err
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		return session.SessionSummary{}, errors.New("sqlite: session transcript exceeds byte budget")
	}
	id := rand.Text()
	now := time.Now().UTC()
	summary := session.SessionSummary{ID: id, Path: id, CWD: cwd, Created: now, Modified: now, MessageCount: len(messages), ByteSize: int64(len(raw))}
	value := sessionData{Summary: summary, Branches: map[branch.BranchID]branch.Branch{"main": {ID: "main", Messages: messages, Created: now, Name: "main", Label: "main"}}}
	err = s.store.mutate(ctx, "sessions", id, func([]byte, uint64) ([]byte, error) { return json.Marshal(value) })
	return summary, err
}
func (s *Sessions) List(ctx context.Context, cwd string) ([]session.SessionSummary, error) {
	values, err := s.store.all(ctx, "sessions")
	if err != nil {
		return nil, err
	}
	out := []session.SessionSummary{}
	for _, raw := range values {
		var value sessionData
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		if value.Summary.CWD == cwd {
			out = append(out, value.Summary)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}
func (s *Sessions) Latest(ctx context.Context, cwd string) (*session.SessionSummary, error) {
	items, err := s.List(ctx, cwd)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}
func (s *Sessions) Resolve(ctx context.Context, cwd, target string) (string, error) {
	items, err := s.List(ctx, cwd)
	if err != nil {
		return "", err
	}
	if target == "" && len(items) > 0 {
		return items[0].ID, nil
	}
	match := ""
	for _, item := range items {
		if item.ID == target {
			return item.ID, nil
		}
		if strings.HasPrefix(item.ID, target) {
			if match != "" {
				return "", errors.New("sqlite: ambiguous session prefix")
			}
			match = item.ID
		}
	}
	if match == "" {
		return "", os.ErrNotExist
	}
	return match, nil
}
func (s *Sessions) load(ctx context.Context, id string) (sessionData, error) {
	raw, _, err := read(ctx, s.store.db, "sessions", id)
	if err != nil {
		return sessionData{}, err
	}
	var value sessionData
	err = json.Unmarshal(raw, &value)
	return value, err
}
func (s *Sessions) change(ctx context.Context, id string, fn func(*sessionData) error) error {
	return s.store.mutate(ctx, "sessions", id, func(raw []byte, _ uint64) ([]byte, error) {
		if raw == nil {
			return nil, os.ErrNotExist
		}
		var value sessionData
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		if err := fn(&value); err != nil {
			return nil, err
		}
		value.Summary.Modified = time.Now().UTC()
		return json.Marshal(value)
	})
}
func (s *Sessions) Fork(ctx context.Context, id string, from branch.BranchID, label string) (branch.BranchID, error) {
	return s.fork(ctx, id, from, label, false)
}
func (s *Sessions) Clone(ctx context.Context, id string, from branch.BranchID, name string) (branch.BranchID, error) {
	return s.fork(ctx, id, from, name, true)
}
func (s *Sessions) fork(ctx context.Context, id string, from branch.BranchID, name string, clone bool) (branch.BranchID, error) {
	newID := branch.BranchID(rand.Text())
	err := s.change(ctx, id, func(value *sessionData) error {
		source, ok := value.Branches[from]
		if !ok {
			return os.ErrNotExist
		}
		parent := from
		forkLen := len(source.Messages)
		if clone {
			parent = ""
			forkLen = 0
		}
		value.Branches[newID] = branch.Branch{ID: newID, Parent: parent, Messages: source.Messages, Created: time.Now().UTC(), Label: name, Name: name, ForkLen: forkLen}
		return nil
	})
	return newID, err
}
func (s *Sessions) GetEntries(ctx context.Context, id string) ([]branch.Entry, error) {
	value, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []branch.Entry{}
	for _, b := range value.Branches {
		out = append(out, branch.Entry{ID: b.ID, Parent: b.Parent, Label: b.Label, Name: b.Name, Created: b.Created, MessageCount: len(b.Messages), ForkLen: b.ForkLen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Sessions) GetTree(ctx context.Context, id string) ([]branch.Entry, error) {
	return s.GetEntries(ctx, id)
}
func (s *Sessions) GetMessagesAt(ctx context.Context, id string, branchID branch.BranchID, n int) ([]ai.Message, error) {
	value, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	b, ok := value.Branches[branchID]
	if !ok {
		return nil, os.ErrNotExist
	}
	if n < 0 {
		n = len(b.Messages)
	}
	if n > len(b.Messages) {
		return nil, errors.New("sqlite: message position out of range")
	}
	return b.Messages[:n], nil
}
func (s *Sessions) ResumeFrom(ctx context.Context, id string, b branch.BranchID) ([]ai.Message, error) {
	return s.GetMessagesAt(ctx, id, b, -1)
}
func (s *Sessions) SetLabel(ctx context.Context, id string, b branch.BranchID, label string) error {
	return s.change(ctx, id, func(value *sessionData) error {
		item, ok := value.Branches[b]
		if !ok {
			return os.ErrNotExist
		}
		item.Label = label
		value.Branches[b] = item
		return nil
	})
}
func (s *Sessions) SetName(ctx context.Context, id string, b branch.BranchID, name string) error {
	return s.change(ctx, id, func(value *sessionData) error {
		item, ok := value.Branches[b]
		if !ok {
			return os.ErrNotExist
		}
		item.Name = name
		value.Branches[b] = item
		return nil
	})
}

var _ session.Store = (*Sessions)(nil)
var _ session.BranchStore = (*Sessions)(nil)
