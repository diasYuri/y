package taskmanager

import (
	"fmt"
	"sort"
	"strings"
)

const SnapshotVersion = 1

// Snapshot is a portable task tree, including the ID allocator and completion
// gate state. Restore validates the entire tree before publishing any changes.
type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Tasks         []Task `json:"tasks"`
	NextID        uint64 `json:"next_id"`
	LastState     string `json:"last_state,omitempty"`
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Snapshot{SchemaVersion: SnapshotVersion, NextID: m.nextID, LastState: m.lastState}
	for _, task := range m.tasks {
		s.Tasks = append(s.Tasks, task)
	}
	sort.Slice(s.Tasks, func(i, j int) bool { return s.Tasks[i].ID < s.Tasks[j].ID })
	return s
}

func (m *Manager) Restore(s Snapshot) error {
	if m == nil {
		return ErrManagerNil
	}
	if s.SchemaVersion != SnapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot version", ErrInvalidTask)
	}
	tasks := make(map[string]Task, len(s.Tasks))
	for _, task := range s.Tasks {
		if task.ID == "" || strings.ContainsAny(task.ID, " \t\r\n") || task.Order <= 0 {
			return ErrInvalidTask
		}
		if err := validateTaskName(task.Name); err != nil {
			return err
		}
		if task.Status != StatusPending && task.Status != StatusInProgress && task.Status != StatusCompleted {
			return ErrInvalidTask
		}
		if _, exists := tasks[task.ID]; exists {
			return ErrTaskExists
		}
		tasks[task.ID] = task
	}
	for _, task := range tasks {
		seen := map[string]bool{task.ID: true}
		parent := task.ParentID
		for parent != "" {
			p, exists := tasks[parent]
			if !exists || seen[parent] {
				return fmt.Errorf("%w: missing parent or cycle", ErrInvalidTask)
			}
			if p.Status == StatusCompleted && task.Status != StatusCompleted {
				return fmt.Errorf("%w: completed ancestor has open child", ErrInvalidTask)
			}
			seen[parent] = true
			parent = p.ParentID
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.maxTasks > 0 && len(tasks) > m.maxTasks {
		return ErrTaskLimit
	}
	m.tasks, m.nextID, m.lastState = tasks, s.NextID, s.LastState
	return nil
}
