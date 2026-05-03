package proactive

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type Scheduler struct {
	mu      sync.Mutex
	service *Service
	entries map[string]Task
	running map[string]bool
	started bool
	cancel  context.CancelFunc
}

func NewScheduler(service *Service) *Scheduler {
	return &Scheduler{
		service: service,
		entries: make(map[string]Task),
		running: make(map[string]bool),
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	tasks, err := s.service.LoadAllTasks()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	for _, task := range tasks {
		if task.Enabled && (task.Schedule.CronExpr != "" || task.Schedule.FireAt != "") {
			s.entries[task.ID] = task
		}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()
	go s.loop(loopCtx)
	return nil
}

func (s *Scheduler) SyncTask(task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !task.Enabled || (task.Schedule.CronExpr == "" && task.Schedule.FireAt == "") {
		delete(s.entries, task.ID)
		return nil
	}
	s.entries[task.ID] = task
	return nil
}

func (s *Scheduler) RemoveTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, taskID)
	delete(s.running, taskID)
}

func (s *Scheduler) Snapshot() SchedulerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	registered := make([]string, 0, len(s.entries))
	nextRunAt := make(map[string]string, len(s.entries))
	now := time.Now()
	for taskID, task := range s.entries {
		registered = append(registered, taskID)
		if next := nextTaskRun(task, now); !next.IsZero() {
			nextRunAt[taskID] = next.UTC().Format(time.RFC3339)
		}
	}
	sort.Strings(registered)
	running := make([]string, 0, len(s.running))
	for taskID, active := range s.running {
		if active {
			running = append(running, taskID)
		}
	}
	sort.Strings(running)
	return SchedulerSnapshot{
		Running:          s.started,
		RegisteredIDs:    registered,
		EntryCount:       len(registered),
		RunningTasks:     running,
		RunningTaskCount: len(running),
		NextRunAt:        nextRunAt,
	}
}

func (s *Scheduler) runTask(taskID string) {
	s.mu.Lock()
	if s.running[taskID] {
		s.mu.Unlock()
		log.Printf("[proactive] task=%s stage=execute state=skipped reason=already-running", taskID)
		return
	}
	task, ok := s.entries[taskID]
	s.running[taskID] = true
	s.mu.Unlock()
	if ok {
		log.Printf("[proactive] task=%s stage=execute state=start kind=%s bot_id=%s to_user_id=%s title=%q cron=%q fire_at=%q timezone=%q", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, task.Schedule.CronExpr, task.Schedule.FireAt, task.Schedule.Timezone)
	}
	defer func() {
		s.mu.Lock()
		delete(s.running, taskID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := s.service.ExecuteTask(ctx, taskID)
	if err != nil {
		log.Printf("[proactive] task=%s stage=execute state=failed err=%v", taskID, err)
		return
	}
	if result.Skipped {
		log.Printf("[proactive] task=%s stage=execute state=skipped reason=%s", taskID, result.SkipReason)
		return
	}
	log.Printf("[proactive] task=%s stage=execute state=sent media=%d reply=%q", taskID, result.SentMedia, result.Reply)
}

func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.checkDueTasks(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.checkDueTasks(ctx, now)
		}
	}
}

func (s *Scheduler) checkDueTasks(ctx context.Context, now time.Time) {
	s.mu.Lock()
	entries := make([]Task, 0, len(s.entries))
	for _, task := range s.entries {
		entries = append(entries, task)
	}
	s.mu.Unlock()
	for _, task := range entries {
		if task.Schedule.CronExpr != "" {
			schedule, err := parseCronStandard(task.Schedule.CronExpr)
			if err != nil {
				log.Printf("[proactive] task=%s stage=schedule state=invalid err=%v", task.ID, err)
				continue
			}
			loc := loadTaskLocation(task.Schedule.Timezone)
			windowEnd := now.In(loc).Truncate(time.Minute)
			windowStart := windowEnd.Add(-1 * time.Minute)
			next := nextCronRun(schedule, windowStart, loc)
			if !next.IsZero() && !next.After(windowEnd) {
				log.Printf("[proactive] task=%s stage=schedule state=due kind=%s bot_id=%s to_user_id=%s title=%q cron=%q timezone=%q next=%s", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, task.Schedule.CronExpr, task.Schedule.Timezone, next.UTC().Format(time.RFC3339))
				go s.runTask(task.ID)
			}
			continue
		}
		if task.Schedule.FireAt != "" {
			fireAt, err := parseFireAt(task.Schedule.FireAt, loadTaskLocation(task.Schedule.Timezone))
			if err != nil {
				log.Printf("[proactive] task=%s stage=schedule state=invalid-fire-at err=%v", task.ID, err)
				continue
			}
			windowEnd := now.UTC().Truncate(time.Minute)
			windowStart := windowEnd.Add(-1 * time.Minute)
			if (fireAt.Equal(windowStart) || fireAt.After(windowStart)) && !fireAt.After(windowEnd) {
				log.Printf("[proactive] task=%s stage=schedule state=due kind=%s bot_id=%s to_user_id=%s title=%q fire_at=%q", task.ID, task.Kind, task.BotID, task.ToUserID, task.Title, task.Schedule.FireAt)
				go s.runTask(task.ID)
			}
		}
	}
	s.service.EvaluateSilencePolicies(ctx, now)
}

func nextTaskRun(task Task, now time.Time) time.Time {
	if task.Schedule.FireAt != "" {
		if fireAt, err := parseFireAt(task.Schedule.FireAt, loadTaskLocation(task.Schedule.Timezone)); err == nil {
			return fireAt.UTC()
		}
	}
	if task.Schedule.CronExpr != "" {
		if schedule, err := parseCronStandard(task.Schedule.CronExpr); err == nil {
			return nextCronRun(schedule, now, loadTaskLocation(task.Schedule.Timezone))
		}
	}
	return time.Time{}
}

func loadTaskLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}
