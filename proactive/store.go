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
			return Library{Tasks: []Task{}, Policies: defaultPolicies(), Activities: []ActivityState{}}, nil
		}
		return Library{}, fmt.Errorf("read tasks: %w", err)
	}
	if len(data) == 0 {
		return Library{Tasks: []Task{}, Policies: defaultPolicies(), Activities: []ActivityState{}}, nil
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("decode tasks: %w", err)
	}
	lib.Tasks = normalizeTasks(lib.Tasks)
	lib.Policies = normalizePolicies(lib.Policies)
	lib.Activities = normalizeActivities(lib.Activities)
	return lib, nil
}

func (s *store) saveLibrary(lib Library) (Library, error) {
	if err := ensureProactiveDir(); err != nil {
		return Library{}, err
	}
	lib.Tasks = normalizeTasks(lib.Tasks)
	lib.Policies = normalizePolicies(lib.Policies)
	lib.Activities = normalizeActivities(lib.Activities)
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
	item.Source = normalizeTaskSource(item.Source)
	item.Status = normalizeTaskStatus(item.Status)
	item.PolicyKind = normalizePolicyKind(item.PolicyKind)
	item.DecisionReason = strings.TrimSpace(item.DecisionReason)
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
	if item.Source == "" {
		item.Source = TaskSourceManual
	}
	if item.Status == "" {
		if item.Enabled {
			item.Status = TaskStatusActive
		} else {
			item.Status = TaskStatusDone
		}
	}
	if item.PolicyKind == "" {
		switch item.Kind {
		case TaskKindScheduledGreeting:
			item.PolicyKind = PolicyKindScheduledGreeting
		case TaskKindSilenceWakeup:
			item.PolicyKind = PolicyKindSilenceWakeup
		case TaskKindEventReminder:
			item.PolicyKind = PolicyKindEventReminder
		}
	}
	return item
}


func normalizeActivities(items []ActivityState) []ActivityState {
	result := make([]ActivityState, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = normalizeActivity(item)
		if item.BotID == "" || item.ToUserID == "" {
			continue
		}
		key := item.BotID + "\x00" + item.ToUserID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			if result[i].BotID == result[j].BotID {
				return result[i].ToUserID < result[j].ToUserID
			}
			return result[i].BotID < result[j].BotID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func normalizeActivity(item ActivityState) ActivityState {
	item.BotID = strings.TrimSpace(item.BotID)
	item.ToUserID = strings.TrimSpace(item.ToUserID)
	item.LastInboundAt = item.LastInboundAt.UTC()
	item.LastOutboundAt = item.LastOutboundAt.UTC()
	item.LastSilenceCheckAt = item.LastSilenceCheckAt.UTC()
	item.LastSilenceDueAt = item.LastSilenceDueAt.UTC()
	item.LastSilenceReason = strings.TrimSpace(item.LastSilenceReason)
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item
}

func normalizePolicies(items []Policy) []Policy {
	byKind := make(map[PolicyKind]Policy, len(items)+3)
	for _, item := range defaultPolicies() {
		byKind[item.Kind] = item
	}
	for _, item := range items {
		item = normalizePolicy(item)
		if item.Kind == "" {
			continue
		}
		byKind[item.Kind] = item
	}
	orderedKinds := []PolicyKind{
		PolicyKindScheduledGreeting,
		PolicyKindSilenceWakeup,
		PolicyKindEventReminder,
	}
	result := make([]Policy, 0, len(orderedKinds))
	for _, kind := range orderedKinds {
		result = append(result, byKind[kind])
	}
	return result
}

func normalizePolicy(item Policy) Policy {
	item.Kind = normalizePolicyKind(item.Kind)
	item.BotID = strings.TrimSpace(item.BotID)
	item.ToUserID = strings.TrimSpace(item.ToUserID)
	item.Title = strings.TrimSpace(item.Title)
	item.Config.PromptTemplate = strings.TrimSpace(item.Config.PromptTemplate)
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item
}


func defaultPolicies() []Policy {
	now := time.Now().UTC()
	return []Policy{
		{
			Kind:      PolicyKindScheduledGreeting,
			Enabled:   true,
			Title:     "定时问候",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			Kind:      PolicyKindSilenceWakeup,
			Enabled:   false,
			Title:     "沉默唤醒",
			CreatedAt: now,
			UpdatedAt: now,
			Config: PolicyConfig{
				IdleForMinutes:           5,
				CooldownHours:            6,
				CheckAfterLocalHour:      9,
				CheckBeforeLocalHour:     22,
				RequireRecentHistoryDays: 7,
				PromptTemplate:           "请自然地主动关心用户，避免打扰感。",
			},
		},
		{
			Kind:      PolicyKindEventReminder,
			Enabled:   true,
			Title:     "事件提醒",
			CreatedAt: now,
			UpdatedAt: now,
			Config: PolicyConfig{
				AllowOneShot:        true,
				AllowRecurring:      true,
				RemindBeforeMinutes: 30,
			},
		},
	}
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

func normalizeTaskSource(source TaskSource) TaskSource {
	switch strings.ToLower(strings.TrimSpace(string(source))) {
	case string(TaskSourceAI):
		return TaskSourceAI
	case string(TaskSourceManual):
		return TaskSourceManual
	default:
		return TaskSource("")
	}
}

func normalizeTaskStatus(status TaskStatus) TaskStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(TaskStatusActive):
		return TaskStatusActive
	case string(TaskStatusDone):
		return TaskStatusDone
	case string(TaskStatusCancelled):
		return TaskStatusCancelled
	default:
		return TaskStatus("")
	}
}

func normalizePolicyKind(kind PolicyKind) PolicyKind {
	switch strings.ToLower(strings.TrimSpace(string(kind))) {
	case string(PolicyKindScheduledGreeting):
		return PolicyKindScheduledGreeting
	case string(PolicyKindSilenceWakeup):
		return PolicyKindSilenceWakeup
	case string(PolicyKindEventReminder):
		return PolicyKindEventReminder
	default:
		return PolicyKind("")
	}
}

func nextRunString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
