package memory

import "regexp"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|credential)\s*[:=]\s*["']?[^\s"']+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`\b(?:sk|rk|ghp|github_pat|xox[baprs])-[\w-]{10,}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----`),
}

func looksSensitive(value string) bool {
	for _, pattern := range sensitivePatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
