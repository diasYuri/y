package mom

import (
	"testing"
	"time"
)

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"abc * * * *",
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("expected error for %q", expr)
		}
	}
}

func TestParseCronWildcard(t *testing.T) {
	c, err := ParseCron("* * * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	got := c.Next(now, time.UTC)
	want := now.Add(time.Minute)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestParseCronDailyMorning(t *testing.T) {
	c, err := ParseCron("0 9 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	loc, _ := time.LoadLocation("UTC")
	from := time.Date(2026, 5, 1, 8, 30, 0, 0, loc)
	got := c.Next(from, loc)
	want := time.Date(2026, 5, 1, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestParseCronWeekdayRange(t *testing.T) {
	c, err := ParseCron("0 9 * * 1-5")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	saturday := time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC) // Saturday
	got := c.Next(saturday, time.UTC)
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC) // Monday
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestParseCronStep(t *testing.T) {
	c, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := time.Date(2026, 5, 1, 12, 7, 0, 0, time.UTC)
	got := c.Next(from, time.UTC)
	want := time.Date(2026, 5, 1, 12, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestParseCronList(t *testing.T) {
	c, err := ParseCron("0,30 9 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := time.Date(2026, 5, 1, 9, 5, 0, 0, time.UTC)
	got := c.Next(from, time.UTC)
	want := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestCronExpression(t *testing.T) {
	c, err := ParseCron("0 9 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if got := c.Expression(); got != "0 9 * * *" {
		t.Fatalf("Expression = %q", got)
	}
}
