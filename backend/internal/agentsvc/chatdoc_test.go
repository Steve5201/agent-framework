// chatdoc_test.go —— 聊天上传文档（模块二）服务层单测。
//
// 覆盖：入参校验（文件名/大小/类型白名单）、属主校验、每会话配额、
// 解析复用（ingest.Parser）、全文落盘工作区、限长注入会话历史、注入失败回退。
// 全部使用 fakeRepo + 临时工作区（不触真实 DB 与沙盒）。
package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// newChatDocService 创建带临时工作区的测试服务（模块二专用）。
func newChatDocService(t *testing.T) (*Service, *fakeRepo, int64) {
	t.Helper()
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.workRoot = t.TempDir()
	// 强制 NoopVision：防止本机 VISION_MODEL 环境变量泄漏进测试（上传图片不发真实视觉调用）。
	svc.vision = NoopVision{}
	s, err := svc.CreateSession(context.Background(), 1, "", "文档会话")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return svc, repo, s.ID
}

// TestUploadChatDocument_Validation 入参校验：文件名/内容/大小/类型白名单。
func TestUploadChatDocument_Validation(t *testing.T) {
	svc, _, sid := newChatDocService(t)
	ctx := context.Background()
	content := []byte("# 标题\n正文内容")

	cases := []struct {
		name     string
		fileName string
		data     []byte
	}{
		{"空文件名", "", content},
		{"点条目", ".", content},
		{"上级目录条目", "..", content},
		{"超大文档", "big.md", make([]byte, defaultChatDocSize+1)},
		{"不支持类型", "a.exe", content},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.UploadChatDocument(ctx, 1, sid, tc.fileName, tc.data); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
				t.Fatalf("应 INVALID_ARGUMENT, got %v", err)
			}
		})
	}
}

// TestUploadChatDocument_EmptyFile 空文件（0 字节）不再拒绝（需求 4）：
// 落盘空文件 + 注入「文件内容为空」提示，让模型知道文件是空的而不是解析失败。
func TestUploadChatDocument_EmptyFile(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()

	res, err := svc.UploadChatDocument(ctx, 1, sid, "empty.md", nil)
	if err != nil {
		t.Fatalf("空文件应成功上传, got %v", err)
	}
	if res.FileName != "empty.md" || res.Segments != 0 || res.Kind != chatDocKindDoc {
		t.Fatalf("空文件结果异常: %+v", res)
	}
	// 注入消息含 [文档] 标记与「内容为空」提示。
	msgs, err := repo.ListMessages(ctx, sid)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var last string
	for _, m := range msgs {
		if m.Role == string(schema.RoleUser) {
			last = m.Content
		}
	}
	if !strings.Contains(last, "[文档]") || !strings.Contains(last, "文件内容为空") {
		t.Fatalf("应注入「文件内容为空」提示, got %q", last)
	}
	// 空文件已落盘工作区（0 字节）。
	if _, err := os.Stat(filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(res.RelPath))); err != nil {
		t.Fatalf("空文件应落盘工作区: %v", err)
	}
}

// TestUploadChatDocument_NotOwner 非本人上传 → NOT_FOUND（防枚举）。
func TestUploadChatDocument_NotOwner(t *testing.T) {
	svc, _, sid := newChatDocService(t)
	if _, err := svc.UploadChatDocument(context.Background(), 2, sid, "a.md", []byte("body")); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}
}

// TestUploadChatDocument_Quota 每会话文档数量配额：达到上限后拒绝。
func TestUploadChatDocument_Quota(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()

	msgs := make([]*Message, 0, defaultChatDocsPerSession)
	for i := 0; i < defaultChatDocsPerSession; i++ {
		msgs = append(msgs, &Message{Role: "user", Content: chatDocMarker + " 预置"})
	}
	if err := repo.AppendMessages(ctx, sid, msgs); err != nil {
		t.Fatalf("预置文档消息: %v", err)
	}
	if _, err := svc.UploadChatDocument(ctx, 1, sid, "a.md", []byte("# ok\nbody")); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超配额应 INVALID_ARGUMENT, got %v", err)
	}
}

