package api

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qiuy-collab/weone/config"
	"github.com/qiuy-collab/weone/ilink"
	"github.com/qiuy-collab/weone/internal/runtime"
	"github.com/qiuy-collab/weone/materials"
	"github.com/qiuy-collab/weone/memory"
	"github.com/qiuy-collab/weone/messaging"
	"github.com/qiuy-collab/weone/proactive"
	"rsc.io/qr"
)

//go:embed static/index.html
var indexHTML string

type Server struct {
	runtimeAccounts *AccountRuntimeManager
	handler         *messaging.Handler
	memorySvc       *memory.Service
	materialsSvc    *materials.Service
	proactiveSvc    *proactive.Service
	addr            string

	configMu   sync.Mutex
	config     *config.Config
	saveConfig func(*config.Config) error
	lastTest   *runtimeTestResponse
	statusText func() string

	bindMu        sync.Mutex
	bindingState  *bindingState
	bindingCancel context.CancelFunc
}

type bindingState struct {
	QRCode       string    `json:"qrcode"`
	QRCodeURL    string    `json:"qrcode_url"`
	QRCodePNG    string    `json:"qrcode_png,omitempty"`
	Status       string    `json:"status"`
	LastError    string    `json:"last_error,omitempty"`
	BotID        string    `json:"bot_id,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	InProgress   bool      `json:"in_progress"`
	HasAccounts  bool      `json:"has_accounts,omitempty"`
	AccountCount int       `json:"account_count,omitempty"`
	PrimaryBotID string    `json:"primary_bot_id,omitempty"`
	CanStartBind bool      `json:"can_start_bind,omitempty"`
	Message      string    `json:"message,omitempty"`
}

type SendRequest struct {
	To       string `json:"to"`
	Text     string `json:"text,omitempty"`
	MediaURL string `json:"media_url,omitempty"`
}

type runtimeConfigResponse struct {
	Enabled      bool   `json:"enabled"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key,omitempty"`
	ModelName    string `json:"model_name"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Identity     string `json:"identity,omitempty"`
	Tone         string `json:"tone,omitempty"`
	Style        string `json:"style,omitempty"`
}

type runtimeConfigRequest struct {
	Enabled      bool   `json:"enabled"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	ModelName    string `json:"model_name"`
	SystemPrompt string `json:"system_prompt"`
	Identity     string `json:"identity"`
	Tone         string `json:"tone"`
	Style        string `json:"style"`
}

type runtimeTestRequest struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	ModelName string `json:"model_name"`
}

type runtimeTestResponse struct {
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Message   string    `json:"message"`
	TestedAt  time.Time `json:"tested_at"`
	Endpoint  string    `json:"endpoint,omitempty"`
}

type wechatAccountRemoveRequest struct {
	BotID string `json:"bot_id"`
}

