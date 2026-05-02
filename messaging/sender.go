package messaging

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/qiuy-collab/weone/ilink"
)

// NewClientID generates a new unique client ID for message correlation.
func NewClientID() string {
	return uuid.New().String()
}

// SendTypingState sends a typing indicator to a user via the iLink sendtyping API.
// It first fetches a typing_ticket via getconfig, then sends the typing status.
func SendTypingState(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	start := time.Now()
	log.Printf("[sender] to=%s kind=typing state=start", userID)

	// Get typing ticket
	configResp, err := client.GetConfig(ctx, userID, contextToken)
	if err != nil {
		log.Printf("[sender] to=%s kind=typing state=failed stage=get-config elapsed=%s err=%v", userID, time.Since(start), err)
		return fmt.Errorf("get config for typing: %w", err)
	}
	if configResp.TypingTicket == "" {
		err := fmt.Errorf("no typing_ticket returned from getconfig")
		log.Printf("[sender] to=%s kind=typing state=failed stage=get-config elapsed=%s err=%v", userID, time.Since(start), err)
		return err
	}

	// Send typing
	if err := client.SendTyping(ctx, userID, configResp.TypingTicket, ilink.TypingStatusTyping); err != nil {
		log.Printf("[sender] to=%s kind=typing state=failed stage=send elapsed=%s err=%v", userID, time.Since(start), err)
		return fmt.Errorf("send typing: %w", err)
	}

	log.Printf("[sender] to=%s kind=typing state=sent elapsed=%s", userID, time.Since(start))
	return nil
}

// SendTextReply sends a text reply to a user through the iLink API.
// If clientID is empty, a new one is generated.
func SendTextReply(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string) error {
	if clientID == "" {
		clientID = NewClientID()
	}

	start := time.Now()

	// Convert markdown to plain text for WeChat display
	plainText := MarkdownToPlainText(text)
	log.Printf("[sender] to=%s client_id=%s kind=text state=start chars=%d preview=%q", toUserID, clientID, len(plainText), truncate(plainText, 80))

	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     clientID,
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{
					Type: ilink.ItemTypeText,
					TextItem: &ilink.TextItem{
						Text: plainText,
					},
				},
			},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		log.Printf("[sender] to=%s client_id=%s kind=text state=failed elapsed=%s err=%v", toUserID, clientID, time.Since(start), err)
		return fmt.Errorf("send message: %w", err)
	}

	if resp.Ret != 0 {
		err := fmt.Errorf("send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
		log.Printf("[sender] to=%s client_id=%s kind=text state=failed elapsed=%s ret=%d errmsg=%q", toUserID, clientID, time.Since(start), resp.Ret, resp.ErrMsg)
		return err
	}

	log.Printf("[sender] to=%s client_id=%s kind=text state=sent elapsed=%s chars=%d preview=%q", toUserID, clientID, time.Since(start), len(plainText), truncate(plainText, 80))
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
