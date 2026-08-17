// service_test.go —— agentsvc 业务层单测（P2-48 / P2-K）。
//
// 覆盖：CRUD 属主校验 / Chat 落库 / 工具调用闭环 / 历史恢复 / 并发锁 /
// 流式增量 / 删除一轮 / 重命名 / 重新生成多版本 / 版本切换 / 分支 / 空会话过滤。
// 全部使用 fakeRepo（内存）+ llm.MockProvider（不触真实模型）。
package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Steve5201/agent-backend/internal/auth"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// fakeRepo：内存版 Repository（模拟 PG 语义：seq 递增、轮次/版本/隐藏、软删）
// ---------------------------------------------------------------------------

type fakeRepo struct {
	mu        sync.Mutex
	sessions  map[int64]*Session
	messages  map[int64][]*Message
	nextID    int64
	nextMsg   int64
	appendErr error // 故障注入
	audits    []AuditToolCall

	// SessionStats 注入：管理端会话统计测试用（避免 fake 复刻 SQL 聚合）。
	stats     *SessionStats
	statsErr  error
	statsDays int // 记录最近一次请求的 days（断言透传）

	// 编排过程输出入库记录（P4-I）：断言用。
	runs []OrchestrationRun
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		sessions: make(map[int64]*Session),
		messages: make(map[int64][]*Message),
	}
}

func (f *fakeRepo) CreateSession(_ context.Context, userID int64, agentID, title string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	s := &Session{ID: f.nextID, UserID: userID, AgentID: agentID, Title: title, Status: SessionStatusActive}
	f.sessions[s.ID] = s
	cp := *s
	return &cp, nil // 返回副本：模拟真实存储每次查询返回新对象（服务层合并不污染落库态）
}

// ListSessions 只返回"有过消息"的有效会话（模拟 PG 的 EXISTS 过滤）。
// agentID：” 精确匹配管理端域；'*' 列出全部域；否则精确匹配智能体域。
// 返回副本：与真实存储一致，调用方修改不影响落库态。
func (f *fakeRepo) ListSessions(_ context.Context, userID int64, agentID string, page, pageSize int) ([]*Session, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]*Session, 0)
	for _, s := range f.sessions {
		if s.UserID == userID && s.Active() && len(f.messages[s.ID]) > 0 &&
			(agentID == "*" || s.AgentID == agentID) {
			cp := *s
			all = append(all, &cp)
		}
	}
	total := int64(len(all))
	start := (page - 1) * pageSize
	if start >= len(all) {
		return []*Session{}, total, nil
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// MergeGuestSessions 把游客命名空间下有效会话转移给目标账号（fake 内存实现）。
func (f *fakeRepo) MergeGuestSessions(_ context.Context, guestUserID, targetUserID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.UserID == guestUserID && s.Active() {
			s.UserID = targetUserID
			n++
		}
	}
	return n, nil
}

// SessionStats 管理端会话统计（fake：返回注入值并记录请求窗口）。
func (f *fakeRepo) SessionStats(_ context.Context, days int) (*SessionStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsDays = days
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	if f.stats != nil {
		cp := *f.stats
		return &cp, nil
	}
	return &SessionStats{}, nil
}

func (f *fakeRepo) GetSession(_ context.Context, sessionID int64) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, apperr.New(apperr.CodeNotFound, "会话不存在")
	}
	cp := *s
	return &cp, nil // 返回副本：模拟真实存储每次查询返回新对象
}

func (f *fakeRepo) DeleteSession(_ context.Context, sessionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.Status = SessionStatusDeleted
	}
	return nil
}

func (f *fakeRepo) UpdateSessionTitle(_ context.Context, sessionID int64, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.Title = title
	}
	return nil
}

func (f *fakeRepo) UpdateSessionConfig(_ context.Context, sessionID int64, cfg SessionConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.Config = cfg
	}
	return nil
}

// InsertAuditToolCall 记录到内存切片（审计断言用）；返回 nil 模拟成功。
func (f *fakeRepo) InsertAuditToolCall(_ context.Context, a *AuditToolCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, *a)
	return nil
}

// SaveOrchestration 记录到内存切片（编排入库断言用）；返回 nil 模拟成功。
func (f *fakeRepo) SaveOrchestration(_ context.Context, run *OrchestrationRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, *run)
	return nil
}

// ListMessages 返回可见消息（hidden=false），并统计每轮版本总数。
func (f *fakeRepo) ListMessages(_ context.Context, sessionID int64) ([]*Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Message, 0)
	for _, m := range f.messages[sessionID] {
		if m.Hidden {
			continue
		}
		mm := *m
		seen := map[int]bool{}
		for _, o := range f.messages[sessionID] {
			if o.RoundNo == m.RoundNo && !seen[o.Version] {
				seen[o.Version] = true
			}
		}
		mm.TotalVersions = len(seen)
		out = append(out, &mm)
	}
	return out, nil
}

