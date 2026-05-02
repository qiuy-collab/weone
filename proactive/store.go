package proactive

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type store struct{}

func newStore() *store {
	return &store{}
}

func (s *store) loadLibrary() (Library, error) {
	path, err := tasksPath()
	if err != nil {
		return Library{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Library{Tasks: []Task{}}, nil
		}
		return Library{}, fmt.Errorf("read tasks: %w", err)
	}
	if len(data) == 0 {
		return Library{Tasks: []Task{}}, nil
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("decode tasks: %w", err)
	}
	lib.Tasks = normalizeTasks(lib.Tasks)
	return lib, nil
}

func (s *store) saveLibrary(lib Library) (Library, error) {
	if err := ensureProactiveDir(); err != nil {
		return Library{}, err
	}
	lib.Tasks = normalizeTasks(lib.Tasks)
	path, err := tasksPath()
	if err != nil {
		return Library{}, err
	}
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return Library{}, fmt.Errorf("encode tasks: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return Library{}, fmt.Errorf("write tasks: %w", err)
	}
	return lib, nil
}

func normalizeTasks(items []Task) []Task {
	result := make([]Task, 0, len(items))
	for _, item := range items {
		item = normalizeTask(item)
		if item.ID == "" || item.Kind == "" || item.BotID == "" || item.ToUserID == "" || item.Title == "" {
			continue
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func normalizeTask(item Task) Task {
	item.ID = strings.TrimSpace(item.ID)
	item.Kind = normalizeTaskKind(item.Kind)
	item.BotID = strings.TrimSpace(item.BotID)
	item.ToUserID = strings.TrimSpace(item.ToUserID)
	item.Title = strings.TrimSpace(item.Title)
	item.Prompt = strings.TrimSpace(item.Prompt)
	item.Schedule.Timezone = strings.TrimSpace(item.Schedule.Timezone)
	item.Schedule.CronExpr = strings.TrimSpace(item.Schedule.CronExpr)
	item.Schedule.FireAt = strings.TrimSpace(item.Schedule.FireAt)
	item.Schedule.EventText = strings.TrimSpace(item.Schedule.EventText)
	item.State.LastError = strings.TrimSpace(item.State.LastError)
	item.State.LastReply = strings.TrimSpace(item.State.LastReply)
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.State.LastRunAt = item.State.LastRunAt.UTC()
	item.State.LastSuccessAt = item.State.LastSuccessAt.UTC()
	item.State.LastErrorAt = item.State.LastErrorAt.UTC()
	item.State.LastInboundAt = item.State.LastInboundAt.UTC()
	item.State.LastOutboundAt = item.State.LastOutboundAt.UTC()
	item.State.NextRunAt = item.State.NextRunAt.UTC()
	return item
}

func normalizeTaskKind(kind TaskKind) TaskKind {
	switch strings.ToLower(strings.TrimSpace(string(kind))) {
	case string(TaskKindScheduledGreeting):
		return TaskKindScheduledGreeting
	case string(TaskKindSilenceWakeup):
		return TaskKindSilenceWakeup
	case string(TaskKindEventReminder):
		return TaskKindEventReminder
	default:
		return TaskKind("")
	}
}

func nextRunString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
