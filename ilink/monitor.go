package ilink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	maxConsecutiveFailures  = 5
	initialBackoff          = 3 * time.Second
	maxBackoff              = 60 * time.Second
	sessionExpiredBackoff   = 5 * time.Second
	errCodeSessionExpired   = -14
	reloginReminderInterval = 2 * time.Minute
)

// MessageHandler is called for each received message.
type MessageHandler func(ctx context.Context, client *Client, msg WeixinMessage)

// Monitor manages the long-poll loop for receiving messages.
type Monitor struct {
	client                *Client
	handler               MessageHandler
	getUpdatesBuf         string
	bufPath               string
	failures              int
	lastActivity          time.Time
	lastReloginLogAt      time.Time
	reloginStateAnnounced bool
}

// NewMonitor creates a new long-poll monitor.
func NewMonitor(client *Client, handler MessageHandler) (*Monitor, error) {
	accountID := NormalizeAccountID(client.BotID())
	accountsDir, err := AccountsDir()
	if err != nil {
		return nil, err
	}
	bufPath := filepath.Join(accountsDir, accountID+".sync.json")

	m := &Monitor{
		client:       client,
		handler:      handler,
		bufPath:      bufPath,
		lastActivity: time.Now(),
	}
	m.loadBuf()
	return m, nil
}

// Run starts the long-poll loop. It blocks until ctx is cancelled.
// Automatically recovers from errors with exponential backoff.
func (m *Monitor) Run(ctx context.Context) error {
	log.Printf("[monitor] bot=%s state=connecting", m.client.BotID())
	log.Println("[monitor] starting long-poll loop")

	for {
		select {
		case <-ctx.Done():
			log.Println("[monitor] shutting down")
			return ctx.Err()
		default:
		}

		resp, err := m.client.GetUpdates(ctx, m.getUpdatesBuf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.failures++
			backoff := m.calcBackoff()
			log.Printf("[monitor] bot=%s state=reconnecting failures=%d/%d backoff=%s err=%v",
				m.client.BotID(), m.failures, maxConsecutiveFailures, backoff, err)
			if m.failures == maxConsecutiveFailures {
				log.Printf("[monitor] WARNING: %d consecutive failures. If this persists, run `weone login` to re-authenticate.", maxConsecutiveFailures)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Reset failure counter on any successful response
		if m.failures > 0 {
			log.Printf("[monitor] bot=%s state=recovered failures=%d", m.client.BotID(), m.failures)
		}
		m.failures = 0
		m.lastActivity = time.Now()

		// Session expired — reset sync buf and reconnect silently
		if resp.ErrCode == errCodeSessionExpired {
			if m.getUpdatesBuf != "" {
				log.Printf("[monitor] bot=%s state=session-expired action=reset-sync-buf", m.client.BotID())
				m.getUpdatesBuf = ""
				m.saveBuf()
				m.reloginStateAnnounced = false
			} else {
				m.logReloginRequired()
			}
			select {
			case <-time.After(sessionExpiredBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		m.clearReloginRequired()

		// Other server errors
		if resp.Ret != 0 && resp.ErrCode != 0 {
			log.Printf("[monitor] server error: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.ErrCode, resp.ErrMsg)
			continue
		}

		// Update buf for next poll
		if resp.GetUpdatesBuf != "" {
			m.getUpdatesBuf = resp.GetUpdatesBuf
			m.saveBuf()
		}

		// Process messages concurrently — don't block the poll loop
		if len(resp.Msgs) > 0 {
			log.Printf("[monitor] bot=%s state=updates-received count=%d", m.client.BotID(), len(resp.Msgs))
		}
		for _, msg := range resp.Msgs {
			go m.handler(ctx, m.client, msg)
		}
	}
}

func (m *Monitor) logReloginRequired() {
	now := time.Now()
	if m.reloginStateAnnounced && now.Sub(m.lastReloginLogAt) < reloginReminderInterval {
		return
	}
	if m.reloginStateAnnounced {
		log.Printf("[monitor] bot=%s state=session-expired action=relogin-required reminder=still-expired suggestion=remove-account-or-login-again", m.client.BotID())
	} else {
		log.Printf("[monitor] bot=%s state=session-expired action=relogin-required message=account-expired suggestion=remove-account-or-login-again", m.client.BotID())
	}
	m.reloginStateAnnounced = true
	m.lastReloginLogAt = now
	MarkAccountReloginRequired(m.client.BotID(), true)
}

func (m *Monitor) clearReloginRequired() {
	if !m.reloginStateAnnounced {
		MarkAccountReloginRequired(m.client.BotID(), false)
		return
	}
	m.reloginStateAnnounced = false
	m.lastReloginLogAt = time.Time{}
	MarkAccountReloginRequired(m.client.BotID(), false)
}

// calcBackoff returns an exponential backoff duration capped at maxBackoff.
func (m *Monitor) calcBackoff() time.Duration {
	d := initialBackoff
	for i := 1; i < m.failures; i++ {
		d *= 2
		if d > maxBackoff {
			return maxBackoff
		}
	}
	return d
}

type syncData struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

func (m *Monitor) loadBuf() {
	data, err := os.ReadFile(m.bufPath)
	if err != nil {
		return
	}
	var s syncData
	if json.Unmarshal(data, &s) == nil && s.GetUpdatesBuf != "" {
		m.getUpdatesBuf = s.GetUpdatesBuf
		log.Printf("[monitor] loaded sync buf from %s", m.bufPath)
	}
}

func (m *Monitor) saveBuf() {
	dir := filepath.Dir(m.bufPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[monitor] failed to create buf dir: %v", err)
		return
	}
	data, _ := json.Marshal(syncData{GetUpdatesBuf: m.getUpdatesBuf})
	if err := os.WriteFile(m.bufPath, data, 0o600); err != nil {
		log.Printf("[monitor] failed to save buf: %v", err)
	}
}

// FormatMessageSummary returns a short description of a message for logging.
func FormatMessageSummary(msg WeixinMessage) string {
	text := ""
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil {
			text = item.TextItem.Text
			break
		}
	}
	if len(text) > 50 {
		text = text[:50] + "..."
	}
	return fmt.Sprintf("from=%s type=%d state=%d text=%q", msg.FromUserID, msg.MessageType, msg.MessageState, text)
}