func (f *fakeRepo) ListMessagesUptoRound(_ context.Context, sessionID, uptoRound int64) ([]*Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Message, 0)
	for _, m := range f.messages[sessionID] {
		if m.Hidden || m.RoundNo > uptoRound {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// AppendMessages 追加消息：RoundNo==0 的自动归入"下一轮"（兼容手工预置）。
func (f *fakeRepo) AppendMessages(_ context.Context, sessionID int64, msgs []*Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	if _, ok := f.messages[sessionID]; !ok {
		f.messages[sessionID] = make([]*Message, 0)
	}
	maxRound := int64(0)
	for _, m := range f.messages[sessionID] {
		if m.RoundNo > maxRound {
			maxRound = m.RoundNo
		}
	}
	newRound := maxRound + 1
	for _, m := range msgs {
		f.nextMsg++
		cp := *m
		cp.ID = f.nextMsg
		if cp.RoundNo == 0 {
			cp.RoundNo = newRound
		}
		f.messages[sessionID] = append(f.messages[sessionID], &cp)
	}
	return nil
}

// GetMessage 定位可见消息的轮次与角色。
func (f *fakeRepo) GetMessage(_ context.Context, sessionID, messageID int64) (*Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages[sessionID] {
		if m.ID == messageID && !m.Hidden {
			return &Message{ID: m.ID, Role: m.Role, RoundNo: m.RoundNo}, nil
		}
	}
	return nil, apperr.New(apperr.CodeNotFound, "消息不存在")
}

// DeleteRound 删除一整轮；删空后自动软删会话。
func (f *fakeRepo) DeleteRound(_ context.Context, sessionID, messageID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.messages[sessionID]
	round := int64(-1)
	for _, m := range list {
		if m.ID == messageID && !m.Hidden {
			round = m.RoundNo
			break
		}
	}
	if round < 0 {
		return nil // 幂等
	}
	kept := list[:0]
	for _, m := range list {
		if m.RoundNo != round {
			kept = append(kept, m)
		}
	}
	f.messages[sessionID] = kept
	if len(kept) == 0 {
		if s, ok := f.sessions[sessionID]; ok {
			s.Status = SessionStatusDeleted
		}
	}
	return nil
}

func (f *fakeRepo) MaxRoundNo(_ context.Context, sessionID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var max int64
	for _, m := range f.messages[sessionID] {
		if m.RoundNo > max {
			max = m.RoundNo
		}
	}
	return max, nil
}

func (f *fakeRepo) MaxRoundVersion(_ context.Context, sessionID, roundNo int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	max := 0
	for _, m := range f.messages[sessionID] {
		if m.RoundNo == roundNo && m.Version > max {
			max = m.Version
		}
	}
	return max, nil
}

func (f *fakeRepo) ActiveRoundVersion(_ context.Context, sessionID, roundNo int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	max := 0
	for _, m := range f.messages[sessionID] {
		if m.RoundNo == roundNo && m.Role != "user" && !m.Hidden && m.Version > max {
			max = m.Version
		}
	}
	return max, nil
}

func (f *fakeRepo) HideRoundAndAfter(_ context.Context, sessionID, roundNo int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages[sessionID] {
		if m.RoundNo > roundNo || (m.RoundNo == roundNo && m.Role != "user") {
			m.Hidden = true
		}
	}
	return nil
}

func (f *fakeRepo) RestoreRoundAndAfter(_ context.Context, sessionID, roundNo int64, version int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages[sessionID] {
		if m.RoundNo > roundNo || (m.RoundNo == roundNo && m.Role != "user" && m.Version == version) {
			m.Hidden = false
		}
	}
	return nil
}

func (f *fakeRepo) SetActiveVersion(_ context.Context, sessionID, roundNo int64, version int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages[sessionID] {
		if m.RoundNo > roundNo {
			m.Hidden = true
		} else if m.RoundNo == roundNo && m.Role != "user" {
			m.Hidden = m.Version != version
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 测试工具
// ---------------------------------------------------------------------------

func newTestService(repo Repository, p llm.Provider) (*Service, error) {
	reg, err := DefaultToolSet()
	if err != nil {
		return nil, err
	}
	return NewService(Config{
		Repo:         repo,
		Provider:     p,
		Registry:     reg,
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "你是测试助手。",
		MaxRounds:    8,
		MaxMessages:  20,
	})
}

// echoProvider 按调用次数返回预设响应的非流式 Provider。
// 第 1 次返回一次 echo 工具调用，之后返回最终回答——模拟"模型先调工具再回答"。
func echoProvider() *llm.MockProvider {
	calls := 0
	var mu sync.Mutex
	return &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return &llm.Response{
					ToolCalls: []schema.ToolCall{{
						ID:        "call_1",
						Name:      "echo",
						Arguments: json.RawMessage(`{"text":"hi"}`),
					}},
				}, nil
			}
			return &llm.Response{Content: "回声结果: echo: hi", Usage: llm.Usage{TotalTokens: 10}}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// 会话 CRUD
// ---------------------------------------------------------------------------

func TestService_CreateSession(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// 自定义标题
	s1, err := svc.CreateSession(context.Background(), 1, "", "考研规划")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s1.Title != "考研规划" || s1.UserID != 1 || !s1.Active() {
		t.Fatalf("session 字段不符: %+v", s1)
	}
	if s1.ID == 0 {
		t.Fatal("会话 ID 应为正数")
	}

	// 空标题 → 默认"新对话"
	s2, err := svc.CreateSession(context.Background(), 1, "", "")
	if err != nil {
		t.Fatalf("CreateSession(empty title): %v", err)
	}
	if s2.Title != "新对话" {
		t.Fatalf("空标题应默认 '新对话', got %q", s2.Title)
	}
}

func TestService_GetSession_NotOwner(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(context.Background(), 1, "", "私有会话")

	// 非本人访问 → CodeNotFound（防枚举，而非 PermissionDenied）
	_, err := svc.GetSession(context.Background(), 2, s.ID)
	if code := apperr.CodeOf(err); code != apperr.CodeNotFound {
		t.Fatalf("非本人应返回 NOT_FOUND, got %s", code)
	}
	// 本人访问成功
	if _, err := svc.GetSession(context.Background(), 1, s.ID); err != nil {
		t.Fatalf("本人访问失败: %v", err)
	}
}

func TestService_DeleteSession_OwnershipAndIdempotent(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(context.Background(), 1, "", "待删")

	// 非本人删除 → NOT_FOUND
	if err := svc.DeleteSession(context.Background(), 2, s.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人删除应 NOT_FOUND, got %v", err)
	}
	// 本人删除成功
	if err := svc.DeleteSession(context.Background(), 1, s.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	// 幂等：再删成功
	if err := svc.DeleteSession(context.Background(), 1, s.ID); err != nil {
		t.Fatalf("重复删除应幂等成功, got %v", err)
	}
	// 删除后列表不包含
	list, _, _ := svc.ListSessions(context.Background(), 1, "", 1, 20)
	if len(list) != 0 {
		t.Fatalf("软删后列表应为空, got %d", len(list))
	}
}

func TestService_ListMessages(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(context.Background(), 1, "", "历史回看")

	// 预置两条消息（模拟已发生的对话）。
	if err := repo.AppendMessages(context.Background(), s.ID, []*Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
	}); err != nil {
		t.Fatalf("预置消息失败: %v", err)
	}

	// 非本人 → NOT_FOUND（防枚举）
	if _, err := svc.ListMessages(context.Background(), 2, s.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}

	// 本人 → 返回全部消息（seq 升序）
	msgs, err := svc.ListMessages(context.Background(), 1, s.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("消息列表不符: %+v", msgs)
	}

	// 已删除会话 → NOT_FOUND
	if err := svc.DeleteSession(context.Background(), 1, s.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := svc.ListMessages(context.Background(), 1, s.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("已删除会话应 NOT_FOUND, got %v", err)
	}
}

// TestService_DeleteMessage_RemovesWholeRound 删除语义：删"一轮完整对话"，
// 该轮 user + assistant（含工具对）全删；删空后会话自动软删。
func TestService_DeleteMessage_RemovesWholeRound(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(ctx, 1, "", "删轮")

	// 预置 2 轮：round1 = 4 条（工具调用对），round2 = 2 条。
	// 注意：fakeRepo 对同一批次自动分配同一轮号，故必须显式指定 RoundNo。
	if err := repo.AppendMessages(ctx, s.ID, []*Message{
		{RoundNo: 1, Role: "user", Content: "计算 1+1"},
		{RoundNo: 1, Role: "assistant", ToolCalls: []schema.ToolCall{{ID: "call_1", Name: "calculator", Arguments: json.RawMessage(`"1+1"`)}}},
		{RoundNo: 1, Role: "tool", ToolCallID: "call_1", Content: "2"},
		{RoundNo: 1, Role: "assistant", Content: "结果是 2。"},
		{RoundNo: 2, Role: "user", Content: "谢谢"},
		{RoundNo: 2, Role: "assistant", Content: "不客气！"},
	}); err != nil {
		t.Fatalf("预置消息失败: %v", err)
	}

	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(msgs) != 6 {
		t.Fatalf("预置应 6 条, got %d", len(msgs))
	}

	// 非本人删除 → NOT_FOUND（防枚举）。
	if err := svc.DeleteMessage(ctx, 2, s.ID, msgs[5].ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}

	// 删除第 2 轮 assistant → 整轮消失，round1 完整保留。
	if err := svc.DeleteMessage(ctx, 1, s.ID, msgs[5].ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	left, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(left) != 4 {
		t.Fatalf("删除第 2 轮后应剩 round1 的 4 条, got %d: %+v", len(left), left)
	}
	if left[0].Content != "计算 1+1" || left[3].Content != "结果是 2。" {
		t.Fatalf("round1 应完整保留: %+v", left)
	}

	// 会话仍活跃时，删除不存在的消息 → 幂等成功。
	if err := svc.DeleteMessage(ctx, 1, s.ID, 999999); err != nil {
		t.Fatalf("删除不存在消息应幂等成功, got %v", err)
	}

	// 再删除 round1 任意一条 → 会话消息清空 → 自动软删（列表不再出现）。
	if err := svc.DeleteMessage(ctx, 1, s.ID, left[1].ID); err != nil {
		t.Fatalf("DeleteMessage round1: %v", err)
	}
	empty, _ := repo.ListMessages(ctx, s.ID)
	if len(empty) != 0 {
		t.Fatalf("删除全部后消息应为空, got %d", len(empty))
	}
	list, _, _ := svc.ListSessions(ctx, 1, "", 1, 20)
	if len(list) != 0 {
		t.Fatalf("空会话应自动删除（列表为空）, got %d", len(list))
	}

	// 已删除会话 → NOT_FOUND。
	if err := svc.DeleteMessage(ctx, 1, s.ID, msgs[0].ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("已删除会话应 NOT_FOUND, got %v", err)
	}
}

// TestService_EmptySessionFiltered 空会话策略：创建后没对话的会话不展示。
func TestService_EmptySessionFiltered(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	empty, _ := svc.CreateSession(ctx, 1, "", "空会话")

	list, total, _ := svc.ListSessions(ctx, 1, "", 1, 20)
	if len(list) != 0 || total != 0 {
		t.Fatalf("无消息的空会话不应展示, got list=%d total=%d", len(list), total)
	}

	// 有消息后出现。
	_ = repo.AppendMessages(ctx, empty.ID, []*Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "你好"}})
	list2, total2, _ := svc.ListSessions(ctx, 1, "", 1, 20)
	if len(list2) != 1 || total2 != 1 {
		t.Fatalf("有消息后应展示, got list=%d total=%d", len(list2), total2)
	}
}

// TestService_RenameSession 重命名会话。
func TestService_RenameSession(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(ctx, 1, "", "旧标题")

	// 非本人 → NOT_FOUND。
	if _, err := svc.RenameSession(ctx, 2, s.ID, "x"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}
	// 空标题 → INVALID_ARGUMENT。
	if _, err := svc.RenameSession(ctx, 1, s.ID, "   "); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("空标题应 INVALID_ARGUMENT, got %v", err)
	}
	// 超长标题（101 字符）→ INVALID_ARGUMENT。
	long := make([]rune, 101)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := svc.RenameSession(ctx, 1, s.ID, string(long)); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超长标题应 INVALID_ARGUMENT, got %v", err)
	}
	// 正常重命名。
	sess, err := svc.RenameSession(ctx, 1, s.ID, "考研规划 V2")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if sess.Title != "考研规划 V2" {
		t.Fatalf("标题应为 '考研规划 V2', got %q", sess.Title)
	}
	got, _ := svc.GetSession(ctx, 1, s.ID)
	if got.Title != "考研规划 V2" {
		t.Fatalf("持久化标题不符: %q", got.Title)
	}
}

// ---------------------------------------------------------------------------
// Chat 非流式
// ---------------------------------------------------------------------------

func TestService_Chat_PersistsUserAndAssistant(t *testing.T) {
	repo := newFakeRepo()
	p := &llm.MockProvider{Content: "你好，我是智能助手。"}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(context.Background(), 1, "", "对话")

	out, err := svc.Chat(context.Background(), 1, s.ID, "早上好")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Message.Content != "你好，我是智能助手。" {
		t.Fatalf("回答不符: %q", out.Message.Content)
	}

	msgs, _ := repo.ListMessages(context.Background(), s.ID)
	if len(msgs) != 2 {
		t.Fatalf("应落库 2 条（user+assistant）, got %d", len(msgs))
	}
	if msgs[0].Role != string(schema.RoleUser) || msgs[0].Content != "早上好" {
		t.Fatalf("第 1 条应为用户消息: %+v", msgs[0])
	}
	if msgs[1].Role != string(schema.RoleAssistant) || msgs[1].Content != "你好，我是智能助手。" {
		t.Fatalf("第 2 条应为 assistant 回答: %+v", msgs[1])
	}
}

// TestService_Chat_AutoRename 首轮对话后会话自动命名（"新对话"→首条用户消息）。
func TestService_Chat_AutoRename(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "ok"})
	s, _ := svc.CreateSession(ctx, 1, "", "新对话")

	if _, err := svc.Chat(ctx, 1, s.ID, "帮我写一份考研数学复习计划，覆盖高数线代概率"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	sess, _ := svc.GetSession(ctx, 1, s.ID)
	if sess.Title == "新对话" {
		t.Fatalf("首轮对话后应自动命名, got %q", sess.Title)
	}
	if len([]rune(sess.Title)) > 24 {
		t.Fatalf("自动标题应截断到 24 字符内, got %q", sess.Title)
	}
}

func TestService_Chat_ToolCallRoundTrip(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, echoProvider())
	s, _ := svc.CreateSession(context.Background(), 1, "", "工具测试")

	out, err := svc.Chat(context.Background(), 1, s.ID, "帮我回显 hi")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.ToolCalls != 1 {
		t.Fatalf("应执行 1 次工具, got %d", out.ToolCalls)
	}

	// 落库应为 4 条：user → assistant(带tool_calls) → tool → assistant(最终)
	msgs, _ := repo.ListMessages(context.Background(), s.ID)
	if len(msgs) != 4 {
		t.Fatalf("工具轮应落库 4 条, got %d", len(msgs))
	}
	toolMsg := msgs[2]
	if toolMsg.Role != string(schema.RoleTool) || toolMsg.ToolCallID != "call_1" {
		t.Fatalf("第 3 条应为 tool 结果（关联 call_1）: %+v", toolMsg)
	}
	if msgs[1].Role != string(schema.RoleAssistant) || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("第 2 条应为带 tool_calls 的 assistant: %+v", msgs[1])
	}
}

