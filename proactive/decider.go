package proactive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type proactiveAction string

const (
	proactiveActionNone            proactiveAction = "none"
	proactiveActionCreateOneShot   proactiveAction = "create_one_shot"
	proactiveActionCreateRecurring proactiveAction = "create_recurring"
	proactiveActionUpdateExisting  proactiveAction = "update_existing"
	proactiveActionSkip            proactiveAction = "skip"
)

type proactiveDecision struct {
	Action                            proactiveAction `json:"action"`
	Kind                              string          `json:"kind"`
	Title                             string          `json:"title,omitempty"`
	Prompt                            string          `json:"prompt,omitempty"`
	EventText                         string          `json:"event_text,omitempty"`
	FireAt                            string          `json:"fire_at,omitempty"`
	CronExpr                          string          `json:"cron_expr,omitempty"`
	Timezone                          string          `json:"timezone,omitempty"`
	Reason                            string          `json:"reason,omitempty"`
	Confidence                        float64         `json:"confidence,omitempty"`
	ExistingTaskID                    string          `json:"existing_task_id,omitempty"`
	MemoryHabit                       string          `json:"memory_habit,omitempty"`
	RemindBeforeMinutes               int             `json:"remind_before_minutes,omitempty"`
	IdleForMinutes                    int             `json:"idle_for_minutes,omitempty"`
	CooldownHours                     int             `json:"cooldown_hours,omitempty"`
	CheckAfterLocalHour               int             `json:"check_after_local_hour,omitempty"`
	CheckBeforeLocalHour              int             `json:"check_before_local_hour,omitempty"`
	RequireRecentHistoryDays          int             `json:"require_recent_history_days,omitempty"`
	SkipIfRecentOutboundWithinMinutes int             `json:"skip_if_recent_outbound_within_minutes,omitempty"`
}

func (d proactiveDecision) taskKind() TaskKind {
	switch strings.TrimSpace(d.Kind) {
	case string(PolicyKindSilenceWakeup):
		return TaskKindSilenceWakeup
	case string(PolicyKindEventReminder):
		return TaskKindEventReminder
	default:
		return TaskKindEventReminder
	}
}

func (d proactiveDecision) policyKind() PolicyKind {
	switch strings.TrimSpace(d.Kind) {
	case string(PolicyKindSilenceWakeup):
		return PolicyKindSilenceWakeup
	case string(PolicyKindScheduledGreeting):
		return PolicyKindScheduledGreeting
	default:
		return PolicyKindEventReminder
	}
}

func (s *Service) DecideFromConversation(ctx context.Context, botID, userID, message, reply, memoryContext string) (proactiveDecision, error) {
	if s.runtimeSvc == nil {
		return proactiveDecision{Action: proactiveActionNone, Reason: "runtime service not configured"}, nil
	}
	policy, err := s.GetPolicy(PolicyKindEventReminder)
	if err != nil {
		return proactiveDecision{}, err
	}
	if !policy.Enabled {
		return proactiveDecision{Action: proactiveActionSkip, Reason: "event reminder policy disabled"}, nil
	}
	existing, err := s.ListTasks(TaskKindEventReminder.String(), botID)
	if err != nil {
		return proactiveDecision{}, err
	}
	prompt := buildConversationDecisionPrompt(policy, userID, message, reply, memoryContext, existing)
	result, err := s.runtimeSvc.Reply(ctx, fmt.Sprintf("proactive-decide:%s:%s", botID, userID), prompt, "")
	if err != nil {
		return proactiveDecision{}, err
	}
	decision, err := parseDecision(result)
	if err != nil {
		return proactiveDecision{}, err
	}
	if !policy.Config.AllowOneShot && decision.Action == proactiveActionCreateOneShot {
		decision.Action = proactiveActionSkip
		decision.Reason = defaultString(decision.Reason, "policy disabled one-shot reminders")
	}
	if !policy.Config.AllowRecurring && decision.Action == proactiveActionCreateRecurring {
		decision.Action = proactiveActionSkip
		decision.Reason = defaultString(decision.Reason, "policy disabled recurring reminders")
	}
	if decision.Timezone == "" {
		decision.Timezone = "Asia/Shanghai"
	}
	if decision.RemindBeforeMinutes == 0 {
		decision.RemindBeforeMinutes = policy.Config.RemindBeforeMinutes
	}
	return decision, nil
}

