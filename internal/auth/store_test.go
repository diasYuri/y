package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAtomicWriteRead(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "auth.json"))

	creds := &Credentials{
		ProviderID:   "anthropic",
		AccessToken:  "tok_123",
		RefreshToken: "ref_456",
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}
	if err := store.Write(creds); err != nil {
		t.Fatalf("write: %v", err)
	}

	read, err := store.Read("anthropic")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read == nil {
		t.Fatal("expected credentials, got nil")
	}
	if read.AccessToken != "tok_123" {
		t.Errorf("access token: got %q, want %q", read.AccessToken, "tok_123")
	}
	if read.RefreshToken != "ref_456" {
		t.Errorf("refresh token: got %q, want %q", read.RefreshToken, "ref_456")
	}
}

func TestStorePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "auth.json"))

	creds := &Credentials{
		ProviderID:  "test",
		AccessToken: "abc",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := store.Write(creds); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions: got %04o, want %04o", perm, 0600)
	}
}

func TestStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "auth.json"))

	if err := store.Write(&Credentials{ProviderID: "a", AccessToken: "x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(&Credentials{ProviderID: "b", AccessToken: "y", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	read, err := store.Read("a")
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if read != nil {
		t.Error("expected nil after delete")
	}

	read, err = store.Read("b")
	if err != nil {
		t.Fatalf("read b: %v", err)
	}
	if read == nil || read.AccessToken != "y" {
		t.Error("b should still exist")
	}
}

func TestStoreList(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "auth.json"))

	if err := store.Write(&Credentials{ProviderID: "x", AccessToken: "1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(&Credentials{ProviderID: "y", AccessToken: "2", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(ids))
	}
}

func TestStoreReadMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "auth.json"))

	read, err := store.Read("nonexistent")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read != nil {
		t.Error("expected nil for missing provider")
	}
}
