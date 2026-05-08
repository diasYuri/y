package mom

import (
	"strings"
	"testing"
)

func TestParseCLIArgsBasic(t *testing.T) {
	args, err := ParseCLIArgs([]string{"--sandbox=docker:my", "/tmp/data"})
	if err != nil {
		t.Fatalf("ParseCLIArgs: %v", err)
	}
	if args.Sandbox.Kind != SandboxDocker || args.Sandbox.Container != "my" {
		t.Fatalf("Sandbox = %#v", args.Sandbox)
	}
	if !strings.HasSuffix(args.WorkingDir, "/tmp/data") && args.WorkingDir != "/tmp/data" {
		// AbsoluteAllowed: filepath.Abs may return the same value on /tmp.
		t.Fatalf("WorkingDir = %q", args.WorkingDir)
	}
}

func TestParseCLIArgsHelp(t *testing.T) {
	args, err := ParseCLIArgs([]string{"--help"})
	if err != nil {
		t.Fatalf("ParseCLIArgs: %v", err)
	}
	if !args.ShowHelp {
		t.Fatal("expected ShowHelp to be true")
	}
}

func TestParseCLIArgsDownload(t *testing.T) {
	args, err := ParseCLIArgs([]string{"--download", "C1"})
	if err != nil {
		t.Fatalf("ParseCLIArgs: %v", err)
	}
	if args.DownloadChannel != "C1" {
		t.Fatalf("DownloadChannel = %q", args.DownloadChannel)
	}
}

func TestParseCLIArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := ParseCLIArgs([]string{"--mystery"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestParseCLIArgsRejectsExtraPositional(t *testing.T) {
	if _, err := ParseCLIArgs([]string{"/tmp/a", "/tmp/b"}); err == nil {
		t.Fatal("expected error when supplying two working directories")
	}
}

func TestEnvConfigValidate(t *testing.T) {
	envs := map[string]string{
		"MOM_SLACK_APP_TOKEN": "xapp",
		"MOM_SLACK_BOT_TOKEN": "xoxb",
		"ANTHROPIC_API_KEY":   "sk-ant",
	}
	cfg := LoadEnvConfig(func(key string) string { return envs[key] })
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.DefaultProviderID != "anthropic" {
		t.Fatalf("DefaultProviderID = %q", cfg.DefaultProviderID)
	}
}

func TestEnvConfigValidateRequiresTokens(t *testing.T) {
	cfg := LoadEnvConfig(func(string) string { return "" })
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for empty config")
	}
}

func TestEnvConfigValidateRequiresProvider(t *testing.T) {
	envs := map[string]string{
		"MOM_SLACK_APP_TOKEN": "xapp",
		"MOM_SLACK_BOT_TOKEN": "xoxb",
	}
	cfg := LoadEnvConfig(func(key string) string { return envs[key] })
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when no provider key is set")
	}
}
