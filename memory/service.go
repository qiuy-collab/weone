package memory

import (
	"fmt"
	"log"
	"strings"
)

type MemoryProcessResult struct {
	ShortTermRecorded bool
	ShortTermSummary  string
	LoadedContext     string
	LoadedProfiles    int
	LoadedShortTerm   int
	Profiles          []MemoryProfileAction
}

type MemoryProfileAction struct {
	Action   string
	Category string
	Content  string
	Trigger  string
}

func (a MemoryProfileAction) ConfirmationText() string {
	switch a.Category {
	case "preference":
		return "我记住了，你" + a.Content + "。"
	case "identity":
		return "我记住了，你" + a.Content + "。"
	case "relationship":
		return "我记住了，称呼上按“" + strings.TrimPrefix(a.Content, "你可以叫我") + "”来。"
	case "boundary":
		return "我记住了，之后会避开“" + strings.TrimPrefix(strings.TrimPrefix(a.Content, "不要聊"), "不想聊") + "”这个话题。"
	default:
		return "我记住了，之后会按这个偏好来。"
	}
}

type ExtractedMemoryContext struct {
	LoadedContext string
	Result        MemoryProcessResult
}

type Service struct {
	shortTerm *shortTermStore
	profiles  *profileStore
}

func NewService() (*Service, error) {
	shortTerm, err := newShortTermStore()
	if err != nil {
		return nil, err
	}
	return &Service{shortTerm: shortTerm, profiles: newProfileStore()}, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	return s.shortTerm.Close()
}

func (s *Service) RecordTurn(botID, userID, conversationID, userMessage, assistantReply string) error {
	if s == nil || s.shortTerm == nil {
		return nil
	}
	return s.shortTerm.Record(botID, userID, conversationID, userMessage, assistantReply)
}

func (s *Service) ListShortTermByBot(botID string, limit int) ([]ShortTermEntry, error) {
	if s == nil || s.shortTerm == nil {
		return []ShortTermEntry{}, nil
	}
	return s.shortTerm.ListByBot(botID, limit)
}

func (s *Service) ListShortTerm(userID string, limit int) ([]ShortTermEntry, error) {
	if s == nil || s.shortTerm == nil {
		return []ShortTermEntry{}, nil
	}
	return s.shortTerm.ListByUser(userID, limit)
}

func (s *Service) ClearShortTermByBot(botID string) error {
	if s == nil || s.shortTerm == nil {
		return nil
	}
	return s.shortTerm.ClearBot(botID)
}

func (s *Service) ClearShortTerm(userID string) error {
	if s == nil || s.shortTerm == nil {
		return nil
	}
	return s.shortTerm.ClearUser(userID)
}

func (s *Service) GetProfileDocument(botID string) (ProfileDocument, error) {
	if s == nil || s.profiles == nil {
		return ProfileDocument{}, fmt.Errorf("memory service not configured")
	}
	return s.profiles.LoadBotDocument(botID)
}

func (s *Service) SaveProfileDocument(botID, markdown, source string) (ProfileDocument, error) {
	if s == nil || s.profiles == nil {
		return ProfileDocument{}, fmt.Errorf("memory service not configured")
	}
	return s.profiles.SaveBotDocument(botID, markdown, source)
}

func (s *Service) BuildRuntimeContext(botID string) string {
	ctx := s.CollectRuntimeContext(botID)
	return ctx.LoadedContext
}

func (s *Service) CollectRuntimeContext(botID string) ExtractedMemoryContext {
	if s == nil {
		return ExtractedMemoryContext{}
	}
	parts := make([]string, 0, 2)
	loadedProfiles := 0
	loadedShortTerm := 0
	if profileContext, profileCount := s.buildProfileContext(botID); profileContext != "" {
		parts = append(parts, profileContext)
		loadedProfiles = profileCount
	}
	if shortTermContext, shortTermCount := s.buildShortTermContext(botID); shortTermContext != "" {
		parts = append(parts, shortTermContext)
		loadedShortTerm = shortTermCount
	}
	loaded := strings.Join(parts, "\n\n")
	return ExtractedMemoryContext{
		LoadedContext: loaded,
		Result: MemoryProcessResult{
			LoadedContext:   loaded,
			LoadedProfiles:  loadedProfiles,
			LoadedShortTerm: loadedShortTerm,
		},
	}
}

func (s *Service) buildShortTermContext(botID string) (string, int) {
	entries, err := s.ListShortTermByBot(botID, defaultShortTermContext)
	if err != nil || len(entries) == 0 {
		return "", 0
	}
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "最近对话记忆：")
	for i := len(entries) - 1; i >= 0; i-- {
		lines = append(lines, "- "+entries[i].Summary)
	}
	return strings.Join(lines, "\n"), len(entries)
}

func (s *Service) buildProfileContext(botID string) (string, int) {
	doc, err := s.GetProfileDocument(botID)
	if err != nil {
		return "", 0
	}
	sections := parseProfileSections(doc.Markdown)
	lines := make([]string, 0, defaultProfileContext+1)
	lines = append(lines, "用户画像：")
	count := 0
	for _, section := range profileSectionOrder() {
		for _, item := range dedupeProfileItems(sections[section.Category]) {
			lines = append(lines, "- ["+section.Category+"] "+item)
			count++
			if count >= defaultProfileContext {
				return truncateRunes(strings.Join(lines, "\n"), maxProfileContextChars), count
			}
		}
	}
	if count == 0 {
		return "", 0
	}
	return truncateRunes(strings.Join(lines, "\n"), maxProfileContextChars), count
}

func (s *Service) ExtractAndUpsertProfiles(botID, message string) ([]MemoryProfileAction, error) {
	extracted := extractProfilesFromMessage(message)
	if len(extracted) == 0 {
		return nil, nil
	}
	actions := make([]MemoryProfileAction, 0, len(extracted))
	for _, extractedProfile := range extracted {
		action := MemoryProfileAction{
			Action:   "updated",
			Category: normalizeCategory(extractedProfile.Category),
			Content:  strings.TrimSpace(extractedProfile.Content),
			Trigger:  extractedProfile.Trigger,
		}
		_, inserted, err := s.profiles.UpsertAutoProfile(botID, action.Category, action.Content, "auto")
		if err != nil {
			return nil, err
		}
		if inserted {
			action.Action = "inserted"
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func (s *Service) RecordRuntimeTurn(botID, userID, conversationID, userMessage, assistantReply string) MemoryProcessResult {
	result := MemoryProcessResult{
		ShortTermRecorded: true,
		ShortTermSummary:  buildShortTermSummary(userMessage, assistantReply),
	}
	if err := s.RecordTurn(botID, userID, conversationID, userMessage, assistantReply); err != nil {
		result.ShortTermRecorded = false
		log.Printf("[memory] record short-term failed bot=%s user=%s err=%v", botID, userID, err)
	}
	profileActions, err := s.ExtractAndUpsertProfiles(botID, userMessage)
	if err != nil {
		log.Printf("[memory] extract profile failed bot=%s user=%s err=%v", botID, userID, err)
		return result
	}
	result.Profiles = profileActions
	return result
}