func TestService_Chat_HistoryRestoredOnNextRound(t *testing.T) {
	repo := newFakeRepo()
	// 捕获每次 Chat 请求的消息序列，验证第二轮带上了第一轮历史。
	var seen [][]schema.Message
	var mu sync.Mutex
	p := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			mu.Lock()
			seen = append(seen, req.Messages)
			mu.Unlock()
			return &llm.Response{Content: "ok"}, nil
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(context.Background(), 1, "", "历史")

	if _, err := svc.Chat(context.Background(), 1, s.ID, "第一问"); err != nil {
		t.Fatalf("第一次 Chat: %v", err)
	}
	if _, err := svc.Chat(context.Background(), 1, s.ID, "第二问"); err != nil {
		t.Fatalf("第二次 Chat: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("应有 2 次 LLM 调用, got %d", len(seen))
	}
	second := seen[1]
	// 第二轮请求应包含：system + 第一轮的 user/assistant + 第二轮的 user。
	foundOld := false
	foundNew := false
	for _, m := range second {
		if m.Content == "第一问" && m.Role == schema.RoleUser {
			foundOld = true
		}
		if m.Content == "第二问" && m.Role == schema.RoleUser {
			foundNew = true
		}
	}
	if !foundOld || !foundNew {
		t.Fatalf("第二轮请求应同时含历史与新消息: old=%v new=%v\nmessages=%+v", foundOld, foundNew, second)
	}
}

func TestService_Chat_EmptyContent(t *testing.T) {
	// 需求 6：纯文件场景（用户只传文件不输入文字）空 content 不应被拒绝——
	// 后端把空消息规范化为占位提示，模型基于上下文中的 [文档]/[图片] 内容回复。
	repo := newFakeRepo()
	var lastReq *llm.Request
	mock := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			lastReq = req
			return &llm.Response{Content: "已读文件", Usage: llm.Usage{}}, nil
		},
	}
	svc, _ := newTestService(repo, mock)
	s, _ := svc.CreateSession(context.Background(), 1, "", "x")

	if _, err := svc.Chat(context.Background(), 1, s.ID, ""); err != nil {
		t.Fatalf("空内容应允许并触发回复, got %v", err)
	}
	if lastReq == nil || len(lastReq.Messages) == 0 {
		t.Fatalf("应携带规范化消息请求模型")
	}
	last := lastReq.Messages[len(lastReq.Messages)-1]
	if last.Role != schema.RoleUser || !strings.Contains(last.Content, "仅上传了文件") {
		t.Fatalf("空内容应规范化为占位提示, got %+v", last)
	}
}

