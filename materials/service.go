package materials

import (
	"context"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Service struct {
	store    *store
	analyzer *ImportAnalyzer
}

func NewService(analyzer *ImportAnalyzer) *Service {
	return &Service{store: newStore(), analyzer: analyzer}
}

const replyIntentMaterialThreshold = 12

func (s *Service) ListMaterials(query, kind string) ([]Material, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return nil, err
	}
	matched := make([]Material, 0, len(lib.Items))
	for _, item := range lib.Items {
		if !item.Enabled {
			continue
		}
		if matchesKind(item, kind) && matchesQuery(item, query) {
			matched = append(matched, item)
		}
	}
	return matched, nil
}

func (s *Service) GetMaterial(id string) (Material, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Material{}, fmt.Errorf("id is required")
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Material{}, err
	}
	for _, item := range lib.Items {
		if item.ID == id {
			return item, nil
		}
	}
	return Material{}, fmt.Errorf("material not found")
}

func (s *Service) UpsertMaterial(input Material) (Material, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Material{}, err
	}
	item, err := normalizeMaterial(input)
	if err != nil {
		return Material{}, err
	}
	if item.Kind != KindText {
		media, err := detectMediaMetadata(item.MediaPath, item.OriginalName, item.MimeType, item.FileSize)
		if err != nil {
			return Material{}, err
		}
		item.MediaPath = media.Path
		item.OriginalName = media.OriginalName
		item.MimeType = media.MimeType
		item.FileSize = media.FileSize
	}
	return s.upsertMaterialInLibrary(lib, item)
}

func (s *Service) SaveImportedMaterial(input Material, fileName string, data []byte, mimeType string) (Material, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return Material{}, err
	}
	item, err := normalizeMaterial(input)
	if err != nil {
		return Material{}, err
	}
	if item.Kind == KindText {
		return Material{}, fmt.Errorf("text materials do not support file import")
	}
	if len(data) == 0 {
		return Material{}, fmt.Errorf("uploaded file is empty")
	}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	stored, err := s.storeMediaFile(item.Kind, item.ID, fileName, data, mimeType)
	if err != nil {
		return Material{}, err
	}
	oldMediaPath := s.findExistingMediaPath(lib, item.ID)
	item.MediaPath = stored.Path
	item.OriginalName = stored.OriginalName
	item.MimeType = stored.MimeType
	item.FileSize = stored.FileSize

	saved, err := s.upsertMaterialInLibrary(lib, item)
	if err != nil {
		_ = os.Remove(stored.Path)
		return Material{}, err
	}
	if oldMediaPath != "" && oldMediaPath != stored.Path {
		_ = os.Remove(oldMediaPath)
	}
	return saved, nil
}

