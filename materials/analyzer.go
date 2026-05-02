package materials

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/qiuy-collab/weone/config"
	internalruntime "github.com/qiuy-collab/weone/internal/runtime"
)

type ImportAnalysis struct {
	Title       string   `json:"title"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary,omitempty"`
}

type ImportInput struct {
	FileName      string
	MimeType      string
	Data          []byte
	Title         string
	Tags          []string
	Description   string
	Enabled       bool
	AnalyzeWithAI bool
}

type ImportResult struct {
	Item     Material       `json:"item"`
	Analysis ImportAnalysis `json:"analysis"`
}

type ImportAnalyzer struct {
	provider internalruntime.Provider
}

func NewImportAnalyzer(cfg config.ProviderConfig) *ImportAnalyzer {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	return &ImportAnalyzer{provider: internalruntime.NewOpenAIProvider(cfg)}
}

func (a *ImportAnalyzer) Analyze(ctx context.Context, fileName, mimeType string, data []byte, text ExtractedText) (ImportAnalysis, error) {
	if a == nil || a.provider == nil {
		return ImportAnalysis{}, fmt.Errorf("import analyzer is not configured")
	}
	kind := detectImportKind(fileName, mimeType)
	log.Printf("[materials] import file=%s stage=analyzing kind=%s sample_chars=%d", fileName, kind, len(text.Sample))

	var reply string
	var err error
	switch kind {
	case KindImage:
		reply, err = a.analyzeImage(ctx, fileName, mimeType, data, text)
	default:
		reply, err = a.analyzeTextLike(ctx, fileName, mimeType, data, kind, text)
	}
	if err != nil {
		return ImportAnalysis{}, err
	}
	return parseImportAnalysis(reply, kind, text)
}

func (a *ImportAnalyzer) AnalyzeTextFallback(ctx context.Context, fileName, mimeType string, data []byte, text ExtractedText) (ImportAnalysis, error) {
	if a == nil || a.provider == nil {
		return ImportAnalysis{}, fmt.Errorf("import analyzer is not configured")
	}
	kind := detectImportKind(fileName, mimeType)
	reply, err := a.analyzeTextLike(ctx, fileName, mimeType, data, kind, text)
	if err != nil {
		return ImportAnalysis{}, err
	}
	return parseImportAnalysis(reply, kind, text)
}

func (a *ImportAnalyzer) analyzeTextLike(ctx context.Context, fileName, mimeType string, data []byte, kind Kind, text ExtractedText) (string, error) {
	prompt := buildImportAnalysisPrompt(fileName, mimeType, len(data), kind, text.Sample)
	return a.provider.Generate(ctx, []internalruntime.ChatMessage{{Role: "user", Content: prompt}})
}

func (a *ImportAnalyzer) analyzeImage(ctx context.Context, fileName, mimeType string, data []byte, text ExtractedText) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("image data is empty")
	}
	prompt := buildImageVisionPrompt(fileName, mimeType, len(data), text.Sample)
	messages := []internalruntime.RichChatMessage{{
		Role: "user",
		Content: []internalruntime.MessagePart{
			internalruntime.NewTextPart(prompt),
			internalruntime.NewImageDataPart(mimeType, data),
		},
	}}
	return a.provider.GenerateRich(ctx, messages)
}

func parseImportAnalysis(reply string, kind Kind, text ExtractedText) (ImportAnalysis, error) {
	var analysis ImportAnalysis
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply)), &analysis); err != nil {
		return ImportAnalysis{}, fmt.Errorf("parse analyzer response: %w", err)
	}
	analysis.Title = strings.TrimSpace(analysis.Title)
	analysis.Description = strings.TrimSpace(analysis.Description)
	analysis.Content = strings.TrimSpace(analysis.Content)
	analysis.Kind = strings.TrimSpace(strings.ToLower(analysis.Kind))
	analysis.Tags = normalizeTags(analysis.Tags)
	if kind == KindText && analysis.Content == "" {
		analysis.Content = text.FullText
	}
	if kind != KindText {
		analysis.Content = ""
	}
	return analysis, nil
}

