package agent

import (
	"testing"

	"github.com/yuri/y/pkg/ai"
)

func TestBranchManagerMain(t *testing.T) {
	bm := NewBranchManager()
	if bm.Main() != BranchID("main") {
		t.Fatalf("main = %q, want 'main'", bm.Main())
	}
	b, ok := bm.Get(bm.Main())
	if !ok {
		t.Fatal("main branch not found")
	}
	if b.Label != "main" {
		t.Fatalf("label = %q, want 'main'", b.Label)
	}
}

func TestBranchManagerFork(t *testing.T) {
	bm := NewBranchManager()
	bm.AppendMessages(bm.Main(), ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}})

	child, err := bm.Fork(bm.Main(), "explore-a")
	if err != nil {
		t.Fatalf("Fork error: %v", err)
	}

	childB, ok := bm.Get(child)
	if !ok {
		t.Fatal("child branch not found")
	}
	if childB.Parent != bm.Main() {
		t.Fatalf("parent = %q, want main", childB.Parent)
	}
	if len(childB.Messages) != 1 {
		t.Fatalf("child messages = %d, want 1", len(childB.Messages))
	}

	// Verify parent is unchanged.
	mainB, _ := bm.Get(bm.Main())
	if len(mainB.Messages) != 1 {
		t.Fatalf("main messages = %d, want 1", len(mainB.Messages))
	}
}

func TestBranchManagerForkNotFound(t *testing.T) {
	bm := NewBranchManager()
	_, err := bm.Fork(BranchID("missing"), "x")
	if err == nil {
		t.Fatal("expected error for missing branch")
	}
}

func TestBranchManagerAppendMessages(t *testing.T) {
	bm := NewBranchManager()
	err := bm.AppendMessages(bm.Main(), ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}}})
	if err != nil {
		t.Fatalf("AppendMessages error: %v", err)
	}
	b, _ := bm.Get(bm.Main())
	if len(b.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(b.Messages))
	}
}

func TestBranchManagerAppendMessagesNotFound(t *testing.T) {
	bm := NewBranchManager()
	err := bm.AppendMessages(BranchID("missing"), ai.Message{Role: ai.RoleUser})
	if err == nil {
		t.Fatal("expected error for missing branch")
	}
}

func TestBranchManagerSetMessages(t *testing.T) {
	bm := NewBranchManager()
	bm.AppendMessages(bm.Main(), ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "a"}}})
	err := bm.SetMessages(bm.Main(), []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "b"}}},
	})
	if err != nil {
		t.Fatalf("SetMessages error: %v", err)
	}
	b, _ := bm.Get(bm.Main())
	if len(b.Messages) != 1 || b.Messages[0].Content[0].Text != "b" {
		t.Fatalf("messages = %+v, want [b]", b.Messages)
	}
}

func TestBranchManagerMerge(t *testing.T) {
	bm := NewBranchManager()
	bm.AppendMessages(bm.Main(), ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "base"}}})

	child, _ := bm.Fork(bm.Main(), "child")
	bm.AppendMessages(child, ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "extra"}}})

	err := bm.Merge(child)
	if err != nil {
		t.Fatalf("Merge error: %v", err)
	}

	mainB, _ := bm.Get(bm.Main())
	if len(mainB.Messages) != 2 {
		t.Fatalf("main messages = %d, want 2", len(mainB.Messages))
	}
	if mainB.Messages[1].Content[0].Text != "extra" {
		t.Fatalf("second message = %q, want 'extra'", mainB.Messages[1].Content[0].Text)
	}
}

func TestBranchManagerMergeNoParent(t *testing.T) {
	bm := NewBranchManager()
	err := bm.Merge(bm.Main())
	if err == nil {
		t.Fatal("expected error merging main branch")
	}
}

func TestBranchManagerDelete(t *testing.T) {
	bm := NewBranchManager()
	child, _ := bm.Fork(bm.Main(), "temp")
	err := bm.Delete(child)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, ok := bm.Get(child); ok {
		t.Fatal("expected child to be deleted")
	}
}

func TestBranchManagerDeleteMain(t *testing.T) {
	bm := NewBranchManager()
	err := bm.Delete(bm.Main())
	if err == nil {
		t.Fatal("expected error deleting main branch")
	}
}

func TestBranchManagerList(t *testing.T) {
	bm := NewBranchManager()
	bm.Fork(bm.Main(), "a")
	bm.Fork(bm.Main(), "b")
	list := bm.List()
	if len(list) != 3 {
		t.Fatalf("list = %d, want 3", len(list))
	}
}

// TestBranchManagerMergeAfterParentGrows exercises the case the previous
// length-based heuristic got wrong: a child branch is forked, then both
// parent and child append messages independently. Merge must replay only the
// child's post-fork messages onto the parent's current tip.
func TestBranchManagerMergeAfterParentGrows(t *testing.T) {
	bm := NewBranchManager()
	if err := bm.AppendMessages(bm.Main(),
		ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "m1"}}},
		ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "m2"}}},
	); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	child, err := bm.Fork(bm.Main(), "explore")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// Parent grows after the fork.
	if err := bm.AppendMessages(bm.Main(),
		ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "m3"}}},
	); err != nil {
		t.Fatalf("AppendMessages parent: %v", err)
	}

	// Child grows after the fork.
	if err := bm.AppendMessages(child,
		ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "m3-child"}}},
		ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "m4-child"}}},
	); err != nil {
		t.Fatalf("AppendMessages child: %v", err)
	}

	if err := bm.Merge(child); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	parent, _ := bm.Get(bm.Main())
	if len(parent.Messages) != 5 {
		t.Fatalf("parent messages = %d, want 5", len(parent.Messages))
	}
	want := []string{"m1", "m2", "m3", "m3-child", "m4-child"}
	for i, w := range want {
		got := parent.Messages[i].Content[0].Text
		if got != w {
			t.Fatalf("message[%d] = %q, want %q", i, got, w)
		}
	}
}
