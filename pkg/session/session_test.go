package session

import (
	"context"
	"testing"

	"github.com/diasYuri/y/pkg/agent/branch"
	"github.com/diasYuri/y/pkg/ai"
)

type fakeStore struct{}

func (fakeStore) List(context.Context, string) ([]SessionSummary, error)  { return nil, nil }
func (fakeStore) Latest(context.Context, string) (*SessionSummary, error) { return nil, nil }
func (fakeStore) Resolve(context.Context, string, string) (string, error) { return "", nil }
func (fakeStore) SaveTranscript(context.Context, string, []ai.Message, int64) (SessionSummary, error) {
	return SessionSummary{}, nil
}

func TestStoreContract(t *testing.T) {
	var _ Store = fakeStore{}
}

type fakeBranchStore struct{}

func (fakeBranchStore) Fork(context.Context, string, branch.BranchID, string) (branch.BranchID, error) {
	return "", nil
}
func (fakeBranchStore) Clone(context.Context, string, branch.BranchID, string) (branch.BranchID, error) {
	return "", nil
}
func (fakeBranchStore) GetTree(context.Context, string) ([]branch.Entry, error)    { return nil, nil }
func (fakeBranchStore) GetEntries(context.Context, string) ([]branch.Entry, error) { return nil, nil }
func (fakeBranchStore) GetMessagesAt(context.Context, string, branch.BranchID, int) ([]ai.Message, error) {
	return nil, nil
}
func (fakeBranchStore) SetLabel(context.Context, string, branch.BranchID, string) error { return nil }
func (fakeBranchStore) SetName(context.Context, string, branch.BranchID, string) error  { return nil }
func (fakeBranchStore) ResumeFrom(context.Context, string, branch.BranchID) ([]ai.Message, error) {
	return nil, nil
}

func TestBranchStoreContract(t *testing.T) { var _ BranchStore = fakeBranchStore{} }
