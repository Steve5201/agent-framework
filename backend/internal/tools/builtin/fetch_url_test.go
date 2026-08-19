package builtin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchURLTool_Execute 正常抓取：注入 httptest（127.0.0.1，测试需跳过
// SSRF 校验），验证标题+正文提取、来源标注。
func TestFetchURLTool_Execute(t *testing.T) {
	htmlBody := `<!DOCTYPE html>
<html><head><title>测试页面</title>
<style>.hidden{color:red}</style></head>
<body>
<nav>导航导航导航</nav>
<article>
  <h1>文章标题</h1>
  <p>这是第一段正文内容。</p>
  <p>这是第二段，包含 中文 和 English 混合文本。</p>
  <script>var x=1;</script>
</article>
<footer>页脚版权</footer>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(htmlBody))
	}))
	defer srv.Close()

	tool := &FetchURLTool{Client: srv.Client(), SkipSSRFCheck: true}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`/page"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "测试页面") {
		t.Errorf("应包含标题，实际：%s", out)
	}
	if !strings.Contains(out, "这是第一段正文内容") {
		t.Errorf("应包含正文，实际：%s", out)
	}
	if !strings.Contains(out, "第二段") {
		t.Errorf("应包含第二段，实际：%s", out)
	}
	// 噪音应被剔除
	if strings.Contains(out, "导航导航") {
		t.Errorf("不应包含 nav 噪音，实际：%s", out)
	}
	if strings.Contains(out, "var x=1") {
		t.Errorf("不应包含 script 内容，实际：%s", out)
	}
	if strings.Contains(out, "hidden") {
		t.Errorf("不应包含 style 内容，实际：%s", out)
	}
	if !strings.Contains(out, "来源："+srv.URL) {
		t.Errorf("应标注来源 URL，实际：%s", out)
	}
}

// TestFetchURLTool_ExtractPageText 直接测提取器：标题、去噪、空白压缩。
func TestFetchURLTool_ExtractPageText(t *testing.T) {
	body := []byte(`<html><head><title>  标题 空格  </title></head>
<body><p>  hello    world  </p><script>bad</script><p>line2</p></body></html>`)
	title, text := extractPageText(body)
	if title != "标题 空格" {
		t.Errorf("标题提取错误：%q", title)
	}
	if strings.Contains(text, "bad") {
		t.Errorf("不应包含 script 内容：%q", text)
	}
	if !strings.Contains(text, "hello world") {
		t.Errorf("空白压缩错误：%q", text)
	}
	if !strings.Contains(text, "line2") {
		t.Errorf("应包含 line2：%q", text)
	}
}

// TestFetchURLTool_ExtractEmbeddedState 测纯 JS 渲染页：正文在 script 内嵌
// window.__INITIAL_STATE__ 里，提取器应捞出并并入返回文本。
func TestFetchURLTool_ExtractEmbeddedState(t *testing.T) {
	body := []byte(`<html><head><title>动态页</title></head>
<body><script>window.__INITIAL_STATE__={"video":{"title":"示例视频","desc":"这是动态加载的正文内容","count":123}}</script><div id="app"></div></body></html>`)
	_, text := extractPageText(body)
	if !strings.Contains(text, "示例视频") {
		t.Errorf("应提取内嵌 JSON 里的标题：%q", text)
	}
	if !strings.Contains(text, "这是动态加载的正文内容") {
		t.Errorf("应提取内嵌 JSON 里的正文：%q", text)
	}
	if strings.Contains(text, "__INITIAL_STATE__") {
		t.Errorf("不应输出内嵌变量名本身：%q", text)
	}
}

// TestFetchURLTool_Truncation 大内容截断。
func TestFetchURLTool_Truncation(t *testing.T) {
	long := strings.Repeat("很长的内容", 8000) // > 20000 rune
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(long))
	}))
	defer srv.Close()
	tool := &FetchURLTool{Client: srv.Client(), SkipSSRFCheck: true}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len([]rune(out)) > fetchURLReturnMax+100 {
		t.Errorf("返回长度应被截断到约 %d，实际 %d", fetchURLReturnMax, len([]rune(out)))
	}
	if !strings.Contains(out, "已截断") {
		t.Errorf("截断时应有提示，实际长度 %d", len(out))
	}
}

// TestFetchURLTool_ServerError 非 200 状态报错。
func TestFetchURLTool_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	tool := &FetchURLTool{Client: srv.Client(), SkipSSRFCheck: true}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("应报 HTTP 404 错误，实际：%v", err)
	}
}

// TestFetchURLTool_InvalidScheme 非 http/https 拒绝。
func TestFetchURLTool_InvalidScheme(t *testing.T) {
	tool := &FetchURLTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"file:///etc/passwd"}`))
	if err == nil || !strings.Contains(err.Error(), "仅支持 http/https") {
		t.Errorf("应拒绝 file:// 协议，实际：%v", err)
	}
}

// TestFetchURLTool_EmptyURL url 为空拒绝。
func TestFetchURLTool_EmptyURL(t *testing.T) {
	tool := &FetchURLTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"  "}`))
	if err == nil || !strings.Contains(err.Error(), "url 不能为空") {
		t.Errorf("应拒绝空 url，实际：%v", err)
	}
}

// TestIsPublicIP 公网判定：内网/回环/链路本地应 false，公网应 true。
func TestIsPublicIP(t *testing.T) {
	privates := []string{"127.0.0.1", "10.0.0.5", "172.16.3.3", "192.168.1.1", "169.254.10.10", "0.0.0.0", "::1", "fe80::1"}
	for _, s := range privates {
		ip := net.ParseIP(s)
		if isPublicIP(ip) {
			t.Errorf("%s 应判定为非公网", s)
		}
	}
	publics := []string{"8.8.8.8", "114.114.114.114", "2001:4860:4860::8888"}
	for _, s := range publics {
		ip := net.ParseIP(s)
		if !isPublicIP(ip) {
			t.Errorf("%s 应判定为公网", s)
		}
	}
}

// TestFetchURLTool_SSRFBlock 不跳过校验时，内网/回环 URL 被拦截。
func TestFetchURLTool_SSRFBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	tool := &FetchURLTool{} // SkipSSRFCheck = false
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("httptest(127.0.0.1) 应被 SSRF 拦截，实际：%v", err)
	}
}

// TestFetchURLTool_LocalhostBlock localhost 文字域名被拦截。
func TestFetchURLTool_LocalhostBlock(t *testing.T) {
	tool := &FetchURLTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"http://localhost:8080/x"}`))
	if err == nil || !strings.Contains(err.Error(), "本地主机") {
		t.Errorf("localhost 应被拦截，实际：%v", err)
	}
}
