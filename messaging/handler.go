package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qiuy-collab/weone/agent"
	"github.com/qiuy-collab/weone/config"
	"github.com/qiuy-collab/weone/ilink"
	internalruntime "github.com/qiuy-collab/weone/internal/runtime"
	"github.com/qiuy-collab/weone/materials"
	"github.com/qiuy-collab/weone/memory"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu            sync.RWMutex
	defaultName   string
	runtimeName   string
	agents        map[string]agent.Agent // name -> running agent
	runtimeSvc    *internalruntime.Service
	memorySvc     *memory.Service
	materialsSvc  *materials.Service
	agentMetas    []AgentMeta       // all configured agents (for /status)
	agentWorkDirs map[string]string // agent name -> configured/runtime cwd
	customAliases map[string]string // custom alias -> agent name (from config)
	factory       AgentFactory
	saveDefault   SaveDefaultFunc
	contextTokens sync.Map // map[userID]contextToken
	saveDir       string   // directory to save images/files to
	seenMsgs      sync.Map // map[int64]time.Time — dedup by message_id
	onInbound     func(botID, userID string, at time.Time)
	onRuntimeTurn func(ctx context.Context, botID, userID, message, reply, memoryContext string)
}

// StatusSnapshot describes the current default reply engine.
type StatusSnapshot struct {
	DefaultAgent  string
	RuntimeName   string
	RuntimeActive bool
}

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc, runtimeName string, runtimeSvc *internalruntime.Service, memorySvc *memory.Service, materialsSvc *materials.Service) *Handler {
	return &Handler{
		runtimeName:   runtimeName,
		runtimeSvc:    runtimeSvc,
		memorySvc:     memorySvc,
		materialsSvc:  materialsSvc,
		agents:        make(map[string]agent.Agent),
		agentWorkDirs: make(map[string]string),
		factory:       factory,
		saveDefault:   saveDefault,
	}
}

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

func (h *Handler) SetInboundRecorder(recorder func(botID, userID string, at time.Time)) {
	h.onInbound = recorder
}

func (h *Handler) SetRuntimeTurnRecorder(recorder func(ctx context.Context, botID, userID, message, reply, memoryContext string)) {
	h.onRuntimeTurn = recorder
}

// cleanSeenMsgs removes entries older than 5 minutes from the dedup cache.
func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// StatusSnapshot returns the current default reply engine state.
func (h *Handler) StatusSnapshot() StatusSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	defaultAgent := h.defaultName
	if defaultAgent == "" && h.runtimeSvc != nil {
		defaultAgent = h.runtimeName
	}

	return StatusSnapshot{
		DefaultAgent:  defaultAgent,
		RuntimeName:   h.runtimeName,
		RuntimeActive: defaultAgent != "" && defaultAgent == h.runtimeName,
	}
}

// UpdateRuntimeConfig hot-reloads the packaged runtime configuration.
func (h *Handler) UpdateRuntimeConfig(cfg config.RuntimeConfig) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.runtimeSvc == nil {
		return fmt.Errorf("runtime not configured")
	}
	h.runtimeSvc.UpdateConfig(cfg)
	return nil
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// getDefaultAgent returns the default agent (may be nil if not ready yet).
func (h *Handler) getDefaultAgent() agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" || h.defaultName == h.runtimeName {
		return nil
	}
	return h.agents[h.defaultName]
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	if name == h.runtimeName {
		return true
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	// Parse consecutive @name or /name tokens from the start
	var names []string
	rest := text
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "@") {
			break
		}

		// Strip prefix
		after := rest[1:]
		idx := strings.IndexAny(after, " /@")
		var token string
		if idx < 0 {
			// Rest is just the name, no message
			token = after
			rest = ""
		} else if after[idx] == '/' || after[idx] == '@' {
			// Next token is another @name or /name
			token = after[:idx]
			rest = after[idx:]
		} else {
			// Space — name ends here
			token = after[:idx]
			rest = strings.TrimSpace(after[idx+1:])
		}

		if token != "" {
			names = append(names, h.resolveAlias(token))
		}

		if rest == "" {
			break
		}
	}

	// Deduplicate names preserving order
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