// TestService_LoadHistory_SkipsEmptyAssistant 历史恢复时过滤"空回复"assistant
// 消息——它会让模型持续返回空回答（会话卡死），分支正常正因没有这条脏数据。
func TestService_LoadHistory_SkipsEmptyAssistant(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "ok"})
	s, _ := svc.CreateSession(ctx, 1, "", "卡死会话")

	// 预置历史：正常问答 + 一条空 assistant（历史污染）+ 工具轮 assistant（有 tool_calls，保留）。
	_ = repo.AppendMessages(ctx, s.ID, []*Message{
		{Role: string(schema.RoleUser), Content: "在吗"},
		{Role: string(schema.RoleAssistant), Content: "在的"},
		{Role: string(schema.RoleAssistant), Content: "", Reasoning: "思考了但没输出"},
		{Role: string(schema.RoleAssistant), Content: "", ToolCalls: []schema.ToolCall{{ID: "c1", Name: "echo"}}},
		{Role: string(schema.RoleTool), ToolCallID: "c1", Content: "ok"},
	})

	history, err := svc.loadHistory(ctx, s.ID)
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("应过滤 1 条空 assistant（保留 user/正常回答/工具轮/tool），got %d", len(history))
	}
	for _, m := range history {
		if m.Role == schema.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatal("空回复 assistant 消息不应出现在模型上下文中")
		}
	}
	// 展示层不受影响：ListMessages 保留原样。
	msgs, _ := repo.ListMessages(ctx, s.ID)
	if len(msgs) != 5 {
		t.Fatalf("展示层应保留全部 5 条, got %d", len(msgs))
	}
}

func TestService_Chat_ConcurrentSessionSerialized(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "reply"})
	s, _ := svc.CreateSession(context.Background(), 1, "", "并发")

	// 并发 5 次 Chat 同一会话：串行锁保证消息不交错、seq 连续。
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Chat(context.Background(), 1, s.ID, "hi"); err != nil {
				t.Errorf("并发 Chat: %v", err)
			}
		}()
	}
	wg.Wait()

	msgs, _ := repo.ListMessages(context.Background(), s.ID)
	// 5 次 × 2 条 = 10 条；且角色严格交替 user/assistant（无交错）。
	if len(msgs) != 10 {
		t.Fatalf("应落库 10 条, got %d", len(msgs))
	}
	for i, m := range msgs {
		want := string(schema.RoleUser)
		if i%2 == 1 {
			want = string(schema.RoleAssistant)
		}
		if m.Role != want {
			t.Fatalf("第 %d 条角色 %q 与期望 %q 不符（消息交错了）", i, m.Role, want)
		}
	}
}

// ---------------------------------------------------------------------------
// StreamChat 流式
// ---------------------------------------------------------------------------

