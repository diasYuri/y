package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yuri/y/pkg/tools"
)

// BeginOperation implements OperationStore using a compare-and-create update
// under the same cross-process run lock as state and events. The operation log
// is a compact JSON map because it is a local development adapter, while the
// contract remains suitable for a transactional external implementation.
func (s *JSONLStore) BeginOperation(ctx context.Context, requested OperationRecord) (OperationRecord, bool, error) {
	if err := contextErr(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	if err := validateOperation(requested); err != nil {
		return OperationRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, requested.RunID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	defer release()

	operations, err := s.loadOperationsUnlocked(requested.RunID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if existing, ok := operations[requested.IdempotencyKey]; ok {
		if err := compareOperationIdentity(existing, requested); err != nil {
			return OperationRecord{}, false, err
		}
		return cloneOperation(existing), false, nil
	}
	requested.SchemaVersion = CurrentOperationSchemaVersion
	requested.State = OperationPending
	requested.StartedAt = time.Now().UTC()
	operations[requested.IdempotencyKey] = cloneOperation(requested)
	if err := s.saveOperationsUnlocked(requested.RunID, operations); err != nil {
		return OperationRecord{}, false, err
	}
	return cloneOperation(requested), true, nil
}

func (s *JSONLStore) CompleteOperation(ctx context.Context, key string, response tools.ToolResponse, operationErr error) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if key == "" {
		return ErrOperationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The operation key contains the run ID in Agent's default construction,
	// but custom callers may not. Search the small local run tree in that case.
	runID, operation, err := s.findOperationUnlocked(key)
	if err != nil {
		return err
	}
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	operations, err := s.loadOperationsUnlocked(runID)
	if err != nil {
		return err
	}
	operation = operations[key]
	if operation.State != OperationPending {
		return nil
	}
	operation.Response = cloneToolResponsePtr(response)
	if operationErr != nil {
		operation.State = OperationFailed
		operation.Error = structuredOperationError(operationErr)
	} else {
		operation.State = OperationCompleted
	}
	operation.CompletedAt = time.Now().UTC()
	operations[key] = cloneOperation(operation)
	return s.saveOperationsUnlocked(runID, operations)
}

func (s *JSONLStore) ReleaseOperation(ctx context.Context, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if key == "" {
		return ErrOperationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, operation, err := s.findOperationUnlocked(key)
	if err != nil {
		return err
	}
	if operation.State != OperationPending {
		return ErrOperationNotFound
	}
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	operations, err := s.loadOperationsUnlocked(runID)
	if err != nil {
		return err
	}
	delete(operations, key)
	return s.saveOperationsUnlocked(runID, operations)
}

func (s *JSONLStore) loadOperationsUnlocked(runID string) (map[string]OperationRecord, error) {
	raw, err := s.readFile(runID, "operations.json")
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]OperationRecord), nil
	}
	if err != nil {
		return nil, err
	}
	var operations map[string]OperationRecord
	if err := json.Unmarshal(raw, &operations); err != nil {
		return nil, fmt.Errorf("decode operations: %w", err)
	}
	if operations == nil {
		operations = make(map[string]OperationRecord)
	}
	for key, operation := range operations {
		migrated, err := operation.Migrate()
		if err != nil {
			return nil, fmt.Errorf("operation %q: %w", key, err)
		}
		operations[key] = migrated
		if err := validateOperation(migrated); err != nil {
			return nil, fmt.Errorf("operation %q: %w", key, err)
		}
	}
	return operations, nil
}

func (s *JSONLStore) saveOperationsUnlocked(runID string, operations map[string]OperationRecord) error {
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "operations.json"), operations)
}

// AcquireLease implements LeaseStore with a lease record protected by the
// same cross-process lock used for state and events.