func shortTraceID(msg ilink.WeixinMessage, clientID string) string {
	if msg.MessageID != 0 {
		return fmt.Sprintf("msg-%d", msg.MessageID)
	}
	if clientID != "" {
		if len(clientID) > 8 {
			return clientID[:8]
		}
		return clientID
	}
	return "unknown"
}

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		// Clean up old entries periodically (fire-and-forget)
		go h.cleanSeenMsgs()
	}

	// Extract text from item list (text message or voice transcription)
	// Generate a clientID for this reply (used to correlate typing → finish)
	clientID := NewClientID()
	traceID := shortTraceID(msg, clientID)

	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] trace=%s user=%s source=voice-transcription text=%q", traceID, msg.FromUserID, truncate(text, 80))
		}
	}
	if text == "" {
		// Check for image message
		if img := extractImage(msg); img != nil && h.saveDir != "" {
			h.handleImageSave(ctx, client, msg, img)
			return
		}
		log.Printf("[handler] trace=%s user=%s route=skip reason=non-text", traceID, msg.FromUserID)
		return
	}

	log.Printf("[handler] trace=%s user=%s message_id=%d route=runtime-only text=%q", traceID, msg.FromUserID, msg.MessageID, truncate(text, 80))

	// Store context token for this user
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	if h.onInbound != nil {
		h.onInbound(client.BotID(), msg.FromUserID, time.Now().UTC())
	}

	// Intercept URLs: save to Linkhoard directly without AI agent
	trimmed := strings.TrimSpace(text)
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] trace=%s user=%s branch=save-url url=%s", traceID, msg.FromUserID, rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}

	// Built-in commands (no typing needed)
	if trimmed == "/info" {
		log.Printf("[handler] trace=%s user=%s branch=command name=/info", traceID, msg.FromUserID)
		reply := h.buildStatus()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/help" {
		log.Printf("[handler] trace=%s user=%s branch=command name=/help", traceID, msg.FromUserID)
		reply := buildHelpText()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		log.Printf("[handler] trace=%s user=%s branch=command name=%s", traceID, msg.FromUserID, trimmed)
		reply := h.resetDefaultSession(ctx, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		log.Printf("[handler] trace=%s user=%s branch=command name=/cwd", traceID, msg.FromUserID)
		reply := h.handleCwd(trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}

	// Route: runtime-only mode ignores agent switching/broadcast commands and treats them as normal text.
	agentNames, message := h.parseCommand(text)
	if len(agentNames) > 0 {
		log.Printf("[handler] trace=%s user=%s route=runtime-only ignored_agent_command raw=%q", traceID, msg.FromUserID, truncate(text, 80))
		if len(agentNames) == 1 && message == "" && agentNames[0] == h.runtimeName {
			reply := h.switchDefault(ctx, agentNames[0])
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}

	h.sendToDefaultAgent(ctx, client, msg, text, clientID)
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	traceID := shortTraceID(msg, clientID)
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] trace=%s user=%s stage=typing state=failed err=%v", traceID, msg.FromUserID, typingErr)
		}
	}()

	h.mu.RLock()
	defaultName := h.defaultName
	defaultIsRuntime := defaultName == h.runtimeName
	h.mu.RUnlock()

	var reply string
	botID := client.BotID()
	log.Printf("[handler] trace=%s bot=%s user=%s route=runtime default=%s input=%q", traceID, botID, msg.FromUserID, defaultName, truncate(text, 80))
	if defaultIsRuntime {
		var err error
		reply, err = h.chatWithRuntime(ctx, botID, msg.FromUserID, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
		h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
		return
	}

	ag := h.getDefaultAgent()
	if ag != nil {
		var err error
		reply, err = h.chatWithAgent(ctx, ag, msg.FromUserID, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
	} else if h.runtimeSvc != nil {
		log.Printf("[handler] default agent not ready, falling back to packaged runtime for bot=%s user=%s", botID, msg.FromUserID)
		var err error
		reply, err = h.chatWithRuntime(ctx, botID, msg.FromUserID, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
		defaultName = h.runtimeName
	} else {
		log.Printf("[handler] agent not ready, using echo mode for %s", msg.FromUserID)
		reply = "[echo] " + text
	}

	h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message, clientID string) {
	if name == h.runtimeName {
		reply, err := h.chatWithRuntime(ctx, client.BotID(), msg.FromUserID, message)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
		h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
		return
	}

	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	reply, err := h.chatWithAgent(ctx, ag, msg.FromUserID, message)
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string) {
	type result struct {
		name  string
		reply string
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			if n == h.runtimeName {
				reply, err := h.chatWithRuntime(ctx, client.BotID(), msg.FromUserID, message)
				if err != nil {
					ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
					return
				}
				ch <- result{name: n, reply: reply}
				return
			}

			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			reply, err := h.chatWithAgent(ctx, ag, msg.FromUserID, message)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			ch <- result{name: n, reply: reply}
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		reply := fmt.Sprintf("[%s] %s", r.name, r.reply)
		clientID := NewClientID()
		h.sendReplyWithMedia(ctx, client, msg, r.name, reply, clientID)
	}
}

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
	traceID := shortTraceID(msg, clientID)
	imageURLs := ExtractImageURLs(reply)
	materialDirectives := ExtractMaterialDirectives(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)
	visibleReply := StripMaterialDirectives(reply)

	log.Printf("[handler] trace=%s user=%s stage=reply-prepare agent=%s text_chars=%d images=%d material_media=%d attachments=%d preview=%q", traceID, msg.FromUserID, agentName, len(visibleReply), len(imageURLs), len(materialDirectives), len(attachmentPaths), truncate(visibleReply, 100))

	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] trace=%s user=%s stage=attachment status=rejected agent=%q path=%s", traceID, msg.FromUserID, agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] trace=%s user=%s stage=attachment status=failed path=%s err=%v", traceID, msg.FromUserID, attachmentPath, err)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		log.Printf("[handler] trace=%s user=%s stage=attachment status=sent path=%s", traceID, msg.FromUserID, attachmentPath)
		sentPaths = append(sentPaths, attachmentPath)
	}

	visibleReply = rewriteReplyWithAttachmentResults(visibleReply, sentPaths, failedPaths)

	if err := SendTextReply(ctx, client, msg.FromUserID, visibleReply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] trace=%s user=%s stage=reply-send status=failed err=%v", traceID, msg.FromUserID, err)
	} else {
		log.Printf("[handler] trace=%s user=%s stage=reply-send status=sent", traceID, msg.FromUserID)
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] trace=%s user=%s stage=image status=failed url=%s err=%v", traceID, msg.FromUserID, imgURL, err)
			log.Printf("[materials] bot=%s user=%s 图片素材发送失败：url=%s err=%v", client.BotID(), msg.FromUserID, imgURL, err)
			continue
		}
		log.Printf("[handler] trace=%s user=%s stage=image status=sent url=%s", traceID, msg.FromUserID, imgURL)
		log.Printf("[materials] bot=%s user=%s 图片素材已发送：url=%s", client.BotID(), msg.FromUserID, imgURL)
	}

	for _, directive := range materialDirectives {
		if h.materialsSvc == nil {
			log.Printf("[materials] bot=%s user=%s 素材发送失败：素材=%q 类型=%s err=materials service not configured", client.BotID(), msg.FromUserID, directive.Title, directive.Kind)
			continue
		}
		item, mediaPath, err := h.materialsSvc.FindMediaPathByID(directive.ID)
		if err != nil {
			log.Printf("[materials] bot=%s user=%s 素材发送失败：素材=%q 类型=%s id=%s err=%v", client.BotID(), msg.FromUserID, directive.Title, directive.Kind, directive.ID, err)
			continue
		}
		mediaCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		if err := SendMediaFromPath(mediaCtx, client, msg.FromUserID, mediaPath, msg.ContextToken); err != nil {
			cancel()
			log.Printf("[materials] bot=%s user=%s 素材发送失败：素材=%q 类型=%s id=%s path=%s err=%v", client.BotID(), msg.FromUserID, item.Title, item.Kind, item.ID, mediaPath, err)
			continue
		}
		cancel()
		log.Printf("[materials] bot=%s user=%s 素材已发送：素材=%q 类型=%s id=%s path=%s", client.BotID(), msg.FromUserID, item.Title, item.Kind, item.ID, mediaPath)
	}
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{defaultAttachmentWorkspace()}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}

	return roots
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, error) {
	info := ag.Info()
	log.Printf("[handler] user=%s route=agent state=start agent=%s model=%s input=%q", userID, info.Name, info.Model, truncate(message, 80))

	start := time.Now()
	reply, err := ag.Chat(ctx, userID, message)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] user=%s route=agent state=failed agent=%s model=%s elapsed=%s err=%v", userID, info.Name, info.Model, elapsed, err)
		return "", err
	}

	log.Printf("[handler] user=%s route=agent state=finished agent=%s model=%s elapsed=%s preview=%q", userID, info.Name, info.Model, elapsed, truncate(reply, 100))
	return reply, nil
}

