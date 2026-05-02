package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type profileStore struct{}

type profileSection struct {
	Category string
	Label    string
	Items    []string
}

func newProfileStore() *profileStore {
	return &profileStore{}
}

func (s *profileStore) LoadBotDocument(botID string) (ProfileDocument, error) {
	if strings.TrimSpace(botID) == "" {
		return ProfileDocument{}, fmt.Errorf("bot_id is required")
	}
	profilesPath, err := profilesDir()
	if err != nil {
		return ProfileDocument{}, err
	}
	path := filepath.Join(profilesPath, profileDocumentFileName(botID))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return ProfileDocument{
				BotID:     strings.TrimSpace(botID),
				Markdown:  renderProfileDocument(strings.TrimSpace(botID), nil, now),
				UpdatedAt: now,
				FileName:  filepath.Base(path),
			}, nil
		}
		return ProfileDocument{}, fmt.Errorf("read profile document: %w", err)
	}
	info, _ := os.Stat(path)
	updatedAt := time.Now().UTC()
	if info != nil {
		updatedAt = info.ModTime().UTC()
	}
	return ProfileDocument{
		BotID:     strings.TrimSpace(botID),
		Markdown:  normalizeMarkdown(string(data), strings.TrimSpace(botID), updatedAt),
		UpdatedAt: updatedAt,
		FileName:  filepath.Base(path),
	}, nil
}

func (s *profileStore) SaveBotDocument(botID, markdown, source string) (ProfileDocument, error) {
	if err := ensureMemoryDirs(); err != nil {
		return ProfileDocument{}, err
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return ProfileDocument{}, fmt.Errorf("bot_id is required")
	}
	updatedAt := time.Now().UTC()
	normalized := normalizeMarkdown(markdown, botID, updatedAt)
	profilesPath, err := profilesDir()
	if err != nil {
		return ProfileDocument{}, err
	}
	fileName := profileDocumentFileName(botID)
	path := filepath.Join(profilesPath, fileName)
	if err := os.WriteFile(path, []byte(normalized), 0o600); err != nil {
		return ProfileDocument{}, fmt.Errorf("write profile document: %w", err)
	}
	return ProfileDocument{
		BotID:     botID,
		Markdown:  normalized,
		UpdatedAt: updatedAt,
		Source:    strings.TrimSpace(source),
		FileName:  fileName,
	}, nil
}

func (s *profileStore) UpsertAutoProfile(botID, category, content, source string) (ProfileDocument, bool, error) {
	doc, err := s.LoadBotDocument(botID)
	if err != nil {
		return ProfileDocument{}, false, err
	}
	sections := parseProfileSections(doc.Markdown)
	category = normalizeCategory(category)
	content = strings.TrimSpace(content)
	if content == "" {
		return doc, false, nil
	}
	items := sections[category]
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), content) {
			updated, err := s.SaveBotDocument(botID, doc.Markdown, source)
			return updated, false, err
		}
	}
	sections[category] = append(items, content)
	updatedMarkdown := renderProfileDocument(botID, sections, time.Now().UTC())
	updatedDoc, err := s.SaveBotDocument(botID, updatedMarkdown, source)
	if err != nil {
		return ProfileDocument{}, false, err
	}
	return updatedDoc, true, nil
}

func profileDocumentFileName(botID string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_")
	name := replacer.Replace(strings.TrimSpace(botID))
	if name == "" {
		name = "unknown-bot"
	}
	return name + ".md"
}

func normalizeMarkdown(markdown, botID string, updatedAt time.Time) string {
	sections := parseProfileSections(markdown)
	return renderProfileDocument(botID, sections, updatedAt)
}

func parseProfileSections(markdown string) map[string][]string {
	sections := make(map[string][]string, len(profileSectionOrder()))
	currentCategory := ""
	for _, rawLine := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			currentCategory = categoryFromLabel(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		if currentCategory == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			item := strings.TrimSpace(line[2:])
			if item != "" && !containsProfileItem(sections[currentCategory], item) {
				sections[currentCategory] = append(sections[currentCategory], item)
			}
		}
	}
	return sections
}

func renderProfileDocument(botID string, sections map[string][]string, updatedAt time.Time) string {
	if sections == nil {
		sections = map[string][]string{}
	}
	var builder strings.Builder
	builder.WriteString("# 长期用户画像\n\n")
	builder.WriteString("- bot_id: " + strings.TrimSpace(botID) + "\n")
	builder.WriteString("- updated_at: " + updatedAt.Format(time.RFC3339) + "\n\n")
	for _, section := range profileSectionOrder() {
		builder.WriteString("## " + section.Label + "\n")
		items := dedupeProfileItems(sections[section.Category])
		if len(items) == 0 {
			builder.WriteString("- 暂无\n\n")
			continue
		}
		for _, item := range items {
			builder.WriteString("- " + strings.TrimSpace(item) + "\n")
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String()) + "\n"
}

func profileSectionOrder() []profileSection {
	return []profileSection{
		{Category: "preference", Label: "偏好"},
		{Category: "identity", Label: "个人信息"},
		{Category: "relationship", Label: "关系设定"},
		{Category: "habit", Label: "习惯"},
		{Category: "boundary", Label: "边界"},
		{Category: "other", Label: "其他"},
	}
}

func categoryFromLabel(label string) string {
	label = strings.TrimSpace(label)
	for _, section := range profileSectionOrder() {
		if label == section.Label || label == section.Category {
			return section.Category
		}
	}
	return ""
}

func containsProfileItem(items []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), needle) {
			return true
		}
	}
	return false
}

func dedupeProfileItems(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || trimmed == "暂无" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}

func normalizeCategory(category string) string {
	switch strings.TrimSpace(category) {
	case "identity", "relationship", "habit", "boundary", "other":
		return strings.TrimSpace(category)
	default:
		return "preference"
	}
}
