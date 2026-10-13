package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/diasYuri/y/pkg/tools/gitignore"
)

// CodeSearchOptions configures repository-aware source search tools.
type CodeSearchOptions struct {
	WorkspaceRoot string
	Policy        Policy
	Limits        ToolLimits
	RipgrepPath   string
	AstGrepPath   string
}

// RegisterCodeSearch registers search_text, search_ast, outline, and find_files.
func RegisterCodeSearch(r *Registry, opts CodeSearchOptions) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	t := &codeSearchTool{
		workspaceRoot: opts.WorkspaceRoot,
		policy:        opts.Policy,
		limits:        filesystemLimits(opts.Limits),
		commandLimit:  commandLimits(opts.Limits).MaxOutputBytes,
		timeout:       commandLimits(opts.Limits).CommandTimeoutSeconds,
		rg:            firstNonEmpty(opts.RipgrepPath, "rg"),
		astGrep:       firstNonEmpty(opts.AstGrepPath, discoverAstGrep()),
	}
	defs := []struct {
		desc    ToolDescriptor
		handler ToolHandler
	}{
		{t.textDescriptor(), ToolHandlerFunc(t.searchText)},
		{t.astDescriptor(), ToolHandlerFunc(t.searchAST)},
		{t.outlineDescriptor(), ToolHandlerFunc(t.outline)},
		{t.findDescriptor(), ToolHandlerFunc(t.findFiles)},
	}
	for _, def := range defs {
		if err := r.Add(def.desc, def.handler); err != nil {
			return err
		}
	}
	return nil
}

type codeSearchTool struct {
	workspaceRoot string
	policy        Policy
	limits        ToolLimits
	commandLimit  int64
	timeout       int64
	rg            string
	astGrep       string
}

const searchTextDescription = `Search source code and text using ripgrep.

Use this as the default tool for discovering files, identifiers, strings,
error messages, configuration keys, endpoints, tests, and other textual
occurrences.

Use it to find candidates, not to determine semantic identity.

When you already have an exact symbol and need its definition,
references, implementations, callers, or callees, prefer LSP tools.

When searching for a syntactic code pattern rather than text,
prefer search_ast.`

const searchASTDescription = `Search code by syntax/AST structure using ast-grep.

Use this when the desired match is defined by code structure rather
than exact text.

Examples include function-call patterns, specific control-flow shapes,
declarations, error-handling patterns, goroutines, async constructs,
or repeated implementation patterns.

Prefer search_text for names, strings, comments, config values, or
initial discovery.

Prefer LSP for semantic symbol relationships.`

const outlineDescription = `Return the structural outline of a source file or directory without
reading all implementation details.

Use this before reading large files when you need to discover their
classes, functions, methods, interfaces, exports, or important symbols.

Prefer outline + targeted read ranges over reading an entire large file.`

func (t *codeSearchTool) textDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "search_text",
		Description:  searchTextDescription,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"language":{"type":"string"},"case_sensitive":{"type":"boolean"},"whole_word":{"type":"boolean"},"fixed_string":{"type":"boolean"},"max_results":{"type":"integer","minimum":1,"maximum":1000},"context_lines":{"type":"integer","minimum":0,"maximum":20}},"required":["query"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemSearch, CapabilityProcessExec},
		Limits:       t.limits,
	}
}

func (t *codeSearchTool) astDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "search_ast",
		Description:  searchASTDescription,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"language":{"type":"string"},"glob":{"type":"string"},"max_results":{"type":"integer","minimum":1,"maximum":1000}},"required":["pattern"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemSearch, CapabilityProcessExec},
		Limits:       t.limits,
	}
}

func (t *codeSearchTool) outlineDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "outline",
		Description:  outlineDescription,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"language":{"type":"string"},"include_private":{"type":"boolean"},"max_depth":{"type":"integer","minimum":1,"maximum":20}},"required":["path"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemSearch, CapabilityProcessExec},
		Limits:       t.limits,
	}
}