func (h *Handler) chatWithRuntime(ctx context.Context, botID, userID, message string) (string, error) {
	if h.runtimeSvc == nil {
		return "", fmt.Errorf("runtime not configured")
	}

	memoryContext := ""
	if h.memorySvc != nil {
		memoryState := h.memorySvc.CollectRuntimeContext(botID)
		memoryContext = memoryState.LoadedContext
		if memoryContext == "" {
			log.Printf("[memory] bot=%s user=%s 当前没有可加载记忆", botID, userID)
		} else {
			log.Printf("[memory] bot=%s user=%s 已加载记忆上下文：长期画像 %d 条，短期记忆 %d 条，上下文 %d 字，预览=%q", botID, userID, memoryState.Result.LoadedProfiles, memoryState.Result.LoadedShortTerm, len(memoryContext), truncate(memoryContext, 120))
		}
	}

	materialsContext := ""
	materialsKeywords := []string(nil)
	materialsMatches := []materials.MaterialMatch(nil)
	if h.materialsSvc != nil {
		searchResult, err := h.materialsSvc.SearchForMessage(message, 5)
		if err != nil {
			log.Printf("[materials] bot=%s user=%s 素材检索失败：err=%v", botID, userID, err)
		} else {
			materialsKeywords = searchResult.Keywords
			materialsMatches = searchResult.Matches
			materialsContext = h.materialsSvc.BuildRuntimeContext(searchResult)
			if len(searchResult.Matches) == 0 {
				if len(searchResult.Keywords) == 0 {
					log.Printf("[materials] bot=%s user=%s 当前没有提取到素材关键词", botID, userID)
				} else {
					log.Printf("[materials] bot=%s user=%s 当前没有命中可用素材，关键词=%q", botID, userID, strings.Join(searchResult.Keywords, ", "))
				}
			} else {
				log.Printf("[materials] bot=%s user=%s 已命中素材候选 %d 条，关键词=%q", botID, userID, len(searchResult.Matches), strings.Join(searchResult.Keywords, ", "))
				for _, match := range searchResult.Matches {
					log.Printf("[materials] bot=%s user=%s 候选素材=%q 类型=%s 分数=%d 命中原因=%s", botID, userID, match.Material.Title, match.Material.Kind, match.Score, formatMaterialReasonsForLog(match.Reasons))
				}
			}
		}
	}

	runtimeContext := joinRuntimeContexts(memoryContext, materialsContext)
	start := time.Now()
	log.Printf("[handler] bot=%s user=%s route=runtime state=start input=%q memory_chars=%d materials_chars=%d", botID, userID, truncate(message, 80), len(memoryContext), len(materialsContext))
	reply, err := h.runtimeSvc.Reply(ctx, userID, message, runtimeContext)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[handler] bot=%s user=%s route=runtime state=failed elapsed=%s err=%v", botID, userID, elapsed, err)
		return "", err
	}

	materialDirectives := ExtractMaterialDirectives(reply)
	imageURLs := ExtractImageURLs(reply)
	if h.materialsSvc != nil && len(materialDirectives) == 0 && len(imageURLs) == 0 {
		replyIntentResult, err := h.materialsSvc.SearchForReplyIntent(StripMaterialDirectives(reply), 1)
		if err != nil {
			log.Printf("[materials] bot=%s user=%s reply-intent 检索失败：err=%v", botID, userID, err)
		} else if len(replyIntentResult.Keywords) > 0 {
			log.Printf("[materials] bot=%s user=%s reply-intent keywords=%q", botID, userID, strings.Join(replyIntentResult.Keywords, ", "))
			if len(replyIntentResult.Matches) == 0 {
				log.Printf("[materials] bot=%s user=%s reply-intent 当前没有命中可追加素材", botID, userID)
			} else {
				match := replyIntentResult.Matches[0]
				log.Printf("[materials] bot=%s user=%s reply-intent 命中候选素材=%q 类型=%s 分数=%d 命中原因=%s", botID, userID, match.Material.Title, match.Material.Kind, match.Score, formatMaterialReasonsForLog(match.Reasons))
				reply = strings.TrimSpace(reply) + "\n[[material:id=" + match.Material.ID + ";kind=" + string(match.Material.Kind) + ";title=" + match.Material.Title + "]]"
				materialDirectives = ExtractMaterialDirectives(reply)
				log.Printf("[materials] bot=%s user=%s reply-intent 已追加素材指令：素材=%q 类型=%s id=%s", botID, userID, match.Material.Title, match.Material.Kind, match.Material.ID)
			}
		}
	}

	if len(materialsMatches) > 0 {
		if len(materialDirectives) == 0 && len(imageURLs) == 0 {
			log.Printf("[materials] bot=%s user=%s 本轮未采用素材候选", botID, userID)
		} else {
			for _, imageURL := range imageURLs {
				log.Printf("[materials] bot=%s user=%s 识别到图片素材使用意图：url=%s", botID, userID, imageURL)
			}
			for _, directive := range materialDirectives {
				log.Printf("[materials] bot=%s user=%s 识别到素材使用意图：素材=%q 类型=%s id=%s", botID, userID, directive.Title, directive.Kind, directive.ID)
			}
		}
	} else if len(materialsKeywords) > 0 {
		log.Printf("[materials] bot=%s user=%s 本轮素材关键词=%q，但无候选可供使用", botID, userID, strings.Join(materialsKeywords, ", "))
	}

	if h.memorySvc != nil {
		memoryResult := h.memorySvc.RecordRuntimeTurn(botID, userID, userID, message, StripMaterialDirectives(reply))
		if memoryResult.ShortTermRecorded {
			log.Printf("[memory] bot=%s user=%s 已写入短期记忆：%s", botID, userID, truncate(memoryResult.ShortTermSummary, 120))
		} else {
			log.Printf("[memory] bot=%s user=%s 本轮短期记忆写入失败，已跳过", botID, userID)
		}
		if len(memoryResult.Profiles) == 0 {
			log.Printf("[memory] bot=%s user=%s 未识别到可写入长期画像的稳定表达，原话=%q", botID, userID, truncate(message, 80))
		} else {
			for _, action := range memoryResult.Profiles {
				actionText := "已写入长期用户画像"
				if action.Action == "updated" {
					actionText = "已更新长期用户画像"
				}
				log.Printf("[memory] bot=%s user=%s 识别到长期记忆意图：触发词=%q，分类=%s，内容=%q；%s", botID, userID, action.Trigger, action.Category, action.Content, actionText)
			}
			reply = appendMemoryConfirmation(reply, memoryResult.Profiles)
		}
	}
	if h.onRuntimeTurn != nil {
		h.onRuntimeTurn(ctx, botID, userID, message, StripMaterialDirectives(reply), memoryContext)
	}

	log.Printf("[handler] bot=%s user=%s route=runtime state=finished elapsed=%s preview=%q", botID, userID, elapsed, truncate(StripMaterialDirectives(reply), 100))
	return reply, nil
}

