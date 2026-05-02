package proactive

import (
	"context"
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
		return s.enrichTask(task), nil
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
	return s.enrichTask(task), nil
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
	for _, task := range lib.Tasks {
		if task.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, task)
	}
	if !found {
		return fmt.Errorf("task not found")
	}
	if _, err := s.store.saveLibrary(Library{Tasks: filtered}); err != nil {
		return err
	}
	s.removeTask(id)
	return nil
}

func (s *Service) ExecuteTask(ctx context.Context, taskID string) (ExecuteResult, error) {
	task, err := s.GetTask(taskID)
	if err != nil {
		return ExecuteResult{}, err
	}
	result := ExecuteResult{Task: task, ExecutedAt: time.Now().UTC()}
	if !task.Enabled {
		result.Skipped = true
		result.SkipReason = "task disabled"
		return result, nil
	}
	if skip, reason := s.shouldSkip(task, result.ExecutedAt); skip {
		updated, updateErr := s.updateTaskState(task.ID, func(t *Task) {
			t.State.LastRunAt = result.ExecutedAt
			t.State.LastError = ""
			t.UpdatedAt = result.ExecutedAt
		})
		if updateErr == nil {
			result.Task = updated
		}
		result.Skipped = true
		result.SkipReason = reason
		return result, updateErr
	}
	client := s.clientForTask(task)
	if client == nil {
		err := fmt.Errorf("bot %q is not online", task.BotID)
		_, _ = s.markTaskFailure(task.ID, result.ExecutedAt, err)
		return result, err
	}
	reply, mediaCount, execErr := s.generateAndSend(ctx, client, task, result.ExecutedAt)
	if execErr != nil {
		updated, _ := s.markTaskFailure(task.ID, result.ExecutedAt, execErr)
		result.Task = updated
		return result, execErr
	}
	updated, updateErr := s.markTaskSuccess(task.ID, result.ExecutedAt, reply)
	result.Task = updated
	result.Reply = reply
	result.SentMedia = mediaCount
	if updateErr != nil {
		return result, updateErr
	}
	if task.Schedule.OneShot {
		updated, updateErr = s.disableTaskAfterOneShot(task.ID, result.ExecutedAt)
		result.Task = updated
		if updateErr != nil {
			return result, updateErr
		}
	}
	return result, nil
}

func (s *Service) RecordInbound(botID, userID string, at time.Time) error {
	return s.updateMatchingTasks(botID, userID, func(t *Task) {
		if at.IsZero() {
			at = time.Now().UTC()
		}
		t.State.LastInboundAt = at.UTC()
		t.UpdatedAt = time.Now().UTC()
	})
}

func (s *Service) RecordOutbound(botID, userID string, at time.Time) error {
	return s.updateMatchingTasks(botID, userID, func(t *Task) {
		if at.IsZero() {
			at = time.Now().UTC()
		}
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
		ID:          input.ID,
		Kind:        input.Kind,
		Enabled:     input.Enabled,
		BotID:       input.BotID,
		ToUserID:    input.ToUserID,
		Title:       input.Title,
		Prompt:      input.Prompt,
		Schedule:    input.Schedule,
		Constraints: input.Constraints,
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
	if task.Schedule.CronExpr == "" {
		return Task{}, fmt.Errorf("schedule.cron_expr is required")
	}
	if task.Schedule.Timezone == "" {
		task.Schedule.Timezone = "Local"
	}
	if _, err := parseCronStandard(task.Schedule.CronExpr); err != nil {
		return Task{}, fmt.Errorf("invalid cron_expr: %w", err)
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
	if task.Constraints.SkipIfRecentOutboundWithinMinutes > 0 && !task.State.LastOutboundAt.IsZero() {
		if now.Sub(task.State.LastOutboundAt) < time.Duration(task.Constraints.SkipIfRecentOutboundWithinMinutes)*time.Minute {
			return true, "recent outbound cooldown"
		}
	}
	if task.Kind != TaskKindSilenceWakeup {
		return false, ""
	}
	if task.Schedule.IdleForMinutes > 0 {
		if task.State.LastInboundAt.IsZero() {
			return true, "no inbound activity recorded"
		}
		if now.Sub(task.State.LastInboundAt) < time.Duration(task.Schedule.IdleForMinutes)*time.Minute {
			return true, "user not idle long enough"
		}
	}
	if task.Schedule.CooldownHours > 0 && !task.State.LastOutboundAt.IsZero() {
		if now.Sub(task.State.LastOutboundAt) < time.Duration(task.Schedule.CooldownHours)*time.Hour {
			return true, "silence wakeup cooldown"
		}
	}
	if task.Schedule.CheckAfterLocalHour > 0 || task.Schedule.CheckBeforeLocalHour > 0 {
		hour := now.Hour()
		if task.Schedule.CheckAfterLocalHour > 0 && hour < task.Schedule.CheckAfterLocalHour {
			return true, "before allowed send window"
		}
		if task.Schedule.CheckBeforeLocalHour > 0 && hour >= task.Schedule.CheckBeforeLocalHour {
			return true, "after allowed send window"
		}
	}
	if task.Schedule.RequireRecentHistoryDays > 0 {
		ref := task.State.LastInboundAt
		if ref.IsZero() {
			ref = task.State.LastOutboundAt
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
		if !task.State.LastInboundAt.IsZero() {
			idleText = now.Sub(task.State.LastInboundAt).Round(time.Minute).String()
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
		t.UpdatedAt = at.UTC()
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

func (s *Service) enrichTask(task Task) Task {
	task = normalizeTask(task)
	if task.Schedule.CronExpr != "" {
		if schedule, err := parseCronStandard(task.Schedule.CronExpr); err == nil {
			loc := loadTaskLocation(task.Schedule.Timezone)
			task.State.NextRunAt = schedule.Next(time.Now(), loc).UTC()
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
