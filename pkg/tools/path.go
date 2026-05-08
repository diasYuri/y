package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type resolvedPath struct {
	WorkspaceRoot    string
	Input            string
	Absolute         string
	EscapesWorkspace bool
}

func resolveForRead(workspaceRoot, input string) (resolvedPath, error) {
	return resolvePath(workspaceRoot, input, true)
}

func resolveForWrite(workspaceRoot, input string) (resolvedPath, error) {
	return resolvePath(workspaceRoot, input, false)
}

func resolvePath(workspaceRoot, input string, followFinal bool) (resolvedPath, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return resolvedPath{}, fmt.Errorf("workspace root is required")
	}
	if strings.TrimSpace(input) == "" {
		return resolvedPath{}, fmt.Errorf("path is required")
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return resolvedPath{}, err
	}
	if evaluated, err := filepath.EvalSymlinks(root); err == nil {
		root = evaluated
	}
	root = filepath.Clean(root)

	var candidate string
	if filepath.IsAbs(input) {
		candidate = filepath.Clean(input)
	} else {
		candidate = filepath.Clean(filepath.Join(root, input))
	}
	if abs, err := filepath.Abs(candidate); err == nil {
		candidate = abs
	}

	checked := candidate
	if followFinal {
		if evaluated, err := filepath.EvalSymlinks(candidate); err == nil {
			checked = evaluated
		}
	} else {
		parent := filepath.Dir(candidate)
		if evaluated, err := filepath.EvalSymlinks(parent); err == nil {
			checked = filepath.Join(evaluated, filepath.Base(candidate))
		}
	}

	return resolvedPath{
		WorkspaceRoot:    root,
		Input:            input,
		Absolute:         candidate,
		EscapesWorkspace: !isInside(root, checked),
	}, nil
}

func isInside(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