// TestUploadChatDocument_ParseEmptyText 空白内容文档（无有效正文）不再拒绝：
// 上传只落盘原文件 + 注入提示词，不做系统自动解析（解析交由 read_document 工具，
// 由模型自主决定是否调用）。
func TestUploadChatDocument_ParseEmptyText(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()

	res, err := svc.UploadChatDocument(ctx, 1, sid, "blank.md", []byte("   \n\n  "))
	if err != nil {
		t.Fatalf("空白文档应上传成功（不做自动解析）, got %v", err)
	}
	// 原文件二进制原样落盘（不解析、不改写）。
	b, _ := os.ReadFile(filepath.Join(svc.workRoot, filepath.FromSlash(res.RelPath)))
	if string(b) != "   \n\n  " {
		t.Fatalf("落盘内容应为原始字节, got %q", string(b))
	}
	// 注入一条提示词消息。
	msgs, _ := repo.ListMessages(ctx, sid)
	if len(msgs) != 1 {
		t.Fatalf("应注入 1 条消息, got %d", len(msgs))
	}
	if !strings.HasPrefix(msgs[0].Content, chatDocMarker) {
		t.Fatalf("注入消息应带 [文档] 前缀: %q", msgs[0].Content)
	}
}

// TestUploadChatDocument_Success 成功链路：原文件落盘 + 注入提示词，不做自动解析。
func TestUploadChatDocument_Success(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()
	data := []byte("# 课程简介\n这是第一段内容。\n\n## 第二章\n第二段内容。")

	res, err := svc.UploadChatDocument(ctx, 1, sid, "intro.md", data)
	if err != nil {
		t.Fatalf("UploadChatDocument: %v", err)
	}
	// 结果字段。
	if res.FileName != "intro.md" {
		t.Fatalf("FileName = %q", res.FileName)
	}
	wantRel := filepath.ToSlash(filepath.Join("users", "1", "chat-files", strconv.FormatInt(sid, 10), "intro.md"))
	if res.RelPath != wantRel {
		t.Fatalf("RelPath = %q, want %q", res.RelPath, wantRel)
	}
	// 系统不再自动解析：Segments / InjectedLen 恒为 0。
	if res.Segments != 0 {
		t.Fatalf("Segments = %d, 系统不应自动解析（应为 0）", res.Segments)
	}
	if res.InjectedLen != 0 {
		t.Fatalf("InjectedLen = %d, 系统不应注入正文（应为 0）", res.InjectedLen)
	}

	// 原文件二进制原样落盘到用户工作区（路径含所属用户与会话），不解析不改写。
	full := filepath.Join(svc.workRoot, filepath.FromSlash(res.RelPath))
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读取落盘文件: %v", err)
	}
	if string(b) != string(data) {
		t.Fatalf("落盘内容应为原始字节: got %q, want %q", string(b), string(data))
	}

	// 注入一条 user 消息：新轮次、前缀 [文档]、携带工作区路径、无正文注入。
	msgs, _ := repo.ListMessages(ctx, sid)
	if len(msgs) != 1 {
		t.Fatalf("应注入 1 条消息, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || !strings.HasPrefix(msgs[0].Content, chatDocMarker) {
		t.Fatalf("注入消息不符: %+v", msgs[0])
	}
	if msgs[0].RoundNo != 1 {
		t.Fatalf("首条文档消息轮次应为 1, got %d", msgs[0].RoundNo)
	}
	if !strings.Contains(msgs[0].Content, "已保存至工作区") {
		t.Fatalf("注入消息应含「已保存至工作区」提示: %q", msgs[0].Content)
	}
	// 注入消息是提示词而非正文：不得出现文档正文内容。
	if strings.Contains(msgs[0].Content, "这是第一段内容") {
		t.Fatalf("注入消息不应携带解析正文（模型自主调用工具）: %q", msgs[0].Content)
	}
	// 注入路径统一用全局相对（含 users/<uid>/ 前缀），与 /files 渲染协议、
	// file_ops 展示路径、read_document 解析路径一致。
	wantInject := filepath.ToSlash(filepath.Join("users", "1", "chat-files", strconv.FormatInt(sid, 10), "intro.md"))
	if !strings.Contains(msgs[0].Content, wantInject) {
		t.Fatalf("注入消息应含全局相对路径 %q: %q", wantInject, msgs[0].Content)
	}
}

