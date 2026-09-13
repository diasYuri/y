package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *JSONLStore) findOperationUnlocked(key string) (string, OperationRecord, error) {
	runsRoot := filepath.Join(s.root, "runs")
	entries, err := os.ReadDir(runsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", OperationRecord{}, ErrOperationNotFound
	}
	if err != nil {
		return "", OperationRecord{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "r-") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(runsRoot, entry.Name(), "operations.json"))
		if readErr != nil {
			continue
		}
		var operations map[string]OperationRecord
		if json.Unmarshal(raw, &operations) == nil {
			if operation, ok := operations[key]; ok {
				runIDBytes, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(entry.Name(), "r-"))
				if decodeErr != nil {
					return "", OperationRecord{}, decodeErr
				}
				return string(runIDBytes), operation, nil
			}
		}
	}
	return "", OperationRecord{}, ErrOperationNotFound
}

// acquireRunLock supplies the cross-process serialization missing from a
// process-local sync.Mutex. The lock file contains no sensitive state and is
// removed on normal release; an old lock is treated as abandoned.
func (s *JSONLStore) acquireRunLock(ctx context.Context, runID string) (func(), error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(s.root, "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(runID))
	path := filepath.Join(lockDir, "r-"+encoded+".lock")
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = file.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > jsonlLockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func atomicJSONWrite(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWriteBytes(path, raw, 0o600)
}

func atomicWriteBytes(path string, raw []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// AgentRunner is a stateless-by-default execution facade. It rehydrates a
// short-lived Agent, observes remote abort state, emits versioned events and
// saves a terminal/checkpoint snapshot. A worker may therefore disappear
// between calls without making the session depend on its goroutines.