func (t *codeSearchTool) findDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "find_files",
		Description:  "Discover files and directories under the workspace. Uses repository ignore rules by default; use this for file discovery, not content search.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"name":{"type":"string"},"glob":{"type":"string"},"type":{"type":"string","enum":["file","directory","any"]},"max_depth":{"type":"integer","minimum":0,"maximum":100},"max_results":{"type":"integer","minimum":1,"maximum":5000},"include_ignored":{"type":"boolean"}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityFilesystemList, CapabilityProcessExec},
		Limits:       t.limits,
	}
}

type searchTextInput struct {
	Query         string `json:"query"`
	Path          string `json:"path,omitempty"`
	Glob          string `json:"glob,omitempty"`
	Language      string `json:"language,omitempty"`
	CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	WholeWord     bool   `json:"whole_word,omitempty"`
	FixedString   bool   `json:"fixed_string,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	ContextLines  int    `json:"context_lines,omitempty"`
}

type searchMatch struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Column int      `json:"column"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type searchTextResult struct {
	Matches   []searchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
	} `json:"data"`
}

func (t *codeSearchTool) searchText(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in searchTextInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "search_text arguments must be valid JSON", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "search_text query is required", ErrInvalidTool)
	}
	path, err := t.resolve(req, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := t.authorize(ctx, req, path, "search_text", CapabilityFilesystemSearch); err != nil {
		return ToolResponse{}, err
	}
	limit := t.resultLimit(in.MaxResults)
	if in.ContextLines < 0 || in.ContextLines > 20 {
		return ToolResponse{}, toolError("invalid_arguments", "context_lines must be between 0 and 20", ErrInvalidTool)
	}
	if _, err := exec.LookPath(t.rg); err != nil {
		return ToolResponse{}, toolError("binary_unavailable", "search_text requires ripgrep (rg); install it or use the compatibility search tool", err)
	}
	args := []string{"--json", "--color", "never", "--line-number", "--column", "--max-count", fmt.Sprint(limit + 1)}
	if in.CaseSensitive != nil && !*in.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if in.WholeWord {
		args = append(args, "--word-regexp")
	}
	if in.FixedString {
		args = append(args, "--fixed-strings")
	}
	if in.ContextLines > 0 {
		args = append(args, "--context", fmt.Sprint(in.ContextLines))
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	if in.Language != "" {
		args = append(args, "--type", in.Language)
	}
	args = append(args, "--", in.Query, path.Absolute)
	startedAt := time.Now()
	result, err := executeCommand(ctx, commandSpec{command: t.rg, args: args, cwd: path.WorkspaceRoot, timeout: t.timeout, limit: t.commandLimit, progress: progressFunc(req)})
	if err != nil {
		if ctx.Err() != nil {
			return ToolResponse{}, ctx.Err()
		}
		return ToolResponse{}, toolError("search_failed", "ripgrep could not complete the search", err)
	}
	if result.TimedOut {
		return ToolResponse{}, toolError("timeout", "ripgrep search timed out; narrow the path or query", context.DeadlineExceeded)
	}
	if result.ExitCode > 1 {
		diagnostic := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout, "ripgrep failed"))
		lowerDiagnostic := strings.ToLower(diagnostic)
		if !in.FixedString && (strings.Contains(lowerDiagnostic, "regex parse error") || strings.Contains(lowerDiagnostic, "regex syntax error")) {
			return ToolResponse{}, toolError("invalid_pattern", "search_text query is not a valid regular expression", errors.New(strings.TrimSpace(result.Stderr)))
		}
		return ToolResponse{}, toolError("search_failed", diagnostic, ErrInvalidTool)
	}
	parsed := parseRipgrepJSON(result.Stdout, path.WorkspaceRoot, limit, t.limits.MaxLineBytes, in.ContextLines)
	parsed.Truncated = parsed.Truncated || result.StdoutTruncated || result.TimedOut
	response, err := searchTextResponse(parsed, t.limits.MaxOutputBytes)
	if err != nil {
		return ToolResponse{}, err
	}
	var delivered searchTextResult
	_ = json.Unmarshal(response.Details, &delivered)
	response.Metadata = marshalToolJSON(map[string]any{"tool": "search_text", "workspace": path.WorkspaceRoot, "language": in.Language, "duration_ms": time.Since(startedAt).Milliseconds(), "exit_status": result.ExitCode, "result_count": len(delivered.Matches), "truncated": delivered.Truncated, "timeout": result.TimedOut, "cache_hit": false})
	return response, nil
}

