package proactive

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qiuy-collab/weone/ilink"
	internalruntime "github.com/qiuy-collab/weone/internal/runtime"
	"github.com/qiuy-collab/weone/materials"
	"github.com/qiuy-collab/weone/memory"
	"github.com/qiuy-collab/weone/messaging"
)

type ClientProvider interface {
	Client(botID string) *ilink.Client
}

type schedulerSyncer interface {
	SyncTask(task Task) error
	RemoveTask(taskID string)
	Snapshot() SchedulerSnapshot
}

type activityTimestamps struct {
	LastInboundAt  time.Time
	LastOutboundAt time.Time
}

type Service struct {
	mu           sync.RWMutex
	store        *store
	runtimeSvc   *internalruntime.Service
	memorySvc    *memory.Service
	materialsSvc *materials.Service
	clients      ClientProvider
	scheduler    schedulerSyncer
}

func NewService(runtimeSvc *internalruntime.Service, memorySvc *memory.Service, materialsSvc *materials.Service, clients ClientProvider) *Service {
	return &Service{
		store:        newStore(),
		runtimeSvc:   runtimeSvc,
		memorySvc:    memorySvc,
		materialsSvc: materialsSvc,
		clients:      clients,
	}
}

func (s *Service) SetScheduler(scheduler schedulerSyncer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler = scheduler
}

func (s *Service) ListTasks(kind, botID string) ([]Task, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return nil, err
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	botID = strings.TrimSpace(botID)
	items := make([]Task, 0, len(lib.Tasks))
	for _, task := range lib.Tasks {
		task = s.enrichTask(task)
		if kind != "" && strings.ToLower(string(task.Kind)) != kind {
			continue
		}
		if botID != "" && task.BotID != botID {
			continue
		}
		items = append(items, task)
	}
	return items, nil
}

func (s *Service) ListTaskItems() ([]TaskListItem, error) {
	tasks, err := s.ListTasks("", "")
	if err != nil {
		return nil, err
	}
	items := make([]TaskListItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, s.taskListItem(task))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Bucket == items[j].Bucket {
			return items[i].ExecuteAt < items[j].ExecuteAt
		}
		return items[i].Bucket < items[j].Bucket
	})
	return items, nil
}

func (s *Service) ListTargetOptions() ([]TargetOption, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return nil, err
	}
	optionsByBot := map[string]TargetOption{}
	mergeOption := func(botID, toUserID string) {
		botID = strings.TrimSpace(botID)
		if botID == "" {
			return
		}
		toUserID = strings.TrimSpace(toUserID)
		label := botID
		if toUserID != "" {
			label = botID + " · " + toUserID
		}
		if existing, ok := optionsByBot[botID]; ok {
			if existing.ToUserID == "" && toUserID != "" {
				optionsByBot[botID] = TargetOption{BotID: botID, ToUserID: toUserID, Label: label}
			}
			return
		}
		optionsByBot[botID] = TargetOption{BotID: botID, ToUserID: toUserID, Label: label}
	}

	accounts, err := ilink.ListAccounts(nil)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		mergeOption(account.BotID, account.UserID)
	}
	for _, task := range lib.Tasks {
		mergeOption(task.BotID, task.ToUserID)
	}
	for _, policy := range lib.Policies {
		mergeOption(policy.BotID, policy.ToUserID)
	}
	items := make([]TargetOption, 0, len(optionsByBot))
	for _, item := range optionsByBot {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Label < items[j].Label
	})
	return items, nil
}

func (s *Service) readActivity(botID, userID string) (ActivityState, bool, error) {
	botID = strings.TrimSpace(botID)
	userID = strings.TrimSpace(userID)
	if botID == "" || userID == "" {
		return ActivityState{}, false, nil
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return ActivityState{}, false, err
	}
	for _, activity := range lib.Activities {
		if activity.BotID == botID && activity.ToUserID == userID {
			return activity, true, nil
		}
	}
	return ActivityState{}, false, nil
}

func (s *Service) getActivityTimestamps(botID, userID string) (activityTimestamps, error) {
	activity, ok, err := s.readActivity(botID, userID)
	if err != nil {
		return activityTimestamps{}, err
	}
	if ok {
		return activityTimestamps{LastInboundAt: activity.LastInboundAt, LastOutboundAt: activity.LastOutboundAt}, nil
	}
	return s.legacyActivityFromTasks(botID, userID)
}

func (s *Service) legacyActivityFromTasks(botID, userID string) (activityTimestamps, error) {
	tasks, err := s.ListTasks("", botID)
	if err != nil {
		return activityTimestamps{}, err
	}
	var latest activityTimestamps
	for _, task := range tasks {
		if task.ToUserID != userID {
			continue
		}
		if task.State.LastInboundAt.After(latest.LastInboundAt) {
			latest.LastInboundAt = task.State.LastInboundAt
		}
		if task.State.LastOutboundAt.After(latest.LastOutboundAt) {
			latest.LastOutboundAt = task.State.LastOutboundAt
		}
	}
	return latest, nil
}

