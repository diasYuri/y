package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yuri/y/internal/gitignore"
)

// FilesystemOptions configures the built-in filesystem tools.
type FilesystemOptions struct {
	WorkspaceRoot string
	Policy        Policy
	Limits        ToolLimits
}

// RegisterFilesystem registers read_file, write_file, list_files, search, edit, and patch.
func RegisterFilesystem(r *Registry, opts FilesystemOptions) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	fs := newFilesystem(opts)
	for _, def := range []struct {
		desc    ToolDescriptor
		handler ToolHandler
	}{
		{fs.readDescriptor(), ToolHandlerFunc(fs.readFile)},
		{fs.writeDescriptor(), ToolHandlerFunc(fs.writeFile)},
		{fs.listDescriptor(), ToolHandlerFunc(fs.listFiles)},
		{fs.searchDescriptor(), ToolHandlerFunc(fs.search)},
		{fs.editDescriptor(), ToolHandlerFunc(fs.editFile)},
		{fs.patchDescriptor(), ToolHandlerFunc(fs.patchFiles)},
	} {
		if err := r.Add(def.desc, def.handler); err != nil {
			return err
		}
	}
	return nil
}

type filesystem struct {
	workspaceRoot string
	policy        Policy
	limits        ToolLimits
}

func newFilesystem(opts FilesystemOptions) *filesystem {
	return &filesystem{
		workspaceRoot: opts.WorkspaceRoot,
		policy:        opts.Policy,
		limits:        filesystemLimits(opts.Limits),
	}
}

func (fs *filesystem) workspace(req ToolRequest) string {
	if req.WorkspaceRoot != "" {
		return req.WorkspaceRoot
	}
	return fs.workspaceRoot
}

func (fs *filesystem) readDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "read_file",
		Description:  "Read a UTF-8 file from the workspace with byte and line limits.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1},"max_bytes":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemRead},
		Limits:       fs.limits,
	}
}

func (fs *filesystem) writeDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "write_file",
		Description:  "Create or overwrite a UTF-8 file in the workspace.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemWrite},
		Limits:       fs.limits,
		Sensitive:    true,
	}
}

func (fs *filesystem) listDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "list_files",
		Description:  "List directory entries in stable order with an entry limit.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemList},
		Limits:       fs.limits,
	}
}

func (fs *filesystem) searchDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "search",
		Description:  "Search workspace files for a literal string or regular expression with output limits.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"ignore_case":{"type":"boolean"},"literal":{"type":"boolean"},"limit":{"type":"integer","minimum":1},"max_bytes":{"type":"integer","minimum":1}},"required":["pattern"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemSearch},
		Limits:       fs.limits,
	}
}

func (fs *filesystem) editDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "edit",
		Description:  "Edit one file by applying exact, unique, non-overlapping text replacements.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"old_text":{"type":"string"},"new_text":{"type":"string"},"oldText":{"type":"string"},"newText":{"type":"string"}},"additionalProperties":false},"minItems":1},"old_text":{"type":"string"},"new_text":{"type":"string"},"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemWrite},
		Limits:       fs.limits,
		Sensitive:    true,
	}
}

func (fs *filesystem) patchDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "patch",
		Description:  "Apply a unified diff to workspace files after validating every hunk.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"},"diff":{"type":"string"}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemWrite},
		Limits:       fs.limits,
		Sensitive:    true,
	}
}