func parseRipgrepJSON(output, root string, limit int, maxLineBytes int64, contextLines int) searchTextResult {
	result := searchTextResult{Matches: make([]searchMatch, 0, min(limit, 32))}
	contexts := make(map[string]map[int]string)
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event rgEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			result.Truncated = true
			continue
		}
		if event.Type != "match" && event.Type != "context" {
			continue
		}
		file := event.Data.Path.Text
		if file == "" {
			continue
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(root, file)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			continue
		}
		line := strings.TrimSuffix(event.Data.Lines.Text, "\n")
		line = strings.TrimSuffix(line, "\r")
		if event.Type == "context" {
			if contexts[rel] == nil {
				contexts[rel] = make(map[int]string)
			}
			contexts[rel][event.Data.LineNumber] = line
			continue
		}
		if len(result.Matches) >= limit {
			result.Truncated = true
			break
		}
		if maxLineBytes > 0 && int64(len(line)) > maxLineBytes {
			line = trimUTF8Bytes(line, maxLineBytes)
			result.Truncated = true
		}
		column := 1
		if len(event.Data.Submatches) > 0 {
			start := event.Data.Submatches[0].Start
			if start > len(event.Data.Lines.Text) {
				start = len(event.Data.Lines.Text)
			}
			column = runeColumn(event.Data.Lines.Text, start)
		}
		result.Matches = append(result.Matches, searchMatch{Path: filepath.ToSlash(rel), Line: event.Data.LineNumber, Column: column, Text: line})
	}
	for i := range result.Matches {
		match := &result.Matches[i]
		lines := contexts[match.Path]
		for n := match.Line - contextLines; n < match.Line; n++ {
			if value, ok := lines[n]; ok {
				match.Before = append(match.Before, value)
			}
		}
		for n := match.Line + 1; n <= match.Line+contextLines; n++ {
			if value, ok := lines[n]; ok {
				match.After = append(match.After, value)
			}
		}
	}
	return result
}

func searchTextResponse(result searchTextResult, maxBytes int64) (ToolResponse, error) {
	for {
		raw, err := json.Marshal(result)
		if err != nil {
			return ToolResponse{}, err
		}
		if maxBytes > 0 && int64(len(raw)) > maxBytes && len(result.Matches) == 0 {
			return ToolResponse{}, toolError("output_limit", "search results exceed the output limit; narrow the path or query", ErrLimitExceeded)
		}
		if maxBytes <= 0 || int64(len(raw)) <= maxBytes {
			text := "No matches found"
			if len(result.Matches) > 0 {
				lines := make([]string, 0, len(result.Matches))
				for _, match := range result.Matches {
					lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", match.Path, match.Line, match.Column, match.Text))
				}
				text = strings.Join(lines, "\n")
			}
			return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: limitTextBytes(text, maxBytes, "Tool output limit reached")}}, Details: raw}, nil
		}
		result.Truncated = true
		result.Matches = result.Matches[:len(result.Matches)-1]
	}
}

type astSearchInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Language   string `json:"language,omitempty"`
	Glob       string `json:"glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type astRange struct {
	Start astPosition `json:"start"`
	End   astPosition `json:"end"`
}

type astPosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type astMatchRaw struct {
	Text     string   `json:"text"`
	Lines    string   `json:"lines"`
	File     string   `json:"file"`
	Language string   `json:"language"`
	Range    astRange `json:"range"`
}

type astMatch struct {
	Path        string `json:"path"`
	Language    string `json:"language,omitempty"`
	Text        string `json:"text"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
}