func (s *Service) updateActivity(botID, userID string, mutate func(*ActivityState)) error {
	botID = strings.TrimSpace(botID)
	userID = strings.TrimSpace(userID)
	if botID == "" || userID == "" {
		return nil
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range lib.Activities {
		if lib.Activities[i].BotID != botID || lib.Activities[i].ToUserID != userID {
			continue
		}
		mutate(&lib.Activities[i])
		lib.Activities[i] = normalizeActivity(lib.Activities[i])
		if lib.Activities[i].CreatedAt.IsZero() {
			lib.Activities[i].CreatedAt = now
		}
		lib.Activities[i].UpdatedAt = now
		_, err = s.store.saveLibrary(lib)
		return err
	}
	activity := normalizeActivity(ActivityState{BotID: botID, ToUserID: userID, CreatedAt: now, UpdatedAt: now})
	mutate(&activity)
	activity = normalizeActivity(activity)
	activity.CreatedAt = now
	activity.UpdatedAt = now
	lib.Activities = append(lib.Activities, activity)
	_, err = s.store.saveLibrary(lib)
	return err
}

func (s *Service) taskListItem(task Task) TaskListItem {
	executeAt := ""
	if task.Schedule.FireAt != "" {
		executeAt = task.Schedule.FireAt
	} else if !task.State.NextRunAt.IsZero() {
		executeAt = task.State.NextRunAt.UTC().Format(time.RFC3339)
	}
	bucket, bucketLabel := classifyTaskBucket(task)
	return TaskListItem{
		ID:             task.ID,
		Kind:           task.Kind,
		Source:         task.Source,
		Status:         task.Status,
		Enabled:        task.Enabled,
		Title:          task.Title,
		Content:        s.taskContent(task),
		ExecuteAt:      executeAt,
		BotID:          task.BotID,
		ToUserID:       task.ToUserID,
		PolicyKind:     task.PolicyKind,
		Bucket:         bucket,
		BucketLabel:    bucketLabel,
		IsRecurring:    task.Schedule.CronExpr != "",
		HasError:       strings.TrimSpace(task.State.LastError) != "",
		LastStatusText: taskLastStatusText(task),
	}
}

func classifyTaskBucket(task Task) (string, string) {
	if strings.TrimSpace(task.State.LastError) != "" {
		return "error", "执行错误"
	}
	if task.Status == TaskStatusDone || task.Status == TaskStatusCancelled || (!task.Enabled && task.Schedule.OneShot) {
		return "finished", "已结束"
	}
	if strings.TrimSpace(task.Schedule.CronExpr) != "" {
		return "daily_loop", "每日循环"
	}
	return "queued", "排队中"
}

func taskLastStatusText(task Task) string {
	if text := strings.TrimSpace(task.State.LastError); text != "" {
		return "发送失败"
	}
	if task.Status == TaskStatusDone {
		return "已结束"
	}
	if task.Status == TaskStatusCancelled {
		return "已取消"
	}
	if !task.State.LastSuccessAt.IsZero() {
		return "最近执行成功"
	}
	if !task.Enabled {
		return "已关闭"
	}
	if strings.TrimSpace(task.Schedule.CronExpr) != "" {
		return "循环等待中"
	}
	return "等待执行"
}

func (s *Service) taskContent(task Task) string {
	if text := strings.TrimSpace(task.Schedule.EventText); text != "" {
		return text
	}
	if text := strings.TrimSpace(task.Prompt); text != "" {
		return text
	}
	return strings.TrimSpace(task.Title)
}

func (s *Service) ListPolicies() ([]Policy, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return nil, err
	}
	return append([]Policy(nil), lib.Policies...), nil
}

func (s *Service) GetPolicy(kind PolicyKind) (Policy, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Policy{}, err
	}
	kind = normalizePolicyKind(kind)
	for _, policy := range lib.Policies {
		if policy.Kind == kind {
			return policy, nil
		}
	}
	return Policy{}, fmt.Errorf("policy not found")
}

func (s *Service) UpsertPolicy(input UpsertPolicyInput) (Policy, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Policy{}, err
	}
	policy, err := s.normalizePolicyInput(input)
	if err != nil {
		return Policy{}, err
	}
	now := time.Now().UTC()
	for i := range lib.Policies {
		if lib.Policies[i].Kind != policy.Kind {
			continue
		}
		policy.CreatedAt = lib.Policies[i].CreatedAt
		if policy.CreatedAt.IsZero() {
			policy.CreatedAt = now
		}
		policy.UpdatedAt = now
		lib.Policies[i] = policy
		if _, err := s.store.saveLibrary(lib); err != nil {
			return Policy{}, err
		}
		log.Printf("[proactive] policy=%s stage=policy state=updated enabled=%v bot_id=%s to_user_id=%s", policy.Kind, policy.Enabled, policy.BotID, policy.ToUserID)
		return policy, nil
	}
	policy.CreatedAt = now
	policy.UpdatedAt = now
	lib.Policies = append(lib.Policies, policy)
	if _, err := s.store.saveLibrary(lib); err != nil {
		return Policy{}, err
	}
	log.Printf("[proactive] policy=%s stage=policy state=created enabled=%v bot_id=%s to_user_id=%s", policy.Kind, policy.Enabled, policy.BotID, policy.ToUserID)
	return policy, nil
}

