package pods

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store persists and loads pod configuration.
type Store struct {
	path string
}

// NewStore creates a Store using the given config directory.
// If configDir is empty, it uses $Y_PODS_CONFIG_DIR or ~/.config/y-pods.
func NewStore(configDir string) *Store {
	if configDir == "" {
		configDir = os.Getenv("Y_PODS_CONFIG_DIR")
	}
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config", "y-pods")
	}
	return &Store{path: filepath.Join(configDir, "pods.json")}
}

// Load reads the config file or returns an empty config.
func (s *Store) Load() (Config, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{Pods: map[string]Pod{}}, nil
		}
		return Config{Pods: map[string]Pod{}}, nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{Pods: map[string]Pod{}}, nil
	}
	if cfg.Pods == nil {
		cfg.Pods = map[string]Pod{}
	}
	return cfg, nil
}

// Save writes the config atomically.
func (s *Store) Save(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// EnsureDir creates the config directory.
func (s *Store) EnsureDir() error {
	return os.MkdirAll(filepath.Dir(s.path), 0o755)
}

// GetActivePod returns the active pod name and its config.
func GetActivePod(cfg Config) (string, Pod, bool) {
	if cfg.Active == "" {
		return "", Pod{}, false
	}
	p, ok := cfg.Pods[cfg.Active]
	return cfg.Active, p, ok
}

// GetPod resolves a pod by name or returns the active pod.
func GetPod(cfg Config, name string) (string, Pod, error) {
	if name != "" {
		p, ok := cfg.Pods[name]
		if !ok {
			return "", Pod{}, fmt.Errorf("pod %q not found", name)
		}
		return name, p, nil
	}
	n, p, ok := GetActivePod(cfg)
	if !ok {
		return "", Pod{}, fmt.Errorf("no active pod; use y-pods active <name>")
	}
	return n, p, nil
}
