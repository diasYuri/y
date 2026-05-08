// Package retryafter parses HTTP Retry-After headers per RFC 7231.
//
// The header value can be either:
//   - a non-negative integer number of seconds, or
//   - an HTTP-date (e.g. "Wed, 21 Oct 2026 07:28:00 GMT").
//
// Parse returns the suggested wait as a time.Duration. It returns 0 when the
// header is empty, unparseable, or refers to a moment in the past.
package retryafter

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Now is the clock used by Parse. Tests may swap it out to return deterministic
// values when parsing HTTP-date forms.
var Now = time.Now

// Parse decodes a Retry-After header value. It accepts an integer number of
// seconds or an HTTP-date. It returns 0 when:
//   - value is empty or whitespace,
//   - value is neither an integer nor a parseable HTTP-date,
//   - the encoded delay is negative or refers to a past moment.
func Parse(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	// Integer seconds form is the common case; try it first.
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	// HTTP-date form (RFC 7231): try the formats accepted by net/http.
	if t, err := http.ParseTime(value); err == nil {
		delta := t.Sub(Now())
		if delta <= 0 {
			return 0
		}
		return delta
	}
	return 0
}