type readFileInput struct {
	Path     string `json:"path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

type readFileDetails struct {
	BytesRead int64 `json:"bytes_read"`
	Truncated bool  `json:"truncated"`
}

func (fs *filesystem) readFile(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input readFileInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "read_file arguments must be valid JSON", err)
	}
	path, err := resolveForRead(fs.workspace(req), input.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := authorize(ctx, fs.policy, PolicyRequest{
		ToolName:         "read_file",
		Capability:       string(CapabilityFilesystemRead),
		WorkspaceRoot:    path.WorkspaceRoot,
		Path:             input.Path,
		ResolvedPath:     path.Absolute,
		EscapesWorkspace: path.EscapesWorkspace,
	}); err != nil {
		return ToolResponse{}, err
	}

	maxRead := fs.limits.MaxFileReadBytes
	if input.MaxBytes > 0 && input.MaxBytes < maxRead {
		maxRead = input.MaxBytes
	}
	data, truncated, err := readLimitedFile(ctx, path.Absolute, maxRead)
	if err != nil {
		return ToolResponse{}, err
	}
	text := string(data)
	text = selectLines(text, input.Offset, input.Limit)
	if truncated {
		text += "\n\n[File read limit reached]"
	}
	text = limitTextBytes(text, fs.limits.MaxOutputBytes, "Tool output limit reached")
	return textResponse(text, readFileDetails{BytesRead: int64(len(data)), Truncated: truncated})
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileDetails struct {
	BytesWritten int64 `json:"bytes_written"`
}

func (fs *filesystem) writeFile(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input writeFileInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "write_file arguments must be valid JSON", err)
	}
	if int64(len(input.Content)) > fs.limits.MaxFileWriteBytes {
		return ToolResponse{}, toolError("file_too_large", "write_file content exceeds file write byte limit", ErrLimitExceeded)
	}
	path, err := resolveForWrite(fs.workspace(req), input.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := authorize(ctx, fs.policy, PolicyRequest{
		ToolName:         "write_file",
		Capability:       string(CapabilityFilesystemWrite),
		WorkspaceRoot:    path.WorkspaceRoot,
		Path:             input.Path,
		ResolvedPath:     path.Absolute,
		EscapesWorkspace: path.EscapesWorkspace,
		Sensitive:        true,
		Approval:         req.Approval,
	}); err != nil {
		return ToolResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path.Absolute), 0o755); err != nil {
		return ToolResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResponse{}, err
	}
	if err := os.WriteFile(path.Absolute, []byte(input.Content), 0o644); err != nil {
		return ToolResponse{}, err
	}
	msg := fmt.Sprintf("Successfully wrote %d bytes to %s", len(input.Content), input.Path)
	return textResponse(msg, writeFileDetails{BytesWritten: int64(len(input.Content))})
}

type listFilesInput struct {
	Path  string `json:"path,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type listFilesDetails struct {
	EntriesReturned int  `json:"entries_returned"`
	Truncated       bool `json:"truncated"`
}

