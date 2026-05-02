package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type shortTermStore struct {
	db *sql.DB
}

func newShortTermStore() (*shortTermStore, error) {
	if err := ensureMemoryDirs(); err != nil {
		return nil, err
	}
	path, err := shortTermDBPath()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &shortTermStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *shortTermStore) init() error {
	const schema = `
CREATE TABLE IF NOT EXISTS conversation_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    summary TEXT NOT NULL,
    user_message TEXT NOT NULL,
    assistant_reply TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_memories_user ON conversation_memories(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_memories_conversation ON conversation_memories(conversation_id, created_at DESC);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("init short-term schema: %w", err)
	}
	if err := s.ensureColumn("conversation_memories", "bot_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_conversation_memories_bot ON conversation_memories(bot_id, created_at DESC)`); err != nil {
		return fmt.Errorf("create bot index: %w", err)
	}
	return nil
}

func (s *shortTermStore) ensureColumn(table, column, ddl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("read table info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan table info: %w", err)
		}
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table info: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + ddl); err != nil {
		return fmt.Errorf("add %s column: %w", column, err)
	}
	return nil
}

func (s *shortTermStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *shortTermStore) Record(botID, userID, conversationID, userMessage, assistantReply string) error {
	now := time.Now().UTC()
	summary := buildShortTermSummary(userMessage, assistantReply)
	_, err := s.db.Exec(`
INSERT INTO conversation_memories (bot_id, user_id, conversation_id, summary, user_message, assistant_reply, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, strings.TrimSpace(botID), userID, conversationID, summary, truncateRunes(trimAndCollapse(userMessage), 500), truncateRunes(trimAndCollapse(assistantReply), 800), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert short-term memory: %w", err)
	}
	trimKey := strings.TrimSpace(botID)
	if trimKey == "" {
		trimKey = conversationID
	}
	_, err = s.db.Exec(`
DELETE FROM conversation_memories
WHERE bot_id = ?
  AND id NOT IN (
    SELECT id FROM conversation_memories
    WHERE bot_id = ?
    ORDER BY created_at DESC, id DESC
    LIMIT ?
  )
`, trimKey, trimKey, defaultShortTermLimit)
	if err != nil {
		return fmt.Errorf("trim short-term memory: %w", err)
	}
	return nil
}

func (s *shortTermStore) ListByBot(botID string, limit int) ([]ShortTermEntry, error) {
	if limit <= 0 {
		limit = defaultShortTermContext
	}
	rows, err := s.db.Query(`
SELECT id, bot_id, user_id, conversation_id, summary, user_message, assistant_reply, created_at, updated_at
FROM conversation_memories
WHERE bot_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?
`, strings.TrimSpace(botID), limit)
	if err != nil {
		return nil, fmt.Errorf("query short-term memory by bot: %w", err)
	}
	defer rows.Close()
	entries := make([]ShortTermEntry, 0, limit)
	for rows.Next() {
		entry, err := scanShortTermEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate short-term memory by bot: %w", err)
	}
	return entries, nil
}

func (s *shortTermStore) ListByUser(userID string, limit int) ([]ShortTermEntry, error) {
	if limit <= 0 {
		limit = defaultShortTermContext
	}
	rows, err := s.db.Query(`
SELECT id, bot_id, user_id, conversation_id, summary, user_message, assistant_reply, created_at, updated_at
FROM conversation_memories
WHERE user_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?
`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query short-term memory: %w", err)
	}
	defer rows.Close()
	entries := make([]ShortTermEntry, 0, limit)
	for rows.Next() {
		entry, err := scanShortTermEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate short-term memory: %w", err)
	}
	return entries, nil
}

func (s *shortTermStore) ClearBot(botID string) error {
	if _, err := s.db.Exec(`DELETE FROM conversation_memories WHERE bot_id = ?`, strings.TrimSpace(botID)); err != nil {
		return fmt.Errorf("clear short-term memory by bot: %w", err)
	}
	return nil
}

func (s *shortTermStore) ClearUser(userID string) error {
	if _, err := s.db.Exec(`DELETE FROM conversation_memories WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear short-term memory: %w", err)
	}
	return nil
}

func scanShortTermEntry(scanner interface{ Scan(dest ...any) error }) (ShortTermEntry, error) {
	var entry ShortTermEntry
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(&entry.ID, &entry.BotID, &entry.UserID, &entry.ConversationID, &entry.Summary, &entry.UserMessage, &entry.AssistantReply, &createdAt, &updatedAt); err != nil {
		return ShortTermEntry{}, fmt.Errorf("scan short-term memory: %w", err)
	}
	entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	entry.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return entry, nil
}

func buildShortTermSummary(userMessage, assistantReply string) string {
	user := truncateRunes(trimAndCollapse(userMessage), maxShortTermSnippetLength)
	assistant := truncateRunes(trimAndCollapse(assistantReply), maxShortTermSnippetLength)
	return fmt.Sprintf("用户：%s\n助手：%s", user, assistant)
}
