// Package taskmanager provides a native task-management extension for y agents.
//
// The manager keeps a small in-memory task tree, exposes mutation tools for
// the model, and supplies an agent option that requires an activity plan to
// be complete before the agent can finish a run.
package taskmanager

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Status describes the progress of a task.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

// Task is one item in the task tree. ParentID is omitted for root tasks.
// Status is managed by Manager in addition to the identifying fields used by
// the task taxonomy.
type Task struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order"`
	Status      Status `json:"status"`
}

// Summary is the compact view used by the completion gate and task tools.
type Summary struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Open      int `json:"open"`
}

var (
	ErrManagerNil     = errors.New("task manager is nil")
	ErrTaskNotFound   = errors.New("task not found")
	ErrTaskExists     = errors.New("task already exists")
	ErrInvalidTask    = errors.New("invalid task")
	ErrTaskIncomplete = errors.New("task has incomplete children")
	ErrTaskLimit      = errors.New("task limit exceeded")
)

// Option configures a Manager.
type Option func(*Manager)

// WithMaxTasks limits the number of tasks that can exist at once. A zero or
// negative value means unlimited.
func WithMaxTasks(max int) Option {
	return func(m *Manager) {
		if max > 0 {
			m.maxTasks = max
		}
	}
}

// Manager owns the task tree for one agent activity. It is safe for tool
// handlers and lifecycle hooks to use concurrently.
type Manager struct {
	mu        sync.RWMutex
	tasks     map[string]Task
	nextID    uint64
	maxTasks  int
	lastState string
}

// New creates an empty task manager.
func New(opts ...Option) *Manager {
	m := &Manager{tasks: make(map[string]Task)}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// Reset removes all tasks. Hosts can call Reset before starting an unrelated
// activity; a completed run is reset automatically by AgentOption.
func (m *Manager) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.tasks = make(map[string]Task)
	m.nextID = 0
	m.mu.Unlock()
}

// Create adds a root or child task. Empty IDs are generated as task-1,
// task-2, and so on.
func (m *Manager) Create(task Task) (Task, error) {
	if m == nil {
		return Task{}, ErrManagerNil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createLocked(task)
}

// CreateMany adds a batch atomically. If one task is invalid, no task from the
// batch is retained.
func (m *Manager) CreateMany(tasks []Task) ([]Task, error) {
	if m == nil {
		return nil, ErrManagerNil
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: at least one task is required", ErrInvalidTask)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createManyLocked(tasks)
}

func (m *Manager) createManyLocked(tasks []Task) ([]Task, error) {
	previous := cloneTasks(m.tasks)
	previousNextID := m.nextID
	created := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		createdTask, err := m.createLocked(task)
		if err != nil {
			m.tasks = previous
			m.nextID = previousNextID
			return nil, err
		}
		created = append(created, createdTask)
	}
	return created, nil
}

func (m *Manager) createLocked(task Task) (Task, error) {
	if err := validateTaskName(task.Name); err != nil {
		return Task{}, err
	}
	if m.maxTasks > 0 && len(m.tasks) >= m.maxTasks {
		return Task{}, fmt.Errorf("%w: maximum is %d", ErrTaskLimit, m.maxTasks)
	}
	task.Name = strings.TrimSpace(task.Name)
	task.ParentID = strings.TrimSpace(task.ParentID)
	if task.ID == "" {
		task.ID = m.nextTaskIDLocked()
	}
	if strings.TrimSpace(task.ID) == "" || strings.ContainsAny(task.ID, " \t\r\n") {
		return Task{}, fmt.Errorf("%w: id must be non-empty and contain no whitespace", ErrInvalidTask)
	}
	if _, exists := m.tasks[task.ID]; exists {
		return Task{}, fmt.Errorf("%w: %q", ErrTaskExists, task.ID)
	}
	if task.ParentID != "" {
		parent, ok := m.tasks[task.ParentID]
		if !ok {
			return Task{}, fmt.Errorf("%w: parent %q", ErrTaskNotFound, task.ParentID)
		}
		if parent.Status == StatusCompleted {
			return Task{}, fmt.Errorf("%w: parent %q is already completed", ErrInvalidTask, task.ParentID)
		}
	}
	if task.Status == "" {
		task.Status = StatusPending
	}
	if task.Status != StatusPending {
		return Task{}, fmt.Errorf("%w: new tasks must be pending", ErrInvalidTask)
	}
	if task.Order <= 0 {
		task.Order = m.nextOrderLocked(task.ParentID)
	}
	m.tasks[task.ID] = task
	return task, nil
}

// Refine expands a task into an ordered sequence of child tasks. The parent
// remains an activity group and is completed automatically after all direct
// children are completed.
func (m *Manager) Refine(parentID string, children []Task) ([]Task, error) {
	if m == nil {
		return nil, ErrManagerNil
	}
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil, fmt.Errorf("%w: parentId is required", ErrInvalidTask)
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("%w: refinement requires at least one child", ErrInvalidTask)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, ok := m.tasks[parentID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTaskNotFound, parentID)
	}
	if parent.Status == StatusCompleted {
		return nil, fmt.Errorf("%w: parent %q is already completed", ErrInvalidTask, parentID)
	}
	if m.maxTasks > 0 && len(m.tasks)+len(children) > m.maxTasks {
		return nil, fmt.Errorf("%w: maximum is %d", ErrTaskLimit, m.maxTasks)
	}
	previous := cloneTasks(m.tasks)
	previousNextID := m.nextID
	restore := func() {
		m.tasks = previous
		m.nextID = previousNextID
	}

	seen := make(map[string]struct{}, len(children))
	for _, child := range children {
		if err := validateTaskName(child.Name); err != nil {
			return nil, err
		}
		if child.ParentID != "" && child.ParentID != parentID {
			return nil, fmt.Errorf("%w: child %q has a different parent", ErrInvalidTask, child.ID)
		}
		if strings.ContainsAny(child.ID, " \t\r\n") {
			return nil, fmt.Errorf("%w: id must contain no whitespace", ErrInvalidTask)
		}
		if child.ID != "" {
			if _, duplicate := seen[child.ID]; duplicate {
				return nil, fmt.Errorf("%w: %q appears twice", ErrTaskExists, child.ID)
			}
			if _, exists := m.tasks[child.ID]; exists {
				return nil, fmt.Errorf("%w: %q", ErrTaskExists, child.ID)
			}
			seen[child.ID] = struct{}{}
		}
	}

	parent.Status = StatusInProgress
	m.tasks[parentID] = parent
	created := make([]Task, 0, len(children))
	for _, child := range children {
		child.ParentID = parentID
		createdTask, err := m.createLocked(child)
		if err != nil {
			restore()
			return nil, err
		}
		created = append(created, createdTask)
	}
	return created, nil
}