type astSearchResult struct {
	Matches   []astMatch `json:"matches"`
	Truncated bool       `json:"truncated"`
}

func (t *codeSearchTool) searchAST(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in astSearchInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "search_ast arguments must be valid JSON", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "search_ast pattern is required", ErrInvalidTool)
	}
	path, err := t.resolve(req, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := t.authorize(ctx, req, path, "search_ast", CapabilityFilesystemSearch); err != nil {
		return ToolResponse{}, err
	}
	if _, err := exec.LookPath(t.astGrep); err != nil {
		return ToolResponse{}, toolError("binary_unavailable", "search_ast requires ast-grep (sg or ast-grep); install it or use search_text", err)
	}
	args := []string{"run", "--pattern", in.Pattern, "--json=stream", "--color", "never"}
	if in.Language != "" {
		args = append(args, "--lang", in.Language)
	}
	if in.Glob != "" {
		args = append(args, "--globs", in.Glob)
	}
	args = append(args, path.Absolute)
	startedAt := time.Now()
	result, err := executeCommand(ctx, commandSpec{command: t.astGrep, args: args, cwd: path.WorkspaceRoot, timeout: t.timeout, limit: t.commandLimit, progress: progressFunc(req)})
	if err != nil {
		if ctx.Err() != nil {
			return ToolResponse{}, ctx.Err()
		}
		return ToolResponse{}, toolError("search_failed", "ast-grep could not complete the search", err)
	}
	if result.TimedOut {
		return ToolResponse{}, toolError("timeout", "ast-grep search timed out; narrow the path or pattern", context.DeadlineExceeded)
	}
	if result.ExitCode > 1 {
		diagnostic := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout, "ast-grep failed"))
		if strings.Contains(strings.ToLower(diagnostic), "pattern") {
			return ToolResponse{}, toolError("invalid_pattern", "ast-grep could not parse the structural pattern: "+diagnostic, ErrInvalidTool)
		}
		return ToolResponse{}, toolError("search_failed", diagnostic, ErrInvalidTool)
	}
	limit := t.resultLimit(in.MaxResults)
	out := astSearchResult{Matches: make([]astMatch, 0, min(limit, 32)), Truncated: result.StdoutTruncated || result.TimedOut}
	lineCache := make(map[string][]string)
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var raw astMatchRaw
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			out.Truncated = true
			continue
		}
		if len(out.Matches) >= limit {
			out.Truncated = true
			break
		}
		file := raw.File
		if !filepath.IsAbs(file) {
			file = filepath.Join(path.WorkspaceRoot, file)
		}
		rel, relErr := filepath.Rel(path.WorkspaceRoot, file)
		if relErr != nil || !pathInsideWorkspace(rel) {
			continue
		}
		matchText := firstNonEmpty(raw.Text, raw.Lines)
		if t.limits.MaxLineBytes > 0 {
			matchText = trimUTF8Bytes(matchText, t.limits.MaxLineBytes)
		}
		lines := sourceLinesForPath(file, raw.Lines, lineCache)
		startColumn := raw.Range.Start.Column + 1
		endColumn := raw.Range.End.Column + 1
		if raw.Range.Start.Line >= 0 && raw.Range.Start.Line < len(lines) {
			startColumn = runeColumn(lines[raw.Range.Start.Line], raw.Range.Start.Column)
		}
		if raw.Range.End.Line >= 0 && raw.Range.End.Line < len(lines) {
			endColumn = runeColumn(lines[raw.Range.End.Line], raw.Range.End.Column)
		}
		out.Matches = append(out.Matches, astMatch{Path: filepath.ToSlash(rel), Language: raw.Language, Text: matchText,
			StartLine: raw.Range.Start.Line + 1, StartColumn: startColumn,
			EndLine: raw.Range.End.Line + 1, EndColumn: endColumn})
	}
	response, err := astSearchResponse(out, t.limits.MaxOutputBytes)
	if err != nil {
		return ToolResponse{}, err
	}
	var delivered astSearchResult
	_ = json.Unmarshal(response.Details, &delivered)
	response.Metadata = marshalToolJSON(map[string]any{"tool": "search_ast", "workspace": path.WorkspaceRoot, "language": in.Language, "duration_ms": time.Since(startedAt).Milliseconds(), "exit_status": result.ExitCode, "result_count": len(delivered.Matches), "truncated": delivered.Truncated, "timeout": result.TimedOut, "cache_hit": false})
	return response, nil
}

