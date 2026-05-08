package log

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLoggerRedactsSensitiveEnvValues(t *testing.T) {
	redactor := RedactorFromEnv([]string{
		"OPENAI_API_KEY=sk-test-secret-value",
		"HOME=/tmp/example",
	})
	var out bytes.Buffer
	logger := New(&out, LevelDebug, FormatText, redactor)
	logger.now = func() time.Time { return time.Unix(0, 0) }

	logger.Info("using sk-test-secret-value", map[string]string{
		"OPENAI_API_KEY": "sk-test-secret-value",
		"path":           "/tmp/example/sk-test-secret-value",
	})

	got := out.String()
	if strings.Contains(got, "sk-test-secret-value") {
		t.Fatalf("log output leaked secret: %q", got)
	}
	if strings.Contains(got, `OPENAI_API_KEY="sk-test-secret-value"`) {
		t.Fatalf("log output leaked sensitive field: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("log output did not contain redaction marker: %q", got)
	}
}

func TestLoggerHonorsLevel(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, LevelWarn, FormatText, Redactor{})

	logger.Info("hidden", nil)
	logger.Warn("shown", nil)

	got := out.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("log output included disabled level: %q", got)
	}
	if !strings.Contains(got, "shown") {
		t.Fatalf("log output missing enabled level: %q", got)
	}
}

func TestLoggerNilWriter(t *testing.T) {
	logger := New(nil, LevelDebug, FormatText, Redactor{})
	// Should not panic.
	logger.Info("test", nil)
	logger.Error("test", nil)
}

func TestLoggerJSONFormat(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, LevelDebug, FormatJSON, Redactor{})
	logger.now = func() time.Time { return time.Unix(0, 0) }

	logger.Info("hello", map[string]string{"key": "value"})

	got := out.String()
	if !strings.Contains(got, `"msg"`) {
		t.Fatalf("JSON missing msg field: %q", got)
	}
	if !strings.Contains(got, `"level"`) {
		t.Fatalf("JSON missing level field: %q", got)
	}
	if !strings.Contains(got, `"time"`) {
		t.Fatalf("JSON missing time field: %q", got)
	}
}

func TestLoggerAllLevels(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, LevelTrace, FormatText, Redactor{})

	logger.Error("err", nil)
	logger.Warn("warn", nil)
	logger.Info("info", nil)
	logger.Debug("debug", nil)
	logger.Trace("trace", nil)

	got := out.String()
	for _, want := range []string{"error", "warn", "info", "debug", "trace"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q: %q", want, got)
		}
	}
}

func TestLoggerEmptyFields(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, LevelDebug, FormatText, Redactor{})
	logger.now = func() time.Time { return time.Unix(0, 0) }

	logger.Info("msg-only", nil)

	got := out.String()
	if !strings.Contains(got, "msg-only") {
		t.Fatalf("log output missing message: %q", got)
	}
}

func TestNewDefaultFormat(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, LevelDebug, "", Redactor{})
	logger.now = func() time.Time { return time.Unix(0, 0) }

	logger.Info("test", nil)

	got := out.String()
	// Text format outputs a space-separated line, not JSON.
	if strings.Contains(got, `"msg"`) {
		t.Fatalf("expected text format, got JSON: %q", got)
	}
}

func TestRedactorSkipsShortValues(t *testing.T) {
	r := Redactor{values: []string{"abc", "long-secret-value"}}
	got := r.Redact("abc and long-secret-value")
	// "abc" is 3 chars (< 4), so it should NOT be redacted.
	if strings.Contains(got, "[REDACTED]") && !strings.Contains(got, "abc") {
		// long-secret-value was redacted, abc was not.
		if strings.Contains(got, "long-secret-value") {
			t.Fatalf("long value not redacted: %q", got)
		}
	}
	if !strings.Contains(got, "abc") {
		t.Fatalf("short value was incorrectly redacted: %q", got)
	}
}

func TestRedactorFieldSensitiveKey(t *testing.T) {
	r := Redactor{}
	got := r.RedactField("MY_API_KEY", "secret")
	if got != "[REDACTED]" {
		t.Fatalf("RedactField(API_KEY) = %q, want [REDACTED]", got)
	}
}

func TestRedactorFieldNonSensitiveKey(t *testing.T) {
	r := Redactor{values: []string{"secret"}}
	got := r.RedactField("path", "secret")
	if got != "[REDACTED]" {
		t.Fatalf("RedactField(path) = %q, want [REDACTED]", got)
	}
}

func TestSensitiveKeyPatterns(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"API_KEY", true},
		{"api_key", true},
		{"TOKEN", true},
		{"auth_token", true},
		{"SECRET", true},
		{"client_secret", true},
		{"PASSWORD", true},
		{"passwd", true},
		{"PRIVATE_KEY", true},
		{"ACCESS_KEY", true},
		{"AUTH", true},
		{"HOME", false},
		{"PATH", false},
		{"USER", false},
	}
	for _, tc := range tests {
		if got := SensitiveKey(tc.key); got != tc.want {
			t.Fatalf("SensitiveKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestRedactorFromEnv(t *testing.T) {
	r := RedactorFromEnv([]string{
		"API_KEY=secret1",
		"HOME=/tmp",
		"EMPTY=",
		"NOVALUE",
	})
	got := r.Redact("secret1 in message")
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("Redact did not redact env value: %q", got)
	}
}