func (s *Service) normalizePolicyInput(input UpsertPolicyInput) (Policy, error) {
	policy := normalizePolicy(Policy{
		Kind:     input.Kind,
		Enabled:  input.Enabled,
		BotID:    input.BotID,
		ToUserID: input.ToUserID,
		Title:    input.Title,
		Config:   input.Config,
	})
	if policy.Kind == "" {
		return Policy{}, fmt.Errorf("kind is required")
	}
	if policy.Title == "" {
		switch policy.Kind {
		case PolicyKindScheduledGreeting:
			policy.Title = "定时问候"
		case PolicyKindSilenceWakeup:
			policy.Title = "沉默唤醒"
		case PolicyKindEventReminder:
			policy.Title = "事件提醒"
		}
	}
	if policy.Kind == PolicyKindEventReminder {
		if !policy.Config.AllowOneShot && !policy.Config.AllowRecurring {
			return Policy{}, fmt.Errorf("event_reminder policy must allow one-shot or recurring")
		}
	}
	if policy.Kind == PolicyKindSilenceWakeup && policy.Enabled {
		if policy.BotID == "" {
			return Policy{}, fmt.Errorf("bot_id is required for enabled silence_wakeup policy")
		}
		if policy.ToUserID == "" {
			return Policy{}, fmt.Errorf("to_user_id is required for enabled silence_wakeup policy")
		}
		if policy.Config.IdleForMinutes <= 0 {
			return Policy{}, fmt.Errorf("config.idle_for_minutes is required for silence_wakeup")
		}
	}
	return policy, nil
}

func (s *Service) GetTask(id string) (Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, fmt.Errorf("id is required")
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Task{}, err
	}
	for _, task := range lib.Tasks {
		if task.ID == id {
			return s.enrichTask(task), nil
		}
	}
	return Task{}, fmt.Errorf("task not found")
}

func (s *Service) UpsertTask(input UpsertTaskInput) (Task, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Task{}, err
	}
	task, err := s.normalizeInput(input)
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	for i := range lib.Tasks {
		if lib.Tasks[i].ID != task.ID || task.ID == "" {
			continue
		}
		existing := lib.Tasks[i]
		task.CreatedAt = existing.CreatedAt
		if task.CreatedAt.IsZero() {
			task.CreatedAt = now
		}
		task.UpdatedAt = now
		task.State = existing.State
		lib.Tasks[i] = task
		if _, err := s.store.saveLibrary(lib); err != nil {
			return Task{}, err
		}
		if err := s.syncTask(task); err != nil {
			return Task{}, err
		}
		updated := s.enrichTask(task)
		log.Printf("[proactive] task=%s stage=task state=updated kind=%s source=%s policy=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q enabled=%v", updated.ID, updated.Kind, updated.Source, updated.PolicyKind, updated.BotID, updated.ToUserID, updated.Title, updated.Schedule.CronExpr, updated.Schedule.FireAt, updated.Schedule.Timezone, updated.Enabled)
		return updated, nil
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	task.CreatedAt = now
	task.UpdatedAt = now
	lib.Tasks = append(lib.Tasks, task)
	if _, err := s.store.saveLibrary(lib); err != nil {
		return Task{}, err
	}
	if err := s.syncTask(task); err != nil {
		return Task{}, err
	}
	created := s.enrichTask(task)
	log.Printf("[proactive] task=%s stage=task state=created kind=%s source=%s policy=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q enabled=%v", created.ID, created.Kind, created.Source, created.PolicyKind, created.BotID, created.ToUserID, created.Title, created.Schedule.CronExpr, created.Schedule.FireAt, created.Schedule.Timezone, created.Enabled)
	return created, nil
}

func (s *Service) DeleteTask(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return err
	}
	filtered := make([]Task, 0, len(lib.Tasks))
	found := false
	var deleted Task
	for _, task := range lib.Tasks {
		if task.ID == id {
			found = true
			deleted = task
			continue
		}
		filtered = append(filtered, task)
	}
	if !found {
		return fmt.Errorf("task not found")
	}
	lib.Tasks = filtered
	if _, err := s.store.saveLibrary(lib); err != nil {
		return err
	}
	s.removeTask(id)
	log.Printf("[proactive] task=%s stage=task state=deleted kind=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q enabled=%v", deleted.ID, deleted.Kind, deleted.BotID, deleted.ToUserID, deleted.Title, deleted.Schedule.CronExpr, deleted.Schedule.FireAt, deleted.Schedule.Timezone, deleted.Enabled)
	return nil
}

