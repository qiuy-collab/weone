package ilink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/qiuy-collab/weone/config"
)

const (
	qrCodeURL       = "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3"
	qrStatusURL     = "https://ilinkai.weixin.qq.com/ilink/bot/get_qrcode_status?qrcode="
	statusWait      = "wait"
	statusScanned   = "scaned"
	statusConfirmed = "confirmed"
	statusExpired   = "expired"
)

var accountStatusRegistry sync.Map

// AccountInfo describes a saved account plus its current runtime status summary.
type AccountInfo struct {
	BotID          string    `json:"bot_id"`
	UserID         string    `json:"user_id,omitempty"`
	BaseURL        string    `json:"base_url,omitempty"`
	CredentialPath string    `json:"credential_path"`
	SyncPath       string    `json:"sync_path"`
	HasSyncState   bool      `json:"has_sync_state"`
	NeedsRelogin   bool      `json:"needs_relogin"`
	Loaded         bool      `json:"loaded"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type accountStatus struct {
	NeedsRelogin bool
	UpdatedAt    time.Time
}

// FetchQRCode retrieves a new QR code for login.
func FetchQRCode(ctx context.Context) (*QRCodeResponse, error) {
	c := NewUnauthenticatedClient()
	var resp QRCodeResponse
	if err := c.doGet(ctx, qrCodeURL, &resp); err != nil {
		return nil, fmt.Errorf("fetch QR code: %w", err)
	}
	return &resp, nil
}

// PollQRStatus polls for QR code scan status until confirmed or expired.
// It calls onStatus for each status change so the caller can display progress.
func PollQRStatus(ctx context.Context, qrcode string, onStatus func(status string)) (*Credentials, error) {
	c := NewUnauthenticatedClient()
	url := qrStatusURL + qrcode

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		var resp QRStatusResponse
		err := c.doGet(pollCtx, url, &resp)
		cancel()

		if err != nil {
			// Timeout is normal for long-poll, retry
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		if onStatus != nil {
			onStatus(resp.Status)
		}

		switch resp.Status {
		case statusConfirmed:
			creds := &Credentials{
				BotToken:    resp.BotToken,
				ILinkBotID:  resp.ILinkBotID,
				BaseURL:     resp.BaseURL,
				ILinkUserID: resp.ILinkUserID,
			}
			return creds, nil
		case statusExpired:
			return nil, fmt.Errorf("QR code expired")
		case statusWait, statusScanned:
			// Continue polling
		default:
			// Unknown status, continue
		}
	}
}

// AccountsDir returns the directory where account credentials are stored.
func AccountsDir() (string, error) {
	root, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "accounts"), nil
}

// NormalizeAccountID converts raw bot ID to filesystem-safe format.
func NormalizeAccountID(raw string) string {
	s := raw
	for _, ch := range []string{"@", ".", ":"} {
		s = filepath.Clean(s)
		s = replaceAll(s, ch, "-")
	}
	return s
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SaveCredentials saves credentials to disk under the runtime accounts directory.
func SaveCredentials(creds *Credentials) error {
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}

	id := NormalizeAccountID(creds.ILinkBotID)
	path := filepath.Join(dir, id+".json")

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	MarkAccountReloginRequired(creds.ILinkBotID, false)
	return nil
}

// LoadAllCredentials loads all saved account credentials.
func LoadAllCredentials() ([]*Credentials, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	var result []*Credentials
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" || isSyncStateFile(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var creds Credentials
		if json.Unmarshal(data, &creds) == nil && creds.BotToken != "" {
			result = append(result, &creds)
		}
	}
	return result, nil
}

// ListAccounts returns saved accounts with runtime status summary.
func ListAccounts(loadedBotIDs []string) ([]AccountInfo, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	loadedSet := make(map[string]bool, len(loadedBotIDs))
	for _, botID := range loadedBotIDs {
		if botID != "" {
			loadedSet[botID] = true
		}
	}

	var result []AccountInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" || isSyncStateFile(name) {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var creds Credentials
		if err := json.Unmarshal(data, &creds); err != nil || creds.BotToken == "" {
			continue
		}

		syncPath := filepath.Join(dir, NormalizeAccountID(creds.ILinkBotID)+".sync.json")
		_, syncErr := os.Stat(syncPath)
		status, _ := GetAccountStatus(creds.ILinkBotID)
		result = append(result, AccountInfo{
			BotID:          creds.ILinkBotID,
			UserID:         creds.ILinkUserID,
			BaseURL:        creds.BaseURL,
			CredentialPath: path,
			SyncPath:       syncPath,
			HasSyncState:   syncErr == nil,
			NeedsRelogin:   status.NeedsRelogin,
			Loaded:         loadedSet[creds.ILinkBotID],
			UpdatedAt:      status.UpdatedAt,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].BotID < result[j].BotID
	})
	return result, nil
}

// RemoveAccount deletes the saved credentials and sync state for an account.
func RemoveAccount(botID string) error {
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	id := NormalizeAccountID(botID)
	credPath := filepath.Join(dir, id+".json")
	syncPath := filepath.Join(dir, id+".sync.json")

	if err := os.Remove(credPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credentials: %w", err)
	}
	if err := os.Remove(syncPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove sync state: %w", err)
	}
	accountStatusRegistry.Delete(botID)
	return nil
}

// MarkAccountReloginRequired updates the current runtime status for an account.
func MarkAccountReloginRequired(botID string, required bool) {
	if botID == "" {
		return
	}
	accountStatusRegistry.Store(botID, accountStatus{NeedsRelogin: required, UpdatedAt: time.Now()})
}

// GetAccountStatus returns the current runtime status for an account.
func GetAccountStatus(botID string) (AccountInfo, bool) {
	v, ok := accountStatusRegistry.Load(botID)
	if !ok {
		return AccountInfo{}, false
	}
	status, ok := v.(accountStatus)
	if !ok {
		return AccountInfo{}, false
	}
	return AccountInfo{BotID: botID, NeedsRelogin: status.NeedsRelogin, UpdatedAt: status.UpdatedAt}, true
}

func isSyncStateFile(name string) bool {
	return len(name) >= len(".sync.json") && name[len(name)-len(".sync.json"):] == ".sync.json"
}

// CredentialsPath returns the path for display purposes.
func CredentialsPath() (string, error) {
	return AccountsDir()
}