// TestUploadChatDocument_InjectFailure 注入会话历史失败 → INTERNAL（不回滚已落盘文件）。
func TestUploadChatDocument_InjectFailure(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	repo.appendErr = errors.New("db down")

	if _, err := svc.UploadChatDocument(context.Background(), 1, sid, "a.md", []byte("# ok\nbody")); apperr.CodeOf(err) != apperr.CodeInternal {
		t.Fatalf("注入失败应 INTERNAL, got %v", err)
	}
}

// TestUploadChatDocument_Image 图片上传（视觉解析预留）：
// 原图二进制落盘、注入 [图片] 标记消息、返回 kind=image + /files 渲染地址。
func TestUploadChatDocument_Image(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()
	img := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 1, 2, 3} // 占位图片字节

	res, err := svc.UploadChatDocument(ctx, 1, sid, "photo.png", img)
	if err != nil {
		t.Fatalf("UploadChatDocument(image): %v", err)
	}
	if res.Kind != chatDocKindImage {
		t.Fatalf("Kind = %q, want %q", res.Kind, chatDocKindImage)
	}
	wantRel := filepath.ToSlash(filepath.Join("users", "1", "chat-files", strconv.FormatInt(sid, 10), "photo.png"))
	if res.RelPath != wantRel {
		t.Fatalf("RelPath = %q, want %q", res.RelPath, wantRel)
	}
	if res.Url != "/files/"+wantRel {
		t.Fatalf("Url = %q, want /files/%q", res.Url, wantRel)
	}
	// 原图二进制一致落盘（不经过文本解析管线）。
	b, err := os.ReadFile(filepath.Join(svc.workRoot, filepath.FromSlash(res.RelPath)))
	if err != nil {
		t.Fatalf("读取落盘图片: %v", err)
	}
	if string(b) != string(img) {
		t.Fatalf("落盘图片内容不符: got %x, want %x", b, img)
	}

	// 注入一条 [图片] 标记 user 消息（无正文）。
	msgs, _ := repo.ListMessages(ctx, sid)
	if len(msgs) != 1 {
		t.Fatalf("应注入 1 条消息, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || !strings.HasPrefix(msgs[0].Content, chatImageMarker) {
		t.Fatalf("注入消息不符: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Content, wantRel) {
		t.Fatalf("注入消息应含工作区路径 %q: %q", wantRel, msgs[0].Content)
	}
}

// TestUploadChatDocument_ImageQuota 图片消息计入每会话配额（与文档同池）。
func TestUploadChatDocument_ImageQuota(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()

	msgs := make([]*Message, 0, defaultChatDocsPerSession)
	for i := 0; i < defaultChatDocsPerSession; i++ {
		msgs = append(msgs, &Message{Role: "user", Content: chatImageMarker + " 预置图"})
	}
	if err := repo.AppendMessages(ctx, sid, msgs); err != nil {
		t.Fatalf("预置图片消息: %v", err)
	}
	if _, err := svc.UploadChatDocument(ctx, 1, sid, "a.md", []byte("# ok\nbody")); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超配额应 INVALID_ARGUMENT, got %v", err)
	}
}