func astSearchResponse(result astSearchResult, maxBytes int64) (ToolResponse, error) {
	for {
		raw, err := json.Marshal(result)
		if err != nil {
			return ToolResponse{}, err
		}
		if maxBytes > 0 && int64(len(raw)) > maxBytes && len(result.Matches) == 0 {
			return ToolResponse{}, toolError("output_limit", "AST search results exceed the output limit; narrow the path or pattern", ErrLimitExceeded)
		}
		if maxBytes <= 0 || int64(len(raw)) <= maxBytes {
			return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: string(raw)}}, Details: raw}, nil
		}
		result.Truncated = true
		result.Matches = result.Matches[:len(result.Matches)-1]
	}
}

type outlineInput struct {
	Path           string `json:"path"`
	Language       string `json:"language,omitempty"`
	IncludePrivate bool   `json:"include_private,omitempty"`
	MaxDepth       int    `json:"max_depth,omitempty"`
}

type outlinePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type outlineRange struct {
	Start outlinePosition `json:"start"`
	End   outlinePosition `json:"end"`
}

type outlineRaw struct {
	Path  string        `json:"path"`
	Items []outlineItem `json:"items"`
}

type outlineItem struct {
	Name       string        `json:"name"`
	SymbolType string        `json:"symbolType"`
	Signature  string        `json:"signature"`
	IsExported *bool         `json:"isExported"`
	IsPublic   *bool         `json:"isPublic"`
	Range      outlineRange  `json:"range"`
	Members    []outlineItem `json:"members"`
}

