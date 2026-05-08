package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"--help"}, os.Getenv)
	if code != 0 {
		t.Fatalf("help exit code: %d", code)
	}
	if !strings.Contains(out.String(), "y-pods") {
		t.Fatal("help missing y-pods")
	}
}

func TestRunVersion(t *testing.T) {
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"--version"}, os.Getenv)
	if code != 0 {
		t.Fatalf("version exit code: %d", code)
	}
	if out.String() == "" {
		t.Fatal("version output empty")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"bogus"}, os.Getenv)
	if code != 2 {
		t.Fatalf("bogus exit code: %d", code)
	}
}

func TestRunPodsListEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("Y_PODS_CONFIG_DIR", tmp)
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"pods"}, os.Getenv)
	if code != 0 {
		t.Fatalf("pods list empty exit code: %d, stderr: %s", code, err.String())
	}
	if !strings.Contains(out.String(), "No pods configured") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRunStartMissingName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("Y_PODS_CONFIG_DIR", tmp)
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"start", "model-id"}, os.Getenv)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(err.String(), "--name is required") {
		t.Fatalf("unexpected stderr: %s", err.String())
	}
}

func TestRunLogsNoActivePod(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("Y_PODS_CONFIG_DIR", tmp)
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"logs", "m"}, os.Getenv)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(err.String(), "no active pod") {
		t.Fatalf("unexpected stderr: %s", err.String())
	}
}

func TestRunAgentNoActivePod(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("Y_PODS_CONFIG_DIR", tmp)
	var out, err bytes.Buffer
	code := run(&out, &err, []string{"agent", "m", "hello"}, os.Getenv)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(err.String(), "no active pod") {
		t.Fatalf("unexpected stderr: %s", err.String())
	}
}
