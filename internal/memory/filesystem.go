package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

const indexName = "index.json"

type indexEntry struct {
	Memory pmemory.Memory `json:"memory"`
	Path   string         `json:"path"`
}

type diskIndex struct {
	Version uint64                `json:"version"`
	Items   map[string]indexEntry `json:"items"`
}

// FilesystemStore persists memory metadata in a private JSON index and the
// human-readable content in Markdown files. The index is replaced atomically.
type FilesystemStore struct {
	root string
	now  func() time.Time
}

// NewFilesystemStore creates a private store rooted at directory. Existing
// symlink roots are rejected so a caller cannot accidentally escape its data
// boundary.
func NewFilesystemStore(directory string) (*FilesystemStore, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("memory directory is empty")
	}
	directory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("resolve memory directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("stat memory directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("memory directory must not be a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("restrict memory directory permissions: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve memory directory: %w", err)
	}
	// Parent directories such as /var may themselves be symlinks on macOS.
	// Canonicalise them instead of rejecting an otherwise private directory;
	// the final directory component was checked with Lstat above.
	return &FilesystemStore{root: realRoot, now: time.Now}, nil
}

// Root returns the canonical, private root used by the store.
func (s *FilesystemStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *FilesystemStore) Search(ctx context.Context, query pmemory.Query) ([]pmemory.Memory, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	index, unlock, err := s.lockedIndex(false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	now := query.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	terms := normalizeTerms(query.Terms)
	entries := make([]indexEntry, 0, len(index.Items))
	for _, entry := range index.Items {
		if entry.Memory.Status == pmemory.StatusForgotten || (!query.IncludePending && entry.Memory.Status != "" && entry.Memory.Status != pmemory.StatusActive) {
			continue
		}
		if entry.Memory.Status == "" {
			entry.Memory.Status = pmemory.StatusActive
		}
		if !query.IncludeExpired && entry.Memory.ExpiresAt != nil && !now.Before(*entry.Memory.ExpiresAt) {
			continue
		}
		if !matchesScope(entry.Memory, query) || !matchesCursor(entry.Memory.ID, query.Cursor) {
			continue
		}
		entries = append(entries, entry)
	}
	type scored struct {
		entry indexEntry
		score int
	}
	scoredEntries := make([]scored, 0, len(entries))
	for _, entry := range entries {
		content, readErr := s.readContent(entry)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, readErr
		}
		entry.Memory.Content = content
		score := lexicalScore(entry.Memory, terms)
		if len(terms) > 0 && score == 0 {
			continue
		}
		scoredEntries = append(scoredEntries, scored{entry: entry, score: score})
	}
	sort.SliceStable(scoredEntries, func(i, j int) bool {
		if scoredEntries[i].score != scoredEntries[j].score {
			return scoredEntries[i].score > scoredEntries[j].score
		}
		left, right := scoredEntries[i].entry.Memory, scoredEntries[j].entry.Memory
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID < right.ID
	})
	limit := query.MaxItems
	if limit <= 0 {
		limit = query.PageSize
	}
	var usedBytes int64
	result := make([]pmemory.Memory, 0, minInt(limit, len(scoredEntries)))
	for _, item := range scoredEntries {
		if limit > 0 && len(result) >= limit {
			break
		}
		value := item.entry.Memory
		valueBytes := int64(len(value.Summary) + len(value.Content))
		if query.MaxBytes > 0 && usedBytes+valueBytes > query.MaxBytes {
			continue
		}
		usedBytes += valueBytes
		result = append(result, value)
	}
	return result, nil
}

func (s *FilesystemStore) Read(ctx context.Context, id string) (pmemory.Memory, error) {
	if err := checkContext(ctx); err != nil {
		return pmemory.Memory{}, err
	}
	if strings.TrimSpace(id) == "" {
		return pmemory.Memory{}, pmemory.ErrNotFound
	}
	index, unlock, err := s.lockedIndex(true)
	if err != nil {
		return pmemory.Memory{}, err
	}
	entry, ok := index.Items[id]
	if !ok || entry.Memory.Status == pmemory.StatusForgotten {
		unlock()
		return pmemory.Memory{}, pmemory.ErrNotFound
	}
	content, err := s.readContent(entry)
	if err != nil {
		unlock()
		if errors.Is(err, os.ErrNotExist) {
			return pmemory.Memory{}, pmemory.ErrNotFound
		}
		return pmemory.Memory{}, err
	}
	value := entry.Memory
	value.Content = content
	value.UsageCount++
	value.LastUsedAt = s.currentTime()
	index.Items[id] = indexEntry{Memory: metadataOnly(value), Path: entry.Path}
	index.Version++
	if err := s.writeIndex(index); err != nil {
		unlock()
		return pmemory.Memory{}, err
	}
	unlock()
	return value, nil
}