func (s *Service) DeleteTasks(ids []string) error {
	trimmed := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		trimmed = append(trimmed, id)
	}
	if len(trimmed) == 0 {
		return fmt.Errorf("ids are required")
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return err
	}
	keep := make([]Task, 0, len(lib.Tasks))
	deleteSet := make(map[string]bool, len(trimmed))
	for _, id := range trimmed {
		deleteSet[id] = true
	}
	deletedCount := 0
	for _, task := range lib.Tasks {
		if deleteSet[task.ID] {
			deletedCount++
			continue
		}
		keep = append(keep, task)
	}
	if deletedCount == 0 {
		return fmt.Errorf("task not found")
	}
	lib.Tasks = keep
	if _, err := s.store.saveLibrary(lib); err != nil {
		return err
	}
	for id := range deleteSet {
		s.removeTask(id)
	}
	log.Printf("[proactive] stage=task state=batch-deleted count=%d", deletedCount)
	return nil
}

func (s *Service) ExecuteTask(ctx context.Context, taskID string) (ExecuteResult, error) {
	task, err := s.GetTask(taskID)
	if err != nil {
		return ExecuteResult{}, err
	}
	result := ExecuteResult{Task: task, ExecutedAt: time.Now().UTC()}
	log.Printf("[proactive] task=%s stage=execute state=requested kind=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, task.Schedule.CronExpr, task.Schedule.FireAt, task.Schedule.Timezone)
	now := result.ExecutedAt
	skip, reason := s.shouldSkip(task, now)
	if skip {
		result.Skipped = true
		result.SkipReason = reason
		log.Printf("[proactive] task=%s stage=execute state=skipped kind=%s bot_id=%s to_user_id=%s title=%q reason=%q", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, reason)
		return result, nil
	}
	client := s.clientForTask(task)
	if client == nil {
		err := fmt.Errorf("bot client not available")
		updated, updateErr := s.markTaskFailure(task.ID, now, err)
		result.Task = updated
		if updateErr != nil {
			return result, updateErr
		}
		log.Printf("[proactive] task=%s stage=execute state=failed kind=%s bot_id=%s to_user_id=%s title=%q err=%v", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, err)
		return result, err
	}
	reply, mediaCount, execErr := s.generateAndSend(ctx, client, task, now)
	if execErr != nil {
		updated, updateErr := s.markTaskFailure(task.ID, now, execErr)
		result.Task = updated
		if updateErr != nil {
			return result, updateErr
		}
		log.Printf("[proactive] task=%s stage=execute state=failed kind=%s bot_id=%s to_user_id=%s title=%q err=%v", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, execErr)
		return result, execErr
	}
	updated, updateErr := s.markTaskSuccess(task.ID, result.ExecutedAt, reply)
	result.Task = updated
	result.Reply = reply
	result.SentMedia = mediaCount
	if updateErr != nil {
		log.Printf("[proactive] task=%s stage=execute state=failed kind=%s bot_id=%s to_user_id=%s title=%q err=%v", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, updateErr)
		return result, updateErr
	}
	if task.Schedule.OneShot {
		updated, updateErr = s.disableTaskAfterOneShot(task.ID, result.ExecutedAt)
		result.Task = updated
		if updateErr != nil {
			log.Printf("[proactive] task=%s stage=execute state=failed kind=%s bot_id=%s to_user_id=%s title=%q err=%v", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, updateErr)
			return result, updateErr
		}
	}
	log.Printf("[proactive] task=%s stage=execute state=success kind=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q sent_media=%d reply=%q", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, task.Schedule.CronExpr, task.Schedule.FireAt, task.Schedule.Timezone, result.SentMedia, truncateProactiveLog(reply, 120))
	return result, nil
}

func (s *Service) RecordInbound(botID, userID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.updateActivity(botID, userID, func(a *ActivityState) {
		a.LastInboundAt = at.UTC()
		a.LastSilenceCheckAt = time.Time{}
		a.LastSilenceDueAt = time.Time{}
		a.LastSilenceReason = ""
	}); err != nil {
		return err
	}
	return s.updateMatchingTasks(botID, userID, func(t *Task) {
		t.State.LastInboundAt = at.UTC()
		t.UpdatedAt = time.Now().UTC()
	})
}

func (s *Service) RecordOutbound(botID, userID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.updateActivity(botID, userID, func(a *ActivityState) {
		a.LastOutboundAt = at.UTC()
	}); err != nil {
		return err
	}
	return s.updateMatchingTasks(botID, userID, func(t *Task) {
		t.State.LastOutboundAt = at.UTC()
		t.UpdatedAt = time.Now().UTC()
	})
}

func (s *Service) SchedulerSnapshot() SchedulerSnapshot {
	s.mu.RLock()
	scheduler := s.scheduler
	s.mu.RUnlock()
	if scheduler == nil {
		return SchedulerSnapshot{}
	}
	return scheduler.Snapshot()
}

func (s *Service) normalizeInput(input UpsertTaskInput) (Task, error) {
	task := normalizeTask(Task{
		ID:             input.ID,
		Kind:           input.Kind,
		Source:         input.Source,
		Status:         input.Status,
		PolicyKind:     input.PolicyKind,
		DecisionReason: input.DecisionReason,
		Enabled:        input.Enabled,
		BotID:          input.BotID,
		ToUserID:       input.ToUserID,
		Title:          input.Title,
		Prompt:         input.Prompt,
		Schedule:       input.Schedule,
		Constraints:    input.Constraints,
	})
	if task.Kind == "" {
		return Task{}, fmt.Errorf("kind is required")
	}
	if task.BotID == "" {
		return Task{}, fmt.Errorf("bot_id is required")
	}
	if task.ToUserID == "" {
		return Task{}, fmt.Errorf("to_user_id is required")
	}
	if task.Title == "" {
		return Task{}, fmt.Errorf("title is required")
	}
	if task.Schedule.Timezone == "" {
		task.Schedule.Timezone = "Local"
	}
	if task.Source == "" {
		task.Source = TaskSourceManual
	}
	if task.Status == "" {
		task.Status = TaskStatusActive
	}
	if task.PolicyKind == "" {
		switch task.Kind {
		case TaskKindScheduledGreeting:
			task.PolicyKind = PolicyKindScheduledGreeting
		case TaskKindSilenceWakeup:
			task.PolicyKind = PolicyKindSilenceWakeup
		case TaskKindEventReminder:
			task.PolicyKind = PolicyKindEventReminder
		}
	}
	if task.Schedule.CronExpr == "" && task.Schedule.FireAt == "" {
		return Task{}, fmt.Errorf("schedule.cron_expr or schedule.fire_at is required")
	}
	if task.Schedule.CronExpr != "" {
		if _, err := parseCronStandard(task.Schedule.CronExpr); err != nil {
			return Task{}, fmt.Errorf("invalid cron_expr: %w", err)
		}
	}
	if task.Schedule.FireAt != "" {
		parsed, err := parseFireAt(task.Schedule.FireAt, loadTaskLocation(task.Schedule.Timezone))
		if err != nil {
			return Task{}, fmt.Errorf("invalid fire_at: %w", err)
		}
		task.Schedule.FireAt = parsed.UTC().Format(time.RFC3339)
		task.Schedule.OneShot = true
	}
	switch task.Kind {
	case TaskKindScheduledGreeting:
		if task.Prompt == "" {
			return Task{}, fmt.Errorf("prompt is required for scheduled_greeting")
		}
	case TaskKindSilenceWakeup:
		if task.Schedule.IdleForMinutes <= 0 {
			return Task{}, fmt.Errorf("schedule.idle_for_minutes is required for silence_wakeup")
		}
		if task.Prompt == "" {
			return Task{}, fmt.Errorf("prompt is required for silence_wakeup")
		}
	case TaskKindEventReminder:
		if task.Schedule.EventText == "" && task.Prompt == "" {
			return Task{}, fmt.Errorf("schedule.event_text or prompt is required for event_reminder")
		}
	default:
		return Task{}, fmt.Errorf("unsupported kind %q", task.Kind)
	}
	return task, nil
}

func (s *Service) shouldSkip(task Task, now time.Time) (bool, string) {
	if task.Status == TaskStatusDone || task.Status == TaskStatusCancelled {
		return true, string(task.Status)
	}
	activity, err := s.getActivityTimestamps(task.BotID, task.ToUserID)
	if err != nil {
		return true, "activity state unavailable"
	}
	if task.Constraints.SkipIfRecentOutboundWithinMinutes > 0 && !activity.LastOutboundAt.IsZero() {
		if now.Sub(activity.LastOutboundAt) < time.Duration(task.Constraints.SkipIfRecentOutboundWithinMinutes)*time.Minute {
			return true, "recent outbound cooldown"
		}
	}
	if task.Kind != TaskKindSilenceWakeup {
		return false, ""
	}
	if task.Schedule.IdleForMinutes > 0 {
		if activity.LastInboundAt.IsZero() {
			return true, "no inbound activity recorded"
		}
		if now.Sub(activity.LastInboundAt) < time.Duration(task.Schedule.IdleForMinutes)*time.Minute {
			return true, "user not idle long enough"
		}
	}
	if task.Schedule.CooldownHours > 0 && !activity.LastOutboundAt.IsZero() {
		if now.Sub(activity.LastOutboundAt) < time.Duration(task.Schedule.CooldownHours)*time.Hour {
			return true, "silence wakeup cooldown"
		}
	}
	if task.Schedule.CheckAfterLocalHour > 0 || task.Schedule.CheckBeforeLocalHour > 0 {
		hour := now.In(loadTaskLocation(task.Schedule.Timezone)).Hour()
		if task.Schedule.CheckAfterLocalHour > 0 && hour < task.Schedule.CheckAfterLocalHour {
			return true, "before allowed send window"
		}
		if task.Schedule.CheckBeforeLocalHour > 0 && hour >= task.Schedule.CheckBeforeLocalHour {
			return true, "after allowed send window"
		}
	}
	if task.Schedule.RequireRecentHistoryDays > 0 {
		ref := activity.LastInboundAt
		if ref.IsZero() {
			ref = activity.LastOutboundAt
		}
		if ref.IsZero() || now.Sub(ref) > time.Duration(task.Schedule.RequireRecentHistoryDays)*24*time.Hour {
			return true, "no recent history"
		}
	}
	return false, ""
}

func (s *Service) buildConversationID(task Task) string {
	return fmt.Sprintf("proactive:%s", task.ID)
}

func (s *Service) buildPrompt(task Task, now time.Time) string {
	switch task.Kind {
	case TaskKindScheduledGreeting:
		return fmt.Sprintf("现在时间是 %s。请你根据人设与已有记忆，给这位用户发送一条自然、简短、不打扰的主动问候。任务标题：%s。额外要求：%s", now.Format(time.RFC3339), task.Title, task.Prompt)
	case TaskKindSilenceWakeup:
		idleText := "未知"
		activity, err := s.getActivityTimestamps(task.BotID, task.ToUserID)
		if err == nil && !activity.LastInboundAt.IsZero() {
			idleText = now.Sub(activity.LastInboundAt).Round(time.Minute).String()
		}
		return fmt.Sprintf("这位用户已经有一段时间没有互动，最近一次入站距今约 %s。请发一条自然的、轻打扰的主动关心或唤醒消息，不要显得推销。任务标题：%s。额外要求：%s", idleText, task.Title, task.Prompt)
	case TaskKindEventReminder:
		eventText := task.Schedule.EventText
		if eventText == "" {
			eventText = task.Prompt
		}
		return fmt.Sprintf("请用自然聊天语气提醒用户以下事项：%s。当前时间：%s。任务标题：%s。", eventText, now.Format(time.RFC3339), task.Title)
	default:
		return task.Prompt
	}
}

func (s *Service) generateAndSend(ctx context.Context, client *ilink.Client, task Task, now time.Time) (string, int, error) {
	if s.runtimeSvc == nil {
		return "", 0, fmt.Errorf("runtime service not configured")
	}
	memoryContext := ""
	if s.memorySvc != nil {
		memoryContext = s.memorySvc.BuildRuntimeContext(task.BotID)
	}
	reply, err := s.runtimeSvc.Reply(ctx, s.buildConversationID(task), s.buildPrompt(task, now), memoryContext)
	if err != nil {
		return "", 0, err
	}
	cleanReply := messaging.StripMaterialDirectives(reply)
	if err := messaging.SendTextReply(ctx, client, task.ToUserID, cleanReply, "", ""); err != nil {
		return "", 0, err
	}
	mediaCount := 0
	for _, imgURL := range messaging.ExtractImageURLs(reply) {
		if err := messaging.SendMediaFromURL(ctx, client, task.ToUserID, imgURL, ""); err != nil {
			log.Printf("[proactive] task=%s stage=image-send state=failed url=%s err=%v", task.ID, imgURL, err)
			continue
		}
		mediaCount++
	}
	directives := messaging.ExtractMaterialDirectives(reply)
	if len(directives) == 0 && s.materialsSvc != nil {
		intentResult, err := s.materialsSvc.SearchForReplyIntent(cleanReply, 1)
		if err == nil && len(intentResult.Matches) > 0 {
			match := intentResult.Matches[0]
			directives = []messaging.MaterialDirective{{ID: match.Material.ID, Kind: string(match.Material.Kind), Title: match.Material.Title}}
		}
	}
	for _, directive := range directives {
		if s.materialsSvc == nil {
			continue
		}
		_, mediaPath, err := s.materialsSvc.FindMediaPathByID(directive.ID)
		if err != nil {
			log.Printf("[proactive] task=%s stage=material-send state=failed id=%s err=%v", task.ID, directive.ID, err)
			continue
		}
		if err := messaging.SendMediaFromPath(ctx, client, task.ToUserID, mediaPath, ""); err != nil {
			log.Printf("[proactive] task=%s stage=material-send state=failed id=%s path=%s err=%v", task.ID, directive.ID, mediaPath, err)
			continue
		}
		mediaCount++
	}
	if s.memorySvc != nil {
		s.memorySvc.RecordRuntimeTurn(task.BotID, task.ToUserID, s.buildConversationID(task), s.buildPrompt(task, now), cleanReply)
	}
	if err := s.RecordOutbound(task.BotID, task.ToUserID, now); err != nil {
		log.Printf("[proactive] task=%s stage=record-outbound state=failed err=%v", task.ID, err)
	}
	return cleanReply, mediaCount, nil
}

func (s *Service) clientForTask(task Task) *ilink.Client {
	if s.clients == nil {
		return nil
	}
	return s.clients.Client(task.BotID)
}

func (s *Service) syncTask(task Task) error {
	s.mu.RLock()
	scheduler := s.scheduler
	s.mu.RUnlock()
	if scheduler == nil {
		return nil
	}
	return scheduler.SyncTask(task)
}

func (s *Service) removeTask(taskID string) {
	s.mu.RLock()
	scheduler := s.scheduler
	s.mu.RUnlock()
	if scheduler != nil {
		scheduler.RemoveTask(taskID)
	}
}

func (s *Service) updateTaskState(taskID string, mutate func(*Task)) (Task, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Task{}, err
	}
	for i := range lib.Tasks {
		if lib.Tasks[i].ID != taskID {
			continue
		}
		mutate(&lib.Tasks[i])
		lib.Tasks[i] = s.enrichTask(lib.Tasks[i])
		if _, err := s.store.saveLibrary(lib); err != nil {
			return Task{}, err
		}
		if err := s.syncTask(lib.Tasks[i]); err != nil {
			return Task{}, err
		}
		return lib.Tasks[i], nil
	}
	return Task{}, fmt.Errorf("task not found")
}

