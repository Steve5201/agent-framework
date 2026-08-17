package ingest

import (
	"fmt"
	"strings"
)

// Chunk 分块产物：内容 + 标题上下文（引用溯源）。
type Chunk struct {
	Content  string
	Source   string // 引用溯源：标题路径（纯文本段为"第 N 节"）
	Metadata map[string]string
}

// ChunkOptions 分块参数。
type ChunkOptions struct {
	MaxLen  int // 单块最大字符数（默认 800；按 rune 计）
	Overlap int // 相邻块重叠字符数（默认 100；须 < MaxLen）
}

// Chunker 分块器：按标题分段，超长段递归字符切分（句子边界优先 + 重叠）。
type Chunker struct{}

// Chunk 把解析产物转换为 chunk 列表。空段跳过。
func (Chunker) Chunk(doc *ParsedDoc, opts ChunkOptions) []Chunk {
	if opts.MaxLen <= 0 {
		opts.MaxLen = 800
	}
	if opts.Overlap < 0 {
		opts.Overlap = 100
	}
	if opts.Overlap >= opts.MaxLen {
		opts.Overlap = opts.MaxLen / 4
	}
	var out []Chunk
	for i, seg := range doc.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		source := seg.Heading
		if source == "" {
			source = fmt.Sprintf("第 %d 节", i+1)
		}
		// 文档级媒体清单（A3b）：图片占位保留在 Content，媒体路径汇总进
		// metadata["media"]（逗号分隔），供检索引用/前端渲染解析。
		var mediaMeta string
		if len(doc.Media) > 0 {
			seen := map[string]bool{}
			parts := make([]string, 0, len(doc.Media))
			for _, m := range doc.Media {
				if !seen[m.Path] {
					seen[m.Path] = true
					parts = append(parts, m.Path)
				}
			}
			mediaMeta = strings.Join(parts, ",")
		}
		for _, part := range splitByLen(text, opts.MaxLen, opts.Overlap) {
			meta := map[string]string{"source": source}
			if doc.Title != "" {
				meta["title"] = doc.Title
			}
			if mediaMeta != "" {
				meta["media"] = mediaMeta
			}
			out = append(out, Chunk{Content: strings.TrimSpace(part), Source: source, Metadata: meta})
		}
	}
	return out
}

// splitByLen 把长文本切分为 ≤maxLen 的片段，相邻片段重叠 overlap 字符。
// 优先在句子边界（。！？；.!?\n）切断，找不到才按字符硬切。
func splitByLen(text string, maxLen, overlap int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + maxLen
		if end >= len(runes) {
			out = append(out, string(runes[start:]))
			break
		}
		// 从 end 往回找句子边界（至少留一半给前段）。
		cut := end
		for i := end; i > start+maxLen/2; i-- {
			if isSentenceBoundary(runes[i-1]) {
				cut = i
				break
			}
		}
		out = append(out, string(runes[start:cut]))
		start = cut - overlap
	}
	return out
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '\n', '.', '!', '?':
		return true
	}
	return false
}
