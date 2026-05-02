package memory

import "time"

type ProfileDocument struct {
	BotID     string    `json:"bot_id"`
	Markdown  string    `json:"markdown"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source,omitempty"`
	FileName  string    `json:"file_name,omitempty"`
}

type ShortTermEntry struct {
	ID             int64     `json:"id"`
	BotID          string    `json:"bot_id"`
	UserID         string    `json:"user_id"`
	ConversationID string    `json:"conversation_id"`
	Summary        string    `json:"summary"`
	UserMessage    string    `json:"user_message"`
	AssistantReply string    `json:"assistant_reply"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
