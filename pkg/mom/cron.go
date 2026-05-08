package mom

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule represents a parsed standard 5-field cron expression with the
// fields: minute, hour, day-of-month, month, day-of-week.
type CronSchedule struct {
	expression string
	minutes    fieldSet
	hours      fieldSet
	doms       fieldSet
	months     fieldSet
	dows       fieldSet
	domStar    bool
	dowStar    bool
}

// Expression returns the original cron expression.
func (c CronSchedule) Expression() string { return c.expression }

// ParseCron parses a 5-field cron expression. Supported syntax:
//   - * for every value
//   - ranges (a-b)
//   - lists (a,b,c)
//   - steps (* /n or a-b/n)
//
// Day-of-week 0 and 7 both stand for Sunday. Day-of-month and day-of-week mix
// with OR semantics matching the standard cron behaviour when both are set.
func ParseCron(expr string) (CronSchedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return CronSchedule{}, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(parts), expr)
	}
	minutes, err := parseField(parts[0], 0, 59, nil)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("cron: minute: %w", err)
	}
	hours, err := parseField(parts[1], 0, 23, nil)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("cron: hour: %w", err)
	}
	doms, err := parseField(parts[2], 1, 31, nil)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("cron: dom: %w", err)
	}
	months, err := parseField(parts[3], 1, 12, monthAlias)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("cron: month: %w", err)
	}
	dows, err := parseField(parts[4], 0, 6, dayOfWeekAlias)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("cron: dow: %w", err)
	}
	return CronSchedule{
		expression: expr,
		minutes:    minutes,
		hours:      hours,
		doms:       doms,
		months:     months,
		dows:       dows,
		domStar:    parts[2] == "*",
		dowStar:    parts[4] == "*",
	}, nil
}

// Next returns the first time strictly after `from` that matches the cron
// expression in loc. If loc is nil, time.UTC is used.
func (c CronSchedule) Next(from time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t := from.In(loc).Add(time.Minute).Truncate(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !c.months.has(int(t.Month())) {
			t = startOfNextMonth(t)
			continue
		}
		if !c.matchesDay(t) {
			t = startOfNextDay(t)
			continue
		}
		if !c.hours.has(t.Hour()) {
			t = t.Add(time.Hour - time.Duration(t.Minute())*time.Minute).Truncate(time.Hour)
			continue
		}
		if !c.minutes.has(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (c CronSchedule) matchesDay(t time.Time) bool {
	dow := int(t.Weekday())
	domMatch := c.doms.has(t.Day())
	dowMatch := c.dows.has(dow)
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowMatch
	case c.dowStar:
		return domMatch
	default:
		return domMatch || dowMatch
	}
}

func startOfNextMonth(t time.Time) time.Time {
	year, month, _ := t.Date()
	return time.Date(year, month+1, 1, 0, 0, 0, 0, t.Location())
}

func startOfNextDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, t.Location())
}

type fieldSet struct {
	bits uint64
}

func (f fieldSet) has(v int) bool {
	if v < 0 || v >= 64 {
		return false
	}
	return f.bits&(uint64(1)<<uint(v)) != 0
}

func (f *fieldSet) set(v int) {
	if v < 0 || v >= 64 {
		return
	}
	f.bits |= uint64(1) << uint(v)
}

func parseField(field string, min, max int, alias map[string]int) (fieldSet, error) {
	var fs fieldSet
	parts := strings.Split(field, ",")
	for _, part := range parts {
		if part == "" {
			return fieldSet{}, fmt.Errorf("empty entry in %q", field)
		}
		step := 1
		body := part
		if idx := strings.Index(part, "/"); idx != -1 {
			s, err := strconv.Atoi(part[idx+1:])
			if err != nil || s <= 0 {
				return fieldSet{}, fmt.Errorf("invalid step in %q", part)
			}
			step = s
			body = part[:idx]
		}
		startVal, endVal := min, max
		if body != "*" {
			if idx := strings.Index(body, "-"); idx != -1 {
				lo, err := lookup(body[:idx], alias)
				if err != nil {
					return fieldSet{}, err
				}
				hi, err := lookup(body[idx+1:], alias)
				if err != nil {
					return fieldSet{}, err
				}
				startVal, endVal = lo, hi
			} else {
				v, err := lookup(body, alias)
				if err != nil {
					return fieldSet{}, err
				}
				startVal, endVal = v, v
				if step == 1 {
					if v < min || v > max {
						return fieldSet{}, fmt.Errorf("value %d out of range [%d,%d] in %q", v, min, max, field)
					}
					fs.set(normalizeDOW(v, min, max))
					continue
				}
				endVal = max
			}
		}
		if startVal < min || endVal > max || startVal > endVal {
			return fieldSet{}, fmt.Errorf("range %d-%d invalid for [%d,%d]", startVal, endVal, min, max)
		}
		for v := startVal; v <= endVal; v += step {
			fs.set(normalizeDOW(v, min, max))
		}
	}
	return fs, nil
}

func normalizeDOW(v, min, max int) int {
	if min == 0 && max == 6 && v == 7 {
		return 0
	}
	return v
}

func lookup(token string, alias map[string]int) (int, error) {
	token = strings.TrimSpace(token)
	if alias != nil {
		if v, ok := alias[strings.ToLower(token)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", token)
	}
	return v, nil
}

var monthAlias = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4,
	"may": 5, "jun": 6, "jul": 7, "aug": 8,
	"sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayOfWeekAlias = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3,
	"thu": 4, "fri": 5, "sat": 6,
}