func (s *Service) DecideSilenceWakeup(ctx context.Context, policy Policy, now time.Time) (proactiveDecision, error) {
	if s.runtimeSvc == nil {
		return proactiveDecision{Action: proactiveActionNone, Reason: "runtime service not configured"}, nil
	}
	if !policy.Enabled {
		return proactiveDecision{Action: proactiveActionSkip, Reason: "silence wakeup policy disabled"}, nil
	}
	activity, err := s.getActivityTimestamps(policy.BotID, policy.ToUserID)
	if err != nil {
		return proactiveDecision{}, err
	}
	if activity.LastInboundAt.IsZero() {
		return proactiveDecision{Action: proactiveActionSkip, Reason: "no inbound activity recorded"}, nil
	}
	if now.Sub(activity.LastInboundAt) < time.Duration(policy.Config.IdleForMinutes)*time.Minute {
		return proactiveDecision{Action: proactiveActionSkip, Reason: "idle threshold not reached"}, nil
	}
	if policy.Config.CooldownHours > 0 && !activity.LastOutboundAt.IsZero() && now.Sub(activity.LastOutboundAt) < time.Duration(policy.Config.CooldownHours)*time.Hour {
		return proactiveDecision{Action: proactiveActionSkip, Reason: "cooldown not reached"}, nil
	}
	prompt := buildSilenceDecisionPrompt(policy, activity.LastInboundAt, activity.LastOutboundAt, now)
	result, err := s.runtimeSvc.Reply(ctx, fmt.Sprintf("proactive-silence:%s:%s", policy.BotID, policy.ToUserID), prompt, "")
	if err != nil {
		return proactiveDecision{}, err
	}
	decision, err := parseDecision(result)
	if err != nil {
		return proactiveDecision{}, err
	}
	if decision.Action == proactiveActionCreateRecurring {
		decision.Action = proactiveActionCreateOneShot
	}
	decision.Kind = string(PolicyKindSilenceWakeup)
	if decision.FireAt == "" {
		decision.FireAt = now.UTC().Format(time.RFC3339)
	}
	if decision.IdleForMinutes == 0 {
		decision.IdleForMinutes = policy.Config.IdleForMinutes
	}
	if decision.CooldownHours == 0 {
		decision.CooldownHours = policy.Config.CooldownHours
	}
	if decision.CheckAfterLocalHour == 0 {
		decision.CheckAfterLocalHour = policy.Config.CheckAfterLocalHour
	}
	if decision.CheckBeforeLocalHour == 0 {
		decision.CheckBeforeLocalHour = policy.Config.CheckBeforeLocalHour
	}
	if decision.RequireRecentHistoryDays == 0 {
		decision.RequireRecentHistoryDays = policy.Config.RequireRecentHistoryDays
	}
	if decision.SkipIfRecentOutboundWithinMinutes == 0 {
		decision.SkipIfRecentOutboundWithinMinutes = 30
	}
	if decision.Timezone == "" {
		decision.Timezone = "Asia/Shanghai"
	}
	return decision, nil
}


func parseDecision(raw string) (proactiveDecision, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)
	var decision proactiveDecision
	if err := json.Unmarshal([]byte(clean), &decision); err != nil {
		return proactiveDecision{}, fmt.Errorf("parse proactive decision: %w", err)
	}
	return decision, nil
}