type outlineSymbol struct {
	Path      string          `json:"path,omitempty"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Signature string          `json:"signature,omitempty"`
	StartLine int             `json:"start_line"`
	EndLine   int             `json:"end_line"`
	Public    *bool           `json:"public,omitempty"`
	Members   []outlineSymbol `json:"members,omitempty"`
}

type outlineResult struct {
	Path      string          `json:"path"`
	Symbols   []outlineSymbol `json:"symbols"`
	Truncated bool            `json:"truncated"`
}

func (t *codeSearchTool) outline(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in outlineInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "outline arguments must be valid JSON", err)
	}
	if in.Path == "" {
		return ToolResponse{}, toolError("invalid_arguments", "outline path is required", ErrInvalidTool)
	}
	path, err := t.resolve(req, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := t.authorize(ctx, req, path, "outline", CapabilityFilesystemSearch); err != nil {
		return ToolResponse{}, err
	}
	if _, err := exec.LookPath(t.astGrep); err != nil {
		return ToolResponse{}, toolError("binary_unavailable", "outline requires an ast-grep version with the outline subcommand", err)
	}
	args := []string{"outline", "--json=compact", "--color", "never"}
	if in.Language != "" {
		args = append(args, "--lang", in.Language)
	}
	if in.MaxDepth > 0 {
		args = append(args, "--max-depth", fmt.Sprint(in.MaxDepth))
	}
	args = append(args, path.Absolute)
	startedAt := time.Now()
	result, err := executeCommand(ctx, commandSpec{command: t.astGrep, args: args, cwd: path.WorkspaceRoot, timeout: t.timeout, limit: t.commandLimit, progress: progressFunc(req)})
	if err != nil {
		if ctx.Err() != nil {
			return ToolResponse{}, ctx.Err()
		}
		return ToolResponse{}, toolError("outline_failed", "ast-grep could not produce an outline", err)
	}
	if result.TimedOut {
		return ToolResponse{}, toolError("timeout", "ast-grep outline timed out; narrow the path", context.DeadlineExceeded)
	}
	if result.ExitCode != 0 {
		return ToolResponse{}, toolError("outline_failed", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout, "ast-grep outline failed")), ErrInvalidTool)
	}
	var files []outlineRaw
	if err := json.Unmarshal([]byte(result.Stdout), &files); err != nil {
		return ToolResponse{}, toolError("outline_failed", "ast-grep returned an unsupported outline JSON format", err)
	}
	out := outlineResult{Path: filepath.ToSlash(in.Path), Symbols: make([]outlineSymbol, 0)}
	info, _ := os.Stat(path.Absolute)
	isDirectory := info != nil && info.IsDir()
	remaining := t.limits.MaxMatches
	for _, file := range files {
		filePath := file.Path
		if filePath == "" {
			filePath = in.Path
		}
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(path.WorkspaceRoot, filePath)
		}
		rel, relErr := filepath.Rel(path.WorkspaceRoot, filePath)
		if relErr != nil || !pathInsideWorkspace(rel) {
			continue
		}
		for _, item := range file.Items {
			if !in.IncludePrivate && item.IsExported != nil && !*item.IsExported {
				continue
			}
			symbol, added := normalizeOutlineItem(item, in.IncludePrivate, 0, in.MaxDepth, &remaining, &out.Truncated)
			if !added {
				out.Truncated = true
				break
			}
			if isDirectory {
				symbol.Path = filepath.ToSlash(rel)
			}
			out.Symbols = append(out.Symbols, symbol)
		}
		if remaining <= 0 {
			out.Truncated = true
			break
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ToolResponse{}, err
	}
	if int64(len(raw)) > t.limits.MaxOutputBytes {
		return ToolResponse{}, toolError("output_limit", "outline exceeds the tool output limit; narrow the path or max_depth", ErrLimitExceeded)
	}
	return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: string(raw)}}, Details: raw, Metadata: marshalToolJSON(map[string]any{"tool": "outline", "workspace": path.WorkspaceRoot, "duration_ms": time.Since(startedAt).Milliseconds(), "exit_status": result.ExitCode, "result_count": countOutlineSymbols(out.Symbols), "truncated": out.Truncated, "timeout": result.TimedOut, "cache_hit": false})}, nil
}

func normalizeOutlineItem(item outlineItem, includePrivate bool, depth, maxDepth int, remaining *int, truncated *bool) (outlineSymbol, bool) {
	if *remaining <= 0 {
		*truncated = true
		return outlineSymbol{}, false
	}
	*remaining--
	public := item.IsPublic
	if public == nil {
		public = item.IsExported
	}
	symbol := outlineSymbol{Name: item.Name, Kind: item.SymbolType, Signature: item.Signature, StartLine: item.Range.Start.Line + 1, EndLine: item.Range.End.Line + 1, Public: public}
	if maxDepth > 0 && depth+1 >= maxDepth {
		if len(item.Members) > 0 {
			*truncated = true
		}
		return symbol, true
	}
	for _, child := range item.Members {
		childPublic := child.IsPublic
		if childPublic == nil {
			childPublic = child.IsExported
		}
		if !includePrivate && childPublic != nil && !*childPublic {
			continue
		}
		member, added := normalizeOutlineItem(child, includePrivate, depth+1, maxDepth, remaining, truncated)
		if !added {
			break
		}
		symbol.Members = append(symbol.Members, member)
	}
	return symbol, true
}

func countOutlineSymbols(symbols []outlineSymbol) int {
	count := len(symbols)
	for _, symbol := range symbols {
		count += countOutlineSymbols(symbol.Members)
	}
	return count
}

type findFilesInput struct {
	Path           string `json:"path,omitempty"`
	Name           string `json:"name,omitempty"`
	Glob           string `json:"glob,omitempty"`
	Type           string `json:"type,omitempty"`
	MaxDepth       int    `json:"max_depth,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
}

