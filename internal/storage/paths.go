package storage

import (
	"os"
	"path/filepath"
	"strings"
)

const agentDirEnv = "Y_CODING_AGENT_DIR"

// DefaultAgentDir returns the root directory used for user state.
func DefaultAgentDir() string {
	if envDir := os.Getenv(agentDirEnv); envDir != "" {
		return expandPath(envDir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(string(os.PathSeparator), ".pi", "agent")
	}
	return filepath.Join(home, ".pi", "agent")
}

// DefaultConfigPath returns the default TOML configuration path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultAgentDir(), "config.toml")
}

// DefaultAuthPath returns the default auth storage path.
func DefaultAuthPath() string {
	return filepath.Join(DefaultAgentDir(), "auth.json")
}

// DefaultSessionsDir returns the directory that holds all session folders.
func DefaultSessionsDir() string {
	return filepath.Join(DefaultAgentDir(), "sessions")
}

// DefaultSessionDir returns the cwd-specific session directory.
func DefaultSessionDir(cwd string) string {
	return filepath.Join(DefaultSessionsDir(), encodeWorkdir(cwd))
}

func encodeWorkdir(cwd string) string {
	cwd = filepath.Clean(cwd)
	cwd = strings.TrimPrefix(cwd, string(filepath.Separator))
	if cwd == "." || cwd == "" {
		return "--.--"
	}
	replacer := strings.NewReplacer(
		string(filepath.Separator), "-",
		"/", "-",
		"\\", "-",
		":", "-",
	)
	return "--" + replacer.Replace(cwd) + "--"
}

func expandPath(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
