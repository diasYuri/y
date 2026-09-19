package memory

import (
	"regexp"
	"strings"

	pmemory "github.com/yuri/y/pkg/memory"
)

var sensitivePatterns = []*regexp.Regexp{regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|credential)\s*[:=]\s*["']?[^\s"']+`), regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]{12,}`), regexp.MustCompile(`\b(?:sk|rk|ghp|github_pat|xox[baprs])-[_a-z0-9-]{10,}\b`), regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----`)}

func redactText(value string) (string, bool) {
	changed := false
	for _, pattern := range sensitivePatterns {
		redacted := pattern.ReplaceAllString(value, "[REDACTED]")
		if redacted != value {
			changed = true
			value = redacted
		}
	}
	return value, changed
}

// RedactText removes common credential forms before a transcript crosses a
// queue or provider boundary. It is intentionally conservative: callers may
// discard the result when they need strict rejection instead of replacement.
func RedactText(value string) string {
	redacted, _ := redactText(value)
	return redacted
}
func sanitizeCandidate(candidate pmemory.Candidate) (pmemory.Candidate, bool) {
	summary, summaryChanged := redactText(candidate.Summary)
	content, contentChanged := redactText(candidate.Content)
	candidate.Summary, candidate.Content = summary, content
	tagsSensitive := false
	for index, tag := range candidate.Tags {
		redacted, changed := redactText(tag)
		candidate.Tags[index] = redacted
		tagsSensitive = tagsSensitive || changed
	}
	return candidate, candidate.Sensitive || summaryChanged || contentChanged || tagsSensitive
}
func normalizedCandidateText(candidate pmemory.Candidate) string {
	return strings.TrimSpace(candidate.Summary + "\n" + candidate.Content)
}