func (s *Service) ImportMaterialWithAI(ctx context.Context, input ImportInput) (ImportResult, error) {
	kind := detectImportKind(input.FileName, input.MimeType)
	if kind != KindText && len(input.Data) == 0 {
		return ImportResult{}, fmt.Errorf("uploaded file is empty")
	}

	text := extractImportText(input.FileName, input.MimeType, input.Data)
	analysis := ImportAnalysis{}
	analysisDone := false
	if input.AnalyzeWithAI && s.analyzer != nil {
		if kind == KindImage {
			log.Printf("[materials] import file=%s stage=vision-start kind=%s", input.FileName, kind)
			result, err := s.analyzer.Analyze(ctx, input.FileName, input.MimeType, input.Data, text)
			if err != nil {
				log.Printf("[materials] import file=%s stage=vision-failed err=%v", input.FileName, err)
			} else {
				analysis = result
				analysisDone = true
				log.Printf("[materials] import file=%s stage=vision-finished title=%q tags=%v", input.FileName, analysis.Title, analysis.Tags)
			}
		}
		if kind == KindImage && !analysisDone {
			log.Printf("[materials] import file=%s stage=ocr-start kind=%s", input.FileName, kind)
			ocrResult, err := extractImageText(ctx, input.FileName, input.Data)
			if err != nil {
				log.Printf("[materials] import file=%s stage=ocr-failed err=%v", input.FileName, err)
			} else if ocrResult.Skipped {
				log.Printf("[materials] import file=%s stage=ocr-skipped reason=%s", input.FileName, ocrResult.Reason)
			} else {
				text = ocrResult.ExtractedText
				log.Printf("[materials] import file=%s stage=ocr-finished backend=%s chars=%d", input.FileName, ocrResult.Backend, len(text.FullText))
			}
		}
		if !analysisDone {
			result, err := s.analyzer.AnalyzeTextFallback(ctx, input.FileName, input.MimeType, input.Data, text)
			if err != nil {
				log.Printf("[materials] import file=%s stage=analyze-failed err=%v", input.FileName, err)
			} else {
				analysis = result
				analysisDone = true
				log.Printf("[materials] import file=%s stage=analyzed title=%q tags=%v", input.FileName, analysis.Title, analysis.Tags)
			}
		}
		if kind == KindImage && !analysisDone {
			log.Printf("[materials] import file=%s stage=metadata-fallback file=%s mime=%s", input.FileName, input.FileName, input.MimeType)
		}
	}

	item := Material{
		Kind:        kind,
		Title:       firstNonEmpty(strings.TrimSpace(input.Title), analysis.Title, defaultImportTitle(input.FileName)),
		Tags:        normalizeTags(append(normalizeTags(input.Tags), analysis.Tags...)),
		Description: firstNonEmpty(strings.TrimSpace(input.Description), analysis.Description, defaultImportDescription(kind, input.FileName)),
		Enabled:     true,
	}
	if !input.Enabled {
		item.Enabled = false
	}
	if kind == KindText {
		item.Content = firstNonEmpty(analysis.Content, text.FullText)
		if item.Content == "" {
			item.Content = firstNonEmpty(text.Sample, defaultImportTitle(input.FileName))
		}
		saved, err := s.UpsertMaterial(item)
		if err != nil {
			return ImportResult{}, err
		}
		log.Printf("[materials] import file=%s stage=saved material_id=%s kind=%s", input.FileName, saved.ID, saved.Kind)
		return ImportResult{Item: saved, Analysis: analysis}, nil
	}

	saved, err := s.SaveImportedMaterial(item, input.FileName, input.Data, input.MimeType)
	if err != nil {
		return ImportResult{}, err
	}
	log.Printf("[materials] import file=%s stage=stored kind=%s material_id=%s", input.FileName, saved.Kind, saved.ID)
	return ImportResult{Item: saved, Analysis: analysis}, nil
}

func (s *Service) DeleteMaterial(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	lib, err := s.store.loadLibrary()
	if err != nil {
		return err
	}
	filtered := make([]Material, 0, len(lib.Items))
	var removed Material
	found := false
	for _, item := range lib.Items {
		if item.ID == id {
			found = true
			removed = item
			continue
		}
		filtered = append(filtered, item)
	}
	if !found {
		return fmt.Errorf("material not found")
	}
	if _, err := s.store.saveLibrary(Library{Items: filtered}); err != nil {
		return err
	}
	if removed.MediaPath != "" {
		_ = os.Remove(removed.MediaPath)
	}
	return nil
}