func appendMemoryConfirmation(reply string, actions []memory.MemoryProfileAction) string {
	if len(actions) == 0 {
		return reply
	}
	confirmation := strings.TrimSpace(actions[0].ConfirmationText())
	if confirmation == "" || strings.Contains(reply, confirmation) {
		return reply
	}
	if strings.TrimSpace(reply) == "" {
		return confirmation
	}
	return strings.TrimSpace(reply) + "\n\n" + confirmation
}

func joinRuntimeContexts(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, "\n\n")
}

func formatMaterialReasonsForLog(reasons []materials.MatchReason) string {
	if len(reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		label := string(reason.Source)
		switch reason.Source {
		case materials.MatchSourceTag:
			label = "标签"
		case materials.MatchSourceTitle:
			label = "标题"
		case materials.MatchSourceDescription:
			label = "用途说明"
		case materials.MatchSourceContent:
			label = "正文"
		}
		parts = append(parts, fmt.Sprintf("%s命中%q", label, reason.Keyword))
	}
	return strings.Join(parts, "；")
}

func (h *Handler) switchDefault(ctx context.Context, name string) string {
	if name == h.runtimeName {
		h.mu.Lock()
		old := h.defaultName
		h.defaultName = name
		h.mu.Unlock()

		if h.saveDefault != nil {
			if err := h.saveDefault(name); err != nil {
				log.Printf("[handler] failed to save default agent to config: %v", err)
			} else {
				log.Printf("[handler] saved default runtime %q to config", name)
			}
		}

		log.Printf("[handler] switched default agent: %s -> %s (packaged runtime)", old, name)
		return fmt.Sprintf("switch to %s", name)
	}

	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// resetDefaultSession resets the session for the given userID on the default agent.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	h.mu.RLock()
	defaultName := h.defaultName
	defaultIsRuntime := defaultName == h.runtimeName
	h.mu.RUnlock()

	if defaultIsRuntime {
		if h.runtimeSvc == nil {
			return "Runtime not configured."
		}
		h.runtimeSvc.ResetConversation(userID)
		if h.memorySvc != nil {
			if err := h.memorySvc.ClearShortTerm(userID); err != nil {
				log.Printf("[memory] clear short-term failed user=%s err=%v", userID, err)
			}
		}
		return fmt.Sprintf("已创建新的%s会话", defaultName)
	}

	ag := h.getDefaultAgent()
	if ag == nil {
		if h.runtimeSvc != nil {
			h.runtimeSvc.ResetConversation(userID)
			if h.memorySvc != nil {
				if err := h.memorySvc.ClearShortTerm(userID); err != nil {
					log.Printf("[memory] clear short-term failed user=%s err=%v", userID, err)
				}
			}
			return fmt.Sprintf("已创建新的%s会话", h.runtimeName)
		}
		return "No agent running."
	}
	name := ag.Info().Name
	sessionID, err := ag.ResetSession(ctx, userID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", userID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return fmt.Sprintf("已创建新的%s会话\n%s", name, sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show current cwd of default agent
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return fmt.Sprintf("cwd: (check agent config)\nagent: %s", info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()

	return fmt.Sprintf("cwd: %s", absPath)
}

// buildStatus returns a short status string showing the current default agent.
func (h *Handler) buildStatus() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.defaultName == "" {
		if h.runtimeSvc != nil {
			return fmt.Sprintf("agent: %s\ntype: runtime\nmodel: %s", h.runtimeName, "configured by provider")
		}
		return "agent: none (echo mode)"
	}

	if h.defaultName == h.runtimeName {
		return fmt.Sprintf("agent: %s\ntype: runtime\nmodel: %s", h.defaultName, "configured by provider")
	}

	ag, ok := h.agents[h.defaultName]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", h.defaultName)
	}

	info := ag.Info()
	return fmt.Sprintf("agent: %s\ntype: %s\nmodel: %s", h.defaultName, info.Type, info.Model)
}

func buildHelpText() string {
	return `Available commands:
/new or /clear - Start a new session
/cwd /path - Show or switch workspace directory
/info - Show current runtime info
/help - Show this help message

Runtime-only mode is enabled. Agent switching and broadcast commands are disabled.`
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	log.Printf("[handler] received image from %s, saving to %s", msg.FromUserID, h.saveDir)

	// Download image data
	var data []byte
	var err error

	if img.URL != "" {
		// Direct URL download
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		// CDN encrypted download
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] image has no URL or media info from %s", msg.FromUserID)
		return
	}

	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Detect extension from content
	ext := detectImageExt(data)

	// Generate filename with timestamp
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath := filepath.Join(h.saveDir, fileName)

	// Ensure save directory exists
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}

	// Write image file
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Write sidecar file
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}

	log.Printf("[handler] saved image to %s (%d bytes)", filePath, len(data))
	reply := fmt.Sprintf("Saved: %s", fileName)
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg" // default to jpg for WeChat images
}