// MarkSurfaced records bounded usage metadata for memories injected into the
// provider context. Content is never logged and the topic timestamps remain
// unchanged, so usage accounting does not invalidate resolver content caches.
func (s *FilesystemStore) MarkSurfaced(ctx context.Context, ids []string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	index, unlock, err := s.lockedIndex(true)
	if err != nil {
		return err
	}
	defer unlock()
	now := s.currentTime()
	changed := false
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		entry, ok := index.Items[id]
		if !ok || entry.Memory.Status == pmemory.StatusForgotten {
			continue
		}
		entry.Memory.UsageCount++
		entry.Memory.LastUsedAt = now
		index.Items[id] = entry
		changed = true
	}
	if !changed {
		return nil
	}
	index.Version++
	return s.writeIndex(index)
}

func (s *FilesystemStore) Save(ctx context.Context, value pmemory.Memory) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := pmemory.Validate(value); err != nil {
		return err
	}
	if value.Sensitive || looksSensitive(value.Summary+"\n"+value.Content) {
		return pmemory.ErrSensitiveContent
	}
	now := s.currentTime()
	if value.ID == "" {
		value.ID = pmemory.StableID(value)
	}
	if value.Status == "" {
		value.Status = pmemory.StatusActive
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	if value.LastVerified.IsZero() {
		value.LastVerified = now
	}
	value.UpdatedAt = now
	value.Tags = cleanTags(value.Tags)
	rel, err := relativeMemoryPath(value)
	if err != nil {
		return err
	}
	index, unlock, err := s.lockedIndex(true)
	if err != nil {
		return err
	}
	defer unlock()
	var oldPath, oldIndexDir string
	if existing, ok := index.Items[value.ID]; ok {
		value.CreatedAt = existing.Memory.CreatedAt
		oldPath = existing.Path
		oldIndexDir = memoryIndexDir(filepath.Dir(oldPath))
	}
	path, err := s.safePath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create memory topic directory: %w", err)
	}
	if err := restrictDirectoryTree(filepath.Dir(path), s.root); err != nil {
		return err
	}
	if err := s.atomicWrite(path, []byte(markdownFor(value)), 0o600); err != nil {
		return fmt.Errorf("write memory topic: %w", err)
	}
	index.Items[value.ID] = indexEntry{Memory: metadataOnly(value), Path: rel}
	index.Version++
	if err := s.writeIndex(index); err != nil {
		return err
	}
	if oldPath != "" && oldPath != rel {
		if old, oldErr := s.safePath(oldPath); oldErr == nil {
			_ = os.Remove(old)
		}
	}
	return s.regenerateIndexes(index, oldIndexDir)
}

func (s *FilesystemStore) Forget(ctx context.Context, id string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	index, unlock, err := s.lockedIndex(true)
	if err != nil {
		return err
	}
	defer unlock()
	entry, ok := index.Items[id]
	if !ok {
		return nil
	}
	oldDir := memoryIndexDir(filepath.Dir(entry.Path))
	delete(index.Items, id)
	index.Version++
	if err := s.writeIndex(index); err != nil {
		return err
	}
	if path, pathErr := s.safePath(entry.Path); pathErr == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.regenerateIndexes(index, oldDir)
}

func (s *FilesystemStore) Version(ctx context.Context, request ycontext.Request) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	index, unlock, err := s.lockedIndex(false)
	if err != nil {
		return "", err
	}
	defer unlock()
	now := s.currentTime()
	var builder strings.Builder
	keys := make([]string, 0, len(index.Items))
	for id := range index.Items {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		entry := index.Items[id]
		if matchesScope(entry.Memory, pmemory.Query{TenantID: request.TenantID, WorkspaceID: request.WorkspaceID, ProjectID: request.ProjectID, SessionID: request.SessionID}) {
			expiresAt := int64(0)
			expired := false
			if entry.Memory.ExpiresAt != nil {
				expiresAt = entry.Memory.ExpiresAt.UTC().UnixNano()
				expired = !now.Before(*entry.Memory.ExpiresAt)
			}
			fmt.Fprintf(&builder, "%s\x00%s\x00%d\x00%d\x00%t\x00%s\n", id, entry.Memory.Status, entry.Memory.UpdatedAt.UnixNano(), expiresAt, expired, entry.Path)
		}
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:]), nil
}

