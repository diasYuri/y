// Package gitignore implements .gitignore pattern matching for the search tool.
// It supports glob patterns, negation (!), directory-only matches (/ suffix),
// and the double-star (**) wildcard.
package gitignore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Matcher holds compiled patterns for a single directory and can be stacked
// with parent matchers to form the full ignore hierarchy.
type Matcher struct {
	patterns []pattern
}

type pattern struct {
	raw      string
	negate   bool
	dirOnly  bool
	anchored bool // pattern starts with /
	globStar bool // contains **
	segments []string
}

// Compile reads a .gitignore file and returns a Matcher.
func Compile(path string) (*Matcher, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Matcher{}, nil
		}
		return nil, err
	}
	defer f.Close()

	m := &Matcher{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := parsePattern(line)
		if err != nil {
			continue // skip malformed patterns
		}
		m.patterns = append(m.patterns, p)
	}
	return m, scanner.Err()
}

// Match reports whether the given path (relative to the directory that owns
// this matcher) is ignored. dir is true when the path is a directory.
func (m *Matcher) Match(path string, dir bool) bool {
	ignored := false
	for _, p := range m.patterns {
		if p.match(path, dir) {
			ignored = !p.negate
		}
	}
	return ignored
}

// Empty reports whether the matcher has no patterns.
func (m *Matcher) Empty() bool {
	return len(m.patterns) == 0
}

func parsePattern(line string) (pattern, error) {
	p := pattern{raw: line}

	// Handle negation.
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}

	// Handle trailing slash (directory-only).
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = line[:len(line)-1]
	}

	// Handle leading slash (anchored to directory root).
	if strings.HasPrefix(line, "/") {
		p.anchored = true
		line = line[1:]
	}

	// Handle escaped spaces and other escapes.
	line = unescapePattern(line)

	if line == "" {
		return pattern{}, fmt.Errorf("empty pattern")
	}

	p.globStar = strings.Contains(line, "**")
	p.segments = splitPattern(line)
	return p, nil
}

func (p pattern) match(path string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}

	// Normalize path separators.
	path = filepath.ToSlash(path)

	if p.globStar {
		return matchGlobStar(p.segments, path, p.anchored)
	}

	if p.anchored {
		return matchAnchored(p.segments, path)
	}

	// Unanchored: pattern can match any path component.
	return matchUnanchored(p.segments, path)
}

func matchAnchored(segments []string, path string) bool {
	pathSegments := strings.Split(path, "/")
	return matchSegments(segments, pathSegments)
}

func matchUnanchored(segments []string, path string) bool {
	pathSegments := strings.Split(path, "/")
	// Try matching at every possible starting position.
	for start := 0; start <= len(pathSegments)-len(segments); start++ {
		if matchSegments(segments, pathSegments[start:]) {
			return true
		}
	}
	return false
}

func matchGlobStar(segments []string, path string, anchored bool) bool {
	pathSegments := strings.Split(path, "/")

	if anchored {
		return matchGlobStarSegments(segments, pathSegments)
	}

	// Unanchored with **: try every starting position.
	for start := 0; start <= len(pathSegments); start++ {
		if matchGlobStarSegments(segments, pathSegments[start:]) {
			return true
		}
	}
	return false
}

func matchGlobStarSegments(patternSegs, pathSegs []string) bool {
	// Dynamic programming approach for ** matching.
	// dp[i][j] = can match pattern[0:i] against path[0:j]
	m, n := len(patternSegs), len(pathSegs)
	if m == 0 {
		return n == 0
	}

	// Use 1D DP to save space.
	prev := make([]bool, n+1)
	curr := make([]bool, n+1)

	// Empty pattern matches empty path.
	prev[0] = true

	for i := 1; i <= m; i++ {
		pseg := patternSegs[i-1]
		isGlobStar := pseg == "**"

		curr[0] = prev[0] && isGlobStar

		for j := 1; j <= n; j++ {
			if isGlobStar {
				// ** matches zero or more segments.
				curr[j] = curr[j-1] || prev[j] || prev[j-1]
			} else {
				curr[j] = prev[j-1] && matchSegment(pseg, pathSegs[j-1])
			}
		}

		prev, curr = curr, make([]bool, n+1)
	}

	return prev[n]
}

func matchSegments(patternSegs, pathSegs []string) bool {
	if len(patternSegs) > len(pathSegs) {
		return false
	}
	for i := range patternSegs {
		if !matchSegment(patternSegs[i], pathSegs[i]) {
			return false
		}
	}
	return true
}

func matchSegment(pattern, segment string) bool {
	// Fast path: exact match.
	if pattern == segment {
		return true
	}
	// Fast path: single * wildcard.
	if pattern == "*" {
		return true
	}
	// General glob matching.
	ok, _ := filepath.Match(pattern, segment)
	return ok
}

func splitPattern(pattern string) []string {
	// filepath.SplitList uses OS separator; we normalized to /.
	return strings.Split(pattern, "/")
}

func unescapePattern(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	escaped := false
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// WalkIgnore is a helper that collects all .gitignore matchers along a path
// and can test if a given relative path is ignored.
type WalkIgnore struct {
	matchers []dirMatcher
}

type dirMatcher struct {
	dir     string
	matcher *Matcher
}

// NewWalkIgnore creates an empty WalkIgnore.
func NewWalkIgnore() *WalkIgnore {
	return &WalkIgnore{}
}

// AddDir registers a directory's .gitignore. dirs should be added in
// root-to-leaf order.
func (w *WalkIgnore) AddDir(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	m, err := Compile(path)
	if err != nil {
		return err
	}
	if !m.Empty() {
		w.matchers = append(w.matchers, dirMatcher{dir: dir, matcher: m})
	}
	return nil
}

// Match tests whether absPath is ignored by any applicable .gitignore.
// isDir is true when absPath is a directory.
func (w *WalkIgnore) Match(absPath string, isDir bool) bool {
	for _, dm := range w.matchers {
		rel, err := filepath.Rel(dm.dir, absPath)
		if err != nil {
			continue
		}
		if strings.HasPrefix(rel, "..") {
			continue
		}
		if dm.matcher.Match(rel, isDir) {
			return true
		}
	}
	return false
}