func (s *Service) markTaskFailure(taskID string, at time.Time, err error) (Task, error) {
	return s.updateTaskState(taskID, func(t *Task) {
		t.State.LastRunAt = at.UTC()
		t.State.LastErrorAt = at.UTC()
		t.State.LastError = err.Error()
		t.UpdatedAt = at.UTC()
	})
}

func (s *Service) markTaskSuccess(taskID string, at time.Time, reply string) (Task, error) {
	return s.updateTaskState(taskID, func(t *Task) {
		t.State.LastRunAt = at.UTC()
		t.State.LastSuccessAt = at.UTC()
		t.State.LastOutboundAt = at.UTC()
		t.State.LastError = ""
		t.State.LastReply = strings.TrimSpace(reply)
		t.UpdatedAt = at.UTC()
	})
}

func (s *Service) disableTaskAfterOneShot(taskID string, at time.Time) (Task, error) {
	return s.updateTaskState(taskID, func(t *Task) {
		t.Enabled = false
		t.Status = TaskStatusDone
		t.State.NextRunAt = time.Time{}
		t.UpdatedAt = at.UTC()
	})
}

func (s *Service) currentSilenceDueAt(activity ActivityState, idleForMinutes int) time.Time {
	if idleForMinutes <= 0 || activity.LastInboundAt.IsZero() {
		return time.Time{}
	}
	return activity.LastInboundAt.Add(time.Duration(idleForMinutes) * time.Minute).UTC()
}