func (s *FilesystemStore) currentTime() time.Time {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UTC()
}

func (s *FilesystemStore) lockedIndex(write bool) (diskIndex, func(), error) {
	if s == nil || s.root == "" {
		return diskIndex{}, func() {}, errors.New("memory filesystem store is nil")
	}
	lockFile, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return diskIndex{}, func() {}, err
	}
	if err := acquireProcessLock(lockFile, write); err != nil {
		_ = lockFile.Close()
		return diskIndex{}, func() {}, err
	}
	unlock := func() { releaseProcessLock(lockFile); _ = lockFile.Close() }
	index, err := s.readIndex()
	if err != nil {
		unlock()
		return diskIndex{}, func() {}, err
	}
	return index, unlock, nil
}

func (s *FilesystemStore) readIndex() (diskIndex, error) {
	data, err := os.ReadFile(filepath.Join(s.root, indexName))
	if errors.Is(err, os.ErrNotExist) {
		return diskIndex{Items: make(map[string]indexEntry)}, nil
	}
	if err != nil {
		return diskIndex{}, err
	}
	var index diskIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return diskIndex{}, fmt.Errorf("decode memory index: %w", err)
	}
	if index.Items == nil {
		index.Items = make(map[string]indexEntry)
	}
	for id, entry := range index.Items {
		clean := filepath.Clean(entry.Path)
		if id == "" || entry.Path == "" || filepath.IsAbs(entry.Path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return diskIndex{}, fmt.Errorf("invalid memory index path for %q", id)
		}
	}
	return index, nil
}

func (s *FilesystemStore) writeIndex(index diskIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return s.atomicWrite(filepath.Join(s.root, indexName), append(data, '\n'), 0o600)
}

func (s *FilesystemStore) atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := s.ensureSafePath(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(tmpName, path)
}

func (s *FilesystemStore) safePath(relative string) (string, error) {
	clean := filepath.Clean(relative)
	if filepath.IsAbs(relative) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("memory path escapes root")
	}
	path := filepath.Join(s.root, clean)
	if err := s.ensureSafePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FilesystemStore) ensureSafePath(path string) error {
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("memory path escapes root")
	}
	current := s.root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory path contains symlink")
		}
	}
	return nil
}

func restrictDirectoryTree(path, root string) error {
	current := path
	for {
		if err := os.Chmod(current, 0o700); err != nil {
			return err
		}
		if current == root || filepath.Dir(current) == current {
			return nil
		}
		current = filepath.Dir(current)
	}
}

func (s *FilesystemStore) readContent(entry indexEntry) (string, error) {
	path, err := s.safePath(entry.Path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("memory topic exceeds read limit")
	}
	return parseMarkdown(data), nil
}

