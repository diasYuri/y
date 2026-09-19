package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ycontext "github.com/diasYuri/y/pkg/context"
	memoryext "github.com/diasYuri/y/pkg/extensions/memory"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

func TestFilesystemStorePersistsScopesAndForgetsImmediately(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystemStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first := pmemory.Memory{Kind: pmemory.KindProjectFact, Scope: ycontext.ScopeProject, Summary: "uses Go", Content: "The project uses Go.", Source: pmemory.SourceRef{ProjectID: "/workspace/one"}, Confidence: .9}
	second := first
	second.Source.ProjectID = "/workspace/two"
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	values, err := store.Search(context.Background(), pmemory.Query{ProjectID: "/workspace/one", Terms: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Content != first.Content {
		t.Fatalf("project one search = %#v", values)
	}
	other, err := store.Search(context.Background(), pmemory.Query{ProjectID: "/workspace/two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Source.ProjectID != "/workspace/two" {
		t.Fatalf("project two search = %#v", other)
	}
	if err := store.Forget(context.Background(), values[0].ID); err != nil {
		t.Fatal(err)
	}
	values, err = store.Search(context.Background(), pmemory.Query{ProjectID: "/workspace/one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("forgotten memory returned: %#v", values)
	}
	if _, err := store.Read(context.Background(), first.ID); !errors.Is(err, pmemory.ErrNotFound) {
		t.Fatalf("Read forgotten err = %v", err)
	}

	reloaded, err := NewFilesystemStore(root)
	if err != nil {
		t.Fatal(err)
	}
	values, err = reloaded.Search(context.Background(), pmemory.Query{ProjectID: "/workspace/two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("reloaded values = %#v", values)
	}
	for _, path := range []string{filepath.Join(root, "index.json"), filepath.Join(root, "projects")} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o, want private", path, info.Mode().Perm())
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "projects", hashOwner("/workspace/two"), "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "uses Go") {
		t.Fatalf("memory index = %q", data)
	}
}

func TestFilesystemStoreRejectsSymlinkRootAndSensitiveContent(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewFilesystemStore(link); err == nil {
		t.Fatal("symlink root accepted")
	}
	store, err := NewFilesystemStore(filepath.Join(parent, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save(context.Background(), pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeGlobal, Summary: "token", Content: "api_key=secret-value"})
	if !errors.Is(err, pmemory.ErrSensitiveContent) {
		t.Fatalf("sensitive save err = %v", err)
	}
}

func TestFilesystemStoreExpiryAndBudget(t *testing.T) {
	store, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := now.Add(-time.Minute)
	if err := store.Save(context.Background(), pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeGlobal, Summary: "old", Content: "old", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeGlobal, Summary: "new", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	values, err := store.Search(context.Background(), pmemory.Query{Now: now, MaxItems: 1, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Summary != "new" {
		t.Fatalf("expiry search = %#v", values)
	}
}

func TestFilesystemVersionChangesWhenMemoryExpires(t *testing.T) {
	store, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store.now = func() time.Time { return now }
	expires := now.Add(time.Minute)
	if err := store.Save(context.Background(), pmemory.Memory{Kind: pmemory.KindReference, Scope: ycontext.ScopeGlobal, Summary: "expiring", Content: "value", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	resolver := ycontext.NewResolver(memoryext.NewSource(store, memoryext.SourceOptions{MaxItems: 5, MaxContextTokens: 100, MaxReadBytes: 1024, FailOpen: false}))
	request := ycontext.Request{Query: "expiring"}
	first, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 {
		t.Fatalf("first resolution = %#v", first.Items)
	}
	now = expires.Add(time.Second)
	second, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 0 {
		t.Fatalf("expired resolution was served from cache: %#v", second.Items)
	}
}

func hashOwner(value string) string {
	// The store deliberately keeps owner paths out of the filesystem. This
	// helper mirrors the stable directory hash for the index assertion above.
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
