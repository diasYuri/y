package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const gitCommitFormat = "%H%x1f%an%x1f%aI%x1f%s"

type gitShowInput struct {
	CWD            string `json:"cwd,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Path           string `json:"path,omitempty"`
	IncludePatch   bool   `json:"include_patch,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitHistoryInput struct {
	CWD            string `json:"cwd,omitempty"`
	Path           string `json:"path,omitempty"`
	Line           int    `json:"line,omitempty"`
	MaxCommits     int    `json:"max_commits,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitBlameInput struct {
	CWD            string `json:"cwd,omitempty"`
	Path           string `json:"path"`
	StartLine      int    `json:"start_line,omitempty"`
	EndLine        int    `json:"end_line,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitSearchHistoryInput struct {
	CWD            string `json:"cwd,omitempty"`
	Query          string `json:"query"`
	Path           string `json:"path,omitempty"`
	Mode           string `json:"mode,omitempty"`
	MaxCommits     int    `json:"max_commits,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitCommitInfo struct {
	Commit  string   `json:"commit"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Files   []string `json:"files,omitempty"`
}

type gitHistoryResult struct {
	Commits   []gitCommitInfo `json:"commits"`
	Truncated bool            `json:"truncated"`
}

type gitShowResult struct {
	CommitInfo gitCommitInfo `json:"commit"`
	Patch      string        `json:"patch,omitempty"`
	Truncated  bool          `json:"truncated"`
}

type gitBlameLine struct {
	Line    int    `json:"line"`
	Commit  string `json:"commit"`
	Author  string `json:"author,omitempty"`
	Date    string `json:"date,omitempty"`
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text"`
}

type gitBlameResult struct {
	Path      string         `json:"path"`
	Lines     []gitBlameLine `json:"lines"`
	Truncated bool           `json:"truncated"`
}

func (t *gitTool) showDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "git_show", Description: "Inspect a commit's metadata and changed files. Include a patch only when source details are needed; output is limited.", InputSchema: json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"commit":{"type":"string"},"path":{"type":"string"},"include_patch":{"type":"boolean"},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`), Capabilities: []Capability{CapabilityGitRead}, Limits: t.limits}
}

func (t *gitTool) historyDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "git_history", Description: "Inspect the history of a file or source line. Use this when current code does not explain a decision, workaround, or unusual behavior.", InputSchema: json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"path":{"type":"string"},"line":{"type":"integer","minimum":1},"max_commits":{"type":"integer","minimum":1,"maximum":100},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`), Capabilities: []Capability{CapabilityGitRead}, Limits: t.limits}
}

func (t *gitTool) blameDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "git_blame", Description: "Identify the commits associated with selected source lines. Use it as an entry point into history when investigating why specific code exists.", InputSchema: json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1},"max_output_bytes":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityGitRead}, Limits: t.limits}
}

func (t *gitTool) searchHistoryDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "git_search_history", Description: "Search commit history for text that was added or removed. Use mode=string for git log -S exact occurrence changes or mode=regex for git log -G matching diff lines.", InputSchema: json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"query":{"type":"string"},"path":{"type":"string"},"mode":{"type":"string","enum":["string","regex"]},"max_commits":{"type":"integer","minimum":1,"maximum":100},"max_output_bytes":{"type":"integer","minimum":1}},"required":["query"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityGitRead}, Limits: t.limits}
}