func (s *Service) shouldRunSilenceDecision(policy Policy, activity ActivityState, now time.Time) bool {
	dueAt := s.currentSilenceDueAt(activity, policy.Config.IdleForMinutes)
	if dueAt.IsZero() || now.Before(dueAt) {
		return false
	}
	if activity.LastSilenceCheckAt.IsZero() {
		return true
	}
	return activity.LastInboundAt.After(activity.LastSilenceCheckAt)
}

func (s *Service) recordSilenceDecision(botID, userID string, dueAt, checkedAt time.Time, reason string) error {
	return s.updateActivity(botID, userID, func(a *ActivityState) {
		a.LastSilenceDueAt = dueAt.UTC()
		a.LastSilenceCheckAt = checkedAt.UTC()
		a.LastSilenceReason = strings.TrimSpace(reason)
	})
}

func (s *Service) updateMatchingTasks(botID, userID string, mutate func(*Task)) error {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return err
	}
	changed := false
	for i := range lib.Tasks {
		if lib.Tasks[i].BotID != strings.TrimSpace(botID) || lib.Tasks[i].ToUserID != strings.TrimSpace(userID) {
			continue
		}
		mutate(&lib.Tasks[i])
		changed = true
	}
	if !changed {
		return nil
	}
	_, err = s.store.saveLibrary(lib)
	return err
}

func truncateProactiveLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func parseFireAt(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty fire_at")
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04"}
	for _, layout := range layouts {
		if layout == time.RFC3339 {
			if t, err := time.Parse(layout, raw); err == nil {
				return t.UTC(), nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format")
}

func (s *Service) enrichTask(task Task) Task {
	task = normalizeTask(task)
	if task.Schedule.FireAt != "" {
		if fireAt, err := parseFireAt(task.Schedule.FireAt, loadTaskLocation(task.Schedule.Timezone)); err == nil {
			task.State.NextRunAt = fireAt.UTC()
			return task
		}
	}
	if task.Schedule.CronExpr != "" {
		if schedule, err := parseCronStandard(task.Schedule.CronExpr); err == nil {
			loc := loadTaskLocation(task.Schedule.Timezone)
			task.State.NextRunAt = nextCronRun(schedule, time.Now(), loc).UTC()
		}
	}
	return task
}

func (s *Service) LoadAllTasks() ([]Task, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return nil, err
	}
	items := make([]Task, 0, len(lib.Tasks))
	for _, task := range lib.Tasks {
		items = append(items, s.enrichTask(task))
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s *Service) EvaluateSilencePolicies(ctx context.Context, now time.Time) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		log.Printf("[proactive] stage=policy state=load-failed kind=silence_wakeup err=%v", err)
		return
	}
	for _, policy := range lib.Policies {
		if policy.Kind != PolicyKindSilenceWakeup || !policy.Enabled {
			continue
		}
		activity, ok, err := s.readActivity(policy.BotID, policy.ToUserID)
		if err != nil {
			log.Printf("[proactive] policy=%s stage=activity state=load-failed bot_id=%s to_user_id=%s err=%v", policy.Kind, policy.BotID, policy.ToUserID, err)
			continue
		}
		if !ok {
			log.Printf("[proactive] policy=%s stage=decide state=skipped bot_id=%s to_user_id=%s reason=%q", policy.Kind, policy.BotID, policy.ToUserID, "no inbound activity recorded")
			continue
		}
		dueAt := s.currentSilenceDueAt(activity, policy.Config.IdleForMinutes)
		if dueAt.IsZero() || now.Before(dueAt) {
			continue
		}
		if !s.shouldRunSilenceDecision(policy, activity, now) {
			continue
		}
		decision, err := s.DecideSilenceWakeup(ctx, policy, now)
		if err != nil {
			log.Printf("[proactive] policy=%s stage=decide state=failed bot_id=%s to_user_id=%s err=%v", policy.Kind, policy.BotID, policy.ToUserID, err)
			continue
		}
		if err := s.recordSilenceDecision(policy.BotID, policy.ToUserID, dueAt, now, decision.Reason); err != nil {
			log.Printf("[proactive] policy=%s stage=decision-record state=failed bot_id=%s to_user_id=%s err=%v", policy.Kind, policy.BotID, policy.ToUserID, err)
		}
		if decision.Action == proactiveActionNone || decision.Action == proactiveActionSkip {
			log.Printf("[proactive] policy=%s stage=decide state=skipped bot_id=%s to_user_id=%s reason=%q", policy.Kind, policy.BotID, policy.ToUserID, decision.Reason)
			continue
		}
		if _, err := s.applyDecision(policy.BotID, policy.ToUserID, decision); err != nil {
			log.Printf("[proactive] policy=%s stage=apply state=failed bot_id=%s to_user_id=%s err=%v", policy.Kind, policy.BotID, policy.ToUserID, err)
			continue
		}
	}
}

func (s *Service) ProcessConversationDecision(ctx context.Context, botID, userID, message, reply, memoryContext string) {
	decision, err := s.DecideFromConversation(ctx, botID, userID, message, reply, memoryContext)
	if err != nil {
		log.Printf("[proactive] stage=decide state=failed kind=conversation bot_id=%s to_user_id=%s err=%v", botID, userID, err)
		return
	}
	if decision.Action == proactiveActionNone || decision.Action == proactiveActionSkip {
		log.Printf("[proactive] stage=decide state=skipped kind=conversation bot_id=%s to_user_id=%s reason=%q", botID, userID, decision.Reason)
		return
	}
	if _, err := s.applyDecision(botID, userID, decision); err != nil {
		log.Printf("[proactive] stage=apply state=failed kind=conversation bot_id=%s to_user_id=%s err=%v", botID, userID, err)
	}
}

func (s *Service) applyDecision(botID, userID string, decision proactiveDecision) (Task, error) {
	input := UpsertTaskInput{
		ID:             strings.TrimSpace(decision.ExistingTaskID),
		Kind:           decision.taskKind(),
		Source:         TaskSourceAI,
		Status:         TaskStatusActive,
		PolicyKind:     decision.policyKind(),
		DecisionReason: decision.Reason,
		Enabled:        true,
		BotID:          strings.TrimSpace(botID),
		ToUserID:       strings.TrimSpace(userID),
		Title:          defaultString(decision.Title, "AI 提醒"),
		Prompt:         strings.TrimSpace(decision.Prompt),
		Schedule: TaskSchedule{
			Timezone:                 defaultString(strings.TrimSpace(decision.Timezone), "Asia/Shanghai"),
			CronExpr:                 strings.TrimSpace(decision.CronExpr),
			FireAt:                   strings.TrimSpace(decision.FireAt),
			EventText:                strings.TrimSpace(decision.EventText),
			RemindBeforeMinutes:      decision.RemindBeforeMinutes,
			OneShot:                  decision.Action == proactiveActionCreateOneShot,
			IdleForMinutes:           decision.IdleForMinutes,
			CooldownHours:            decision.CooldownHours,
			CheckAfterLocalHour:      decision.CheckAfterLocalHour,
			CheckBeforeLocalHour:     decision.CheckBeforeLocalHour,
			RequireRecentHistoryDays: decision.RequireRecentHistoryDays,
		},
		Constraints: TaskConstraints{SkipIfRecentOutboundWithinMinutes: decision.SkipIfRecentOutboundWithinMinutes},
	}
	task, err := s.UpsertTask(input)
	if err != nil {
		return Task{}, err
	}
	if decision.Action == proactiveActionCreateRecurring {
		if s.memorySvc != nil && strings.TrimSpace(decision.MemoryHabit) != "" {
			if _, _, err := s.memorySvc.UpsertProfileItem(botID, "habit", decision.MemoryHabit, "auto-proactive"); err != nil {
				log.Printf("[proactive] task=%s stage=memory state=failed category=habit err=%v", task.ID, err)
			}
		}
	}
	log.Printf("[proactive] task=%s stage=decide state=applied action=%s policy=%s confidence=%.2f reason=%q", task.ID, decision.Action, decision.Kind, decision.Confidence, decision.Reason)
	return task, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (s *Service) DebugDecision(decision proactiveDecision) string {
	data, _ := json.Marshal(decision)
	return string(data)
}