func (s *FilesystemStore) regenerateIndexes(index diskIndex, extraDirs ...string) error {
	byDir := make(map[string][]indexEntry)
	for _, entry := range index.Items {
		if entry.Memory.Status == pmemory.StatusForgotten || (entry.Memory.Status != "" && entry.Memory.Status != pmemory.StatusActive) {
			continue
		}
		dir := memoryIndexDir(filepath.Dir(entry.Path))
		byDir[dir] = append(byDir[dir], entry)
	}
	for _, dir := range extraDirs {
		if dir != "" {
			if _, ok := byDir[dir]; !ok {
				byDir[dir] = nil
			}
		}
	}
	for dir, entries := range byDir {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Memory.ID < entries[j].Memory.ID })
		var builder strings.Builder
		builder.WriteString("# Memory index\n\n")
		for _, entry := range entries {
			fmt.Fprintf(&builder, "- `%s` — %s (%s, updated %s)\n", entry.Memory.ID, oneLine(entry.Memory.Summary), entry.Memory.Scope, entry.Memory.UpdatedAt.UTC().Format(time.RFC3339))
		}
		path, err := s.safePath(filepath.ToSlash(filepath.Join(dir, "MEMORY.md")))
		if err != nil {
			return err
		}
		if err := s.atomicWrite(path, []byte(builder.String()), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func relativeMemoryPath(value pmemory.Memory) (string, error) {
	owner, base := "", ""
	if value.Status == pmemory.StatusPending {
		base = filepath.Join("pending", string(value.Scope))
	}
	switch {
	case base != "":
		owner = value.Source.ProjectID + "\x00" + value.Source.WorkspaceID + "\x00" + value.Source.TenantID + "\x00" + value.Source.SessionID
	case value.Scope == ycontext.ScopeGlobal:
		base = "global"
	case value.Scope == ycontext.ScopeTenant:
		base, owner = "tenants", value.Source.TenantID
	case value.Scope == ycontext.ScopeWorkspace:
		base, owner = "workspaces", value.Source.WorkspaceID
	case value.Scope == ycontext.ScopeProject:
		base, owner = "projects", value.Source.ProjectID
	case value.Scope == ycontext.ScopeSession:
		base, owner = "sessions", value.Source.SessionID
	default:
		return "", fmt.Errorf("%w: request scope cannot be persisted", pmemory.ErrInvalidMemory)
	}
	if base != "global" && strings.TrimSpace(owner) == "" {
		return "", fmt.Errorf("%w: %s memory requires an owner", pmemory.ErrInvalidMemory, value.Scope)
	}
	ownerHash := ""
	if owner != "" {
		digest := sha256.Sum256([]byte(owner))
		ownerHash = hex.EncodeToString(digest[:])
	}
	name := value.ID
	if name == "" {
		name = pmemory.StableID(value)
	}
	if !isHex(name) {
		digest := sha256.Sum256([]byte(name))
		name = hex.EncodeToString(digest[:])
	}
	if ownerHash != "" {
		base = filepath.Join(base, ownerHash)
	}
	return filepath.ToSlash(filepath.Join(base, "items", name+".md")), nil
}

func memoryIndexDir(topicDir string) string {
	if filepath.Base(topicDir) == "items" {
		return filepath.Dir(topicDir)
	}
	return topicDir
}

func metadataOnly(value pmemory.Memory) pmemory.Memory { value.Content = ""; return value }
func markdownFor(value pmemory.Memory) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "<!-- y-memory id=%q -->\n# %s\n\n", value.ID, oneLine(value.Summary))
	builder.WriteString(value.Content)
	if !strings.HasSuffix(value.Content, "\n") {
		builder.WriteByte('\n')
	}
	return builder.String()
}
func parseMarkdown(data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if index := strings.Index(text, "\n\n"); index >= 0 {
		return strings.TrimSpace(text[index+2:])
	}
	return strings.TrimSpace(text)
}

func matchesScope(value pmemory.Memory, query pmemory.Query) bool {
	if len(query.Scopes) > 0 {
		found := false
		for _, scope := range query.Scopes {
			if scope == value.Scope {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	switch value.Scope {
	case ycontext.ScopeGlobal:
		return true
	case ycontext.ScopeTenant:
		return query.TenantID != "" && value.Source.TenantID == query.TenantID
	case ycontext.ScopeWorkspace:
		return query.WorkspaceID != "" && value.Source.WorkspaceID == query.WorkspaceID
	case ycontext.ScopeProject:
		return query.ProjectID != "" && value.Source.ProjectID == query.ProjectID
	case ycontext.ScopeSession:
		return query.SessionID != "" && value.Source.SessionID == query.SessionID
	default:
		return false
	}
}

func lexicalScore(value pmemory.Memory, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	text := normalize(value.Summary + " " + strings.Join(value.Tags, " ") + " " + value.Content)
	score := 0
	for _, term := range terms {
		score += strings.Count(text, term)
	}
	return score
}
func normalizeTerms(values []string) []string {
	var out []string
	for _, value := range values {
		for _, term := range strings.Fields(normalize(value)) {
			if term != "" {
				out = append(out, term)
			}
		}
	}
	return out
}
func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func cleanTags(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func oneLine(value string) string          { return strings.Join(strings.Fields(value), " ") }
func matchesCursor(id, cursor string) bool { return cursor == "" || id > cursor }
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func isHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'a' && r <= 'f' {
			continue
		}
		if r >= 'A' && r <= 'F' {
			continue
		}
		return false
	}
	return true
}
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

var _ pmemory.Store = (*FilesystemStore)(nil)