func (s *Service) SearchMaterials(query string, limit int) ([]Material, error) {
	items, err := s.ListMaterials(query, "")
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Service) SearchForMessage(message string, limit int) (SearchResult, error) {
	lib, err := s.store.loadLibrary()
	if err != nil {
		return SearchResult{}, err
	}
	keywords := extractKeywords(message)
	matches := make([]MaterialMatch, 0)
	for _, item := range lib.Items {
		if !item.Enabled {
			continue
		}
		match := matchMaterial(item, keywords)
		if len(match.Reasons) == 0 {
			continue
		}
		matches = append(matches, match)
	}
	sortMaterialMatches(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return SearchResult{Keywords: keywords, Matches: matches}, nil
}

func (s *Service) SearchForReplyIntent(reply string, limit int) (SearchResult, error) {
	result, err := s.SearchForMessage(reply, 0)
	if err != nil {
		return SearchResult{}, err
	}
	filtered := make([]MaterialMatch, 0, len(result.Matches))
	for _, match := range result.Matches {
		if match.Material.Kind == KindText {
			continue
		}
		if !hasTagReason(match.Reasons) {
			continue
		}
		if match.Score < replyIntentMaterialThreshold {
			continue
		}
		filtered = append(filtered, match)
	}
	sortMaterialMatches(filtered)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	result.Matches = filtered
	return result, nil
}

func (s *Service) FormatMaterialsContext(results []Material) string {
	if len(results) == 0 {
		return ""
	}
	lines := make([]string, 0, len(results)+1)
	lines = append(lines, "可用素材：")
	for _, item := range results {
		parts := []string{fmt.Sprintf("- [%s] %s", item.Kind, item.Title)}
		if item.Description != "" {
			parts = append(parts, item.Description)
		}
		if len(item.Tags) > 0 {
			parts = append(parts, "标签: "+strings.Join(item.Tags, ", "))
		}
		lines = append(lines, strings.Join(parts, " · "))
	}
	return strings.Join(lines, "\n")
}

func (s *Service) BuildRuntimeContext(result SearchResult) string {
	if len(result.Matches) == 0 {
		return ""
	}
	lines := make([]string, 0, len(result.Matches)*6+4)
	lines = append(lines,
		"候选素材：",
		"请先判断当前用户意图是否真的适合发送以下素材。仅在素材用途与当前对话明确匹配时才使用；如果不适合，则忽略素材。",
		"如果使用文本素材，可直接把正文自然融入回复。",
		"如果使用图片、视频或文件素材，请只输出独立一行的结构化指令：[[material:id=<素材ID>;kind=<类型>;title=<素材标题>]]。不要输出本地路径。",
	)
	if len(result.Keywords) > 0 {
		lines = append(lines, "本轮提取关键词："+strings.Join(result.Keywords, ", "))
	}
	for idx, match := range result.Matches {
		item := match.Material
		lines = append(lines, fmt.Sprintf("%d. [%s] %s", idx+1, item.Kind, item.Title))
		if len(item.Tags) > 0 {
			lines = append(lines, "   标签："+strings.Join(item.Tags, ", "))
		}
		if item.Description != "" {
			lines = append(lines, "   用途说明："+item.Description)
		}
		if item.Kind == KindText && item.Content != "" {
			lines = append(lines, "   文本正文："+item.Content)
		}
		if item.Kind != KindText && item.HasMedia() {
			mediaInfo := []string{fmt.Sprintf("素材ID=%s", item.ID)}
			if item.OriginalName != "" {
				mediaInfo = append(mediaInfo, "文件名="+item.OriginalName)
			}
			if item.MimeType != "" {
				mediaInfo = append(mediaInfo, "类型="+item.MimeType)
			}
			lines = append(lines, "   媒体信息："+strings.Join(mediaInfo, "；"))
		}
		lines = append(lines, "   命中原因："+formatMatchReasons(match.Reasons))
	}
	return strings.Join(lines, "\n")
}

func (s *Service) FindMediaPathByID(id string) (Material, string, error) {
	item, err := s.GetMaterial(id)
	if err != nil {
		return Material{}, "", err
	}
	if item.Kind == KindText {
		return Material{}, "", fmt.Errorf("material %q is text only", item.Title)
	}
	if item.MediaPath == "" {
		return Material{}, "", fmt.Errorf("material %q has no media file", item.Title)
	}
	if _, err := os.Stat(item.MediaPath); err != nil {
		return Material{}, "", fmt.Errorf("material media file missing: %w", err)
	}
	return item, item.MediaPath, nil
}

func (s *Service) upsertMaterialInLibrary(lib Library, item Material) (Material, error) {
	now := time.Now().UTC()
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	for i := range lib.Items {
		if lib.Items[i].ID != item.ID {
			continue
		}
		old := lib.Items[i]
		item.CreatedAt = old.CreatedAt
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = now
		if old.MediaPath != "" && old.MediaPath != item.MediaPath {
			_ = os.Remove(old.MediaPath)
		}
		lib.Items[i] = item
		_, err := s.store.saveLibrary(lib)
		return item, err
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	lib.Items = append(lib.Items, item)
	_, err := s.store.saveLibrary(lib)
	return item, err
}

func (s *Service) findExistingMediaPath(lib Library, id string) string {
	for _, item := range lib.Items {
		if item.ID == id {
			return item.MediaPath
		}
	}
	return ""
}

func (s *Service) storeMediaFile(kind Kind, materialID string, fileName string, data []byte, mimeType string) (MediaFile, error) {
	targetPath, err := materialStoredPath(kind, materialID, fileName)
	if err != nil {
		return MediaFile{}, err
	}
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		return MediaFile{}, fmt.Errorf("write media file: %w", err)
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	}
	return MediaFile{
		Path:         targetPath,
		OriginalName: strings.TrimSpace(fileName),
		MimeType:     strings.TrimSpace(mimeType),
		FileSize:     int64(len(data)),
	}, nil
}

func defaultImportTitle(fileName string) string {
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName)))
	if name == "" || name == "." {
		return "未命名素材"
	}
	return name
}

