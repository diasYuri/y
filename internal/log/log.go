// Package log provides a small leveled logger with secret redaction.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level controls which log entries are emitted.
type Level int

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

// Format controls the on-disk/on-stderr representation.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Logger writes redacted log entries to a writer.
type Logger struct {
	mu       sync.Mutex
	w        io.Writer
	level    Level
	format   Format
	redactor Redactor
	now      func() time.Time
}

// New creates a logger. A nil writer discards all entries.
func New(w io.Writer, level Level, format Format, redactor Redactor) *Logger {
	if format == "" {
		format = FormatText
	}
	return &Logger{
		w:        w,
		level:    level,
		format:   format,
		redactor: redactor,
		now:      time.Now,
	}
}

// NewEnvRedactor creates a redactor from the current process environment.
func NewEnvRedactor() Redactor {
	return RedactorFromEnv(os.Environ())
}

// Error logs an error-level message.
func (l *Logger) Error(message string, fields map[string]string) {
	l.write(LevelError, "error", message, fields)
}

// Warn logs a warning-level message.
func (l *Logger) Warn(message string, fields map[string]string) {
	l.write(LevelWarn, "warn", message, fields)
}

// Info logs an info-level message.
func (l *Logger) Info(message string, fields map[string]string) {
	l.write(LevelInfo, "info", message, fields)
}

// Debug logs a debug-level message.
func (l *Logger) Debug(message string, fields map[string]string) {
	l.write(LevelDebug, "debug", message, fields)
}

// Trace logs a trace-level message.
func (l *Logger) Trace(message string, fields map[string]string) {
	l.write(LevelTrace, "trace", message, fields)
}

func (l *Logger) write(level Level, label, message string, fields map[string]string) {
	if l == nil || l.w == nil || level > l.level {
		return
	}

	entry := map[string]string{
		"level": label,
		"msg":   l.redactor.Redact(message),
		"time":  l.now().UTC().Format(time.RFC3339),
	}
	for key, value := range fields {
		entry[key] = l.redactor.RedactField(key, value)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.format == FormatJSON {
		_ = json.NewEncoder(l.w).Encode(entry)
		return
	}

	fmt.Fprintf(l.w, "%s %-5s %s", entry["time"], label, entry["msg"])
	for _, key := range sortedFieldKeys(entry) {
		if key == "time" || key == "level" || key == "msg" {
			continue
		}
		fmt.Fprintf(l.w, " %s=%q", key, entry[key])
	}
	fmt.Fprintln(l.w)
}

func sortedFieldKeys(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Redactor masks sensitive values before they reach logs.
type Redactor struct {
	values []string
}

// RedactorFromEnv creates a redactor from KEY=value environment entries.
func RedactorFromEnv(env []string) Redactor {
	values := make([]string, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !SensitiveKey(key) {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return Redactor{values: values}
}

// Redact masks sensitive environment values embedded in a string.
func (r Redactor) Redact(s string) string {
	for _, value := range r.values {
		if len(value) < 4 {
			continue
		}
		s = strings.ReplaceAll(s, value, "[REDACTED]")
	}
	return s
}

// RedactField masks fields whose key name is sensitive, then applies value
// redaction to catch secrets embedded in otherwise safe fields.
func (r Redactor) RedactField(key, value string) string {
	if SensitiveKey(key) {
		return "[REDACTED]"
	}
	return r.Redact(value)
}

// SensitiveKey reports whether a name conventionally carries secret material.
func SensitiveKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"API_KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "PRIVATE_KEY", "ACCESS_KEY", "AUTH"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
