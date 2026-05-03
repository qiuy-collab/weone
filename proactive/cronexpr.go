package proactive

import (
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

var standardCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func parseCronStandard(spec string) (cron.Schedule, error) {
	normalized, err := normalizeCronSpec(spec)
	if err != nil {
		return nil, err
	}
	schedule, err := standardCronParser.Parse(normalized)
	if err != nil {
		return nil, err
	}
	return schedule, nil
}

func nextCronRun(schedule cron.Schedule, after time.Time, loc *time.Location) time.Time {
	if schedule == nil {
		return time.Time{}
	}
	if loc == nil {
		loc = time.Local
	}
	candidate := schedule.Next(after.In(loc))
	if candidate.IsZero() {
		return time.Time{}
	}
	return candidate.In(loc)
}

func normalizeCronSpec(spec string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) == 6 {
		parts = parts[1:]
	}
	if len(parts) != 5 {
		return "", fmt.Errorf("expected 5 cron fields, got %d", len(parts))
	}
	if parts[4] == "7" {
		parts[4] = "0"
	}
	return strings.Join(parts, " "), nil
}
