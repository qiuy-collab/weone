package proactive

import "time"

type TaskKind string

type TaskSource string

type TaskStatus string

type PolicyKind string

const (
	TaskKindScheduledGreeting TaskKind = "scheduled_greeting"
	TaskKindSilenceWakeup     TaskKind = "silence_wakeup"
	TaskKindEventReminder     TaskKind = "event_reminder"
)

const (
	TaskSourceManual TaskSource = "manual"
	TaskSourceAI     TaskSource = "ai"
)

const (
	TaskStatusActive    TaskStatus = "active"
	TaskStatusDone      TaskStatus = "done"
	TaskStatusCancelled TaskStatus = "cancelled"
)

const (
	PolicyKindScheduledGreeting PolicyKind = "scheduled_greeting"
	PolicyKindSilenceWakeup     PolicyKind = "silence_wakeup"
	PolicyKindEventReminder     PolicyKind = "event_reminder"
)

type Task struct {
	ID             string          `json:"id"`
	Kind           TaskKind        `json:"kind"`
	Source         TaskSource      `json:"source,omitempty"`
	Status         TaskStatus      `json:"status,omitempty"`
	PolicyKind     PolicyKind      `json:"policy_kind,omitempty"`
	DecisionReason string          `json:"decision_reason,omitempty"`
	Enabled        bool            `json:"enabled"`
	BotID          string          `json:"bot_id"`
	ToUserID       string          `json:"to_user_id"`
	Title          string          `json:"title"`
	Prompt         string          `json:"prompt,omitempty"`
	Schedule       TaskSchedule    `json:"schedule"`
	Constraints    TaskConstraints `json:"constraints,omitempty"`
	State          TaskState       `json:"state,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
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

type ActivityState struct {
	BotID               string    `json:"bot_id"`
	ToUserID            string    `json:"to_user_id"`
	LastInboundAt       time.Time `json:"last_inbound_at,omitempty"`
	LastOutboundAt      time.Time `json:"last_outbound_at,omitempty"`
	LastSilenceCheckAt  time.Time `json:"last_silence_check_at,omitempty"`
	LastSilenceDueAt    time.Time `json:"last_silence_due_at,omitempty"`
	LastSilenceReason   string    `json:"last_silence_reason,omitempty"`
	CreatedAt           time.Time `json:"created_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type Policy struct {
	Kind      PolicyKind   `json:"kind"`
	Enabled   bool         `json:"enabled"`
	BotID     string       `json:"bot_id,omitempty"`
	ToUserID  string       `json:"to_user_id,omitempty"`
	Title     string       `json:"title,omitempty"`
	Config    PolicyConfig `json:"config,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type PolicyConfig struct {
	IdleForMinutes           int    `json:"idle_for_minutes,omitempty"`
	CooldownHours            int    `json:"cooldown_hours,omitempty"`
	CheckAfterLocalHour      int    `json:"check_after_local_hour,omitempty"`
	CheckBeforeLocalHour     int    `json:"check_before_local_hour,omitempty"`
	AllowOneShot             bool   `json:"allow_one_shot,omitempty"`
	AllowRecurring           bool   `json:"allow_recurring,omitempty"`
	RemindBeforeMinutes      int    `json:"remind_before_minutes,omitempty"`
	RequireRecentHistoryDays int    `json:"require_recent_history_days,omitempty"`
	PromptTemplate           string `json:"prompt_template,omitempty"`
}

type Library struct {
	Tasks      []Task          `json:"tasks"`
	Policies   []Policy        `json:"policies,omitempty"`
	Activities []ActivityState `json:"activities,omitempty"`
}

type UpsertTaskInput struct {
	ID             string          `json:"id,omitempty"`
	Kind           TaskKind        `json:"kind"`
	Source         TaskSource      `json:"source,omitempty"`
	Status         TaskStatus      `json:"status,omitempty"`
	PolicyKind     PolicyKind      `json:"policy_kind,omitempty"`
	DecisionReason string          `json:"decision_reason,omitempty"`
	Enabled        bool            `json:"enabled"`
	BotID          string          `json:"bot_id"`
	ToUserID       string          `json:"to_user_id"`
	Title          string          `json:"title"`
	Prompt         string          `json:"prompt,omitempty"`
	Schedule       TaskSchedule    `json:"schedule"`
	Constraints    TaskConstraints `json:"constraints,omitempty"`
}

type UpsertPolicyInput struct {
	Kind     PolicyKind   `json:"kind"`
	Enabled  bool         `json:"enabled"`
	BotID    string       `json:"bot_id,omitempty"`
	ToUserID string       `json:"to_user_id,omitempty"`
	Title    string       `json:"title,omitempty"`
	Config   PolicyConfig `json:"config,omitempty"`
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
	Running          bool              `json:"running"`
	RegisteredIDs    []string          `json:"registered_ids,omitempty"`
	EntryCount       int               `json:"entry_count"`
	RunningTasks     []string          `json:"running_tasks,omitempty"`
	RunningTaskCount int               `json:"running_task_count"`
	NextRunAt        map[string]string `json:"next_run_at,omitempty"`
}

type TaskListItem struct {
	ID             string     `json:"id"`
	Kind           TaskKind   `json:"kind"`
	Source         TaskSource `json:"source,omitempty"`
	Status         TaskStatus `json:"status,omitempty"`
	Enabled        bool       `json:"enabled"`
	Title          string     `json:"title"`
	Content        string     `json:"content"`
	ExecuteAt      string     `json:"execute_at,omitempty"`
	BotID          string     `json:"bot_id,omitempty"`
	ToUserID       string     `json:"to_user_id,omitempty"`
	PolicyKind     PolicyKind `json:"policy_kind,omitempty"`
	Bucket         string     `json:"bucket,omitempty"`
	BucketLabel    string     `json:"bucket_label,omitempty"`
	IsRecurring    bool       `json:"is_recurring,omitempty"`
	HasError       bool       `json:"has_error,omitempty"`
	LastStatusText string     `json:"last_status_text,omitempty"`
}

type TargetOption struct {
	BotID    string `json:"bot_id"`
	ToUserID string `json:"to_user_id,omitempty"`
	Label    string `json:"label"`
}
