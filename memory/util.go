package memory

import "strings"

const (
	defaultShortTermLimit     = 8
	defaultShortTermContext   = 4
	defaultProfileContext     = 8
	maxShortTermSnippetLength = 200
	maxProfileContextChars    = 1200
)

func trimAndCollapse(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Fields(s)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