func buildConversationDecisionPrompt(policy Policy, userID, message, reply, memoryContext string, existing []Task) string {
	var existingBuilder strings.Builder
	for _, task := range existing {
		if task.ToUserID != userID || !task.Enabled {
			continue
		}
		existingBuilder.WriteString("- id=")
		existingBuilder.WriteString(task.ID)
		existingBuilder.WriteString(" title=")
		existingBuilder.WriteString(task.Title)
		existingBuilder.WriteString(" cron=")
		existingBuilder.WriteString(task.Schedule.CronExpr)
		existingBuilder.WriteString(" fire_at=")
		existingBuilder.WriteString(task.Schedule.FireAt)
		existingBuilder.WriteString(" event=")
		existingBuilder.WriteString(task.Schedule.EventText)
		existingBuilder.WriteString("\n")
	}
	return fmt.Sprintf(`你是主动提醒决策器。请根据用户消息、AI回复、长期记忆，判断是否需要创建提醒任务。

要求：
1. 只输出 JSON，不要输出任何额外解释。
2. action 只能是 none / create_one_shot / create_recurring / update_existing / skip。
3. kind 固定填 event_reminder。
4. 对“明天、后天、某个具体时间”的临时事项，用 create_one_shot + fire_at。
5. 对“每天/每周/上班/上学/吃药”等长期规律，用 create_recurring + cron_expr。
6. cron_expr 必须严格返回 5 段标准 cron：分 时 日 月 周，绝对不要返回秒字段。
7. 示例：每天早上八点是 "0 8 * * *"；每周一早上八点是 "0 8 * * 1"。
8. 若与已有任务重复，优先 update_existing，并填写 existing_task_id。
9. 如果不该创建，返回 skip 或 none，并给 reason。
10. timezone 默认 Asia/Shanghai。
11. recurring 时可补 memory_habit，简要描述长期习惯。
12. 现在的绝对时间是：%s；当前时区是：Asia/Shanghai。请把“明天/后天/今晚/下午两点”这类相对时间换算成可靠的 fire_at。

策略：
- allow_one_shot=%v
- allow_recurring=%v
- default_remind_before_minutes=%d

当前用户消息：%s
当前 AI 回复：%s
长期/短期记忆上下文：%s
已有提醒任务：
%s

JSON 字段示例：
{"action":"create_one_shot","kind":"event_reminder","title":"明早上班提醒","event_text":"提醒你明天上班前别忘了出门","fire_at":"2026-05-03T08:30:00+08:00","timezone":"Asia/Shanghai","reason":"用户明确说明明天九点上班","confidence":0.92}
`, time.Now().In(time.FixedZone("CST", 8*60*60)).Format(time.RFC3339), policy.Config.AllowOneShot, policy.Config.AllowRecurring, policy.Config.RemindBeforeMinutes, message, reply, memoryContext, existingBuilder.String())
}

func buildSilenceDecisionPrompt(policy Policy, latestInbound, latestOutbound, now time.Time) string {
	return fmt.Sprintf(`你是主动聊天决策器。请根据沉默状态决定是否应该现在主动发一条消息。

要求：
1. 只输出 JSON，不要输出任何额外解释。
2. action 只能是 create_one_shot / skip / none。
3. kind 固定填 silence_wakeup。
4. 如果应该主动触达，用 create_one_shot，并给出 title、prompt、reason、confidence。
5. fire_at 可以直接填当前时间 %s。
6. 若不适合主动打扰，就返回 skip，并说明 reason。
7. 语气要自然、轻打扰、有主动性。

策略：
- idle_for_minutes=%d
- cooldown_hours=%d
- active_hours=%02d-%02d
- recent_history_days=%d
- prompt_template=%s

最近一次入站：%s
最近一次出站：%s
当前时间：%s

JSON 示例：
{"action":"create_one_shot","kind":"silence_wakeup","title":"沉默后主动关心","prompt":"自然问候一下对方今天过得怎么样","fire_at":"%s","timezone":"Asia/Shanghai","reason":"用户沉默已超过阈值，且仍在适合触达的时间窗内","confidence":0.81}
`, now.UTC().Format(time.RFC3339), policy.Config.IdleForMinutes, policy.Config.CooldownHours, policy.Config.CheckAfterLocalHour, policy.Config.CheckBeforeLocalHour, policy.Config.RequireRecentHistoryDays, policy.Config.PromptTemplate, latestInbound.UTC().Format(time.RFC3339), latestOutbound.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
}

func (k TaskKind) String() string {
	return string(k)
}