type logsResponse struct {
	Path      string    `json:"path"`
	Lines     []string  `json:"lines"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Truncated bool      `json:"truncated"`
}

type memoryProfileUpsertRequest struct {
	BotID    string `json:"bot_id"`
	Markdown string `json:"markdown"`
	Source   string `json:"source,omitempty"`
}

type memoryDocumentResponse struct {
	BotID           string                  `json:"bot_id"`
	ProfileDocument memory.ProfileDocument  `json:"profile_document"`
	ShortTerm       []memory.ShortTermEntry `json:"short_term"`
}

type materialUpsertRequest struct {
	ID           string   `json:"id,omitempty"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Content      string   `json:"content,omitempty"`
	MediaPath    string   `json:"media_path,omitempty"`
	OriginalName string   `json:"original_name,omitempty"`
	MimeType     string   `json:"mime_type,omitempty"`
	FileSize     int64    `json:"file_size,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Description  string   `json:"description,omitempty"`
	Enabled      bool     `json:"enabled"`
}

type materialDeleteRequest struct {
	ID string `json:"id"`
}

type materialsListResponse struct {
	Items []materials.Material `json:"items"`
}

type materialImportResponse struct {
	Total     int                       `json:"total"`
	Processed int                       `json:"processed"`
	Item      *materials.Material       `json:"item,omitempty"`
	Analysis  *materials.ImportAnalysis `json:"analysis,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

type proactiveTaskDeleteRequest struct {
	ID string `json:"id"`
}

type proactiveTasksDeleteRequest struct {
	IDs []string `json:"ids"`
}

type proactiveTaskExecuteRequest struct {
	ID string `json:"id"`
}

type proactiveStatusResponse struct {
	Scheduler        proactive.SchedulerSnapshot `json:"scheduler"`
	BotIDs           []string                    `json:"bot_ids"`
	Policies         []proactive.Policy          `json:"policies"`
	Tasks            []proactive.Task            `json:"tasks"`
	TaskItems        []proactive.TaskListItem    `json:"task_items"`
	Targets          []proactive.TargetOption    `json:"targets"`
	HasTasks         bool                        `json:"has_tasks"`
	TaskCount        int                         `json:"task_count"`
	RunningTaskCount int                         `json:"running_task_count"`
	SchedulerRunning bool                        `json:"scheduler_running"`
}

func NewServer(runtimeAccounts *AccountRuntimeManager, handler *messaging.Handler, memorySvc *memory.Service, materialsSvc *materials.Service, proactiveSvc *proactive.Service, addr string, cfg *config.Config, saveConfig func(*config.Config) error, statusText func() string) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	if cfg != nil {
		cfg.APIAddr = addr
	}
	if runtimeAccounts == nil {
		runtimeAccounts = NewAccountRuntimeManager()
	}
	return &Server{runtimeAccounts: runtimeAccounts, handler: handler, memorySvc: memorySvc, materialsSvc: materialsSvc, proactiveSvc: proactiveSvc, addr: addr, config: cfg, saveConfig: saveConfig, statusText: statusText}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/runtime/config", s.handleRuntimeConfig)
	mux.HandleFunc("/api/runtime/status", s.handleRuntimeStatus)
	mux.HandleFunc("/api/runtime/test", s.handleRuntimeTest)
	mux.HandleFunc("/api/wechat/bind/start", s.handleBindStart)
	mux.HandleFunc("/api/wechat/bind/status", s.handleBindStatus)
	mux.HandleFunc("/api/wechat/bind/cancel", s.handleBindCancel)
	mux.HandleFunc("/api/wechat/accounts", s.handleAccounts)
	mux.HandleFunc("/api/wechat/accounts/remove", s.handleRemoveAccount)
	mux.HandleFunc("/api/memory/overview", s.handleMemoryOverview)
	mux.HandleFunc("/api/memory/profile", s.handleMemoryProfile)
	mux.HandleFunc("/api/memory/short-term/clear", s.handleMemoryShortTermClear)
	mux.HandleFunc("/api/materials", s.handleMaterials)
	mux.HandleFunc("/api/materials/item", s.handleMaterialItem)
	mux.HandleFunc("/api/materials/import", s.handleMaterialImport)
	mux.HandleFunc("/api/materials/upload", s.handleMaterialUpload)
	mux.HandleFunc("/api/materials/media", s.handleMaterialMedia)
	mux.HandleFunc("/api/materials/delete", s.handleMaterialDelete)
	mux.HandleFunc("/api/proactive/tasks", s.handleProactiveTasks)
	mux.HandleFunc("/api/proactive/task", s.handleProactiveTask)
	mux.HandleFunc("/api/proactive/task/delete", s.handleProactiveTaskDelete)
	mux.HandleFunc("/api/proactive/tasks/delete", s.handleProactiveTasksDelete)
	mux.HandleFunc("/api/proactive/task/execute", s.handleProactiveTaskExecute)
	mux.HandleFunc("/api/proactive/policies", s.handleProactivePolicies)
	mux.HandleFunc("/api/proactive/policy", s.handleProactivePolicy)
	mux.HandleFunc("/api/proactive/status", s.handleProactiveStatus)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: s.addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("[api] listening on %s", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, "render page failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tmpl.Execute(w, map[string]any{"Title": "weone 控制台"})
}

func (s *Server) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.runtimeConfigResponseLocked())
	case http.MethodPost:
		var req runtimeConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.BaseURL) == "" {
			http.Error(w, `"base_url" is required`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.ModelName) == "" {
			http.Error(w, `"model_name" is required`, http.StatusBadRequest)
			return
		}

		s.config.Runtime.Enabled = req.Enabled
		s.config.Runtime.Provider.Endpoint = strings.TrimSpace(req.BaseURL)
		s.config.Runtime.Provider.APIKey = req.APIKey
		s.config.Runtime.Provider.Model = strings.TrimSpace(req.ModelName)
		s.config.Runtime.Persona.SystemPrompt = strings.TrimSpace(req.SystemPrompt)
		s.config.Runtime.Persona.Identity = strings.TrimSpace(req.Identity)
		s.config.Runtime.Persona.Tone = strings.TrimSpace(req.Tone)
		s.config.Runtime.Persona.Style = strings.TrimSpace(req.Style)
		log.Printf("[api] runtime config update enabled=%v base_url=%q model=%q persona_identity=%q persona_tone=%q persona_style=%q has_system_prompt=%v", s.config.Runtime.Enabled, s.config.Runtime.Provider.Endpoint, s.config.Runtime.Provider.Model, s.config.Runtime.Persona.Identity, s.config.Runtime.Persona.Tone, s.config.Runtime.Persona.Style, s.config.Runtime.Persona.SystemPrompt != "")
		if s.config.Runtime.Provider.Type == "" {
			s.config.Runtime.Provider.Type = "openai"
		}
		if s.config.Runtime.Provider.TimeoutMs == 0 {
			s.config.Runtime.Provider.TimeoutMs = 120000
		}

		if s.saveConfig != nil {
			if err := s.saveConfig(s.config); err != nil {
				http.Error(w, "save config failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if s.handler != nil {
			if err := s.handler.UpdateRuntimeConfig(s.config.Runtime); err != nil {
				http.Error(w, "apply runtime config failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, s.runtimeConfigResponseLocked())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	s.configMu.Lock()
	defaultAgent := ""
	if s.statusText != nil {
		defaultAgent = strings.TrimSpace(s.statusText())
	}
	runtimeName := s.config.Runtime.Name
	if runtimeName == "" {
		runtimeName = "companion"
	}
	loadedBotIDs := s.clientBotIDs()
	savedAccounts, _ := ilink.ListAccounts(loadedBotIDs)
	savedAccountCount := len(savedAccounts)
	savedPrimaryBotID := ""
	if savedAccountCount > 0 {
		savedPrimaryBotID = savedAccounts[0].BotID
	}
	loadedPrimaryBotID := ""
	for _, account := range savedAccounts {
		if account.Loaded {
			loadedPrimaryBotID = account.BotID
			break
		}
	}
	if loadedPrimaryBotID == "" && len(loadedBotIDs) > 0 {
		loadedPrimaryBotID = loadedBotIDs[0]
	}
	resp := map[string]any{
		"runtime_enabled":       s.config.Runtime.Enabled,
		"base_url":              s.config.Runtime.Provider.Endpoint,
		"model_name":            s.config.Runtime.Provider.Model,
		"saved_account_count":   savedAccountCount,
		"loaded_account_count":  len(loadedBotIDs),
		"saved_primary_bot_id":  savedPrimaryBotID,
		"loaded_primary_bot_id": loadedPrimaryBotID,
		"default_agent":         defaultAgent,
		"runtime_name":          runtimeName,
		"runtime_active":        defaultAgent != "" && defaultAgent == runtimeName,
		"api_addr":              s.addr,
		"console_url":           "http://" + s.addr,
		"persona_identity":      s.config.Runtime.Persona.Identity,
		"persona_tone":          s.config.Runtime.Persona.Tone,
		"persona_style":         s.config.Runtime.Persona.Style,
		"has_system_prompt":     strings.TrimSpace(s.config.Runtime.Persona.SystemPrompt) != "",
	}
	if s.lastTest != nil {
		resp["last_test"] = s.lastTest
	}
	s.configMu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRuntimeTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req runtimeTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		http.Error(w, `"base_url" is required`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ModelName) == "" {
		http.Error(w, `"model_name" is required`, http.StatusBadRequest)
		return
	}

	providerCfg := config.ProviderConfig{
		Type:      "openai",
		Endpoint:  strings.TrimSpace(req.BaseURL),
		APIKey:    req.APIKey,
		Model:     strings.TrimSpace(req.ModelName),
		TimeoutMs: s.currentTimeout(),
	}
	endpoint := runtime.NormalizeEndpoint(providerCfg.Endpoint)

	startedAt := time.Now()
	err := runtime.TestConnection(r.Context(), providerCfg)
	resp := &runtimeTestResponse{
		OK:        err == nil,
		LatencyMs: time.Since(startedAt).Milliseconds(),
		TestedAt:  time.Now(),
		Endpoint:  endpoint,
	}
	if err != nil {
		resp.Message = formatRuntimeTestError(err)
	} else {
		resp.Message = "连接成功"
	}

	s.configMu.Lock()
	s.lastTest = resp
	s.configMu.Unlock()

	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, resp)
}

func (s *Server) handleBindStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	s.bindMu.Lock()
	if s.bindingCancel != nil {
		s.bindingCancel()
		s.bindingCancel = nil
	}
	s.bindMu.Unlock()

	qrResp, err := ilink.FetchQRCode(r.Context())
	if err != nil {
		http.Error(w, "fetch QR code failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	pngData, err := renderQRCodePNG(qrResp.QRCodeImgContent)
	if err != nil {
		http.Error(w, "render QR code failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	state := &bindingState{
		QRCode:       qrResp.QRCode,
		QRCodeURL:    qrResp.QRCodeImgContent,
		QRCodePNG:    "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData),
		Status:       "wait",
		StartedAt:    time.Now(),
		InProgress:   true,
		CanStartBind: false,
		Message:      "二维码已生成，5 分钟内有效；如果暂时不扫，可以直接取消本次绑定。",
	}
	state.HasAccounts, state.AccountCount, state.PrimaryBotID = s.accountSummary()

	pollCtx, cancel := context.WithCancel(context.Background())
	s.bindMu.Lock()
	s.bindingState = state
	s.bindingCancel = cancel
	s.bindMu.Unlock()

	go s.pollBinding(pollCtx, qrResp.QRCode)
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleBindStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if s.bindingState == nil {
		writeJSON(w, http.StatusOK, s.defaultBindingState())
		return
	}
	state := *s.bindingState
	state.HasAccounts, state.AccountCount, state.PrimaryBotID = s.accountSummary()
	state.CanStartBind = !state.InProgress
	if state.Status == "cancelled" && state.Message == "" {
		state.Message = "本次绑定已取消，你可以重新开始。"
	}
	if !state.InProgress && state.HasAccounts && (state.Status == "" || state.Status == "idle") {
		state.Status = "bound"
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleBindCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	s.bindMu.Lock()
	defer s.bindMu.Unlock()

	if s.bindingState == nil {
		state := s.defaultBindingState()
		state.Message = "当前没有进行中的绑定流程。"
		writeJSON(w, http.StatusOK, state)
		return
	}

	state := s.bindingState
	if !state.InProgress {
		state.CanStartBind = true
		if state.Message == "" {
			state.Message = "当前没有进行中的绑定流程。"
		}
		copyState := *state
		writeJSON(w, http.StatusOK, copyState)
		return
	}

	if s.bindingCancel != nil {
		s.bindingCancel()
		s.bindingCancel = nil
	}
	state.Status = "cancelled"
	state.LastError = ""
	state.BotID = ""
	state.QRCode = ""
	state.QRCodeURL = ""
	state.QRCodePNG = ""
	state.InProgress = false
	state.CanStartBind = true
	state.FinishedAt = time.Now()
	state.Message = "本次绑定已取消，你可以稍后重新生成二维码。"
	state.HasAccounts, state.AccountCount, state.PrimaryBotID = s.accountSummary()
	copyState := *state
	writeJSON(w, http.StatusOK, copyState)
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	accounts, err := ilink.ListAccounts(s.clientBotIDs())
	if err != nil {
		http.Error(w, "list accounts failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req wechatAccountRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	botID := strings.TrimSpace(req.BotID)
	if botID == "" {
		http.Error(w, `"bot_id" is required`, http.StatusBadRequest)
		return
	}
	if s.runtimeAccounts != nil {
		s.runtimeAccounts.Remove(botID)
	}
	if err := ilink.RemoveAccount(botID); err != nil {
		http.Error(w, "remove account failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[api] removed runtime and saved account bot_id=%s", botID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "账号已卸载并删除。",
		"bot_id":  botID,
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	tail := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
			if parsed > 1000 {
				parsed = 1000
			}
			tail = parsed
		}
	}
	resp, err := readLogsTail(tail)
	if err != nil {
		http.Error(w, "read logs failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMemoryOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.memorySvc == nil {
		http.Error(w, "memory service not configured", http.StatusServiceUnavailable)
		return
	}
	botID := strings.TrimSpace(r.URL.Query().Get("bot_id"))
	if botID == "" {
		writeJSON(w, http.StatusOK, memoryDocumentResponse{
			BotID:           "",
			ProfileDocument: memory.ProfileDocument{},
			ShortTerm:       []memory.ShortTermEntry{},
		})
		return
	}
	profileDocument, err := s.memorySvc.GetProfileDocument(botID)
	if err != nil {
		http.Error(w, "load profile document failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	shortTerm, err := s.memorySvc.ListShortTermByBot(botID, 5)
	if err != nil {
		http.Error(w, "list short-term memories failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, memoryDocumentResponse{
		BotID:           botID,
		ProfileDocument: profileDocument,
		ShortTerm:       shortTerm,
	})
}

func (s *Server) handleMemoryProfile(w http.ResponseWriter, r *http.Request) {
	if s.memorySvc == nil {
		http.Error(w, "memory service not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		botID := strings.TrimSpace(r.URL.Query().Get("bot_id"))
		if botID == "" {
			http.Error(w, `"bot_id" is required`, http.StatusBadRequest)
			return
		}
		doc, err := s.memorySvc.GetProfileDocument(botID)
		if err != nil {
			http.Error(w, "load profile document failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profile_document": doc})
	case http.MethodPost:
		var req memoryProfileUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		doc, err := s.memorySvc.SaveProfileDocument(strings.TrimSpace(req.BotID), req.Markdown, strings.TrimSpace(req.Source))
		if err != nil {
			http.Error(w, "save profile document failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profile_document": doc})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMemoryShortTermClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.memorySvc == nil {
		http.Error(w, "memory service not configured", http.StatusServiceUnavailable)
		return
	}
	botID := strings.TrimSpace(r.URL.Query().Get("bot_id"))
	if botID == "" {
		http.Error(w, `"bot_id" is required`, http.StatusBadRequest)
		return
	}
	if err := s.memorySvc.ClearShortTermByBot(botID); err != nil {
		http.Error(w, "clear short-term memory failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleMaterials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	items, err := s.materialsSvc.ListMaterials(strings.TrimSpace(r.URL.Query().Get("query")), strings.TrimSpace(r.URL.Query().Get("kind")))
	if err != nil {
		http.Error(w, "list materials failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, materialsListResponse{Items: items})
}

func (s *Server) handleMaterialItem(w http.ResponseWriter, r *http.Request) {
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, `"id" is required`, http.StatusBadRequest)
			return
		}
		item, err := s.materialsSvc.GetMaterial(id)
		if err != nil {
			http.Error(w, "load material failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
	case http.MethodPost:
		var req materialUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		item, err := s.materialsSvc.UpsertMaterial(materials.Material{
			ID:           strings.TrimSpace(req.ID),
			Kind:         materials.Kind(strings.TrimSpace(req.Kind)),
			Title:        strings.TrimSpace(req.Title),
			Content:      req.Content,
			MediaPath:    strings.TrimSpace(req.MediaPath),
			OriginalName: strings.TrimSpace(req.OriginalName),
			MimeType:     strings.TrimSpace(req.MimeType),
			FileSize:     req.FileSize,
			Tags:         req.Tags,
			Description:  req.Description,
			Enabled:      req.Enabled,
		})
		if err != nil {
			http.Error(w, "save material failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMaterialImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse multipart form failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		if single := r.MultipartForm.File["file"]; len(single) > 0 {
			files = single
		}
	}
	if len(files) == 0 {
		http.Error(w, "files are required", http.StatusBadRequest)
		return
	}
	index := 0
	if raw := strings.TrimSpace(r.FormValue("index")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &index); err != nil || index < 0 {
			http.Error(w, "invalid index", http.StatusBadRequest)
			return
		}
	}
	if index >= len(files) {
		http.Error(w, "index out of range", http.StatusBadRequest)
		return
	}
	analyzeWithAI := parseEnabledFlag(r.FormValue("analyze_ai"))
	enabled := parseEnabledFlag(r.FormValue("enabled"))
	header := files[index]
	file, err := header.Open()
	if err != nil {
		http.Error(w, "open upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("[materials] import start total=%d file=%s index=%d", len(files), header.Filename, index+1)
	result, err := s.materialsSvc.ImportMaterialWithAI(r.Context(), materials.ImportInput{
		FileName:      header.Filename,
		MimeType:      header.Header.Get("Content-Type"),
		Data:          data,
		Title:         strings.TrimSpace(r.FormValue("title")),
		Tags:          splitCommaValues(r.FormValue("tags")),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Enabled:       enabled,
		AnalyzeWithAI: analyzeWithAI,
	})
	if err != nil {
		log.Printf("[materials] import file=%s stage=failed err=%v", header.Filename, err)
		writeJSON(w, http.StatusBadRequest, materialImportResponse{Total: len(files), Processed: index, Error: err.Error()})
		return
	}
	log.Printf("[materials] import finished success=1 failed=0 file=%s material_id=%s", header.Filename, result.Item.ID)
	writeJSON(w, http.StatusOK, materialImportResponse{Total: len(files), Processed: index + 1, Item: &result.Item, Analysis: &result.Analysis})
}

func (s *Server) handleMaterialUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse multipart form failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	kind := materials.Kind(strings.TrimSpace(r.FormValue("kind")))
	if kind == materials.KindText {
		http.Error(w, "text materials do not support file upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	item, err := s.materialsSvc.SaveImportedMaterial(materials.Material{
		ID:           strings.TrimSpace(r.FormValue("id")),
		Kind:         kind,
		Title:        strings.TrimSpace(r.FormValue("title")),
		Tags:         splitCommaValues(r.FormValue("tags")),
		Description:  strings.TrimSpace(r.FormValue("description")),
		Enabled:      parseEnabledFlag(r.FormValue("enabled")),
		OriginalName: header.Filename,
		MimeType:     header.Header.Get("Content-Type"),
		FileSize:     header.Size,
	}, header.Filename, data, header.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "upload material failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleMaterialMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, `"id" is required`, http.StatusBadRequest)
		return
	}
	item, mediaPath, err := s.materialsSvc.FindMediaPathByID(id)
	if err != nil {
		http.Error(w, "load material media failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if item.MimeType != "" {
		w.Header().Set("Content-Type", item.MimeType)
	}
	if item.OriginalName != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", item.OriginalName))
	}
	http.ServeFile(w, r, mediaPath)
}

func splitCommaValues(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，'
	})
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func parseEnabledFlag(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) handleMaterialDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.materialsSvc == nil {
		http.Error(w, "materials service not configured", http.StatusServiceUnavailable)
		return
	}
	var req materialDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.materialsSvc.DeleteMaterial(strings.TrimSpace(req.ID)); err != nil {
		http.Error(w, "delete material failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleProactiveTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	tasks, err := s.proactiveSvc.ListTasks(strings.TrimSpace(r.URL.Query().Get("kind")), strings.TrimSpace(r.URL.Query().Get("bot_id")))
	if err != nil {
		http.Error(w, "list proactive tasks failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.proactiveSvc.ListTaskItems()
	if err != nil {
		http.Error(w, "list proactive task items failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	targets, err := s.proactiveSvc.ListTargetOptions()
	if err != nil {
		http.Error(w, "list proactive target options failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "items": items, "targets": targets})
}

func (s *Server) handleProactiveTask(w http.ResponseWriter, r *http.Request) {
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, `"id" is required`, http.StatusBadRequest)
			return
		}
		task, err := s.proactiveSvc.GetTask(id)
		if err != nil {
			http.Error(w, "load proactive task failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": task})
	case http.MethodPost:
		var req proactive.UpsertTaskInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		task, err := s.proactiveSvc.UpsertTask(req)
		if err != nil {
			http.Error(w, "save proactive task failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": task})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProactiveTaskDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	var req proactiveTaskDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.proactiveSvc.DeleteTask(strings.TrimSpace(req.ID)); err != nil {
		http.Error(w, "delete proactive task failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleProactiveTasksDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	var req proactiveTasksDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.proactiveSvc.DeleteTasks(req.IDs); err != nil {
		http.Error(w, "delete proactive tasks failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleProactiveTaskExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	var req proactiveTaskExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.proactiveSvc.ExecuteTask(r.Context(), strings.TrimSpace(req.ID))
	if err != nil {
		http.Error(w, "execute proactive task failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleProactivePolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	policies, err := s.proactiveSvc.ListPolicies()
	if err != nil {
		http.Error(w, "list proactive policies failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

func (s *Server) handleProactivePolicy(w http.ResponseWriter, r *http.Request) {
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		kind := proactive.PolicyKind(strings.TrimSpace(r.URL.Query().Get("kind")))
		if kind == "" {
			http.Error(w, `"kind" is required`, http.StatusBadRequest)
			return
		}
		policy, err := s.proactiveSvc.GetPolicy(kind)
		if err != nil {
			http.Error(w, "load proactive policy failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": policy})
	case http.MethodPost:
		var req proactive.UpsertPolicyInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		policy, err := s.proactiveSvc.UpsertPolicy(req)
		if err != nil {
			http.Error(w, "save proactive policy failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": policy})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProactiveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.proactiveSvc == nil {
		http.Error(w, "proactive service not configured", http.StatusServiceUnavailable)
		return
	}
	tasks, err := s.proactiveSvc.ListTasks("", "")
	if err != nil {
		http.Error(w, "list proactive tasks failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.proactiveSvc.ListTaskItems()
	if err != nil {
		http.Error(w, "list proactive task items failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	policies, err := s.proactiveSvc.ListPolicies()
	if err != nil {
		http.Error(w, "list proactive policies failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	targets, err := s.proactiveSvc.ListTargetOptions()
	if err != nil {
		http.Error(w, "list proactive target options failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	snapshot := s.proactiveSvc.SchedulerSnapshot()
	writeJSON(w, http.StatusOK, proactiveStatusResponse{
		Scheduler:        snapshot,
		BotIDs:           s.clientBotIDs(),
		Policies:         policies,
		Tasks:            tasks,
		TaskItems:        items,
		Targets:          targets,
		HasTasks:         len(tasks) > 0,
		TaskCount:        len(tasks),
		RunningTaskCount: snapshot.RunningTaskCount,
		SchedulerRunning: snapshot.Running,
	})
}

func (s *Server) defaultBindingState() bindingState {
	hasAccounts, count, primaryBotID := s.accountSummary()
	status := "idle"
	if hasAccounts {
		status = "bound"
	}
	return bindingState{
		Status:       status,
		HasAccounts:  hasAccounts,
		AccountCount: count,
		PrimaryBotID: primaryBotID,
		CanStartBind: true,
		InProgress:   false,
		Message:      map[bool]string{true: "当前已有绑定账号，如需新增可点击“新增账号”。", false: "当前还没有绑定账号。"}[hasAccounts],
	}
}

func (s *Server) accountSummary() (bool, int, string) {
	accounts, err := ilink.ListAccounts(s.clientBotIDs())
	if err != nil || len(accounts) == 0 {
		return false, 0, ""
	}
	primary := accounts[0].BotID
	for _, account := range accounts {
		if account.Loaded {
			primary = account.BotID
			break
		}
	}
	return true, len(accounts), primary
}

func (s *Server) pollBinding(ctx context.Context, qrcode string) {
	pollCtx, cancelTimeout := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelTimeout()

	creds, err := ilink.PollQRStatus(pollCtx, qrcode, func(status string) {
		s.bindMu.Lock()
		if s.bindingState != nil && s.bindingState.QRCode == qrcode {
			s.bindingState.Status = status
			s.bindingState.Message = bindProgressMessage(status)
		}
		s.bindMu.Unlock()
	})

	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if s.bindingState == nil || s.bindingState.QRCode != qrcode {
		return
	}
	if s.bindingCancel != nil {
		s.bindingCancel = nil
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.bindingState.Status = "error"
		s.bindingState.LastError = err.Error()
		s.bindingState.InProgress = false
		s.bindingState.CanStartBind = true
		s.bindingState.FinishedAt = time.Now()
		s.bindingState.Message = "绑定流程已结束，请刷新后重试。"
		return
	}

	if err := ilink.SaveCredentials(creds); err != nil {
		s.bindingState.Status = "error"
		s.bindingState.LastError = err.Error()
		s.bindingState.InProgress = false
		s.bindingState.CanStartBind = true
		s.bindingState.FinishedAt = time.Now()
		s.bindingState.Message = "账号保存失败，请重新绑定。"
		return
	}

	if s.runtimeAccounts != nil {
		s.runtimeAccounts.AddCredentials(creds)
	}
	s.bindingState.Status = "confirmed"
	s.bindingState.BotID = creds.ILinkBotID
	s.bindingState.InProgress = false
	s.bindingState.CanStartBind = true
	s.bindingState.FinishedAt = time.Now()
	s.bindingState.Message = "绑定成功，账号已经立即加入当前运行进程。"
	s.bindingState.HasAccounts, s.bindingState.AccountCount, s.bindingState.PrimaryBotID = s.accountSummary()
}

func bindProgressMessage(status string) string {
	switch status {
	case "wait":
		return "二维码已生成，等待扫码。"
	case "scaned":
		return "已扫码，等待手机确认。"
	case "confirmed":
		return "绑定已确认，正在保存账号。"
	default:
		return ""
	}
}

func (s *Server) runtimeConfigResponseLocked() runtimeConfigResponse {
	modelName := s.config.Runtime.Provider.Model
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	return runtimeConfigResponse{
		Enabled:      s.config.Runtime.Enabled,
		BaseURL:      s.config.Runtime.Provider.Endpoint,
		APIKey:       s.config.Runtime.Provider.APIKey,
		ModelName:    modelName,
		SystemPrompt: s.config.Runtime.Persona.SystemPrompt,
		Identity:     s.config.Runtime.Persona.Identity,
		Tone:         s.config.Runtime.Persona.Tone,
		Style:        s.config.Runtime.Persona.Style,
	}
}

func (s *Server) currentTimeout() int {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.config.Runtime.Provider.TimeoutMs > 0 {
		return s.config.Runtime.Provider.TimeoutMs
	}
	return 120000
}

func (s *Server) clientBotIDs() []string {
	if s.runtimeAccounts == nil {
		return nil
	}
	return s.runtimeAccounts.BotIDs()
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return
	}
	client := s.runtimeAccounts.FirstClient()
	if client == nil {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, "", ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			http.Error(w, "send text failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s: %q", req.To, req.Text)
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, ""); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
			}
		}
	}

	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, ""); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s: %s", req.To, req.MediaURL)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func formatRuntimeTestError(err error) string {
	message := err.Error()
	const prefix = "API error HTTP "
	if !strings.HasPrefix(message, prefix) {
		return message
	}

	rest := strings.TrimPrefix(message, prefix)
	statusPart, bodyPart, found := strings.Cut(rest, ":")
	statusPart = strings.TrimSpace(statusPart)
	bodyPart = strings.TrimSpace(bodyPart)
	if !found || bodyPart == "" {
		return message
	}

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(bodyPart), &payload); err != nil {
		return fmt.Sprintf("请求失败（HTTP %s）", statusPart)
	}

	parts := make([]string, 0, 3)
	if payload.Error.Message != "" {
		parts = append(parts, payload.Error.Message)
	}
	if payload.Error.Code != "" {
		parts = append(parts, "代码: "+payload.Error.Code)
	}
	if payload.Error.Type != "" {
		parts = append(parts, "类型: "+payload.Error.Type)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("请求失败（HTTP %s）", statusPart)
	}
	return fmt.Sprintf("请求失败（HTTP %s）：%s", statusPart, strings.Join(parts, "，"))
}

func renderQRCodePNG(content string) ([]byte, error) {
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		return nil, err
	}
	return code.PNG(), nil
}

func weoneLogPath() (string, error) {
	root, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "weone.log"), nil
}

func readLogsTail(limit int) (*logsResponse, error) {
	path, err := weoneLogPath()
	if err != nil {
		return nil, err
	}
	resp := &logsResponse{Path: path, Lines: []string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return resp, nil
		}
		return nil, err
	}
	info, err := os.Stat(path)
	if err == nil {
		resp.UpdatedAt = info.ModTime()
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		resp.Truncated = true
		lines = lines[len(lines)-limit:]
	}
	resp.Lines = lines
	return resp, nil
}