func (t *gitTool) show(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	var in gitShowInput
	if err := decodeGitInput(req, t.limits, &in); err != nil {
		return ToolResponse{}, err
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, in.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	commit := strings.TrimSpace(in.Commit)
	if commit == "" {
		commit = "HEAD"
	}
	if strings.ContainsAny(commit, "\x00\n\r") {
		return ToolResponse{}, toolError("invalid_arguments", "commit must be a single revision", ErrInvalidTool)
	}
	path, err := validateGitPath(cwd, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	maxBytes := t.outputLimit(in.MaxOutputBytes)
	meta, err := t.gitData(ctx, cwd, in.MaxOutputBytes, []string{"show", "-s", "--format=" + gitCommitFormat, "--end-of-options", commit}...)
	if err != nil {
		return ToolResponse{}, err
	}
	info, err := parseGitCommitLine(strings.TrimSpace(meta.Stdout))
	if err != nil {
		return ToolResponse{}, toolError("git_invalid_response", "git returned invalid commit metadata", err)
	}
	files, err := t.gitData(ctx, cwd, in.MaxOutputBytes, gitNameOnlyArgs(commit, path)...)
	if err != nil {
		return ToolResponse{}, err
	}
	info.Files = cleanGitFiles(files.Stdout)
	out := gitShowResult{CommitInfo: info, Truncated: meta.StdoutTruncated || files.StdoutTruncated}
	combined := commandResult{ExitCode: files.ExitCode, DurationMs: meta.DurationMs + files.DurationMs, StdoutBytes: meta.StdoutBytes + files.StdoutBytes, StderrBytes: meta.StderrBytes + files.StderrBytes, StdoutTruncated: out.Truncated}
	if in.IncludePatch {
		args := []string{"show", "--no-ext-diff", "--format=", "--end-of-options", commit}
		if path != "" {
			args = append(args, "--", path)
		}
		patch, err := t.gitData(ctx, cwd, in.MaxOutputBytes, args...)
		if err != nil {
			return ToolResponse{}, err
		}
		out.Patch = patch.Stdout
		out.Truncated = out.Truncated || patch.StdoutTruncated
		combined.ExitCode = patch.ExitCode
		combined.DurationMs += patch.DurationMs
		combined.StdoutBytes += patch.StdoutBytes
		combined.StderrBytes += patch.StderrBytes
		combined.StdoutTruncated = out.Truncated
	}
	return gitInvestigationResponse(out, maxBytes, "git_show", cwd, combined, len(info.Files), out.Truncated)
}

func gitNameOnlyArgs(commit, path string) []string {
	args := []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--end-of-options", commit}
	if path != "" {
		args = append(args, "--", path)
	}
	return args
}

func (t *gitTool) history(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	var in gitHistoryInput
	if err := decodeGitInput(req, t.limits, &in); err != nil {
		return ToolResponse{}, err
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, in.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	path, err := validateGitPath(cwd, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	maxCommits := t.commitLimit(in.MaxCommits)
	args := []string{"log", fmt.Sprintf("-%d", min(maxCommits+1, 101)), "--format=" + gitCommitFormat, "--name-only", "--no-ext-diff", "--follow"}
	if in.Line > 0 {
		if path == "" {
			return ToolResponse{}, toolError("invalid_arguments", "git_history line requires a path", ErrInvalidTool)
		}
		args = []string{"log", fmt.Sprintf("-%d", min(maxCommits+1, 101)), "--format=" + gitCommitFormat, "-L", fmt.Sprintf("%d,%d:%s", in.Line, in.Line, path), "--no-ext-diff"}
	} else if path != "" {
		args = append(args, "--", path)
	}
	result, err := t.gitData(ctx, cwd, in.MaxOutputBytes, args...)
	if err != nil {
		return ToolResponse{}, err
	}
	commits := parseGitLog(result.Stdout, in.Line == 0)
	truncated := result.StdoutTruncated || len(commits) > maxCommits
	if len(commits) > maxCommits {
		commits = commits[:maxCommits]
	}
	out := gitHistoryResult{Commits: commits, Truncated: truncated}
	return gitInvestigationResponse(out, t.outputLimit(in.MaxOutputBytes), "git_history", cwd, result, len(out.Commits), out.Truncated)
}

func (t *gitTool) blame(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	var in gitBlameInput
	if err := decodeGitInput(req, t.limits, &in); err != nil {
		return ToolResponse{}, err
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, in.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	path, err := validateGitPath(cwd, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if path == "" {
		return ToolResponse{}, toolError("invalid_arguments", "git_blame path is required", ErrInvalidTool)
	}
	start, end := in.StartLine, in.EndLine
	if start == 0 {
		start = 1
	}
	if end == 0 {
		end = start + t.limits.MaxMatches - 1
	}
	maxLines := t.limits.MaxMatches
	if end-start+1 > maxLines {
		end = start + maxLines - 1
	}
	if end < start {
		return ToolResponse{}, toolError("invalid_arguments", "end_line must be greater than or equal to start_line", ErrInvalidTool)
	}
	args := []string{"blame", "--line-porcelain", "-L", fmt.Sprintf("%d,%d", start, end), "--", path}
	result, err := t.gitData(ctx, cwd, in.MaxOutputBytes, args...)
	if err != nil {
		return ToolResponse{}, err
	}
	lines := parseGitBlame(result.Stdout, t.limits.MaxMatches)
	out := gitBlameResult{Path: path, Lines: lines, Truncated: result.StdoutTruncated || len(lines) < end-start+1}
	return gitInvestigationResponse(out, t.outputLimit(in.MaxOutputBytes), "git_blame", cwd, result, len(out.Lines), out.Truncated)
}

func (t *gitTool) searchHistory(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	var in gitSearchHistoryInput
	if err := decodeGitInput(req, t.limits, &in); err != nil {
		return ToolResponse{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "git_search_history query is required", ErrInvalidTool)
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = "string"
	}
	if mode != "string" && mode != "regex" {
		return ToolResponse{}, toolError("invalid_arguments", "mode must be string or regex", ErrInvalidTool)
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, in.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	path, err := validateGitPath(cwd, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	maxCommits := t.commitLimit(in.MaxCommits)
	args := []string{"log", fmt.Sprintf("-%d", min(maxCommits+1, 101)), "--format=" + gitCommitFormat, "--name-only", "--no-ext-diff"}
	if mode == "string" {
		args = append(args, "-S", in.Query)
	} else {
		args = append(args, "-G", in.Query)
	}
	if path != "" {
		args = append(args, "--", path)
	}
	result, err := t.gitData(ctx, cwd, in.MaxOutputBytes, args...)
	if err != nil {
		return ToolResponse{}, err
	}
	commits := parseGitLog(result.Stdout, true)
	truncated := result.StdoutTruncated || len(commits) > maxCommits
	if len(commits) > maxCommits {
		commits = commits[:maxCommits]
	}
	out := gitHistoryResult{Commits: commits, Truncated: truncated}
	return gitInvestigationResponse(out, t.outputLimit(in.MaxOutputBytes), "git_search_history", cwd, result, len(out.Commits), out.Truncated)
}

func decodeGitInput(req ToolRequest, limits ToolLimits, dst any) error {
	if err := enforceInputLimit(req.Arguments, limits); err != nil {
		return err
	}
	if len(req.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Arguments, dst); err != nil {
		return toolError("invalid_arguments", req.Name+" arguments must be valid JSON", err)
	}
	return nil
}

func validateGitPath(root, input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", nil
	}
	if filepath.IsAbs(input) {
		return "", toolError("path_outside_workspace", "git path must be relative to the workspace", ErrInvalidTool)
	}
	clean := filepath.Clean(input)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", toolError("path_outside_workspace", "git path escapes the workspace", ErrInvalidTool)
	}
	return filepath.ToSlash(clean), nil
}

func validateGitPaths(root string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	validated := make([]string, 0, len(paths))
	for _, path := range paths {
		clean, err := validateGitPath(root, path)
		if err != nil {
			return nil, err
		}
		if clean == "" {
			return nil, toolError("invalid_arguments", "git path cannot be empty", ErrInvalidTool)
		}
		validated = append(validated, clean)
	}
	return validated, nil
}

func (t *gitTool) gitData(ctx context.Context, cwd string, maxOutputBytes int64, args ...string) (commandResult, error) {
	result, err := t.runGit(ctx, cwd, args, maxOutputBytes, nil, false, CapabilityGitRead)
	if err != nil {
		return commandResult{}, err
	}
	if result.TimedOut {
		return commandResult{}, toolError("timeout", "git investigation command timed out", context.DeadlineExceeded)
	}
	if result.ExitCode != 0 && len(args) > 0 && args[0] == "log" && strings.Contains(result.Stderr, "does not have any commits") {
		return commandResult{}, nil
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout, "git command failed"))
		return commandResult{}, toolError("git_failed", message, ErrInvalidTool)
	}
	return result, nil
}

func parseGitCommitLine(line string) (gitCommitInfo, error) {
	parts := strings.SplitN(line, "\x1f", 4)
	if len(parts) != 4 || parts[0] == "" {
		return gitCommitInfo{}, fmt.Errorf("expected commit, author, date, and subject fields")
	}
	return gitCommitInfo{Commit: parts[0], Author: parts[1], Date: parts[2], Subject: parts[3]}, nil
}

func parseGitLog(output string, includeFiles bool) []gitCommitInfo {
	commits := make([]gitCommitInfo, 0)
	var current *gitCommitInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.Contains(line, "\x1f") {
			if info, err := parseGitCommitLine(strings.TrimSpace(line)); err == nil {
				commits = append(commits, info)
				current = &commits[len(commits)-1]
				continue
			}
		}
		if includeFiles && current != nil && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "diff --") && !strings.HasPrefix(line, "@@") {
			file := strings.TrimSpace(line)
			current.Files = append(current.Files, filepath.ToSlash(file))
		}
	}
	return commits
}

func cleanGitFiles(output string) []string {
	files := make([]string, 0)
	for _, file := range strings.Split(output, "\n") {
		file = strings.TrimSpace(file)
		if file != "" {
			files = append(files, filepath.ToSlash(file))
		}
	}
	return files
}

func parseGitBlame(output string, maxLines int) []gitBlameLine {
	lines := make([]gitBlameLine, 0)
	var current *gitBlameLine
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "\t") {
			if current != nil {
				current.Text = strings.TrimPrefix(line, "\t")
				lines = append(lines, *current)
				current = nil
				if len(lines) >= maxLines {
					break
				}
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && len(strings.TrimPrefix(fields[0], "^")) >= 40 {
			lineNo, _ := strconv.Atoi(fields[2])
			current = &gitBlameLine{Line: lineNo, Commit: strings.TrimPrefix(fields[0], "^")}
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "author":
			current.Author = value
		case "author-time":
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
				current.Date = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
			}
		case "summary":
			current.Subject = value
		}
	}
	return lines
}

func (t *gitTool) outputLimit(requested int64) int64 {
	if requested > 0 && requested < t.limits.MaxOutputBytes {
		return requested
	}
	return t.limits.MaxOutputBytes
}

func (t *gitTool) commitLimit(requested int) int {
	limit := t.limits.MaxMatches
	if limit > 100 {
		limit = 100
	}
	if requested > 0 && requested < limit {
		limit = requested
	}
	return limit
}

func gitJSONResponse(value any, maxBytes int64) (ToolResponse, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return ToolResponse{}, err
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		return ToolResponse{}, toolError("output_limit", "git result exceeds the output limit; narrow the path, range, or commit limit", ErrLimitExceeded)
	}
	return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: string(raw)}}, Details: raw}, nil
}

func gitInvestigationResponse(value any, maxBytes int64, tool, workspace string, result commandResult, count int, truncated bool) (ToolResponse, error) {
	response, err := gitJSONResponse(value, maxBytes)
	if err != nil {
		return ToolResponse{}, err
	}
	response.Metadata, _ = json.Marshal(map[string]any{"tool": tool, "workspace": workspace, "duration_ms": result.DurationMs, "exit_status": result.ExitCode, "result_count": count, "truncated": truncated || result.StdoutTruncated || result.StderrTruncated, "timeout": result.TimedOut, "cache_hit": false})
	return response, nil
}