type findFilesResult struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

func (t *codeSearchTool) findFiles(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in findFilesInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "find_files arguments must be valid JSON", err)
	}
	if in.Type == "" {
		in.Type = "file"
	}
	if in.Type != "file" && in.Type != "directory" && in.Type != "any" {
		return ToolResponse{}, toolError("invalid_arguments", "type must be file, directory, or any", ErrInvalidTool)
	}
	path, err := t.resolve(req, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := t.authorize(ctx, req, path, "find_files", CapabilityFilesystemList); err != nil {
		return ToolResponse{}, err
	}
	limit := t.resultLimit(in.MaxResults)
	paths := []string(nil)
	truncated := false
	startedAt := time.Now()
	if in.Type == "file" && !in.IncludeIgnored {
		if _, lookErr := exec.LookPath(t.rg); lookErr == nil {
			args := []string{"--files", "--hidden", "--glob", "!.git/**"}
			if in.Glob != "" {
				args = append(args, "--glob", in.Glob)
			}
			if in.MaxDepth > 0 {
				args = append(args, "--maxdepth", fmt.Sprint(in.MaxDepth))
			}
			args = append(args, path.Absolute)
			result, runErr := executeCommand(ctx, commandSpec{command: t.rg, args: args, cwd: path.WorkspaceRoot, timeout: t.timeout, limit: t.commandLimit})
			if runErr != nil {
				return ToolResponse{}, toolError("find_failed", "ripgrep could not list files", runErr)
			}
			if result.ExitCode > 1 {
				return ToolResponse{}, toolError("find_failed", strings.TrimSpace(result.Stderr), ErrInvalidTool)
			}
			for _, candidate := range strings.Split(result.Stdout, "\n") {
				candidate = strings.TrimSpace(candidate)
				if candidate == "" {
					continue
				}
				rel, relErr := filepath.Rel(path.WorkspaceRoot, candidate)
				if relErr != nil || !pathInsideWorkspace(rel) || !fileNameMatches(rel, in.Name, in.Glob) {
					continue
				}
				paths = append(paths, filepath.ToSlash(rel))
				if len(paths) > limit {
					truncated = true
					paths = paths[:limit]
					break
				}
			}
			truncated = truncated || result.StdoutTruncated || result.TimedOut
			response, responseErr := findFilesResponse(paths, truncated, t.limits.MaxOutputBytes)
			if responseErr == nil {
				response.Metadata = marshalToolJSON(map[string]any{"tool": "find_files", "workspace": path.WorkspaceRoot, "duration_ms": time.Since(startedAt).Milliseconds(), "result_count": len(paths), "truncated": truncated, "timeout": result.TimedOut, "cache_hit": false})
			}
			return response, responseErr
		}
	}
	ignores := gitignore.NewWalkIgnore()
	err = filepath.WalkDir(path.Absolute, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relFromRoot, relErr := filepath.Rel(path.WorkspaceRoot, candidate)
		if relErr != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if !in.IncludeIgnored {
				_ = ignores.AddDir(candidate)
				if ignores.Match(candidate, true) {
					return filepath.SkipDir
				}
			}
			if in.MaxDepth > 0 {
				rel, _ := filepath.Rel(path.Absolute, candidate)
				if depthOf(rel) >= in.MaxDepth && candidate != path.Absolute {
					return filepath.SkipDir
				}
			}
		}
		if candidate == path.Absolute {
			return nil
		}
		isDir := entry.IsDir()
		if in.Type == "file" && isDir || in.Type == "directory" && !isDir {
			return nil
		}
		if !in.IncludeIgnored && ignores.Match(candidate, false) || !fileNameMatches(relFromRoot, in.Name, in.Glob) {
			return nil
		}
		paths = append(paths, filepath.ToSlash(relFromRoot))
		if len(paths) > limit {
			truncated = true
			return errStopWalk
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		paths = paths[:limit]
		err = nil
	}
	if err != nil {
		return ToolResponse{}, err
	}
	response, responseErr := findFilesResponse(paths, truncated, t.limits.MaxOutputBytes)
	if responseErr == nil {
		response.Metadata = marshalToolJSON(map[string]any{"tool": "find_files", "workspace": path.WorkspaceRoot, "duration_ms": time.Since(startedAt).Milliseconds(), "result_count": len(paths), "truncated": truncated, "timeout": false, "cache_hit": false})
	}
	return response, responseErr
}