func defaultImportDescription(kind Kind, fileName string) string {
	title := defaultImportTitle(fileName)
	switch kind {
	case KindImage:
		return fmt.Sprintf("适合在需要发送图片素材时使用：%s", title)
	case KindVideo:
		return fmt.Sprintf("适合在需要发送视频素材时使用：%s", title)
	case KindFile:
		return fmt.Sprintf("适合在需要发送文件素材时使用：%s", title)
	default:
		return fmt.Sprintf("适合在相关场景中使用：%s", title)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeMaterial(input Material) (Material, error) {
	item := Material{
		ID:           strings.TrimSpace(input.ID),
		Kind:         normalizeKind(input.Kind),
		Title:        strings.TrimSpace(input.Title),
		Content:      strings.TrimSpace(input.Content),
		MediaPath:    strings.TrimSpace(input.MediaPath),
		OriginalName: strings.TrimSpace(input.OriginalName),
		MimeType:     strings.TrimSpace(input.MimeType),
		FileSize:     input.FileSize,
		Tags:         normalizeTags(input.Tags),
		Description:  strings.TrimSpace(input.Description),
		Enabled:      input.Enabled,
		CreatedAt:    input.CreatedAt.UTC(),
		UpdatedAt:    input.UpdatedAt.UTC(),
	}
	if item.Title == "" {
		return Material{}, fmt.Errorf("title is required")
	}
	if item.Kind == "" {
		return Material{}, fmt.Errorf("kind is required")
	}
	if item.Enabled != false {
		item.Enabled = true
	}
	if item.FileSize < 0 {
		item.FileSize = 0
	}
	if item.Kind == KindText {
		if item.Content == "" {
			return Material{}, fmt.Errorf("content is required for text materials")
		}
		item.MediaPath = ""
		item.OriginalName = ""
		item.MimeType = ""
		item.FileSize = 0
	} else {
		if len(item.Tags) == 0 && item.Description == "" {
			return Material{}, fmt.Errorf("tags or description is required for media materials")
		}
		item.Content = ""
	}
	return item, nil
}

func normalizeKind(kind Kind) Kind {
	switch strings.ToLower(strings.TrimSpace(string(kind))) {
	case string(KindText):
		return KindText
	case string(KindImage):
		return KindImage
	case string(KindVideo):
		return KindVideo
	case string(KindFile):
		return KindFile
	default:
		return Kind("")
	}
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, trimmed)
	}
	return result
}

func matchesKind(item Material, kind string) bool {
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		return true
	}
	return strings.EqualFold(string(item.Kind), kind)
}

func matchesQuery(item Material, query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(item.Title), query) {
		return true
	}
	if strings.Contains(strings.ToLower(item.Description), query) {
		return true
	}
	if strings.Contains(strings.ToLower(item.Content), query) {
		return true
	}
	if strings.Contains(strings.ToLower(item.OriginalName), query) {
		return true
	}
	for _, tag := range item.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