func (fs *filesystem) listFiles(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input listFilesInput
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ToolResponse{}, toolError("invalid_arguments", "list_files arguments must be valid JSON", err)
		}
	}
	if input.Path == "" {
		input.Path = "."
	}
	limit := fs.limits.MaxEntries
	if input.Limit > 0 && input.Limit < limit {
		limit = input.Limit
	}
	path, err := resolveForRead(fs.workspace(req), input.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := authorize(ctx, fs.policy, PolicyRequest{
		ToolName:         "list_files",
		Capability:       string(CapabilityFilesystemList),
		WorkspaceRoot:    path.WorkspaceRoot,
		Path:             input.Path,
		ResolvedPath:     path.Absolute,
		EscapesWorkspace: path.EscapesWorkspace,
	}); err != nil {
		return ToolResponse{}, err
	}
	entries, err := os.ReadDir(path.Absolute)
	if err != nil {
		return ToolResponse{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	names := make([]string, 0, min(limit, len(entries)))
	truncated := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return ToolResponse{}, err
		}
		if len(names) >= limit {
			truncated = true
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	text := "(empty directory)"
	if len(names) > 0 {
		text = strings.Join(names, "\n")
	}
	if truncated {
		text += fmt.Sprintf("\n\n[%d entries limit reached]", limit)
	}
	text = limitTextBytes(text, fs.limits.MaxOutputBytes, "Tool output limit reached")
	return textResponse(text, listFilesDetails{EntriesReturned: len(names), Truncated: truncated})
}

type searchInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	Literal    bool   `json:"literal,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	MaxBytes   int64  `json:"max_bytes,omitempty"`
}

type searchDetails struct {
	MatchesReturned int  `json:"matches_returned"`
	MatchLimitHit   bool `json:"match_limit_hit"`
	OutputTruncated bool `json:"output_truncated"`
	LinesTruncated  bool `json:"lines_truncated"`
	FilesLimited    bool `json:"files_limited"`
}

func (fs *filesystem) search(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input searchInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "search arguments must be valid JSON", err)
	}
	if input.Pattern == "" {
		return ToolResponse{}, toolError("invalid_arguments", "search pattern is required", ErrInvalidTool)
	}
	if input.Path == "" {
		input.Path = "."
	}
	limit := fs.limits.MaxMatches
	if input.Limit > 0 && input.Limit < limit {
		limit = input.Limit
	}
	maxOutput := fs.limits.MaxOutputBytes
	if input.MaxBytes > 0 && input.MaxBytes < maxOutput {
		maxOutput = input.MaxBytes
	}
	matcher, err := newMatcher(input.Pattern, input.IgnoreCase, input.Literal)
	if err != nil {
		return ToolResponse{}, err
	}
	path, err := resolveForRead(fs.workspace(req), input.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := authorize(ctx, fs.policy, PolicyRequest{
		ToolName:         "search",
		Capability:       string(CapabilityFilesystemSearch),
		WorkspaceRoot:    path.WorkspaceRoot,
		Path:             input.Path,
		ResolvedPath:     path.Absolute,
		EscapesWorkspace: path.EscapesWorkspace,
	}); err != nil {
		return ToolResponse{}, err
	}

	info, err := os.Stat(path.Absolute)
	if err != nil {
		return ToolResponse{}, err
	}
	details := searchDetails{}
	preallocLines := limit
	if preallocLines > 64 {
		preallocLines = 64
	}
	lines := make([]string, 0, preallocLines)
	if info.IsDir() {
		ignores := gitignore.NewWalkIgnore()
		err = filepath.WalkDir(path.Absolute, func(filePath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				_ = ignores.AddDir(filePath)
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(path.Absolute, filePath)
			if err != nil {
				return nil
			}
			if ignores.Match(filePath, false) {
				return nil
			}
			if !globMatches(input.Glob, rel) {
				return nil
			}
			more, err := fs.searchFile(ctx, filePath, rel, matcher, limit, &lines, &details)
			if err != nil {
				return err
			}
			if !more {
				return errStopWalk
			}
			return nil
		})
		if errors.Is(err, errStopWalk) {
			err = nil
		}
	} else {
		rel := filepath.Base(path.Absolute)
		if globMatches(input.Glob, rel) {
			_, err = fs.searchFile(ctx, path.Absolute, rel, matcher, limit, &lines, &details)
		}
	}
	if err != nil {
		return ToolResponse{}, err
	}
	text := "No matches found"
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	if details.MatchLimitHit {
		text += fmt.Sprintf("\n\n[%d matches limit reached]", limit)
	}
	limited := limitTextBytes(text, maxOutput, "Tool output limit reached")
	details.OutputTruncated = limited != text
	return textResponse(limited, details)
}

var errStopWalk = errors.New("stop walking")

// searchReaderPool reuses bufio.Reader buffers across files in a single search call
// and across concurrent searches. The buffer size is tuned for typical text files.
var searchReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, 32*1024)
	},
}

// searchFormatPool reuses small []byte buffers for formatting matched lines.
var searchFormatPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 256)
		return &buf
	},
}

func (fs *filesystem) searchFile(ctx context.Context, path, displayPath string, matcher lineMatcher, limit int, output *[]string, details *searchDetails) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return true, nil
	}
	defer f.Close()

	reader := searchReaderPool.Get().(*bufio.Reader)
	reader.Reset(io.LimitReader(f, fs.limits.MaxFileReadBytes+1))
	defer func() {
		reader.Reset(nil)
		searchReaderPool.Put(reader)
	}()

	formatBufPtr := searchFormatPool.Get().(*[]byte)
	formatBuf := *formatBufPtr
	defer func() {
		// Write back any grown capacity so the pool keeps the larger backing
		// array for the next caller; reset length so the next user starts empty.
		*formatBufPtr = formatBuf[:0]
		searchFormatPool.Put(formatBufPtr)
	}()

	displaySlash := filepath.ToSlash(displayPath)
	var bytesRead int64
	lineNo := 0
	var carry []byte
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		segment, readErr := reader.ReadSlice('\n')
		bytesRead += int64(len(segment))
		if bytesRead > fs.limits.MaxFileReadBytes {
			details.FilesLimited = true
			return true, nil
		}
		var line []byte
		if errors.Is(readErr, bufio.ErrBufferFull) {
			carry = append(carry, segment...)
			continue
		}
		if len(carry) > 0 {
			carry = append(carry, segment...)
			line = carry
			carry = carry[:0]
		} else {
			line = segment
		}
		if len(line) > 0 {
			lineNo++
			line = trimTrailingNewline(line)
			if bytes.IndexByte(line, 0) >= 0 {
				if errors.Is(readErr, io.EOF) {
					return true, nil
				}
				if readErr != nil {
					return true, nil
				}
				continue
			}
			if matcher.MatchBytes(line) {
				if len(*output) >= limit {
					details.MatchLimitHit = true
					return false, nil
				}
				lineStr := string(line)
				truncatedLine := limitTextBytes(lineStr, fs.limits.MaxLineBytes, "line truncated")
				if truncatedLine != lineStr {
					details.LinesTruncated = true
				}
				formatBuf = formatBuf[:0]
				formatBuf = append(formatBuf, displaySlash...)
				formatBuf = append(formatBuf, ':')
				formatBuf = strconv.AppendInt(formatBuf, int64(lineNo), 10)
				formatBuf = append(formatBuf, ':', ' ')
				formatBuf = append(formatBuf, truncatedLine...)
				*output = append(*output, string(formatBuf))
				details.MatchesReturned++
			}
		}
		if errors.Is(readErr, io.EOF) {
			return true, nil
		}
		if readErr != nil {
			return true, nil
		}
	}
}

// trimTrailingNewline strips a trailing \n and optional preceding \r in-place.
func trimTrailingNewline(line []byte) []byte {
	end := len(line)
	if end > 0 && line[end-1] == '\n' {
		end--
		if end > 0 && line[end-1] == '\r' {
			end--
		}
	}
	return line[:end]
}

type lineMatcher interface {
	Match(line string) bool
	MatchBytes(line []byte) bool
}

type literalMatcher struct {
	pattern    string
	lowered    string
	patternB   []byte
	loweredB   []byte
	ignoreCase bool
	asciiOnly  bool
}

func (m literalMatcher) Match(line string) bool {
	if !m.ignoreCase {
		return strings.Contains(line, m.pattern)
	}
	if m.asciiOnly {
		return containsFoldASCIIString(line, m.lowered)
	}
	return strings.Contains(strings.ToLower(line), m.lowered)
}

func (m literalMatcher) MatchBytes(line []byte) bool {
	if !m.ignoreCase {
		return bytes.Contains(line, m.patternB)
	}
	if m.asciiOnly {
		return containsFoldASCIIBytes(line, m.loweredB)
	}
	return bytes.Contains(bytes.ToLower(line), m.loweredB)
}

type regexpMatcher struct {
	re *regexp.Regexp
}

func (m regexpMatcher) Match(line string) bool {
	return m.re.MatchString(line)
}

func (m regexpMatcher) MatchBytes(line []byte) bool {
	return m.re.Match(line)
}

func newMatcher(pattern string, ignoreCase, literal bool) (lineMatcher, error) {
	if literal {
		lowered := strings.ToLower(pattern)
		return literalMatcher{
			pattern:    pattern,
			lowered:    lowered,
			patternB:   []byte(pattern),
			loweredB:   []byte(lowered),
			ignoreCase: ignoreCase,
			asciiOnly:  isASCII(pattern),
		}, nil
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, toolError("invalid_pattern", "search pattern is not a valid regular expression", err)
	}
	return regexpMatcher{re: re}, nil
}

// isASCII reports whether s contains only bytes < 0x80.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// containsFoldASCIIBytes performs a case-insensitive search for an ASCII needle
// inside an arbitrary haystack. The needle must be lowercase ASCII; the
// haystack may contain any bytes.
func containsFoldASCIIBytes(s, lowerNeedle []byte) bool {
	n := len(lowerNeedle)
	if n == 0 {
		return true
	}
	if len(s) < n {
		return false
	}
	first := lowerNeedle[0]
	firstUpper := first
	if first >= 'a' && first <= 'z' {
		firstUpper = first - 32
	}
	end := len(s) - n
	for i := 0; i <= end; i++ {
		b := s[i]
		if b != first && b != firstUpper {
			continue
		}
		match := true
		for j := 1; j < n; j++ {
			lb := lowerNeedle[j]
			sb := s[i+j]
			if sb == lb {
				continue
			}
			if lb >= 'a' && lb <= 'z' && sb == lb-32 {
				continue
			}
			match = false
			break
		}
		if match {
			return true
		}
	}
	return false
}

// containsFoldASCIIString is the string-typed twin of containsFoldASCIIBytes.
func containsFoldASCIIString(s, lowerNeedle string) bool {
	n := len(lowerNeedle)
	if n == 0 {
		return true
	}
	if len(s) < n {
		return false
	}
	first := lowerNeedle[0]
	firstUpper := first
	if first >= 'a' && first <= 'z' {
		firstUpper = first - 32
	}
	end := len(s) - n
	for i := 0; i <= end; i++ {
		b := s[i]
		if b != first && b != firstUpper {
			continue
		}
		match := true
		for j := 1; j < n; j++ {
			lb := lowerNeedle[j]
			sb := s[i+j]
			if sb == lb {
				continue
			}
			if lb >= 'a' && lb <= 'z' && sb == lb-32 {
				continue
			}
			match = false
			break
		}
		if match {
			return true
		}
	}
	return false
}

func globMatches(glob, rel string) bool {
	if glob == "" {
		return true
	}
	rel = filepath.ToSlash(rel)
	if ok, err := filepath.Match(glob, rel); err == nil && ok {
		return true
	}
	if ok, err := filepath.Match(glob, filepath.Base(rel)); err == nil && ok {
		return true
	}
	return false
}

func readLimitedFile(ctx context.Context, path string, maxBytes int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	data := buf.Bytes()
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func selectLines(text string, offset, limit int) string {
	if offset <= 0 && limit <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}