func findFilesResponse(paths []string, truncated bool, maxBytes int64) (ToolResponse, error) {
	out := findFilesResult{Paths: paths, Truncated: truncated}
	raw, err := json.Marshal(out)
	if err != nil {
		return ToolResponse{}, err
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		return ToolResponse{}, toolError("output_limit", "file listing exceeds the output limit; narrow the path or filters", ErrLimitExceeded)
	}
	return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: string(raw)}}, Details: raw}, nil
}

func fileNameMatches(path, name, glob string) bool {
	base := filepath.Base(path)
	if name != "" && base != name {
		return false
	}
	if glob != "" {
		matched, err := filepath.Match(glob, base)
		if err != nil || !matched {
			matched, err = filepath.Match(glob, filepath.ToSlash(path))
			if err != nil || !matched {
				return false
			}
		}
	}
	return true
}

func pathInsideWorkspace(path string) bool {
	clean := filepath.Clean(path)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(os.PathSeparator)) && !filepath.IsAbs(clean)
}

func discoverAstGrep() string {
	if _, err := exec.LookPath("ast-grep"); err == nil {
		return "ast-grep"
	}
	if _, err := exec.LookPath("sg"); err == nil {
		return "sg"
	}
	return "ast-grep"
}

func depthOf(path string) int {
	if path == "." || path == "" {
		return 0
	}
	return strings.Count(filepath.Clean(path), string(os.PathSeparator)) + 1
}

func (t *codeSearchTool) resolve(req ToolRequest, input string) (resolvedPath, error) {
	if input == "" {
		input = "."
	}
	path, err := resolveForRead(firstNonEmpty(req.WorkspaceRoot, t.workspaceRoot), input)
	if err != nil {
		return resolvedPath{}, err
	}
	if _, err := os.Stat(path.Absolute); err != nil {
		return resolvedPath{}, err
	}
	return path, nil
}

func (t *codeSearchTool) authorize(ctx context.Context, req ToolRequest, path resolvedPath, name string, capability Capability) error {
	if err := authorize(ctx, t.policy, PolicyRequest{ToolName: name, Capability: string(capability), WorkspaceRoot: path.WorkspaceRoot, Path: path.Input, ResolvedPath: path.Absolute, EscapesWorkspace: path.EscapesWorkspace}); err != nil {
		return err
	}
	return authorize(ctx, t.policy, PolicyRequest{ToolName: name, Capability: string(CapabilityProcessExec), WorkspaceRoot: path.WorkspaceRoot, Path: path.Input, ResolvedPath: path.Absolute, EscapesWorkspace: path.EscapesWorkspace})
}

func (t *codeSearchTool) resultLimit(requested int) int {
	limit := t.limits.MaxMatches
	if requested > 0 && requested < limit {
		limit = requested
	}
	return limit
}

func progressFunc(req ToolRequest) func(ToolProgress) {
	if req.Progress == nil {
		return nil
	}
	return func(progress ToolProgress) {
		progress.ToolCallID = req.ID
		progress.ToolName = req.Name
		req.Progress(progress)
	}
}

// Keep the byte-to-rune conversion used by search output explicit and shared
// with coordinate normalization tests.
func runeColumn(text string, byteOffset int) int {
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset > len(text) {
		byteOffset = len(text)
	}
	for byteOffset > 0 && byteOffset < len(text) && !utf8.RuneStart(text[byteOffset]) {
		byteOffset--
	}
	return utf8.RuneCountInString(text[:byteOffset]) + 1
}