// Start marks a task as in progress.
func (m *Manager) Start(id string) (Task, error) {
	if m == nil {
		return Task{}, ErrManagerNil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	if !ok {
		return Task{}, fmt.Errorf("%w: %q", ErrTaskNotFound, id)
	}
	if task.Status == StatusCompleted {
		return Task{}, fmt.Errorf("%w: %q is already completed", ErrInvalidTask, id)
	}
	task.Status = StatusInProgress
	m.tasks[task.ID] = task
	return task, nil
}

// Complete marks a task complete. A task with children cannot be completed
// until every direct child is complete.
func (m *Manager) Complete(id string) (Task, error) {
	if m == nil {
		return Task{}, ErrManagerNil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("%w: %q", ErrTaskNotFound, id)
	}
	if task.Status == StatusCompleted {
		return task, nil
	}
	if !m.childrenCompleteLocked(id) {
		return Task{}, fmt.Errorf("%w: %q", ErrTaskIncomplete, id)
	}
	task.Status = StatusCompleted
	m.tasks[id] = task
	m.reconcileAncestorsLocked(task.ParentID)
	return task, nil
}

func (m *Manager) childrenCompleteLocked(parentID string) bool {
	for _, task := range m.tasks {
		if task.ParentID != parentID {
			continue
		}
		if task.Status != StatusCompleted {
			return false
		}
	}
	return true
}

func (m *Manager) reconcileAncestorsLocked(parentID string) {
	for parentID != "" {
		parent, ok := m.tasks[parentID]
		if !ok || !m.childrenCompleteLocked(parentID) {
			return
		}
		parent.Status = StatusCompleted
		m.tasks[parentID] = parent
		parentID = parent.ParentID
	}
}

// Get returns one task by ID.
func (m *Manager) Get(id string) (Task, bool) {
	if m == nil {
		return Task{}, false
	}
	m.mu.RLock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	m.mu.RUnlock()
	return task, ok
}

// List returns a deterministic snapshot ordered by parent, order, and ID.
func (m *Manager) List() []Task {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	out := make([]Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		out = append(out, task)
	}
	m.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ParentID != out[j].ParentID {
			return out[i].ParentID < out[j].ParentID
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Summary returns the current task counts.
func (m *Manager) Summary() Summary {
	if m == nil {
		return Summary{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := Summary{Total: len(m.tasks)}
	for _, task := range m.tasks {
		if task.Status == StatusCompleted {
			result.Completed++
		} else {
			result.Open++
		}
	}
	return result
}

// AllComplete reports whether there is a non-empty, fully completed plan.
func (m *Manager) AllComplete() bool {
	summary := m.Summary()
	return summary.Total > 0 && summary.Open == 0
}

func (m *Manager) nextTaskIDLocked() string {
	for {
		m.nextID++
		id := fmt.Sprintf("task-%d", m.nextID)
		if _, exists := m.tasks[id]; !exists {
			return id
		}
	}
}

func (m *Manager) nextOrderLocked(parentID string) int {
	order := 0
	for _, task := range m.tasks {
		if task.ParentID == parentID && task.Order > order {
			order = task.Order
		}
	}
	return order + 1
}

func validateTaskName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidTask)
	}
	return nil
}

func cloneTasks(tasks map[string]Task) map[string]Task {
	cloned := make(map[string]Task, len(tasks))
	for id, task := range tasks {
		cloned[id] = task
	}
	return cloned
}