func buildImportAnalysisPrompt(fileName, mimeType string, size int, kind Kind, sample string) string {
	var b strings.Builder
	b.WriteString("你是素材导入分析器。你的任务是为素材库生成可检索、可触发的语义化字段，而不是泛泛描述文件。\n")
	b.WriteString("要求：\n")
	b.WriteString("1. 只返回 JSON，不要输出 markdown 代码块，不要解释。\n")
	b.WriteString("2. JSON 字段固定为 title, tags, description, content, kind。\n")
	b.WriteString("3. title 要简洁但有语义；tags 要 3-8 个，优先输出可检索的情绪词、动作词、场景词、常见回复短语。\n")
	b.WriteString("4. 如果是文本素材，可在 content 中返回整理后的正文；非文本素材 content 必须为空字符串。\n")
	b.WriteString("5. kind 只允许 text/image/video/file 之一，但最终以后端检测为准。\n")
	b.WriteString("6. 不要输出‘可爱回复表情包’‘聊天图片’‘社交图片’这类泛化标题或标签。\n\n")
	b.WriteString(fmt.Sprintf("文件名: %s\n", fileName))
	if ext := strings.ToLower(filepath.Ext(fileName)); ext != "" {
		b.WriteString(fmt.Sprintf("扩展名: %s\n", ext))
	}
	b.WriteString(fmt.Sprintf("MIME: %s\n", mimeType))
	b.WriteString(fmt.Sprintf("后端检测类型: %s\n", kind))
	b.WriteString(fmt.Sprintf("文件大小: %d bytes\n", size))

	switch kind {
	case KindImage:
		b.WriteString("\n这是图片素材。你现在会直接看到图片本身，请基于图片中的真实内容生成语义，不要只根据文件名猜测。\n")
		b.WriteString("请优先读取图片中的可见文字、表情情绪、人物动作、姿态、场景和整体语气。\n")
		b.WriteString("如果图片里有明确短句或文案，title 必须尽量围绕这句真实文字来写，例如‘我超级爱你表情包’、‘你心里有我吗表情包’。\n")
		b.WriteString("tags 要优先输出可检索的回复语义词、情绪词、动作词、常见短句，例如爱你、喜欢、告白、撒娇、委屈、试探、晚安、抱抱、安慰、搞笑。\n")
		b.WriteString("description 要写成‘适合 AI 想表达什么语义时发送’，并贴合图片真实语义。\n")
		b.WriteString("如果图片里没有文字，再根据视觉语义生成准确标签；仍然不要输出‘聊天图片’‘通用回复图’‘社交图片’这类泛化标题或标签。\n")
	case KindVideo:
		b.WriteString("\n这是视频素材。请根据文件名和元数据生成适合聊天检索的场景语义，不要泛化。\n")
	case KindFile:
		b.WriteString("\n这是文件素材。请根据文件名和元数据生成适合检索的用途说明，例如资料、手册、说明文档、附件。\n")
	case KindText:
		b.WriteString("\n这是文本素材。请优先基于正文样本生成标题、标签、用途说明。\n")
	}

	if strings.TrimSpace(sample) != "" {
		if kind == KindImage {
			b.WriteString("\n以下是额外可用的 OCR 兜底文本；只有当它和图片内容一致时再参考，仍应以直接看到的图片内容为准：\n")
		} else {
			b.WriteString("\n可读文本样本如下：\n")
		}
		b.WriteString(sample)
		b.WriteString("\n")
	}
	b.WriteString("\n返回示例：")
	b.WriteString(`{"title":"...","tags":["..."],"description":"...","content":"...","kind":"text"}`)
	return b.String()
}

func buildImageVisionPrompt(fileName, mimeType string, size int, ocrSample string) string {
	return buildImportAnalysisPrompt(fileName, mimeType, size, KindImage, ocrSample)
}
