package tools

import (
	"unicode/utf8"
)

const (
	DefaultMaxInputBytes         int64 = 1 << 20
	DefaultMaxOutputBytes        int64 = 64 << 10
	DefaultMaxCommandOutputBytes int64 = 256 << 10
	DefaultCommandTimeoutSeconds int64 = 30
	DefaultMaxFileReadBytes      int64 = 1 << 20
	DefaultMaxFileWriteBytes     int64 = 1 << 20
	DefaultMaxEntries                  = 500
	DefaultMaxMatches                  = 100
	DefaultMaxLineBytes          int64 = 512
)

func filesystemLimits(base ToolLimits) ToolLimits {
	limits := base
	if limits.MaxInputBytes <= 0 {
		limits.MaxInputBytes = DefaultMaxInputBytes
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if limits.MaxFileReadBytes <= 0 {
		limits.MaxFileReadBytes = DefaultMaxFileReadBytes
	}
	if limits.MaxFileWriteBytes <= 0 {
		limits.MaxFileWriteBytes = DefaultMaxFileWriteBytes
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = DefaultMaxEntries
	}
	if limits.MaxMatches <= 0 {
		limits.MaxMatches = DefaultMaxMatches
	}
	if limits.MaxLineBytes <= 0 {
		limits.MaxLineBytes = DefaultMaxLineBytes
	}
	return limits
}

func commandLimits(base ToolLimits) ToolLimits {
	limits := base
	if limits.MaxInputBytes <= 0 {
		limits.MaxInputBytes = DefaultMaxInputBytes
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultMaxCommandOutputBytes
	}
	if limits.MaxCommandOutputBytes > 0 {
		limits.MaxOutputBytes = limits.MaxCommandOutputBytes
	}
	if limits.CommandTimeoutSeconds <= 0 {
		limits.CommandTimeoutSeconds = DefaultCommandTimeoutSeconds
	}
	return limits
}

func enforceInputLimit(args []byte, limits ToolLimits) error {
	if int64(len(args)) > limits.MaxInputBytes {
		return toolError("input_too_large", "tool arguments exceed input byte limit", ErrLimitExceeded)
	}
	return nil
}

func limitTextBytes(s string, max int64, notice string) string {
	if max <= 0 || int64(len(s)) <= max {
		return s
	}
	if notice != "" {
		notice = "\n\n[" + notice + "]"
	}
	noticeBytes := int64(len(notice))
	if noticeBytes >= max {
		return trimUTF8Bytes(notice, max)
	}
	return trimUTF8Bytes(s, max-noticeBytes) + notice
}

func trimUTF8Bytes(s string, max int64) string {
	if max <= 0 {
		return ""
	}
	if int64(len(s)) <= max {
		return s
	}
	cut := int(max)
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut]
}