func extractKeywords(message string) []string {
	trimmed := strings.TrimSpace(strings.ToLower(message))
	if trimmed == "" {
		return nil
	}
	splits := strings.FieldsFunc(trimmed, func(r rune) bool {
		if unicode.IsSpace(r) {
			return true
		}
		switch r {
		case ',', '，', '.', '。', '!', '！', '?', '？', ';', '；', ':', '：', '/', '\\', '|', '-', '_', '+', '(', ')', '[', ']', '{', '}', '"', '\'', '“', '”', '‘', '’', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]bool)
	keywords := make([]string, 0, len(splits)*2)
	for _, part := range splits {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if utf8.RuneCountInString(part) > 1 {
			addKeyword(keywords, seen, part, &keywords)
		}
		for _, token := range splitCJKFragments(part) {
			addKeyword(keywords, seen, token, &keywords)
		}
	}
	return keywords
}

func addKeyword(_ []string, seen map[string]bool, value string, target *[]string) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return
	}
	if utf8.RuneCountInString(value) <= 1 {
		return
	}
	if seen[value] {
		return
	}
	seen[value] = true
	*target = append(*target, value)
}

func splitCJKFragments(part string) []string {
	runes := []rune(part)
	if len(runes) <= 2 {
		return nil
	}
	allCJK := true
	for _, r := range runes {
		if !isCJKLike(r) {
			allCJK = false
			break
		}
	}
	if !allCJK {
		return nil
	}
	fragments := make([]string, 0, len(runes)-1)
	for size := len(runes); size >= 2; size-- {
		for start := 0; start+size <= len(runes); start++ {
			fragments = append(fragments, string(runes[start:start+size]))
		}
	}
	return fragments
}

func isCJKLike(r rune) bool {
	return unicode.In(r,
		unicode.Han,
		unicode.Hiragana,
		unicode.Katakana,
	)
}

func matchMaterial(item Material, keywords []string) MaterialMatch {
	match := MaterialMatch{Material: item}
	seenReasons := make(map[string]bool)
	for _, keyword := range keywords {
		for _, tag := range item.Tags {
			if containsFold(tag, keyword) {
				addReason(&match, seenReasons, MatchReason{Source: MatchSourceTag, Keyword: keyword, Field: tag}, 12)
			}
		}
		if containsFold(item.Title, keyword) {
			addReason(&match, seenReasons, MatchReason{Source: MatchSourceTitle, Keyword: keyword, Field: item.Title}, 6)
		}
		if containsFold(item.Description, keyword) {
			addReason(&match, seenReasons, MatchReason{Source: MatchSourceDescription, Keyword: keyword, Field: item.Description}, 4)
		}
		if item.Kind == KindText && containsFold(item.Content, keyword) {
			addReason(&match, seenReasons, MatchReason{Source: MatchSourceContent, Keyword: keyword, Field: item.Content}, 2)
		}
	}
	return match
}

func addReason(match *MaterialMatch, seen map[string]bool, reason MatchReason, score int) {
	key := string(reason.Source) + "|" + reason.Keyword + "|" + reason.Field
	if seen[key] {
		return
	}
	seen[key] = true
	match.Score += score
	match.Reasons = append(match.Reasons, reason)
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func hasTagReason(reasons []MatchReason) bool {
	for _, reason := range reasons {
		if reason.Source == MatchSourceTag {
			return true
		}
	}
	return false
}

func sortMaterialMatches(matches []MaterialMatch) {
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if len(matches[i].Reasons) != len(matches[j].Reasons) {
			return len(matches[i].Reasons) > len(matches[j].Reasons)
		}
		return matches[i].Material.UpdatedAt.After(matches[j].Material.UpdatedAt)
	})
}

func formatMatchReasons(reasons []MatchReason) string {
	if len(reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		label := string(reason.Source)
		switch reason.Source {
		case MatchSourceTag:
			label = "标签"
		case MatchSourceTitle:
			label = "标题"
		case MatchSourceDescription:
			label = "用途说明"
		case MatchSourceContent:
			label = "正文"
		}
		parts = append(parts, fmt.Sprintf("%s命中“%s”", label, reason.Keyword))
	}
	return strings.Join(parts, "；")
}
