package mom

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseSandboxArg(t *testing.T) {
	cases := []struct {
		input string
		ok    bool
		want  SandboxConfig
	}{
		{"", true, SandboxConfig{Kind: SandboxHost}},
		{"host", true, SandboxConfig{Kind: SandboxHost}},
		{"docker:my-container", true, SandboxConfig{Kind: SandboxDocker, Container: "my-container"}},
		{"docker:", false, SandboxConfig{}},
		{"podman:foo", false, SandboxConfig{}},
	}
	for _, tc := range cases {
		got, err := ParseSandboxArg(tc.input)
		if tc.ok && err != nil {
			t.Errorf("ParseSandboxArg(%q) error = %v", tc.input, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseSandboxArg(%q) expected error", tc.input)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseSandboxArg(%q) = %#v, want %#v", tc.input, got, tc.want)
		}
	}
}

func TestSandboxConfigString(t *testing.T) {
	if (SandboxConfig{Kind: SandboxHost}).String() != "host" {
		t.Fatalf("host string mismatch")
	}
	if (SandboxConfig{Kind: SandboxDocker, Container: "x"}).String() != "docker:x" {
		t.Fatalf("docker string mismatch")
	}
}

func TestNewSandbox(t *testing.T) {
	if _, err := NewSandbox(SandboxConfig{Kind: SandboxHost}); err != nil {
		t.Fatalf("NewSandbox host: %v", err)
	}
	if _, err := NewSandbox(SandboxConfig{Kind: SandboxDocker}); err == nil {
		t.Fatal("expected error when docker container is missing")
	}
	if _, err := NewSandbox(SandboxConfig{Kind: SandboxDocker, Container: "name"}); err != nil {
		t.Fatalf("NewSandbox docker: %v", err)
	}
}

func TestHostSandboxExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host sandbox shell varies on Windows")
	}
	sandbox := NewHostSandbox()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sandbox.Exec(ctx, "echo y-mom test", ExecOptions{Timeout: 2 * time.Second, MaxOutput: 1024})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "y-mom test") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestHostSandboxTruncatesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	sandbox := NewHostSandbox()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sandbox.Exec(ctx, "yes A | head -c 64", ExecOptions{Timeout: 2 * time.Second, MaxOutput: 16})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.StdoutTruncated {
		t.Fatalf("expected stdout to be truncated")
	}
	if int64(len(res.Stdout)) != 16 {
		t.Fatalf("truncated stdout length = %d", len(res.Stdout))
	}
}

func TestFakeSandboxConsumesResponses(t *testing.T) {
	fs := &FakeSandbox{Responses: []FakeExecResponse{{Result: ExecResult{Stdout: "queued", ExitCode: 0}}}}
	res, err := fs.Exec(context.Background(), "anything", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.Stdout != "queued" {
		t.Fatalf("Stdout = %q", res.Stdout)
	}
	if len(fs.Calls) != 1 || fs.Calls[0].Command != "anything" {
		t.Fatalf("Calls = %#v", fs.Calls)
	}
	res, err = fs.Exec(context.Background(), "fallback", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec fallback: %v", err)
	}
	if res.Stdout != "" || res.ExitCode != 0 {
		t.Fatalf("default response: %#v", res)
	}
}

func TestValidateSandboxHost(t *testing.T) {
	if err := ValidateSandbox(context.Background(), SandboxConfig{Kind: SandboxHost}); err != nil {
		t.Fatalf("ValidateSandbox host: %v", err)
	}
	if err := ValidateSandbox(context.Background(), SandboxConfig{}); err != nil {
		t.Fatalf("ValidateSandbox empty: %v", err)
	}
	if err := ValidateSandbox(context.Background(), SandboxConfig{Kind: SandboxDocker}); err == nil {
		t.Fatal("expected error when docker container is empty")
	}
}
