package taskmanager

import (
	"reflect"
	"testing"
)

func TestSnapshotRestorePreservesHierarchyAndAllocator(t *testing.T) {
	one := New()
	parent, err := one.Create(Task{Name: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	children, err := one.Refine(parent.ID, []Task{{Name: "first"}, {Name: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Start(children[0].ID); err != nil {
		t.Fatal(err)
	}
	snapshot := one.Snapshot()
	two := New()
	if err := two.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.List(), two.List()) {
		t.Fatal("task identities/state changed")
	}
	newTask, err := two.Create(Task{Name: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if newTask.ID == parent.ID || newTask.ID == children[0].ID || newTask.ID == children[1].ID {
		t.Fatal("allocator collided")
	}
	bad := snapshot
	bad.Tasks = append([]Task(nil), snapshot.Tasks...)
	bad.Tasks[0].ParentID = "missing"
	before := two.Snapshot()
	if err := two.Restore(bad); err == nil {
		t.Fatal("missing parent accepted")
	}
	if !reflect.DeepEqual(before, two.Snapshot()) {
		t.Fatal("failed restore modified manager")
	}
	bad = snapshot
	bad.SchemaVersion = 99
	if err := two.Restore(bad); err == nil {
		t.Fatal("future schema accepted")
	}
}
