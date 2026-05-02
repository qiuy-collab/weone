package proactive

import "time"

type TaskKind string

const (
	TaskKindScheduledGreeting TaskKind = "scheduled_greeting"
	TaskKindSilenceWakeup     TaskKind = "silence_wakeup"
	TaskKindEventReminder     TaskKind = "event_reminder"
)

type Task struct {
	ID          string          `json:"id"`
	Kind        TaskKind        `json:"kind"`
	Enabled     bool            `json:"enabled"`
	BotID       string          `json:"bot_id"`
	ToUserID    string          `json:"to_user_id"`
	Title       string          `json:"title"`
	Prompt      string          `json:"prompt,omitempty"`
	Schedule    TaskSchedule    `json:"schedule"`
	Constraints TaskConstraints `json:"constraints,omitempty"`
	State       TaskState       `json:"state,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type TaskSchedule struct {
	Timezone                 string `json:"timezone,omitempty"`
	CronExpr                 string `json:"cron_expr,omitempty"`
	FireAt                   string `json:"fire_at,omitempty"`
	IdleForMinutes           int    `json:"idle_for_minutes,omitempty"`
	CooldownHours            int    `json:"cooldown_hours,omitempty"`
	CheckAfterLocalHour      int    `json:"check_after_local_hour,omitempty"`
	CheckBeforeLocalHour     int    `json:"check_before_local_hour,omitempty"`
	RequireRecentHistoryDays int    `json:"require_recent_history_days,omitempty"`
	EventText                string `json:"event_text,omitempty"`
	RemindBeforeMinutes      int    `json:"remind_before_minutes,omitempty"`
	OneShot                  bool   `json:"one_shot,omitempty"`
}

type TaskConstraints struct {
	SkipIfRecentOutboundWithinMinutes int `json:"skip_if_recent_outbound_within_minutes,omitempty"`
}

type TaskState struct {
	LastRunAt      time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	LastInboundAt  time.Time `json:"last_inbound_at,omitempty"`
	LastOutboundAt time.Time `json:"last_outbound_at,omitempty"`
	NextRunAt      time.Time `json:"next_run_at,omitempty"`
	LastReply      string    `json:"last_reply,omitempty"`
}

type Library struct {
	Tasks []Task `json:"tasks"`
}

type UpsertTaskInput struct {
	ID          string          `json:"id,omitempty"`
	Kind        TaskKind        `json:"kind"`
	Enabled     bool            `json:"enabled"`
	BotID       string          `json:"bot_id"`
	ToUserID    string          `json:"to_user_id"`
	Title       string          `json:"title"`
	Prompt      string          `json:"prompt,omitempty"`
	Schedule    TaskSchedule    `json:"schedule"`
	Constraints TaskConstraints `json:"constraints,omitempty"`
}

type ExecuteResult struct {
	Task       Task      `json:"task"`
	Reply      string    `json:"reply"`
	SentMedia  int       `json:"sent_media"`
	Skipped    bool      `json:"skipped"`
	SkipReason string    `json:"skip_reason,omitempty"`
	ExecutedAt time.Time `json:"executed_at"`
}

type SchedulerSnapshot struct {
	Running       bool              `json:"running"`
	RegisteredIDs []string          `json:"registered_ids,omitempty"`
	EntryCount    int               `json:"entry_count"`
	RunningTasks  []string          `json:"running_tasks,omitempty"`
	NextRunAt     map[string]string `json:"next_run_at,omitempty"`
}
