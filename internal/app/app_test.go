package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/internal/storage"
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"--help"}, BuildInfo{Version: "test"})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Usage:") || !strings.Contains(got, "run") || !strings.Contains(got, "chat") {
		t.Fatalf("help output missing expected sections:\n%s", got)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"--version"}, BuildInfo{Version: "0.1.0-test"})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "0.1.0-test" {
		t.Fatalf("version output = %q, want %q", got, "0.1.0-test")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"version"}, BuildInfo{Version: "0.1.0-test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "0.1.0-test" {
		t.Fatalf("version output = %q, want %q", got, "0.1.0-test")
	}
}

func TestRunVersionVerbose(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"version", "--verbose"}, BuildInfo{Version: "0.1.0-test", Commit: "abc123", Date: "2026-01-02"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "0.1.0-test") {
		t.Fatalf("output missing version: %s", got)
	}
	if !strings.Contains(got, "abc123") {
		t.Fatalf("output missing commit: %s", got)
	}
	if !strings.Contains(got, "go:") {
		t.Fatalf("output missing go version: %s", got)
	}
	if !strings.Contains(got, "os/arch:") {
		t.Fatalf("output missing os/arch: %s", got)
	}
}

func TestRunVersionJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"version", "--json"}, BuildInfo{Version: "0.1.0-test", Commit: "abc123", Date: "2026-01-02"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	var info BuildInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("unmarshal JSON: %v\noutput: %s", err, stdout.String())
	}
	if info.Version != "0.1.0-test" {
		t.Fatalf("version = %q, want %q", info.Version, "0.1.0-test")
	}
	if info.Commit != "abc123" {
		t.Fatalf("commit = %q, want %q", info.Commit, "abc123")
	}
}

func TestRunVersionUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"version", "--nope"}, BuildInfo{Version: "test"})
	if code != exitCodeUsage {
		t.Fatalf("Run returned code %d, want %d", code, exitCodeUsage)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"nope"}, BuildInfo{Version: "test"})

	if code != 2 {
		t.Fatalf("Run returned code %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, `unknown command "nope"`) {
		t.Fatalf("stderr missing unknown command message: %q", got)
	}
}

func TestRunFeatures(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"features"}, BuildInfo{Version: "test"})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"KIND", "config.validate", "doctor", "session.list", "session.show", "feature", "git", "feature_git"} {
		if !strings.Contains(got, want) {
			t.Fatalf("features output missing %q:\n%s", want, got)
		}
	}
}

func TestRunDoctorText(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"doctor"}, BuildInfo{
		Version: "0.1.0-test",
		Commit:  "abc123",
		Date:    "2026-01-02",
		Tags:    []string{"feature_openai"},
	})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"Y doctor", "status: ok", "version: 0.1.0-test", "tags: feature_openai", "compiled_features_registered"} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, got)
		}
	}
}

func TestRunDoctorJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"doctor", "--json"}, BuildInfo{
		Version: "0.1.0-test",
		Commit:  "abc123",
		Date:    "2026-01-02",
	})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var report struct {
		Status string `json:"status"`
		Build  struct {
			Version string   `json:"version"`
			Commit  string   `json:"commit"`
			Date    string   `json:"date"`
			Tags    []string `json:"tags"`
		} `json:"build"`
		Capabilities struct {
			Compiled []string `json:"compiled"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor JSON is invalid: %v\n%s", err, stdout.String())
	}
	if report.Status != "ok" || report.Build.Version != "0.1.0-test" || report.Build.Commit != "abc123" || report.Build.Date != "2026-01-02" {
		t.Fatalf("unexpected doctor JSON: %s", stdout.String())
	}
	if report.Build.Tags == nil {
		t.Fatalf("doctor JSON tags = nil, want []")
	}
	if len(report.Capabilities.Compiled) == 0 {
		t.Fatalf("doctor JSON missing compiled capabilities: %s", stdout.String())
	}
}

func TestRunConfigValidateNoFile(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"config", "validate"}, BuildInfo{Version: "test"})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "configuration valid" {
		t.Fatalf("stdout = %q, want configuration valid", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunConfigValidateRejectsUncompiledFeature(t *testing.T) {
	reg := feature.NewRegistry()
	if err := feature.RegisterCompiledFeatures(reg); err != nil {
		t.Fatalf("RegisterCompiledFeatures returned error: %v", err)
	}
	var missing feature.Status
	for _, status := range reg.Status() {
		if !status.Compiled && status.Kind != feature.KindCommand {
			missing = status
			break
		}
	}
	if missing.ID == "" {
		t.Skip("all known runtime capabilities are compiled in this build")
	}

	section := string(missing.Kind) + "s"
	if missing.Kind == feature.KindFeature {
		section = "features"
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "y.toml")
	contents := "[" + section + "]\n" + missing.ID + " = true\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	var stdout, stderr bytes.Buffer

	code := Run(&stdout, &stderr, []string{"config", "validate", "--config", path}, BuildInfo{Version: "test"})

	if code != 1 {
		t.Fatalf("Run returned code %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := string(missing.Kind) + ` "` + missing.ID + `" requested by config but not compiled into this binary`
	if got := stderr.String(); !strings.Contains(got, want) {
		t.Fatalf("stderr missing uncompiled feature message: %q", got)
	}
}

func TestRunSessionListAndShow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("Y_CODING_AGENT_DIR", filepath.Join(root, "agent"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}

	store := storage.NewSessionStore(storage.DefaultAgentDir())
	summary, err := store.SaveTranscript(context.Background(), cwd, []ai.Message{
		{Role: ai.RoleUser, Timestamp: time.Unix(1, 0).UTC(), Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}},
	}, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript returned error: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"session", "list"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, summary.ID) || !strings.Contains(got, "MESSAGES") {
		t.Fatalf("session list output missing expected content:\n%s", got)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(&stdout, &stderr, []string{"session", "show", summary.ID[:8]}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, `"type":"session"`) || !strings.Contains(got, `"hello"`) {
		t.Fatalf("session show output missing expected transcript:\n%s", got)
	}
}

func TestRunCommandStreamsTextAndSavesSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("Y_CODING_AGENT_DIR", filepath.Join(root, "agent"))

	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hel"},
			ai.TextDelta{Text: "lo"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	factory := func(context.Context, *feature.Registry, headlessOptions) (agent.Provider, error) {
		return provider, nil
	}

	var stdout, stderr bytes.Buffer
	code := runHeadlessCommand("run", &stdout, &stderr, strings.NewReader(""), false, []string{"hello"}, feature.NewRegistry(), factory)
	if code != 0 {
		t.Fatalf("runHeadlessCommand returned code %d, want 0", code)
	}
	if got := stdout.String(); got != "hello\n" {
		t.Fatalf("stdout = %q, want streamed hello with newline", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider call count = %d, want 1", got)
	}

	store := storage.NewSessionStore(storage.DefaultAgentDir())
	sessions, err := store.List(context.Background(), mustGetWorkingDir(t))
	if err != nil {
		t.Fatalf("session List returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}
}

func TestChatCommandStreamsMultipleTurnsWithoutTUI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("Y_CODING_AGENT_DIR", filepath.Join(root, "agent"))

	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{ai.TextDelta{Text: "first"}, ai.StopEvent{Reason: ai.StopReasonStop}},
		},
		providers.FakeResponse{
			Events: []ai.Event{ai.TextDelta{Text: "second"}, ai.StopEvent{Reason: ai.StopReasonStop}},
		},
	))
	factory := func(context.Context, *feature.Registry, headlessOptions) (agent.Provider, error) {
		return provider, nil
	}

	var stdout, stderr bytes.Buffer
	code := runHeadlessCommand("chat", &stdout, &stderr, strings.NewReader("second prompt\n"), false, []string{"first prompt"}, feature.NewRegistry(), factory)
	if code != 0 {
		t.Fatalf("runHeadlessCommand returned code %d, want 0", code)
	}
	if got := stdout.String(); got != "first\nsecond\n" {
		t.Fatalf("stdout = %q, want both streamed turns", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("provider call count = %d, want 2", got)
	}
}

func TestRunCommandRejectsMissingPromptOnTTY(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHeadlessCommand("run", &stdout, &stderr, strings.NewReader(""), true, nil, feature.NewRegistry(), func(context.Context, *feature.Registry, headlessOptions) (agent.Provider, error) {
		t.Fatal("provider factory should not be called when prompt is missing")
		return nil, nil
	})
	if code != 2 {
		t.Fatalf("runHeadlessCommand returned code %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "prompt is required") {
		t.Fatalf("stderr = %q, want prompt required error", got)
	}
}

func TestRunConfigShowMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"config", "show", "nonexistent.toml"}, BuildInfo{Version: "test"})
	if code != 1 {
		t.Fatalf("Run returned code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "config show:") {
		t.Fatalf("stderr missing error: %q", stderr.String())
	}
}

func TestRunConfigShowValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "y.toml")
	contents := `[features]
git = true

[providers]
openai = true

[limits]
max_tokens = 4096
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"config", "show", path}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "git:") {
		t.Fatalf("output missing git feature: %s", got)
	}
	if !strings.Contains(got, "openai:") {
		t.Fatalf("output missing openai provider: %s", got)
	}
	if !strings.Contains(got, "max_tokens:") {
		t.Fatalf("output missing max_tokens limit: %s", got)
	}
}

func TestRunInitSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer

	dir := t.TempDir()
	path := filepath.Join(dir, "y.toml")

	code := Run(&stdout, &stderr, []string{"init", path}, BuildInfo{Version: "test"})

	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Created") {
		t.Fatalf("stdout missing Created: %q", got)
	}

	// File should exist and contain default config.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("init created empty file")
	}
}

func TestRunInitFileExists(t *testing.T) {
	var stdout, stderr bytes.Buffer

	dir := t.TempDir()
	path := filepath.Join(dir, "y.toml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code := Run(&stdout, &stderr, []string{"init", path}, BuildInfo{Version: "test"})

	if code != 1 {
		t.Fatalf("Run returned code %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, "already exists") {
		t.Fatalf("stderr missing already exists: %q", got)
	}
}

func TestRunInitHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"init", "--help"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := stdout.String(); !strings.Contains(got, "Usage:") {
		t.Fatalf("stdout missing Usage: %q", got)
	}
}

func TestRunAuthHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"auth", "--help"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := stdout.String(); !strings.Contains(got, "login") || !strings.Contains(got, "logout") || !strings.Contains(got, "list") {
		t.Fatalf("auth help missing subcommands: %q", got)
	}
}

func TestRunAuthUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"auth", "nope"}, BuildInfo{Version: "test"})
	if code != 2 {
		t.Fatalf("Run returned code %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown subcommand") {
		t.Fatalf("stderr missing unknown subcommand: %q", got)
	}
}

func TestRunAuthLoginMissingProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"auth", "login"}, BuildInfo{Version: "test"})
	if code != 2 {
		t.Fatalf("Run returned code %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "provider is required") {
		t.Fatalf("stderr missing provider required: %q", got)
	}
}

func TestRunAuthLogoutMissingProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"auth", "logout"}, BuildInfo{Version: "test"})
	if code != 2 {
		t.Fatalf("Run returned code %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "provider is required") {
		t.Fatalf("stderr missing provider required: %q", got)
	}
}

func TestRunAuthListEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("Y_CODING_AGENT_DIR", filepath.Join(root, "agent"))

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"auth", "list"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned code %d, want 0", code)
	}
	if got := stdout.String(); !strings.Contains(got, "no stored credentials") {
		t.Fatalf("stdout missing no credentials: %q", got)
	}
}

func TestRunExtension(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "list"}, BuildInfo{Version: "test"})
	reg := feature.NewRegistry()
	_ = feature.RegisterCompiledFeatures(reg)
	if reg.Has(feature.KindFeature, "wasm_extensions") {
		if code != 0 {
			t.Fatalf("Run returned code %d, want 0", code)
		}
		if got := stdout.String(); !strings.Contains(got, "EXTENSION") && !strings.Contains(got, "no extensions") {
			t.Fatalf("stdout missing expected content: %q", got)
		}
	} else {
		if code != exitCodeUsage {
			t.Fatalf("Run returned code %d, want %d", code, exitCodeUsage)
		}
		if got := stderr.String(); !strings.Contains(got, "unavailable") {
			t.Fatalf("stderr missing unavailable: %q", got)
		}
	}
}

func TestRunRPC(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"rpc", "--help"}, BuildInfo{Version: "test"})
	reg := feature.NewRegistry()
	_ = feature.RegisterCompiledFeatures(reg)
	if reg.Has(feature.KindFeature, "rpc") {
		if code != 0 {
			t.Fatalf("Run returned code %d, want 0", code)
		}
		if got := stdout.String(); !strings.Contains(got, "Usage:") {
			t.Fatalf("stdout missing Usage: %q", got)
		}
	} else {
		if code != 1 {
			t.Fatalf("Run returned code %d, want 1", code)
		}
		if got := stderr.String(); !strings.Contains(got, "not compiled") {
			t.Fatalf("stderr missing not compiled: %q", got)
		}
	}
}

func TestRunLSP(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"lsp", "--help"}, BuildInfo{Version: "test"})
	reg := feature.NewRegistry()
	_ = feature.RegisterCompiledFeatures(reg)
	if reg.Has(feature.KindFeature, "lsp") {
		if code != 0 {
			t.Fatalf("Run returned code %d, want 0", code)
		}
		if got := stdout.String(); !strings.Contains(got, "Usage:") {
			t.Fatalf("stdout missing Usage: %q", got)
		}
	} else {
		if code != 1 {
			t.Fatalf("Run returned code %d, want 1", code)
		}
		if got := stderr.String(); !strings.Contains(got, "not compiled") {
			t.Fatalf("stderr missing not compiled: %q", got)
		}
	}
}

func mustGetWorkingDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	return cwd
}
