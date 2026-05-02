package memory

import "strings"

type extractedProfile struct {
	Category string
	Content  string
	Trigger  string
}

func extractProfilesFromMessage(message string) []extractedProfile {
	text := strings.TrimSpace(message)
	if text == "" {
		return nil
	}
	normalized := normalizeExtractText(text)
	patterns := []struct {
		keyword  string
		category string
		format   func(string) string
	}{
		{keyword: "我不喜欢", category: "preference", format: func(v string) string { return "不喜欢" + v }},
		{keyword: "不喜欢", category: "preference", format: func(v string) string { return "不喜欢" + v }},
		{keyword: "我喜欢", category: "preference", format: func(v string) string { return "喜欢" + v }},
		{keyword: "喜欢", category: "preference", format: func(v string) string { return "喜欢" + v }},
		{keyword: "我讨厌", category: "preference", format: func(v string) string { return "讨厌" + v }},
		{keyword: "讨厌", category: "preference", format: func(v string) string { return "讨厌" + v }},
		{keyword: "你可以叫我", category: "relationship", format: func(v string) string { return "你可以叫我" + v }},
		{keyword: "我是", category: "identity", format: func(v string) string { return "是" + v }},
		{keyword: "我现在在", category: "identity", format: func(v string) string { return "现在在" + v }},
		{keyword: "我住在", category: "identity", format: func(v string) string { return "住在" + v }},
		{keyword: "我在", category: "identity", format: func(v string) string { return normalizeIdentityContent(v) }},
		{keyword: "不要跟我聊", category: "boundary", format: func(v string) string { return "不要聊" + v }},
		{keyword: "别跟我聊", category: "boundary", format: func(v string) string { return "不要聊" + v }},
		{keyword: "不想聊", category: "boundary", format: func(v string) string { return "不想聊" + v }},
	}
	seen := make(map[string]bool)
	profiles := make([]extractedProfile, 0, 3)
	for _, pattern := range patterns {
		value, ok := extractAfterKeyword(normalized, pattern.keyword)
		if !ok {
			continue
		}
		content := strings.TrimSpace(pattern.format(value))
		if content == "" {
			continue
		}
		key := pattern.category + "\x00" + content
		if seen[key] {
			continue
		}
		seen[key] = true
		profiles = append(profiles, extractedProfile{
			Category: pattern.category,
			Content:  content,
			Trigger:  pattern.keyword,
		})
	}
	return profiles
}

func normalizeExtractText(text string) string {
	replacer := strings.NewReplacer(
		"，", " ",
		"。", " ",
		"！", " ",
		"？", " ",
		"；", " ",
		",", " ",
		".", " ",
		"!", " ",
		"?", " ",
		";", " ",
		"：", " ",
		":", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(strings.TrimSpace(text))), " ")
}

func extractAfterKeyword(text, keyword string) (string, bool) {
	idx := strings.Index(text, keyword)
	if idx < 0 {
		return "", false
	}
	value := strings.TrimSpace(strings.Trim(text[idx+len(keyword):], " ，。！？!?,；;:"))
	if value == "" {
		return "", false
	}
	for _, stop := range []string{" 但是", " 不过", " 所以", " 然后", " 以后", " 希望你", " 请你"} {
		if stopIdx := strings.Index(value, stop); stopIdx > 0 {
			value = strings.TrimSpace(value[:stopIdx])
			break
		}
	}
	return value, value != ""
}

func normalizeIdentityContent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "上学") || strings.Contains(value, "读书") {
		return "在" + value
	}
	if strings.HasPrefix(value, "广东") || strings.HasPrefix(value, "北京") || strings.HasPrefix(value, "上海") || strings.HasPrefix(value, "深圳") {
		return "在" + value
	}
	return "在" + value
}
