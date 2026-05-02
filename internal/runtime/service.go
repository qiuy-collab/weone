package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/qiuy-collab/weone/config"
)

// ChatMessage is an OpenAI-compatible chat message.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MessagePart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL string `json:"url"`
}

type RichChatMessage struct {
	Role    string        `json:"role"`
	Content []MessagePart `json:"content"`
}

// Provider sends requests to a model API.
type Provider interface {
	Generate(ctx context.Context, messages []ChatMessage) (string, error)
	GenerateRich(ctx context.Context, messages []RichChatMessage) (string, error)
}

// OpenAIProvider calls an OpenAI-compatible chat completions endpoint.
type OpenAIProvider struct {
	endpoint   string
	apiKey     string
	headers    map[string]string
	model      string
	httpClient *http.Client
}

// NewOpenAIProvider creates a provider from runtime config.
func NewOpenAIProvider(cfg config.ProviderConfig) *OpenAIProvider {
	timeout := 120 * time.Second
	if cfg.TimeoutMs > 0 {
		timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
	}
	model := cfg.Model
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAIProvider{
		endpoint:   NormalizeEndpoint(cfg.Endpoint),
		apiKey:     cfg.APIKey,
		headers:    cfg.Headers,
		model:      model,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Generate sends the messages to the provider endpoint.
func (p *OpenAIProvider) Generate(ctx context.Context, messages []ChatMessage) (string, error) {
	start := time.Now()
	log.Printf("[runtime-provider] state=request-start endpoint=%s model=%s mode=text messages=%d", p.endpoint, p.model, len(messages))
	return p.generate(ctx, map[string]any{
		"model":    p.model,
		"messages": messages,
		"stream":   false,
	}, start, "text")
}

func (p *OpenAIProvider) GenerateRich(ctx context.Context, messages []RichChatMessage) (string, error) {
	imageBytes := estimateRichImageBytes(messages)
	start := time.Now()
	log.Printf("[runtime-provider] state=request-start endpoint=%s model=%s mode=multimodal messages=%d image_bytes=%d", p.endpoint, p.model, len(messages), imageBytes)
	return p.generate(ctx, map[string]any{
		"model":    p.model,
		"messages": messages,
		"stream":   false,
	}, start, "multimodal")
}

func (p *OpenAIProvider) generate(ctx context.Context, reqBody map[string]any, start time.Time, mode string) (string, error) {
	if p.endpoint == "" {
		return "", fmt.Errorf("provider endpoint is empty")
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=marshal elapsed=%s err=%v", p.endpoint, p.model, mode, time.Since(start), err)
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=create-request elapsed=%s err=%v", p.endpoint, p.model, mode, time.Since(start), err)
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=http-do elapsed=%s err=%v", p.endpoint, p.model, mode, time.Since(start), err)
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=read-response elapsed=%s status=%d err=%v", p.endpoint, p.model, mode, time.Since(start), resp.StatusCode, err)
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=api-response elapsed=%s status=%d body=%q", p.endpoint, p.model, mode, time.Since(start), resp.StatusCode, truncateForLog(string(body), 200))
		return "", fmt.Errorf("API error HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=parse-response elapsed=%s err=%v", p.endpoint, p.model, mode, time.Since(start), err)
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(result.Choices) == 0 {
		err := fmt.Errorf("no choices in response")
		log.Printf("[runtime-provider] state=request-failed endpoint=%s model=%s mode=%s stage=validate-response elapsed=%s err=%v", p.endpoint, p.model, mode, time.Since(start), err)
		return "", err
	}

	reply := result.Choices[0].Message.Content
	log.Printf("[runtime-provider] state=request-finished endpoint=%s model=%s mode=%s elapsed=%s reply_chars=%d preview=%q", p.endpoint, p.model, mode, time.Since(start), len(reply), truncateForLog(reply, 120))
	return reply, nil
}

func TestConnection(ctx context.Context, cfg config.ProviderConfig) error {
	provider := NewOpenAIProvider(cfg)
	_, err := provider.Generate(ctx, []ChatMessage{{Role: "user", Content: "ping"}})
	return err
}

func NormalizeEndpoint(raw string) string {
	endpoint := strings.TrimSpace(raw)
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		return ""
	}
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/v1") {
		return endpoint + "/chat/completions"
	}
	if strings.Contains(endpoint, "/v1/") {
		return endpoint
	}
	return endpoint + "/chat/completions"
}

// Service is the packaged in-process runtime for phase 1.
type Service struct {
	mu         sync.Mutex
	provider   Provider
	persona    config.PersonaConfig
	maxHistory int
	history    map[string][]ChatMessage
}

// UpdateConfig updates the runtime provider and persona without discarding history.
func (s *Service) UpdateConfig(cfg config.RuntimeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxHistory := cfg.MaxHistory
	if maxHistory <= 0 {
		maxHistory = 20
	}

	s.provider = NewOpenAIProvider(cfg.Provider)
	s.persona = cfg.Persona
	s.maxHistory = maxHistory
	for conversationID, messages := range s.history {
		if len(messages) > s.maxHistory*2 {
			s.history[conversationID] = messages[len(messages)-s.maxHistory*2:]
		}
	}

	log.Printf("[runtime] state=config-updated provider_type=%q model=%q max_history=%d persona_identity=%q persona_tone=%q persona_style=%q has_system_prompt=%v", cfg.Provider.Type, cfg.Provider.Model, maxHistory, cfg.Persona.Identity, cfg.Persona.Tone, cfg.Persona.Style, strings.TrimSpace(cfg.Persona.SystemPrompt) != "")
}

// NewService constructs the in-package runtime service.
func NewService(cfg config.RuntimeConfig) *Service {
	maxHistory := cfg.MaxHistory
	if maxHistory <= 0 {
		maxHistory = 20
	}
	log.Printf("[runtime] state=init provider_type=%q model=%q max_history=%d persona_identity=%q persona_tone=%q persona_style=%q has_system_prompt=%v", cfg.Provider.Type, cfg.Provider.Model, maxHistory, cfg.Persona.Identity, cfg.Persona.Tone, cfg.Persona.Style, strings.TrimSpace(cfg.Persona.SystemPrompt) != "")

	providerType := cfg.Provider.Type
	if providerType == "" || providerType == "openai" {
		return &Service{
			provider:   NewOpenAIProvider(cfg.Provider),
			persona:    cfg.Persona,
			maxHistory: maxHistory,
			history:    make(map[string][]ChatMessage),
		}
	}

	return &Service{
		provider:   NewOpenAIProvider(cfg.Provider),
		persona:    cfg.Persona,
		maxHistory: maxHistory,
		history:    make(map[string][]ChatMessage),
	}
}

// Reply generates a reply for a conversation.
func (s *Service) Reply(ctx context.Context, conversationID, message, memoryContext string) (string, error) {
	messages, historyCount := s.buildMessages(conversationID, message, memoryContext)
	start := time.Now()
	log.Printf("[runtime] conversation=%s state=reply-start history_messages=%d request_messages=%d input_chars=%d memory_chars=%d", conversationID, historyCount, len(messages), len(message), len(memoryContext))

	reply, err := s.provider.Generate(ctx, messages)
	if err != nil {
		log.Printf("[runtime] conversation=%s state=reply-failed elapsed=%s err=%v", conversationID, time.Since(start), err)
		return "", err
	}

	s.mu.Lock()
	s.history[conversationID] = append(s.history[conversationID],
		ChatMessage{Role: "user", Content: message},
		ChatMessage{Role: "assistant", Content: reply},
	)
	if len(s.history[conversationID]) > s.maxHistory*2 {
		s.history[conversationID] = s.history[conversationID][len(s.history[conversationID])-s.maxHistory*2:]
	}
	updatedHistoryCount := len(s.history[conversationID])
	s.mu.Unlock()

	log.Printf("[runtime] conversation=%s state=reply-finished elapsed=%s history_messages=%d reply_chars=%d preview=%q", conversationID, time.Since(start), updatedHistoryCount, len(reply), truncateForLog(reply, 120))
	return reply, nil
}

// ResetConversation clears the current conversation history.
func (s *Service) ResetConversation(conversationID string) {
	s.mu.Lock()
	delete(s.history, conversationID)
	s.mu.Unlock()
	log.Printf("[runtime] conversation=%s state=reset", conversationID)
}

func (s *Service) buildMessages(conversationID, message, memoryContext string) ([]ChatMessage, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	historyCount := len(s.history[conversationID])
	messages := make([]ChatMessage, 0, 2+historyCount)
	systemPrompt := buildSystemPrompt(s.persona, memoryContext)
	if systemPrompt != "" {
		log.Printf("[runtime] conversation=%s state=system-prompt-ready chars=%d preview=%q", conversationID, len(systemPrompt), truncateForLog(systemPrompt, 120))
		messages = append(messages, ChatMessage{Role: "system", Content: systemPrompt})
	}
	if hist, ok := s.history[conversationID]; ok {
		messages = append(messages, hist...)
	}
	messages = append(messages, ChatMessage{Role: "user", Content: message})
	return messages, historyCount
}

func buildSystemPrompt(persona config.PersonaConfig, memoryContext string) string {
	parts := make([]string, 0, 5)
	if persona.SystemPrompt != "" {
		parts = append(parts, persona.SystemPrompt)
	}
	if persona.Identity != "" {
		parts = append(parts, "身份："+persona.Identity)
	}
	if persona.Tone != "" {
		parts = append(parts, "语气："+persona.Tone)
	}
	if persona.Style != "" {
		parts = append(parts, "回复风格："+persona.Style)
	}
	if strings.TrimSpace(memoryContext) != "" {
		parts = append(parts, memoryContext)
	}

	if len(parts) == 0 {
		return ""
	}

	var buf bytes.Buffer
	for i, part := range parts {
		if i > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(part)
	}
	return buf.String()
}

func NewTextPart(text string) MessagePart {
	return MessagePart{Type: "text", Text: text}
}

func NewImageDataPart(mimeType string, data []byte) MessagePart {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return MessagePart{
		Type: "image_url",
		ImageURL: &ImageURLPart{
			URL: "data:" + mimeType + ";base64," + encoded,
		},
	}
}

func estimateRichImageBytes(messages []RichChatMessage) int {
	total := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type != "image_url" || part.ImageURL == nil {
				continue
			}
			url := strings.TrimSpace(part.ImageURL.URL)
			idx := strings.Index(url, ",")
			if idx < 0 || idx == len(url)-1 {
				continue
			}
			payload := url[idx+1:]
			total += base64.StdEncoding.DecodedLen(len(payload))
		}
	}
	return total
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