// TestUploadChatDocument_Image_NoAutoVision 上传图片不再自动调用视觉解析
// （需求 P2·模型自主调用工具）：注入消息只含提示词、无【图片内容】描述，
// vision.Describe 不应被调用（解析由 describe_image 工具按模型决策触发）。
func TestUploadChatDocument_Image_NoAutoVision(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	fv := &fakeVision{desc: "这是一张课程表，包含周一至周五的课程安排"}
	svc.vision = fv
	ctx := context.Background()

	res, err := svc.UploadChatDocument(ctx, 1, sid, "photo.png", []byte("img"))
	if err != nil {
		t.Fatalf("UploadChatDocument(image): %v", err)
	}
	if res.Kind != chatDocKindImage {
		t.Fatalf("Kind = %q, want %q", res.Kind, chatDocKindImage)
	}
	msgs, _ := repo.ListMessages(ctx, sid)
	if len(msgs) != 1 {
		t.Fatalf("应注入 1 条消息, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "已保存至工作区") {
		t.Fatalf("注入消息应含「已保存至工作区」提示: %q", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "【图片内容】") {
		t.Fatalf("上传不应自动注入视觉描述: %q", msgs[0].Content)
	}
	if fv.calls != 0 {
		t.Fatalf("上传图片不应触发 vision.Describe, calls = %d", fv.calls)
	}
}

// fakeVision 返回固定描述的测试视觉实现（模拟配置了 VISION_MODEL 后的行为）。
// calls 记录 Describe 被调用的次数（多模态分流测试断言"跳过自动描述"）。
type fakeVision struct {
	desc  string
	calls int
}

func (f *fakeVision) Describe(context.Context, []byte, string) (string, error) {
	f.calls++
	if f.desc == "" {
		return "", errVisionNotEnabled
	}
	return f.desc, nil
}

// TestDescribeImageTool 视觉工具化（需求 8）：describe_image 工具绑定 Service，
// 智能体可随时从其它角度重新解析图片（读工作区文件 → vision.Describe）。
func TestDescribeImageTool(t *testing.T) {
	svc, _, sid := newChatDocService(t)
	svc.vision = &fakeVision{desc: "图中文字：示例大学"}
	ctx := context.Background()

	// 写入一张假图到用户工作区。
	rel := filepath.Join("users", "1", "chat-files", strconv.FormatInt(sid, 10), "a.png")
	full := filepath.Join(svc.effectiveWorkRoot(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := svc.getRegistry()
	if _, err := reg.Get("describe_image"); err != nil {
		t.Fatalf("describe_image 工具应已注册: %v", err)
	}
	res, err := reg.Execute(ctx, schema.ToolCall{
		Name:      "describe_image",
		Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q,"focus":"提取图中文字"}`, filepath.ToSlash(rel))),
	}, true)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "示例大学") {
		t.Fatalf("应返回视觉描述, got %q", res.Content)
	}

	// 缺 path → 参数校验错误（err 非 nil）。
	if _, err := reg.Execute(ctx, schema.ToolCall{Name: "describe_image", Arguments: json.RawMessage(`{}`)}, true); err == nil {
		t.Fatalf("缺 path 应报错")
	}
	// 路径不存在 → 执行失败以 IsError=true 的 ToolResult 回填（registry 约定，
	// error 为 nil），失败原因带回给 LLM 以便调整策略。
	failed, err := reg.Execute(ctx, schema.ToolCall{
		Name:      "describe_image",
		Arguments: json.RawMessage(`{"path":"users/1/chat-files/999/not-exist.png"}`),
	}, true)
	if err != nil {
		t.Fatalf("执行失败不应返回 error（走 ToolResult.IsError）: %v", err)
	}
	if failed == nil || !failed.IsError || !strings.Contains(failed.Content, "读取图片失败") {
		t.Fatalf("不存在的图片应返回 IsError 结果, got %+v", failed)
	}
}

// TestReadDocumentTool 文档解析工具（需求 P2）：read_document 工具绑定 Service，
// 读工作区文档 → ingest 解析 → 返回正文（按 max_chars 截断）。
func TestReadDocumentTool(t *testing.T) {
	svc, _, sid := newChatDocService(t)
	ctx := context.Background()

	// 写入一份文档到用户工作区。
	rel := filepath.Join("users", "1", "chat-files", strconv.FormatInt(sid, 10), "intro.md")
	full := filepath.Join(svc.effectiveWorkRoot(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte("# 标题\n正文内容：示例大学"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := svc.getRegistry()
	if _, err := reg.Get("read_document"); err != nil {
		t.Fatalf("read_document 工具应已注册: %v", err)
	}
	res, err := reg.Execute(ctx, schema.ToolCall{
		Name:      "read_document",
		Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q}`, filepath.ToSlash(rel))),
	}, true)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "示例大学") {
		t.Fatalf("应返回文档正文, got %q", res.Content)
	}

	// max_chars 截断生效。
	res, err = reg.Execute(ctx, schema.ToolCall{
		Name:      "read_document",
		Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q,"max_chars":4}`, filepath.ToSlash(rel))),
	}, true)
	if err != nil {
		t.Fatalf("Execute(max_chars): %v", err)
	}
	if !strings.Contains(res.Content, "……（文档内容过长") {
		t.Fatalf("超长截断提示缺失: %q", res.Content)
	}

	// 缺 path → 参数校验错误。
	if _, err := reg.Execute(ctx, schema.ToolCall{Name: "read_document", Arguments: json.RawMessage(`{}`)}, true); err == nil {
		t.Fatalf("缺 path 应报错")
	}
	// 路径不存在 → IsError 结果回填（失败原因带回给 LLM 以便调整策略）。
	failed, err := reg.Execute(ctx, schema.ToolCall{
		Name:      "read_document",
		Arguments: json.RawMessage(`{"path":"users/1/chat-files/999/not-exist.md"}`),
	}, true)
	if err != nil {
		t.Fatalf("执行失败不应返回 error（走 ToolResult.IsError）: %v", err)
	}
	if failed == nil || !failed.IsError || !strings.Contains(failed.Content, "读取文档失败") {
		t.Fatalf("不存在的文档应返回 IsError 结果, got %+v", failed)
	}
	// 图片类型拒绝，并明确引导使用 describe_image。
	imgFail, err := reg.Execute(ctx, schema.ToolCall{
		Name:      "read_document",
		Arguments: json.RawMessage(`{"path":"users/1/chat-files/3/a.png"}`),
	}, true)
	if err != nil {
		t.Fatalf("图片类型应返回 IsError 结果而非 error: %v", err)
	}
	if imgFail == nil || !imgFail.IsError || !strings.Contains(imgFail.Content, "describe_image") {
		t.Fatalf("图片类型应提示用 describe_image, got %+v", imgFail)
	}
}

// TestTruncateRunes 按字符数截断：短文本原样、超长截断并追加提示。
func TestTruncateRunes(t *testing.T) {
	short := "你好世界"
	if got := truncateRunes(short, 10); got != short {
		t.Fatalf("短文本不应截断: %q", got)
	}
	long := strings.Repeat("a", 100)
	want := strings.Repeat("a", 10) + fmt.Sprintf("\n……（文档内容过长，仅截取前 %d 字符，完整内容见工作区文件）", 10)
	if got := truncateRunes(long, 10); got != want {
		t.Fatalf("截断结果不符:\n got  %q\n want %q", got, want)
	}
	// 空串边界：len(r)==0 ≤ n，原样返回。
	if got := truncateRunes("", 10); got != "" {
		t.Fatalf("空串应原样返回: %q", got)
	}
}

// TestChatDocCount 配额统计：仅统计 user 角色且 [文档] 前缀的消息。
func TestChatDocCount(t *testing.T) {
	svc, repo, sid := newChatDocService(t)
	ctx := context.Background()
	_ = repo.AppendMessages(ctx, sid, []*Message{
		{Role: "user", Content: chatDocMarker + " a"},
		{Role: "user", Content: "普通问题"},
		{Role: "assistant", Content: chatDocMarker + " 不应计入"},
		{Role: "user", Content: chatDocMarker + " b"},
	})
	n, err := svc.chatDocCount(ctx, sid)
	if err != nil {
		t.Fatalf("chatDocCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("应统计 2 条, got %d", n)
	}
}

// TestEffectiveWorkRoot 工作区根回退：显式配置优先，空则进程工作目录。
func TestEffectiveWorkRoot(t *testing.T) {
	svc, _ := newTestService(newFakeRepo(), &llm.MockProvider{})
	svc.workRoot = "/custom/root"
	if got := svc.effectiveWorkRoot(); got != "/custom/root" {
		t.Fatalf("显式配置应优先, got %q", got)
	}
	svc.workRoot = ""
	if got := svc.effectiveWorkRoot(); got == "" || got == "." {
		t.Fatalf("空配置应回退进程目录, got %q", got)
	}
}
