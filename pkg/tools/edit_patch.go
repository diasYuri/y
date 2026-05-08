package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type editInput struct {
	Path         string     `json:"path"`
	Edits        []textEdit `json:"edits,omitempty"`
	OldText      string     `json:"old_text,omitempty"`
	NewText      string     `json:"new_text,omitempty"`
	OldTextCamel string     `json:"oldText,omitempty"`
	NewTextCamel string     `json:"newText,omitempty"`
}

type textEdit struct {
	OldText      string `json:"old_text"`
	NewText      string `json:"new_text"`
	OldTextCamel string `json:"oldText,omitempty"`
	NewTextCamel string `json:"newText,omitempty"`
}

type editDetails struct {
	Path         string `json:"path"`
	EditsApplied int    `json:"edits_applied"`
	BytesWritten int64  `json:"bytes_written"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
}

func (fs *filesystem) editFile(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input editInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "edit arguments must be valid JSON", err)
	}
	for i := range input.Edits {
		input.Edits[i] = normalizeTextEdit(input.Edits[i])
	}
	if input.OldText == "" {
		input.OldText = input.OldTextCamel
		input.NewText = input.NewTextCamel
	}
	if len(input.Edits) == 0 && input.OldText != "" {
		input.Edits = []textEdit{{OldText: input.OldText, NewText: input.NewText}}
	}
	if len(input.Edits) == 0 {
		return ToolResponse{}, toolError("invalid_arguments", "edit requires at least one replacement", ErrInvalidTool)
	}
	path, err := resolveForWrite(fs.workspace(req), input.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := fs.authorizeMutation(ctx, req, "edit", input.Path, path); err != nil {
		return ToolResponse{}, err
	}
	original, err := fs.readEditableFile(ctx, path.Absolute)
	if err != nil {
		return ToolResponse{}, err
	}
	updated, err := applyTextEdits(input.Path, string(original), input.Edits)
	if err != nil {
		return ToolResponse{}, err
	}
	if int64(len(updated)) > fs.limits.MaxFileWriteBytes {
		return ToolResponse{}, toolError("file_too_large", "edit result exceeds file write byte limit", ErrLimitExceeded)
	}
	if err := ctx.Err(); err != nil {
		return ToolResponse{}, err
	}
	if err := os.WriteFile(path.Absolute, []byte(updated), 0o644); err != nil {
		return ToolResponse{}, err
	}
	details := editDetails{
		Path:         input.Path,
		EditsApplied: len(input.Edits),
		BytesWritten: int64(len(updated)),
		BeforeSHA256: sha256Hex(original),
		AfterSHA256:  sha256Hex([]byte(updated)),
	}
	text := fmt.Sprintf("Successfully applied %d edit(s) to %s\n\n%s", len(input.Edits), input.Path, unifiedDiff(input.Path, string(original), updated))
	text = limitTextBytes(text, fs.limits.MaxOutputBytes, "Tool output limit reached")
	return textResponse(text, details)
}

func normalizeTextEdit(edit textEdit) textEdit {
	if edit.OldText == "" {
		edit.OldText = edit.OldTextCamel
		edit.NewText = edit.NewTextCamel
	}
	return edit
}

type patchInput struct {
	Patch string `json:"patch,omitempty"`
	Diff  string `json:"diff,omitempty"`
}

type patchDetails struct {
	FilesChanged []editDetails `json:"files_changed"`
}

func (fs *filesystem) patchFiles(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, fs.limits); err != nil {
		return ToolResponse{}, err
	}
	var input patchInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "patch arguments must be valid JSON", err)
	}
	diff := input.Patch
	if diff == "" {
		diff = input.Diff
	}
	if strings.TrimSpace(diff) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "patch requires a unified diff", ErrInvalidTool)
	}
	files, err := parseUnifiedPatch(diff)
	if err != nil {
		return ToolResponse{}, err
	}

	type pendingWrite struct {
		inputPath string
		resolved  resolvedPath
		original  []byte
		updated   string
	}
	pending := make([]pendingWrite, 0, len(files))
	for _, file := range files {
		path, err := resolveForWrite(fs.workspace(req), file.Path)
		if err != nil {
			return ToolResponse{}, err
		}
		if err := fs.authorizeMutation(ctx, req, "patch", file.Path, path); err != nil {
			return ToolResponse{}, err
		}
		original, err := fs.readEditableFile(ctx, path.Absolute)
		if err != nil {
			return ToolResponse{}, err
		}
		updated, err := applyPatchFile(file.Path, string(original), file.Hunks)
		if err != nil {
			return ToolResponse{}, err
		}
		if int64(len(updated)) > fs.limits.MaxFileWriteBytes {
			return ToolResponse{}, toolError("file_too_large", fmt.Sprintf("patch result for %s exceeds file write byte limit", file.Path), ErrLimitExceeded)
		}
		pending = append(pending, pendingWrite{inputPath: file.Path, resolved: path, original: original, updated: updated})
	}

	details := patchDetails{FilesChanged: make([]editDetails, 0, len(pending))}
	var diffs []string
	for _, write := range pending {
		if err := ctx.Err(); err != nil {
			return ToolResponse{}, err
		}
		if err := os.WriteFile(write.resolved.Absolute, []byte(write.updated), 0o644); err != nil {
			return ToolResponse{}, err
		}
		details.FilesChanged = append(details.FilesChanged, editDetails{
			Path:         write.inputPath,
			EditsApplied: 1,
			BytesWritten: int64(len(write.updated)),
			BeforeSHA256: sha256Hex(write.original),
			AfterSHA256:  sha256Hex([]byte(write.updated)),
		})
		diffs = append(diffs, unifiedDiff(write.inputPath, string(write.original), write.updated))
	}
	text := fmt.Sprintf("Successfully patched %d file(s)\n\n%s", len(pending), strings.Join(diffs, "\n"))
	text = limitTextBytes(text, fs.limits.MaxOutputBytes, "Tool output limit reached")
	return textResponse(text, details)
}

func (fs *filesystem) authorizeMutation(ctx context.Context, req ToolRequest, toolName, inputPath string, path resolvedPath) error {
	return authorize(ctx, fs.policy, PolicyRequest{
		ToolName:         toolName,
		Capability:       string(CapabilityFilesystemWrite),
		WorkspaceRoot:    path.WorkspaceRoot,
		Path:             inputPath,
		ResolvedPath:     path.Absolute,
		EscapesWorkspace: path.EscapesWorkspace,
		Sensitive:        true,
		Approval:         req.Approval,
	})
}

func (fs *filesystem) readEditableFile(ctx context.Context, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > fs.limits.MaxFileReadBytes {
		return nil, toolError("file_too_large", "file exceeds edit read byte limit", ErrLimitExceeded)
	}
	data, truncated, err := readLimitedFile(ctx, path, fs.limits.MaxFileReadBytes)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, toolError("file_too_large", "file exceeds edit read byte limit", ErrLimitExceeded)
	}
	return data, nil
}

type editMatch struct {
	index  int
	length int
	edit   textEdit
}

func applyTextEdits(path, original string, edits []textEdit) (string, error) {
	matches := make([]editMatch, 0, len(edits))
	for i, edit := range edits {
		if edit.OldText == "" {
			if original != "" {
				return "", toolError("invalid_edit", fmt.Sprintf("edits[%d].old_text must not be empty in %s", i, path), ErrInvalidTool)
			}
			matches = append(matches, editMatch{index: 0, length: 0, edit: edit})
			continue
		}
		count := strings.Count(original, edit.OldText)
		if count == 0 {
			return "", toolError("edit_not_found", fmt.Sprintf("could not find edits[%d].old_text in %s", i, path), ErrInvalidTool)
		}
		if count > 1 {
			return "", toolError("edit_not_unique", fmt.Sprintf("found %d occurrences of edits[%d].old_text in %s", count, i, path), ErrInvalidTool)
		}
		matches = append(matches, editMatch{index: strings.Index(original, edit.OldText), length: len(edit.OldText), edit: edit})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].index < matches[j].index })
	for i := 1; i < len(matches); i++ {
		if matches[i-1].index+matches[i-1].length > matches[i].index {
			return "", toolError("edit_overlap", fmt.Sprintf("edits overlap in %s", path), ErrInvalidTool)
		}
	}
	updated := original
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		updated = updated[:match.index] + match.edit.NewText + updated[match.index+match.length:]
	}
	if updated == original {
		return "", toolError("edit_no_change", fmt.Sprintf("edit would not change %s", path), ErrInvalidTool)
	}
	return updated, nil
}

type patchFile struct {
	Path  string
	Hunks []patchHunk
}

type patchHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []patchLine
}

type patchLine struct {
	Kind byte
	Text string
}

func parseUnifiedPatch(diff string) ([]patchFile, error) {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	var files []patchFile
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "--- ") {
			i++
			continue
		}
		oldPath := strings.TrimSpace(strings.TrimPrefix(lines[i], "--- "))
		i++
		if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
			return nil, toolError("invalid_patch", "unified diff missing +++ file header", ErrInvalidTool)
		}
		newPath := strings.TrimSpace(strings.TrimPrefix(lines[i], "+++ "))
		path, err := patchTargetPath(oldPath, newPath)
		if err != nil {
			return nil, err
		}
		file := patchFile{Path: path}
		i++
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			hunk, next, err := parsePatchHunk(lines, i)
			if err != nil {
				return nil, err
			}
			file.Hunks = append(file.Hunks, hunk)
			i = next
		}
		if len(file.Hunks) == 0 {
			return nil, toolError("invalid_patch", fmt.Sprintf("patch for %s has no hunks", path), ErrInvalidTool)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, toolError("invalid_patch", "unified diff contains no file patches", ErrInvalidTool)
	}
	return files, nil
}

func patchTargetPath(oldPath, newPath string) (string, error) {
	if oldPath == "/dev/null" || newPath == "/dev/null" {
		return "", toolError("unsupported_patch", "patch does not support file creation or deletion yet", ErrInvalidTool)
	}
	path := strings.TrimPrefix(newPath, "b/")
	if path == newPath {
		path = strings.TrimPrefix(oldPath, "a/")
	}
	if strings.TrimSpace(path) == "" {
		return "", toolError("invalid_patch", "patch file path is empty", ErrInvalidTool)
	}
	return path, nil
}

func parsePatchHunk(lines []string, start int) (patchHunk, int, error) {
	header := lines[start]
	end := strings.Index(header[3:], "@@")
	if end < 0 {
		return patchHunk{}, start, toolError("invalid_patch", "invalid hunk header", ErrInvalidTool)
	}
	oldStart, oldCount, newStart, newCount, err := parseHunkRanges(strings.TrimSpace(header[3 : 3+end]))
	if err != nil {
		return patchHunk{}, start, err
	}
	hunk := patchHunk{OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}
	i := start + 1
	var oldSeen, newSeen int
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "@@ ") {
			break
		}
		if line == "" && i == len(lines)-1 {
			break
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			i++
			continue
		}
		if len(line) == 0 {
			return patchHunk{}, start, toolError("invalid_patch", "invalid empty patch line", ErrInvalidTool)
		}
		kind := line[0]
		if kind != ' ' && kind != '-' && kind != '+' {
			return patchHunk{}, start, toolError("invalid_patch", fmt.Sprintf("invalid patch line prefix %q", kind), ErrInvalidTool)
		}
		text := line[1:] + "\n"
		hunk.Lines = append(hunk.Lines, patchLine{Kind: kind, Text: text})
		if kind != '+' {
			oldSeen++
		}
		if kind != '-' {
			newSeen++
		}
		i++
	}
	if oldSeen != oldCount || newSeen != newCount {
		return patchHunk{}, start, toolError("invalid_patch", "hunk line counts do not match header", ErrInvalidTool)
	}
	return hunk, i, nil
}

func parseHunkRanges(ranges string) (int, int, int, int, error) {
	parts := strings.Fields(ranges)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "-") || !strings.HasPrefix(parts[1], "+") {
		return 0, 0, 0, 0, toolError("invalid_patch", "invalid hunk ranges", ErrInvalidTool)
	}
	oldStart, oldCount, err := parseHunkRange(parts[0][1:])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	newStart, newCount, err := parseHunkRange(parts[1][1:])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return oldStart, oldCount, newStart, newCount, nil
}

func parseHunkRange(value string) (int, int, error) {
	startText, countText, ok := strings.Cut(value, ",")
	start, err := strconv.Atoi(startText)
	if err != nil {
		return 0, 0, toolError("invalid_patch", "invalid hunk start", err)
	}
	if !ok {
		return start, 1, nil
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return 0, 0, toolError("invalid_patch", "invalid hunk count", err)
	}
	return start, count, nil
}

func applyPatchFile(path, original string, hunks []patchHunk) (string, error) {
	originalLines := splitLinesKeepNewline(original)
	estLines := len(originalLines)
	for _, hunk := range hunks {
		// Reserve room for added context plus a small slack.
		estLines += hunk.NewCount
	}
	out := make([]string, 0, estLines)
	estBytes := len(original)
	cursor := 0
	for _, hunk := range hunks {
		start := hunk.OldStart - 1
		if hunk.OldStart == 0 {
			start = 0
		}
		if start < cursor || start > len(originalLines) {
			return "", toolError("patch_mismatch", fmt.Sprintf("hunk for %s targets an invalid line", path), ErrInvalidTool)
		}
		out = append(out, originalLines[cursor:start]...)
		cursor = start
		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				if cursor >= len(originalLines) || originalLines[cursor] != line.Text {
					return "", toolError("patch_mismatch", fmt.Sprintf("context does not match %s", path), ErrInvalidTool)
				}
				out = append(out, originalLines[cursor])
				cursor++
			case '-':
				if cursor >= len(originalLines) || originalLines[cursor] != line.Text {
					return "", toolError("patch_mismatch", fmt.Sprintf("removal does not match %s", path), ErrInvalidTool)
				}
				cursor++
			case '+':
				out = append(out, line.Text)
				estBytes += len(line.Text)
			}
		}
	}
	out = append(out, originalLines[cursor:]...)

	var b strings.Builder
	b.Grow(estBytes)
	for _, line := range out {
		b.WriteString(line)
	}
	updated := b.String()
	if updated == original {
		return "", toolError("patch_no_change", fmt.Sprintf("patch would not change %s", path), ErrInvalidTool)
	}
	return updated, nil
}

func splitLinesKeepNewline(s string) []string {
	if s == "" {
		return []string{""}
	}
	count := strings.Count(s, "\n")
	cap := count
	if !strings.HasSuffix(s, "\n") {
		cap++
	} else {
		cap++ // trailing newline produces an extra empty last element
	}
	if cap == 0 {
		cap = 1
	}
	out := make([]string, 0, cap)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) || strings.HasSuffix(s, "\n") {
		out = append(out, s[start:])
	}
	return out
}

func unifiedDiff(path, before, after string) string {
	beforeLines := splitLinesKeepNewline(before)
	afterLines := splitLinesKeepNewline(after)
	// Header bytes (--- a/path\n+++ b/path\n@@ -1,N +1,M @@\n) plus per-line
	// prefix and trailing newline for every line.
	estBytes := 16 + len(path)*2 + 32 + len(before) + len(after) + len(beforeLines) + len(afterLines)
	var b strings.Builder
	b.Grow(estBytes)
	pathSlash := filepath.ToSlash(path)
	b.WriteString("--- a/")
	b.WriteString(pathSlash)
	b.WriteString("\n+++ b/")
	b.WriteString(pathSlash)
	b.WriteString("\n@@ -1,")
	writeIntTo(&b, len(beforeLines))
	b.WriteString(" +1,")
	writeIntTo(&b, len(afterLines))
	b.WriteString(" @@\n")
	for _, line := range beforeLines {
		b.WriteByte('-')
		writeWithoutTrailingNewline(&b, line)
		b.WriteByte('\n')
	}
	for _, line := range afterLines {
		b.WriteByte('+')
		writeWithoutTrailingNewline(&b, line)
		b.WriteByte('\n')
	}
	return b.String()
}

// writeWithoutTrailingNewline writes line into b minus a trailing \n if any.
func writeWithoutTrailingNewline(b *strings.Builder, line string) {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		b.WriteString(line[:n-1])
		return
	}
	b.WriteString(line)
}

// writeIntTo formats v as decimal directly into b without escaping to the heap.
func writeIntTo(b *strings.Builder, v int) {
	var buf [20]byte
	out := strconv.AppendInt(buf[:0], int64(v), 10)
	b.Write(out)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
