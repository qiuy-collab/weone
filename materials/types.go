package materials

import "time"

type Kind string

type MatchSource string

const (
	KindText  Kind = "text"
	KindImage Kind = "image"
	KindVideo Kind = "video"
	KindFile  Kind = "file"
)

const (
	MatchSourceTag         MatchSource = "tag"
	MatchSourceTitle       MatchSource = "title"
	MatchSourceDescription MatchSource = "description"
	MatchSourceContent     MatchSource = "content"
)

type Material struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Title        string    `json:"title"`
	Content      string    `json:"content,omitempty"`
	MediaPath    string    `json:"media_path,omitempty"`
	OriginalName string    `json:"original_name,omitempty"`
	MimeType     string    `json:"mime_type,omitempty"`
	FileSize     int64     `json:"file_size,omitempty"`
	Tags         []string  `json:"tags"`
	Description  string    `json:"description,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (m Material) HasMedia() bool {
	return m.Kind != KindText && m.MediaPath != ""
}

func (m Material) PreviewURL() string {
	if m.ID == "" || !m.HasMedia() {
		return ""
	}
	return "/api/materials/media?id=" + m.ID
}

type Library struct {
	Items []Material `json:"items"`
}

type MatchReason struct {
	Source  MatchSource `json:"source"`
	Keyword string      `json:"keyword"`
	Field   string      `json:"field,omitempty"`
}

type MaterialMatch struct {
	Material Material      `json:"material"`
	Score    int           `json:"score"`
	Reasons  []MatchReason `json:"reasons"`
}

type SearchResult struct {
	Keywords []string        `json:"keywords"`
	Matches  []MaterialMatch `json:"matches"`
}
