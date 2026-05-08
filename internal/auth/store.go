package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/yuri/y/internal/storage"
)

// Store provides atomic read/write of the auth file.
type Store struct {
	mu   sync.RWMutex
	path string
}

// NewStore creates a Store using the default auth path.
func NewStore() *Store {
	return &Store{path: storage.DefaultAuthPath()}
}

// NewStoreAt creates a Store at an explicit path.
func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// Read returns credentials for the given provider, or nil if not found.
func (s *Store) Read(providerID string) (*Credentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var all map[string]*Credentials
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("auth file corrupt: %w", err)
	}
	return all[providerID], nil
}

// Write stores credentials for a provider, atomically.
func (s *Store) Write(c *Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var all map[string]*Credentials
	data, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &all); err != nil {
			return fmt.Errorf("auth file corrupt: %w", err)
		}
	}
	if all == nil {
		all = make(map[string]*Credentials)
	}
	all[c.ProviderID] = c

	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// Delete removes credentials for a provider.
func (s *Store) Delete(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var all map[string]*Credentials
	if err := json.Unmarshal(data, &all); err != nil {
		return fmt.Errorf("auth file corrupt: %w", err)
	}
	delete(all, providerID)

	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all stored provider IDs.
func (s *Store) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var all map[string]*Credentials
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("auth file corrupt: %w", err)
	}

	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	return ids, nil
}
