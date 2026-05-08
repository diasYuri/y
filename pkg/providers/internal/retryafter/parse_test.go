package retryafter

import (
	"testing"
	"time"
)

func TestParseEmpty(t *testing.T) {
	if got := Parse(""); got != 0 {
		t.Fatalf("Parse(\"\") = %v, want 0", got)
	}
	if got := Parse("   "); got != 0 {
		t.Fatalf("Parse(\"   \") = %v, want 0", got)
	}
}

func TestParseSeconds(t *testing.T) {
	if got := Parse("60"); got != 60*time.Second {
		t.Fatalf("Parse(\"60\") = %v, want 60s", got)
	}
	if got := Parse("0"); got != 0 {
		t.Fatalf("Parse(\"0\") = %v, want 0", got)
	}
}

func TestParseHTTPDateFuture(t *testing.T) {
	fixed := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	old := Now
	Now = func() time.Time { return fixed }
	defer func() { Now = old }()

	got := Parse("Wed, 21 Oct 2026 07:28:00 GMT")
	want := 28 * time.Minute
	if got != want {
		t.Fatalf("Parse future date = %v, want %v", got, want)
	}
}

func TestParseHTTPDatePast(t *testing.T) {
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := Now
	Now = func() time.Time { return fixed }
	defer func() { Now = old }()

	if got := Parse("Wed, 21 Oct 2026 07:28:00 GMT"); got != 0 {
		t.Fatalf("Parse past date = %v, want 0", got)
	}
}

func TestParseGarbage(t *testing.T) {
	if got := Parse("garbage"); got != 0 {
		t.Fatalf("Parse(\"garbage\") = %v, want 0", got)
	}
	if got := Parse("123abc"); got != 0 {
		t.Fatalf("Parse(\"123abc\") = %v, want 0", got)
	}
}
