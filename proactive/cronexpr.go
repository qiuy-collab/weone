package proactive

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronSchedule struct {
	minutes  fieldSet
	hours    fieldSet
	days     fieldSet
	months   fieldSet
	weekdays fieldSet
}

type fieldSet struct {
	any    bool
	values map[int]bool
}

func parseCronStandard(spec string) (*cronSchedule, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected 5 cron fields, got %d", len(parts))
	}
	minutes, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hours, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	days, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month: %w", err)
	}
	months, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	weekdays, err := parseField(parts[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week: %w", err)
	}
	return &cronSchedule{minutes: minutes, hours: hours, days: days, months: months, weekdays: weekdays}, nil
}

func (s *cronSchedule) Next(after time.Time, loc *time.Location) time.Time {
	if s == nil {
		return time.Time{}
	}
	if loc == nil {
		loc = time.Local
	}
	candidate := after.In(loc).Add(time.Minute).Truncate(time.Minute)
	limit := candidate.AddDate(2, 0, 0)
	for !candidate.After(limit) {
		if s.matches(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

func (s *cronSchedule) matches(t time.Time) bool {
	return s.minutes.match(t.Minute()) &&
		s.hours.match(t.Hour()) &&
		s.days.match(t.Day()) &&
		s.months.match(int(t.Month())) &&
		s.weekdays.match(int(t.Weekday()))
}

func parseField(raw string, min, max int) (fieldSet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "*" {
		return fieldSet{any: true}, nil
	}
	set := fieldSet{values: map[int]bool{}}
	for _, piece := range strings.Split(raw, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return fieldSet{}, fmt.Errorf("empty segment")
		}
		if strings.HasPrefix(piece, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(piece, "*/"))
			if err != nil || step <= 0 {
				return fieldSet{}, fmt.Errorf("invalid step %q", piece)
			}
			for value := min; value <= max; value += step {
				set.values[value] = true
			}
			continue
		}
		if strings.Contains(piece, "-") {
			rangeParts := strings.SplitN(piece, "-", 2)
			start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err1 != nil || err2 != nil || start > end {
				return fieldSet{}, fmt.Errorf("invalid range %q", piece)
			}
			if start < min || end > max {
				return fieldSet{}, fmt.Errorf("range %q out of bounds", piece)
			}
			for value := start; value <= end; value++ {
				set.values[value] = true
			}
			continue
		}
		value, err := strconv.Atoi(piece)
		if err != nil {
			return fieldSet{}, fmt.Errorf("invalid value %q", piece)
		}
		if max == 6 && value == 7 {
			value = 0
		}
		if value < min || value > max {
			return fieldSet{}, fmt.Errorf("value %d out of bounds", value)
		}
		set.values[value] = true
	}
	if len(set.values) == 0 {
		return fieldSet{}, fmt.Errorf("no values parsed")
	}
	return set, nil
}

func (f fieldSet) match(value int) bool {
	if f.any {
		return true
	}
	return f.values[value]
}
