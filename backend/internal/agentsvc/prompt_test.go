package agentsvc

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_IncludesRenderProtocol(t *testing.T) {
	got := BuildSystemPrompt("你是助手。")
	for _, kw := range []string{"echarts", "svg", ".mp4", "Markdown", "ECharts option", "KaTeX", "$x^2$"} {
		if !strings.Contains(got, kw) {
			t.Errorf("BuildSystemPrompt 缺失协议关键词 %q", kw)
		}
	}
	// 协议必须给出真实围栏示例，模型才知道按什么语法输出
	if !strings.Contains(got, "```echarts") || !strings.Contains(got, "```svg") {
		t.Errorf("渲染协议必须包含 ```echarts / ```svg 真实围栏示例")
	}
	// 嵌套围栏：代码块内容里含反引号时外层须用 4 个反引号包裹，防止内层 ``` 提前闭合
	if !strings.Contains(got, "````") {
		t.Errorf("渲染协议必须说明嵌套代码块时外层用 4 个反引号包裹，实际：%q", got)
	}
	// 语法高亮：协议须要求代码块标注语言标签，前端才能按语言高亮
	if !strings.Contains(got, "语法高亮") {
		t.Errorf("渲染协议必须要求代码块标注语言标签（语法高亮），实际：%q", got)
	}
	if !strings.HasPrefix(got, "你是助手。") {
		t.Errorf("基础提示词应保留在最前，实际以 %q 开头", got[:min(12, len(got))])
	}
}

func TestBuildSystemPrompt_EmptyBase(t *testing.T) {
	got := BuildSystemPrompt("")
	if !strings.Contains(got, "内容渲染协议") {
		t.Errorf("空基础提示词时也应包含渲染协议，实际：%q", got)
	}
}

func TestBuildSystemPromptWithMedia_FilesBaseURL(t *testing.T) {
	// 配置了本地媒体基址：协议应注入 /files 用法，且含实际基址 URL。
	got := BuildSystemPromptWithMedia("你是助手。", "http://localhost:8182")
	for _, kw := range []string{"http://localhost:8182/files/", "本地媒体", "file://", "禁止使用"} {
		if !strings.Contains(got, kw) {
			t.Errorf("BuildSystemPromptWithMedia 缺失关键词 %q", kw)
		}
	}
	// 未配置基址：保持原协议，不出现本地媒体基址注入（协议第 7 条文档下载
	// 协议是恒存在的，其中 "/files/" 只是"禁止自行拼接"的示例文本）。
	plain := BuildSystemPrompt("你是助手。")
	if strings.Contains(plain, "http://localhost:8182") {
		t.Errorf("未配置 filesBaseURL 时不应注入本地媒体基址，实际：%q", plain)
	}
	if !strings.Contains(plain, "```doc") {
		t.Errorf("文档下载协议（```doc 卡片）应恒存在，实际：%q", plain)
	}
}
