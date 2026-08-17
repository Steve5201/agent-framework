package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

// fakeDDGHTML 模拟 DuckDuckGo 结果页结构（result__a / result__snippet）。
const fakeDDGHTML = `<html><body>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&amp;rut=abc">Example &amp; Co <b>Page</b></a>
  </h2>
  <a rel="nofollow" class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&amp;rut=abc">这是 <b>摘要</b> 文本</a>
</div>
</body></html>`

func TestWebSearchTool_Execute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "golang" {
			t.Errorf("请求 query = %q, want golang", got)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求缺少 User-Agent")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fakeDDGHTML))
	}))
	defer srv.Close()

	tool := &WebSearchTool{Backend: "duckduckgo", BaseURL: srv.URL, Client: srv.Client()}
	args, _ := json.Marshal(map[string]any{"query": "golang", "max_results": 5})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() 失败: %v", err)
	}
	for _, want := range []string{
		"搜索“golang”的结果",
		"[Example & Co Page](https://example.com/page)", // 标题剥离 HTML、链接取 uddg 真实地址
		"这是 摘要 文本",                                      // 摘要剥离标签并折叠空白
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}

// fakeBingHTML 模拟 Bing 结果页结构（b_algo 块）。
const fakeBingHTML = `<html><body><ol id="b_results">
<li class="b_algo">
  <h2><a href="https://example.com/weather">成都今天天气 <b>实时</b></a></h2>
  <div class="b_caption"><p>成都今天白天多云，最高 <b>32°C</b>，夜间有阵雨。</p></div>
</li>
<li class="b_algo">
  <h2><a href="https://example.org/page2">另一个结果</a></h2>
  <p>第二条摘要文本。</p>
</li>
</ol></body></html>`

func TestWebSearchTool_BingExecute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "成都 今天 天气" {
			t.Errorf("请求 query = %q, want 成都 今天 天气", got)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求缺少 User-Agent")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fakeBingHTML))
	}))
	defer srv.Close()

	// Backend 为空 = 默认 bing 解析器；BaseURL 注入测试地址。
	tool := &WebSearchTool{BaseURL: srv.URL, Client: srv.Client()}
	args, _ := json.Marshal(map[string]any{"query": "成都 今天 天气", "max_results": 5})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() 失败: %v", err)
	}
	for _, want := range []string{
		"搜索“成都 今天 天气”的结果",
		"[成都今天天气 实时](https://example.com/weather)",
		"成都今天白天多云", // 摘要剥离标签（避免断言整句受空白折叠影响，按片段校验）
		"夜间有阵雨",
		"[另一个结果](https://example.org/page2)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}

func TestWebSearchTool_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>nothing here</body></html>"))
	}))
	defer srv.Close()

	tool := &WebSearchTool{BaseURL: srv.URL, Client: srv.Client()}
	args, _ := json.Marshal(map[string]string{"query": "zzz"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() 失败: %v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Fatalf("无结果时应友好提示，实际：%s", out)
	}
}

func TestWebSearchTool_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	tool := &WebSearchTool{BaseURL: srv.URL, Client: srv.Client()}
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("限流时应返回明确错误，实际 err=%v", err)
	}
}

func TestWebSearchTool_Schema(t *testing.T) {
	s := (&WebSearchTool{}).Schema()
	if s.Name != "web_search" {
		t.Fatalf("Name = %q, want web_search", s.Name)
	}
	if s.Permission != schema.PermissionL1Read {
		t.Fatalf("Permission = %v, want L1 只读", s.Permission)
	}
	if len(s.Required) != 1 || s.Required[0] != "query" {
		t.Fatalf("Required = %v, want [query]", s.Required)
	}
}