func TestService_StreamChat_DeltasAndPersist(t *testing.T) {
	repo := newFakeRepo()
	p := &llm.MockProvider{
		Events: []llm.StreamEvent{
			{Content: "你"},
			{Content: "好"},
			{Usage: &llm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(context.Background(), 1, "", "流式")

	var deltas []string
	_, err := svc.StreamChat(context.Background(), 1, s.ID, "嗨", func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(deltas) != 2 || deltas[0]+deltas[1] != "你好" {
		t.Fatalf("增量应为 ['你','好'], got %v", deltas)
	}

	msgs, _ := repo.ListMessages(context.Background(), s.ID)
	if len(msgs) != 2 {
		t.Fatalf("流式也应收录 user+assistant, got %d", len(msgs))
	}
	if msgs[1].Content != "你好" {
		t.Fatalf("assistant 内容应为完整文本 %q, got %q", "你好", msgs[1].Content)
	}
}

// TestService_Chat_PersistsReasoning 思考内容随 assistant 消息落库
// （工具轮后续请求必须回传 reasoning_content，落库是回传的前提）。
func TestService_Chat_PersistsReasoning(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	calls := 0
	p := &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			calls++
			if calls == 1 {
				return &llm.Response{
					Reasoning: "思考：需要先回显",
					ToolCalls: []schema.ToolCall{{
						ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`),
					}},
				}, nil
			}
			return &llm.Response{Content: "已回显", Reasoning: "思考：完成"}, nil
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "思考")

	if _, err := svc.Chat(ctx, 1, s.ID, "回显 hi"); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	msgs, _ := repo.ListMessages(ctx, s.ID)
	// 两条 assistant 消息都应携带思考内容
	var assistantReasonings []string
	for _, m := range msgs {
		if m.Role == string(schema.RoleAssistant) {
			assistantReasonings = append(assistantReasonings, m.Reasoning)
		}
	}
	if len(assistantReasonings) != 2 ||
		assistantReasonings[0] != "思考：需要先回显" ||
		assistantReasonings[1] != "思考：完成" {
		t.Fatalf("assistant 思考内容未按序落库: %v", assistantReasonings)
	}
}

// TestService_StreamChat_ReasoningAndToolEvents 流式思考增量 + 工具调用/返回事件
// 实时通知，且思考内容最终落库。
func TestService_StreamChat_ReasoningAndToolEvents(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	calls := 0
	p := &llm.MockProvider{
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			calls++
			if calls == 1 {
				return (&llm.MockProvider{Events: []llm.StreamEvent{
					{Reasoning: "先算"},
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "echo", Arguments: `{"text":"hi"}`}}},
				}}).ChatStream(nil, nil)
			}
			return (&llm.MockProvider{Events: []llm.StreamEvent{
				{Reasoning: "再答"},
				{Content: "done"},
			}}).ChatStream(nil, nil)
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "流式思考")

	var reasoningParts, contentParts, toolNames, toolResults []string
	_, err := svc.StreamChatEvents(ctx, 1, s.ID, "帮我", &agent.StreamObserver{
		OnReasoning:  func(d string) { reasoningParts = append(reasoningParts, d) },
		OnContent:    func(d string) { contentParts = append(contentParts, d) },
		OnToolCall:   func(c schema.ToolCall) { toolNames = append(toolNames, c.Name) },
		OnToolResult: func(c schema.ToolCall, r *schema.ToolResult, e error) { toolResults = append(toolResults, r.Content) },
	})
	if err != nil {
		t.Fatalf("StreamChatEvents: %v", err)
	}
	if strings.Join(reasoningParts, "") != "先算再答" {
		t.Fatalf("思考增量 = %v", reasoningParts)
	}
	if strings.Join(contentParts, "") != "done" {
		t.Fatalf("回答增量 = %v", contentParts)
	}
	if len(toolNames) != 1 || toolNames[0] != "echo" {
		t.Fatalf("工具调用事件 = %v", toolNames)
	}
	if len(toolResults) != 1 || toolResults[0] != "echo: hi" {
		t.Fatalf("工具返回事件 = %v", toolResults)
	}

	// 落库校验：思考内容（含工具轮）已持久化
	msgs, _ := repo.ListMessages(ctx, s.ID)
	var persisted []string
	for _, m := range msgs {
		if m.Role == string(schema.RoleAssistant) {
			persisted = append(persisted, m.Reasoning)
		}
	}
	if len(persisted) != 2 || persisted[0] != "先算" || persisted[1] != "再答" {
		t.Fatalf("流式思考内容未落库: %v", persisted)
	}
}

// ---------------------------------------------------------------------------
// 重新生成 / 版本切换 / 分支（P2-K）
// ---------------------------------------------------------------------------

// TestService_Regenerate 重新生成：新版本落库、旧版本保留隐藏、后续轮截断。
func TestService_Regenerate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	calls := 0
	p := &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			calls++
			return &llm.Response{Content: fmt.Sprintf("回答-%d", calls)}, nil
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "重生成")

	// 两轮对话：round1 = 回答-1，round2 = 回答-2。
	if _, err := svc.Chat(ctx, 1, s.ID, "第一问"); err != nil {
		t.Fatalf("Chat1: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s.ID, "第二问"); err != nil {
		t.Fatalf("Chat2: %v", err)
	}
	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(msgs) != 4 {
		t.Fatalf("两轮应 4 条, got %d", len(msgs))
	}
	secondRoundAsst := msgs[3]

	// 重新生成第二轮 → 版本 1，新回答 = 回答-3。
	out, version, err := svc.Regenerate(ctx, 1, s.ID, secondRoundAsst.ID)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if version != 1 {
		t.Fatalf("新版本应为 1, got %d", version)
	}
	if out.Message.Content != "回答-3" {
		t.Fatalf("新回答应为 '回答-3', got %q", out.Message.Content)
	}

	// 重新拉取：round1 完整 + round2 user + 新回答 = 4 条可见。
	left, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(left) != 4 {
		t.Fatalf("重生成后应 4 条可见, got %d: %+v", len(left), left)
	}
	if left[0].Content != "第一问" || left[1].Content != "回答-1" {
		t.Fatalf("round1 应保留: %+v", left)
	}
	if left[2].Content != "第二问" {
		t.Fatalf("round2 user 应保留: %+v", left[2])
	}
	if left[3].Content != "回答-3" {
		t.Fatalf("应显示新回答, got %q", left[3].Content)
	}
	// 该轮版本总数 = 2（旧回答-2 被隐藏保留 + 新回答-3）。
	if left[3].TotalVersions != 2 {
		t.Fatalf("round2 版本总数应为 2, got %d", left[3].TotalVersions)
	}

	// 非本人重生成 → NOT_FOUND。
	if _, _, err := svc.Regenerate(ctx, 2, s.ID, secondRoundAsst.ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}
}

// TestService_StreamRegenerate 流式重新生成：正文增量经 observer 逐段下发，
// 版本语义与 Regenerate 一致（旧版本保留隐藏、新版本落库、失败回滚）。
func TestService_StreamRegenerate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	calls := 0
	p := &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			calls++
			return &llm.Response{Content: fmt.Sprintf("回答-%d", calls)}, nil
		},
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			calls++
			return (&llm.MockProvider{Events: []llm.StreamEvent{
				{Content: fmt.Sprintf("回答-%d", calls)},
				{Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
			}}).ChatStream(nil, nil)
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "流式重生成")

	// 两轮对话：round1 = 回答-1，round2 = 回答-2。
	if _, err := svc.Chat(ctx, 1, s.ID, "第一问"); err != nil {
		t.Fatalf("Chat1: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s.ID, "第二问"); err != nil {
		t.Fatalf("Chat2: %v", err)
	}
	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(msgs) != 4 {
		t.Fatalf("两轮应 4 条, got %d", len(msgs))
	}
	secondRoundAsst := msgs[3]

	// 流式重新生成第二轮 → 正文增量逐段下发，新版本 = 1。
	var deltas []string
	res, version, err := svc.StreamRegenerate(ctx, 1, s.ID, secondRoundAsst.ID, &agent.StreamObserver{
		OnContent: func(d string) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("StreamRegenerate: %v", err)
	}
	if version != 1 {
		t.Fatalf("新版本应为 1, got %d", version)
	}
	if strings.Join(deltas, "") != "回答-3" {
		t.Fatalf("正文增量应为 '回答-3', got %v", deltas)
	}
	if res.Content != "回答-3" {
		t.Fatalf("Result.Content 应为 '回答-3', got %q", res.Content)
	}

	// 落库校验：round2 显示新版本，旧版本隐藏；round1 保留。
	left, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(left) != 4 {
		t.Fatalf("重生成后应 4 条可见, got %d: %+v", len(left), left)
	}
	if left[3].Content != "回答-3" {
		t.Fatalf("应显示新回答, got %q", left[3].Content)
	}
	if left[3].TotalVersions != 2 {
		t.Fatalf("round2 版本总数应为 2, got %d", left[3].TotalVersions)
	}
}

// TestService_StreamRegenerate_FailureRollback 流式重新生成失败：截断回滚，
// 旧活跃版本与后续轮次全部恢复（不破坏原对话）。
func TestService_StreamRegenerate_FailureRollback(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	p := &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "回答-1"}, nil
		},
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			return nil, errors.New("llm: HTTP 504, 上游模型服务响应超时")
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "失败回滚")

	if _, err := svc.Chat(ctx, 1, s.ID, "第一问"); err != nil {
		t.Fatalf("Chat1: %v", err)
	}
	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	asst := msgs[1]

	if _, _, err := svc.StreamRegenerate(ctx, 1, s.ID, asst.ID, &agent.StreamObserver{}); err == nil {
		t.Fatalf("流式重生成应失败（上游 504）")
	}
	// 回滚后原回答仍可见（未被截断/清空）。
	left, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(left) != 2 || left[1].Content != "回答-1" {
		t.Fatalf("失败后应恢复原回答, got %+v", left)
	}
}

// TestService_SetActiveVersion 版本切换：显示目标版本、隐藏其它、截断后续轮。
func TestService_SetActiveVersion(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := repo.CreateSession(ctx, 1, "", "版本切换")

	// 预置：round1 有两个版本的回答（v0 与 v1），round2 一组对话。
	_ = repo.AppendMessages(ctx, s.ID, []*Message{
		{Role: "user", Content: "q1", RoundNo: 1, Version: 0},
		{Role: "assistant", Content: "a1-v0", RoundNo: 1, Version: 0},
		{Role: "assistant", Content: "a1-v1", RoundNo: 1, Version: 1},
		{Role: "user", Content: "q2", RoundNo: 2, Version: 0},
		{Role: "assistant", Content: "a2", RoundNo: 2, Version: 0},
	})

	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(msgs) != 5 {
		t.Fatalf("初始应 5 条可见, got %d", len(msgs))
	}
	userMsg := msgs[0] // round1 的 user 消息（定位轮次用）

	// 切到 v1：round1 只显示 a1-v1，round2 被截断。
	if err := svc.SetActiveVersion(ctx, 1, s.ID, userMsg.ID, 1); err != nil {
		t.Fatalf("SetActiveVersion: %v", err)
	}
	left, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(left) != 2 {
		t.Fatalf("切换 v1 后应剩 2 条（q1+a1-v1）, got %d: %+v", len(left), left)
	}
	if left[1].Content != "a1-v1" {
		t.Fatalf("应显示 v1 回答, got %q", left[1].Content)
	}
	if left[1].TotalVersions != 2 {
		t.Fatalf("版本总数应为 2, got %d", left[1].TotalVersions)
	}

	// 切回 v0：round1 显示 a1-v0。
	if err := svc.SetActiveVersion(ctx, 1, s.ID, userMsg.ID, 0); err != nil {
		t.Fatalf("SetActiveVersion back: %v", err)
	}
	back, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(back) != 2 || back[1].Content != "a1-v0" {
		t.Fatalf("切回 v0 应显示 a1-v0, got %+v", back)
	}

	// 非法版本号 → INVALID_ARGUMENT。
	if err := svc.SetActiveVersion(ctx, 1, s.ID, userMsg.ID, -1); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("负版本应 INVALID_ARGUMENT, got %v", err)
	}
}

// TestService_CreateBranch 分支：复制到分支点（含）为止的历史到新会话。
func TestService_CreateBranch(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "ok"})
	s, _ := svc.CreateSession(ctx, 1, "", "源会话")

	// 给源会话一份配置快照（模拟快照固化结果，含管理员级字段）。
	srcCfg := SessionConfig{
		Thinking:          &ThinkingConfig{Enabled: false, ReasoningEffort: "low"},
		KBIDs:             []string{"kb_demo"},
		KBIDsSet:          true,
		MaxRounds:         20,
		MaxMessages:       30,
		MaxThinkingRounds: 5,
	}
	if err := repo.UpdateSessionConfig(ctx, s.ID, srcCfg); err != nil {
		t.Fatalf("设置源会话配置: %v", err)
	}

	// 两轮对话（每轮 2 条）。
	if _, err := svc.Chat(ctx, 1, s.ID, "第一问"); err != nil {
		t.Fatalf("Chat1: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s.ID, "第二问"); err != nil {
		t.Fatalf("Chat2: %v", err)
	}
	msgs, _ := svc.ListMessages(ctx, 1, s.ID)
	if len(msgs) != 4 {
		t.Fatalf("应 4 条, got %d", len(msgs))
	}

	// 非本人分支 → NOT_FOUND。
	if _, err := svc.CreateBranch(ctx, 2, s.ID, msgs[1].ID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}

	// 在 round1 assistant（第 2 条）处分支 → 新会话复制 round1 的 2 条。
	branch, err := svc.CreateBranch(ctx, 1, s.ID, msgs[1].ID)
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if branch.ID == s.ID {
		t.Fatal("分支会话 ID 应不同")
	}
	if branch.Title != "源会话（分支）" {
		t.Fatalf("分支标题应为 '源会话（分支）', got %q", branch.Title)
	}
	// 分支应继承源会话配置快照（含管理员级字段），行为保持一致。
	if branch.Config.MaxRounds != 20 || branch.Config.MaxMessages != 30 || branch.Config.MaxThinkingRounds != 5 {
		t.Fatalf("分支应继承管理员级字段: %+v", branch.Config)
	}
	if branch.Config.Thinking == nil || branch.Config.Thinking.Enabled || branch.Config.Thinking.ReasoningEffort != "low" {
		t.Fatalf("分支应继承思考配置: %+v", branch.Config.Thinking)
	}
	if len(branch.Config.KBIDs) != 1 || branch.Config.KBIDs[0] != "kb_demo" || !branch.Config.KBIDsSet {
		t.Fatalf("分支应继承知识库绑定: %+v", branch.Config.KBIDs)
	}
	bmsgs, _ := svc.ListMessages(ctx, 1, branch.ID)
	if len(bmsgs) != 2 {
		t.Fatalf("分支应复制 2 条历史, got %d: %+v", len(bmsgs), bmsgs)
	}
	if bmsgs[0].Content != "第一问" || bmsgs[1].Content != "ok" {
		t.Fatalf("分支历史内容不符: %+v", bmsgs)
	}

	// 分支上继续对话：新轮次正确分配（3 轮 → 共 6 条）。
	if _, err := svc.Chat(ctx, 1, branch.ID, "分支新问题"); err != nil {
		t.Fatalf("分支 Chat: %v", err)
	}
	after, _ := svc.ListMessages(ctx, 1, branch.ID)
	if len(after) != 4 {
		t.Fatalf("分支继续对话后应 4 条, got %d: %+v", len(after), after)
	}
}

// ---------------------------------------------------------------------------
// 构造与故障注入
// ---------------------------------------------------------------------------

func TestNewService_Validation(t *testing.T) {
	reg, _ := DefaultToolSet()
	cases := []struct {
		name string
		cfg  Config
	}{
		{"nil repo", Config{Provider: &llm.MockProvider{}, Registry: reg, Log: zap.NewNop()}},
		{"nil provider", Config{Repo: newFakeRepo(), Registry: reg, Log: zap.NewNop()}},
		{"nil registry", Config{Repo: newFakeRepo(), Provider: &llm.MockProvider{}, Log: zap.NewNop()}},
		{"nil log", Config{Repo: newFakeRepo(), Provider: &llm.MockProvider{}, Registry: reg}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewService(c.cfg); err == nil {
				t.Fatal("应校验失败")
			}
		})
	}
}

func TestService_Chat_AppendFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.appendErr = errors.New("db down")
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "ok"})
	s, _ := svc.CreateSession(context.Background(), 1, "", "x")

	_, err := svc.Chat(context.Background(), 1, s.ID, "hi")
	if err == nil {
		t.Fatal("落库失败应返回错误")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeInternal {
		t.Fatalf("落库失败应 INTERNAL, got %s", code)
	}
}

// ---------------------------------------------------------------------------
// 会话配置（需求 10：工具权限 + 思考模式）
// ---------------------------------------------------------------------------

// TestService_UpdateSessionConfig 更新会话配置：合法白名单 + 思考配置全量落库。
func TestService_UpdateSessionConfig(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(ctx, 1, "", "配置")

	cfg := SessionConfig{
		EnabledTools: []string{"calculator", "get_current_time"},
		Thinking:     &ThinkingConfig{Enabled: false, ReasoningEffort: "high"},
	}
	got, err := svc.UpdateSessionConfig(ctx, 1, s.ID, cfg)
	if err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if len(got.Config.EnabledTools) != 2 ||
		got.Config.Thinking == nil ||
		got.Config.Thinking.Enabled {
		t.Fatalf("配置未按预期落库: %+v", got.Config)
	}

	// 非本人更新 → NOT_FOUND（属主校验）。
	if _, err := svc.UpdateSessionConfig(ctx, 2, s.ID, cfg); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("非本人应 NOT_FOUND, got %v", err)
	}
}

// TestService_UpdateSessionConfig_Invalid 非法配置被拒绝：
// 未注册工具名 / 非法 reasoning_effort。
func TestService_UpdateSessionConfig_Invalid(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	s, _ := svc.CreateSession(ctx, 1, "", "配置")

	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledTools: []string{"not_a_tool"},
	}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("未注册工具应 INVALID_ARGUMENT, got %v", err)
	}

	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		Thinking: &ThinkingConfig{Enabled: true, ReasoningEffort: "ultra"},
	}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 effort 应 INVALID_ARGUMENT, got %v", err)
	}
}

// TestService_ListTools 默认工具集含通用工具（echo / calculator / get_current_time）。
func TestService_ListTools(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	tools := svc.ListTools("")
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		if tl.Name == "" || tl.Description == "" {
			t.Fatalf("工具信息缺失: %+v", tl)
		}
		names[tl.Name] = true
	}
	for _, want := range []string{"echo", "calculator", "get_current_time"} {
		if !names[want] {
			t.Fatalf("默认工具集缺少 %q, got %v", want, names)
		}
	}
}

// TestService_Chat_EnabledToolsFilter 工具权限：会话只启用 calculator 时，
// 请求里工具集被过滤（不暴露 echo/get_current_time）。
func TestService_Chat_EnabledToolsFilter(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()

	var lastTools []schema.ToolSchema
	p := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			lastTools = req.Tools
			return &llm.Response{Content: "done"}, nil
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "过滤")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledTools: []string{"calculator"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}

	if _, err := svc.Chat(ctx, 1, s.ID, "只允许计算"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(lastTools) != 1 || lastTools[0].Name != "calculator" {
		t.Fatalf("工具集应按白名单过滤, got %+v", lastTools)
	}
}

// TestService_Chat_VisionCapabilityToggle 识图能力可配置化：勾选"识图"→
// describe_image 工具注入；未勾选（仅 search）→ 工具被过滤，模型无从调用。
// 与搜索能力同语义：enabled_resources 白名单翻译驱动 registryForConfig 过滤。
func TestService_Chat_VisionCapabilityToggle(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()

	var lastTools []schema.ToolSchema
	p := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			lastTools = req.Tools
			return &llm.Response{Content: "done"}, nil
		},
	}
	svc, _ := newTestService(repo, p)

	hasTool := func(name string) bool {
		for _, ts := range lastTools {
			if ts.Name == name {
				return true
			}
		}
		return false
	}

	// 勾选识图能力：describe_image 应注入工具集。
	s1, _ := svc.CreateSession(ctx, 1, "", "识图开启")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s1.ID, SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"vision"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s1.ID, "看下这张图"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !hasTool("describe_image") {
		t.Fatalf("勾选识图应注入 describe_image, got %+v", lastTools)
	}

	// 未勾选识图（仅 search）：describe_image 应被过滤。
	s2, _ := svc.CreateSession(ctx, 1, "", "识图关闭")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s2.ID, SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"search"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s2.ID, "搜索一下"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hasTool("describe_image") {
		t.Fatalf("未勾选识图应过滤 describe_image, got %+v", lastTools)
	}
}

// TestService_Chat_DocCapabilityToggle 文档解析能力可配置化：勾选"文档解析"→
// read_document 工具注入；未勾选 → 工具被过滤，模型无从调用（与识图/搜索同语义）。
func TestService_Chat_DocCapabilityToggle(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()

	var lastTools []schema.ToolSchema
	p := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			lastTools = req.Tools
			return &llm.Response{Content: "done"}, nil
		},
	}
	svc, _ := newTestService(repo, p)

	hasTool := func(name string) bool {
		for _, ts := range lastTools {
			if ts.Name == name {
				return true
			}
		}
		return false
	}

	// 勾选文档解析能力：read_document 应注入工具集。
	s1, _ := svc.CreateSession(ctx, 1, "", "文档解析开启")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s1.ID, SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"doc"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s1.ID, "总结下这份文档"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !hasTool("read_document") {
		t.Fatalf("勾选文档解析应注入 read_document, got %+v", lastTools)
	}

	// 未勾选（仅 search）：read_document 应被过滤。
	s2, _ := svc.CreateSession(ctx, 1, "", "文档解析关闭")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s2.ID, SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"search"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if _, err := svc.Chat(ctx, 1, s2.ID, "搜索一下"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hasTool("read_document") {
		t.Fatalf("未勾选文档解析应过滤 read_document, got %+v", lastTools)
	}
}

// TestService_Chat_ResourcesExplicitEmpty 显式"全不选"（set=true + 空资源）：
// 不启用任何能力/技能 → 会话装配空工具集（只保留基础对话），不能回退到全量。
func TestService_Chat_ResourcesExplicitEmpty(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()

	var lastTools []schema.ToolSchema
	p := &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			lastTools = req.Tools
			return &llm.Response{Content: "done"}, nil
		},
	}
	svc, _ := newTestService(repo, p)
	s, _ := svc.CreateSession(ctx, 1, "", "全不选")
	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResourcesSet: true,
		EnabledResources:    []string{},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}

	if _, err := svc.Chat(ctx, 1, s.ID, "你好"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(lastTools) != 0 {
		t.Fatalf("显式全不选应装配空工具集, got %+v", lastTools)
	}

	// 对照：未设置（set=false + 空）→ 全部启用 = DefaultToolSet 全量 +
	// describe_image + read_document + render_document + render_html（上传/生成
	// 链路工具绑定实例注册，不在 DefaultToolSet 内）。
	s2, _ := svc.CreateSession(ctx, 1, "", "默认")
	if _, err := svc.Chat(ctx, 1, s2.ID, "你好"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	reg, _ := DefaultToolSet()
	want := len(reg.Schemas()) + 4
	if len(lastTools) != want {
		t.Fatalf("未设置应全部启用, got %d 工具, want %d", len(lastTools), want)
	}
	hasVision := false
	for _, ts := range lastTools {
		if ts.Name == "describe_image" {
			hasVision = true
		}
	}
	if !hasVision {
		t.Fatalf("describe_image 视觉工具应默认启用")
	}
}

// ---------------------------------------------------------------------------
// mapRunError 错误映射（P2-xx：上游 4xx/429/401 不再误报为 503 UNAVAILABLE）
// ---------------------------------------------------------------------------

func TestMapRunError_UpstreamHTTP(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       []byte
		wantCode   apperr.ErrorCode
		wantMsgSub string // 要求出现在错误消息中的子串
	}{
		{
			name:       "400空响应体不再报 UNAVAILABLE",
			status:     400,
			body:       nil,
			wantCode:   apperr.CodeInvalidArgument,
			wantMsgSub: "HTTP 400",
		},
		{
			name:       "400携带 OpenAI error 详情",
			status:     400,
			body:       []byte(`{"error":{"message":"Model Not Exist","type":"invalid_request_error"}}`),
			wantCode:   apperr.CodeInvalidArgument,
			wantMsgSub: "Model Not Exist",
		},
		{
			name:       "400携带网关 message 包装",
			status:     400,
			body:       []byte(`{"code":40001,"message":"上游模型服务返回错误","request_id":"x"}`),
			wantCode:   apperr.CodeInvalidArgument,
			wantMsgSub: "上游模型服务返回错误",
		},
		{
			name:       "429限流映射为 RESOURCE_EXHAUSTED",
			status:     429,
			body:       []byte(`{"error":{"message":"rate limited"}}`),
			wantCode:   apperr.CodeResourceExhausted,
			wantMsgSub: "过于频繁",
		},
		{
			name:       "401映射为 PERMISSION_DENIED",
			status:     401,
			body:       []byte(`{"error":{"message":"invalid key"}}`),
			wantCode:   apperr.CodePermissionDenied,
			wantMsgSub: "拒绝访问",
		},
		{
			name:       "500保持 UNAVAILABLE（可重试）",
			status:     503,
			body:       []byte(`{"error":{"message":"upstream down"}}`),
			wantCode:   apperr.CodeUnavailable,
			wantMsgSub: "Agent 运行失败",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &llm.HTTPStatusError{Status: tc.status, Body: tc.body}
			got := mapRunError(err)
			if code := apperr.CodeOf(got); code != tc.wantCode {
				t.Fatalf("CodeOf = %s, want %s", code, tc.wantCode)
			}
			if !strings.Contains(got.Error(), tc.wantMsgSub) {
				t.Fatalf("错误消息不含 %q: %v", tc.wantMsgSub, got.Error())
			}
		})
	}
}

func TestMapRunError_NonHTTP(t *testing.T) {
	// 超时 → DEADLINE_EXCEEDED
	got := mapRunError(context.DeadlineExceeded)
	if code := apperr.CodeOf(got); code != apperr.CodeDeadlineExceeded {
		t.Fatalf("超时映射 CodeOf = %s, want %s", code, apperr.CodeDeadlineExceeded)
	}
	// 普通框架错误 → UNAVAILABLE（保留原因链）
	got = mapRunError(fmt.Errorf("llm: 构造请求失败: boom"))
	if code := apperr.CodeOf(got); code != apperr.CodeUnavailable {
		t.Fatalf("普通错误映射 CodeOf = %s, want %s", code, apperr.CodeUnavailable)
	}
	if !strings.Contains(got.Error(), "boom") {
		t.Fatalf("原因链丢失: %v", got.Error())
	}
	// 已封装的统一错误原样返回
	app := apperr.New(apperr.CodeInvalidArgument, "参数非法")
	if got := mapRunError(app); got != app {
		t.Fatalf("已封装错误应原样返回, got %v", got)
	}
	// nil 返回 nil
	if got := mapRunError(nil); got != nil {
		t.Fatalf("nil 应返回 nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 阶段2·智能体域与会话合并
// ---------------------------------------------------------------------------

// TestCreateSession_AgentIDValidation 创建会话时智能体域 ID 的格式校验。
func TestCreateSession_AgentIDValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(newFakeRepo(), &llm.MockProvider{})

	valid := []string{"", "tutor", "tutor-1", "A1", "x-9-y"}
	for _, id := range valid {
		s, err := svc.CreateSession(ctx, 1, id, "标题")
		if err != nil {
			t.Fatalf("合法 agent_id %q 应通过: %v", id, err)
		}
		if s.AgentID != id {
			t.Fatalf("会话 agent_id = %q, want %q", s.AgentID, id)
		}
	}

	invalid := []string{"bad/id", "中文", "has space", "a_b!", "" + "x\n"}
	for _, id := range invalid {
		if _, err := svc.CreateSession(ctx, 1, id, "标题"); err == nil {
			t.Fatalf("非法 agent_id %q 应被拒绝", id)
		}
	}
}

// TestListSessions_AgentScope 会话列表按智能体域隔离：
// 同一用户在不同智能体域下只看到各自域的会话。
func TestListSessions_AgentScope(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(newFakeRepo(), &llm.MockProvider{
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "ok", Usage: llm.Usage{TotalTokens: 1}}, nil
		},
	})

	// 同一用户 1 在三个域各建一个会话，并各发一条消息使其可被列出。
	tutor, _ := svc.CreateSession(ctx, 1, "tutor", "辅导")
	admin, _ := svc.CreateSession(ctx, 1, "", "管理")
	math, _ := svc.CreateSession(ctx, 1, "math", "数学")
	for _, s := range []*Session{tutor, admin, math} {
		if _, err := svc.Chat(ctx, 1, s.ID, "hi"); err != nil {
			t.Fatalf("Chat: %v", err)
		}
	}

	assertIDs := func(agentID string, want ...int64) {
		t.Helper()
		list, total, err := svc.ListSessions(ctx, 1, agentID, 1, 20)
		if err != nil {
			t.Fatalf("ListSessions(%q): %v", agentID, err)
		}
		got := map[int64]bool{}
		for _, s := range list {
			got[s.ID] = true
		}
		if int64(len(list)) != total || len(got) != len(want) {
			t.Fatalf("ListSessions(%q) 数量不符: got %d want %d", agentID, len(got), len(want))
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("ListSessions(%q) 缺少会话 %d", agentID, id)
			}
		}
	}

	assertIDs("tutor", tutor.ID)
	assertIDs("math", math.ID)
	assertIDs("", admin.ID)
	assertIDs("*", tutor.ID, admin.ID, math.ID)
}

// TestMergeGuestSessions 游客会话合并到真实账号。
func TestMergeGuestSessions(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})

	// 游客命名空间（负 user_id，由 auth.GuestUserID 派生）。
	const guestID = "550e8400-e29b-41d4-a716-446655440000"
	guestUID := auth.GuestUserID(guestID)
	const realUID = 7

	gs1, _ := svc.CreateSession(ctx, guestUID, "tutor", "游客会话1")
	gs2, _ := svc.CreateSession(ctx, guestUID, "", "游客会话2")
	owned, _ := svc.CreateSession(ctx, realUID, "tutor", "本人会话")

	// 1) 正常合并：两个游客会话归属转移，本人会话不受影响。
	n, err := svc.MergeGuestSessions(ctx, realUID, guestID)
	if err != nil {
		t.Fatalf("MergeGuestSessions: %v", err)
	}
	if n != 2 {
		t.Fatalf("迁移会话数 = %d, want 2", n)
	}
	for _, id := range []int64{gs1.ID, gs2.ID} {
		s, err := repo.GetSession(ctx, id)
		if err != nil || s.UserID != realUID {
			t.Fatalf("会话 %d 属主应为 %d, got %d err=%v", id, realUID, s.UserID, err)
		}
	}
	if s, _ := repo.GetSession(ctx, owned.ID); s.UserID != realUID {
		t.Fatalf("本人会话属主不应被改动, got %d", s.UserID)
	}

	// 2) 非法游客 ID → 参数错误。
	if _, err := svc.MergeGuestSessions(ctx, realUID, "非法ID!"); err == nil {
		t.Fatal("非法游客 ID 应被拒绝")
	}

	// 3) 游客身份不能作为合并目标。
	if _, err := svc.MergeGuestSessions(ctx, guestUID, guestID); err == nil {
		t.Fatal("游客身份调用合并应被拒绝")
	}
}
