package agentsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/orchestrate"
	"github.com/Steve5201/agent-framework/schema"
)

// TestPersistOrchestrationRun 验证编排过程输出入库的转换逻辑：
// 状态聚合（任一非 completed → partial）、任务字段映射、Content 截断。
func TestPersistOrchestrationRun(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	sess := &Session{ID: 7, AgentID: "default"}

	longContent := strings.Repeat("正文内容。", orchestrationTaskContentCap/5+50)
	res := &orchestrate.RunResult{
		Goal:  "围绕用户目标「测试」生成大纲",
		Final: "最终汇总结果",
		Tasks: []orchestrate.TaskResult{
			{TaskID: "research", Role: "research", Status: orchestrate.TaskCompleted, Content: "要点摘要", Usage: llm.Usage{TotalTokens: 100}, Duration: 1.5},
			{TaskID: "outline", Role: "outline", Status: orchestrate.TaskFailed, Error: "上游 400: UnsupportedParamsError", Usage: llm.Usage{TotalTokens: 50}, Duration: 2.0},
			{TaskID: "content", Role: "content", Status: orchestrate.TaskSkipped, Content: "上游任务失败，本任务被跳过", Duration: 0},
			{TaskID: "review", Role: "review", Status: orchestrate.TaskCompleted, Content: longContent, Usage: llm.Usage{TotalTokens: 300}, Duration: 3.25},
		},
		TotalUsage: llm.Usage{TotalTokens: 450},
	}

	svc.persistOrchestrationRun(context.Background(), sess, 11, res, "最终汇总结果", 3)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.runs) != 1 {
		t.Fatalf("期望 1 条编排入库记录，实际 %d", len(repo.runs))
	}
	run := repo.runs[0]
	if run.SessionID != 7 || run.UserID != 11 {
		t.Errorf("会话/用户标识不符: %+v", run)
	}
	if run.Status != "partial" {
		t.Errorf("存在 failed/skipped 任务，status 应为 partial，实际 %q", run.Status)
	}
	if run.Result != "最终汇总结果" {
		t.Errorf("Result 未透传: %q", run.Result)
	}
	if len(run.Tasks) != 4 {
		t.Fatalf("期望 4 条任务记录，实际 %d", len(run.Tasks))
	}
	// 任务字段映射抽查：failed 任务的 Error 保留。
	if run.Tasks[1].Role != "outline" || run.Tasks[1].Status != "failed" ||
		run.Tasks[1].Error == "" || run.Tasks[1].Tokens != 50 {
		t.Errorf("failed 任务映射不符: %+v", run.Tasks[1])
	}
	// 长正文截断：入库存量 <= 上限 + 截断后缀。
	if len(run.Tasks[3].Output) > orchestrationTaskContentCap+len(orchestrationTaskContentSuffix) ||
		!strings.HasSuffix(run.Tasks[3].Output, orchestrationTaskContentSuffix) {
		t.Errorf("长正文未截断: 长度=%d", len(run.Tasks[3].Output))
	}
	if run.TotalTokens != 450 {
		t.Errorf("累计 token 应为 450，实际 %d", run.TotalTokens)
	}
}

// TestStripExternalTools 验证子代理工具集裁剪：External=true（local_shell）
// 被移除（编排内无桌面确认通道），其余服务器工具保留。
func TestStripExternalTools(t *testing.T) {
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet 失败: %v", err)
	}
	// 前置：默认注册表确实含 local_shell 且 External=true。
	ls, err := reg.Get("local_shell")
	if err != nil {
		t.Fatalf("默认工具集应含 local_shell: %v", err)
	}
	if !ls.Schema().External {
		t.Fatal("local_shell 应为 External=true")
	}

	stripped := stripExternalTools(reg)
	if _, err := stripped.Get("local_shell"); err == nil {
		t.Fatal("裁剪后不应保留 local_shell")
	}
	for _, name := range []string{"calculator", "web_search", "code_executor", "file_ops", "get_current_time"} {
		if _, err := stripped.Get(name); err != nil {
			t.Errorf("裁剪后应保留 %s: %v", name, err)
		}
	}
	// nil 注册表安全返回。
	if stripExternalTools(nil) != nil {
		t.Fatal("nil 注册表应返回 nil")
	}
}

// TestFilterSubtaskTools 验证子任务工具"能力继承"（P4-N 黑名单模式）：
// 保留全部会话能力（calculator/get_current_time/web_search/file_ops/
// kb_search/code_executor）与文档生成 render_document（docgen 是编排一等能力，
// P4-M 修正后不再裁掉），只剔除对子任务无用的 echo（调试）与
// describe_image/read_document（消费用户上传物，子任务输入为文本成果）。
func TestFilterSubtaskTools(t *testing.T) {
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet 失败: %v", err)
	}
	// render_document 是实例绑定工具（DefaultToolSet 不注册），补注册一个
	// stub 验证黑名单不会裁掉它（docgen 能力在编排内可用）。
	if err := reg.Register(stubDocTool{}); err != nil {
		t.Fatalf("注册 stub render_document 失败: %v", err)
	}
	// 补注册一个消费上传物的工具，验证黑名单命中剔除。
	if err := reg.Register(stubUploadTool{}); err != nil {
		t.Fatalf("注册 stub 上传工具失败: %v", err)
	}

	filtered := filterSubtaskTools(reg)
	for _, name := range []string{"calculator", "get_current_time", "web_search", "file_ops", "code_executor", "render_document"} {
		if _, err := filtered.Get(name); err != nil {
			t.Errorf("能力继承：应保留 %s: %v", name, err)
		}
	}
	for _, name := range []string{"echo", "read_document", "local_shell"} {
		if _, err := filtered.Get(name); err == nil {
			t.Errorf("无用/调试/外部工具应被剔除 %s", name)
		}
	}
	// nil 注册表安全返回。
	if filterSubtaskTools(nil) != nil {
		t.Fatal("nil 注册表应返回 nil")
	}
}

// stubDocTool 测试用 render_document 存根（验证黑名单保留 docgen 工具）。
type stubDocTool struct{}

func (stubDocTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{Name: "render_document", Description: "stub"}
}

func (stubDocTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

// stubUploadTool 测试用 read_document 存根（验证黑名单剔除上传消费工具）。
type stubUploadTool struct{}

func (stubUploadTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{Name: "read_document", Description: "stub"}
}

func (stubUploadTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

// TestBuildOrchestrationSummary 验证编排过程摘要（system 消息，P4-M）：
// 子任务输出 output 按 rune 截断入库（供历史回看）、失败 error 截断保留、
// 空任务列表返回空串。system 消息不进入模型上下文（loadHistory 过滤）。
func TestBuildOrchestrationSummary(t *testing.T) {
	longOut := strings.Repeat("汉字", orchestrationOutputCap+20)
	res := &orchestrate.RunResult{
		Tasks: []orchestrate.TaskResult{
			{TaskID: "content", Role: "content", Status: orchestrate.TaskCompleted, Content: longOut},
			{TaskID: "review", Role: "review", Status: orchestrate.TaskFailed, Content: "", Error: "执行失败"},
		},
	}
	raw := buildOrchestrationSummary(res)
	if !strings.HasPrefix(raw, orchestrationSummaryMarker) {
		t.Fatalf("摘要应以标记开头: %q", raw)
	}
	var parsed struct {
		Tasks []struct {
			ID     string `json:"id"`
			Output string `json:"output"`
			Error  string `json:"error"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(raw[len(orchestrationSummaryMarker):]), &parsed); err != nil {
		t.Fatalf("摘要 JSON 解析失败: %v", err)
	}
	if len(parsed.Tasks) != 2 {
		t.Fatalf("任务数应为 2，实际 %d", len(parsed.Tasks))
	}
	// output 截断到上限 + 省略号（rune 安全，中文不乱码）。
	if !strings.HasSuffix(parsed.Tasks[0].Output, "…") || len([]rune(parsed.Tasks[0].Output)) > orchestrationOutputCap+1 {
		t.Errorf("output 应截断到 %d runes: 长度=%d", orchestrationOutputCap, len([]rune(parsed.Tasks[0].Output)))
	}
	if parsed.Tasks[1].Error != "执行失败" {
		t.Errorf("error 应保留原样，实际 %q", parsed.Tasks[1].Error)
	}
	// 空任务列表返回空串。
	if buildOrchestrationSummary(&orchestrate.RunResult{}) != "" {
		t.Error("空任务列表应返回空串")
	}
}

// TestClassifySubTaskError 验证失败原因分类前缀：
// "未收敛"与"连接/上游故障"两类主因可一眼区分。
func TestClassifySubTaskError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"未收敛", fmt.Errorf("agent: 达到最大轮数，对话未收敛: %w", agent.ErrMaxRounds), "生成未收敛（达到最大轮数）"},
		{"思考轮超限", fmt.Errorf("agent: 思考（工具调用）轮次已达上限: %w", agent.ErrMaxThinkingRounds), "思考（工具调用）轮次超限"},
		{"上游504", &llm.HTTPStatusError{Status: 504}, "上游服务超时（HTTP 504）"},
		{"上游502", &llm.HTTPStatusError{Status: 502}, "上游服务超时（HTTP 502）"},
		{"上游429", &llm.HTTPStatusError{Status: 429}, "上游限流（HTTP 429）"},
		{"上游500", &llm.HTTPStatusError{Status: 500}, "上游服务故障（HTTP 500）"},
		{"上游400", &llm.HTTPStatusError{Status: 400}, "上游请求失败（HTTP 400）"},
		{"上下文超时", context.DeadlineExceeded, "执行超时"},
		{"网络错误", &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, "网络连接失败"},
		{"兜底", nil, "执行失败"},
	}
	for _, c := range cases {
		if got := classifySubTaskError(c.err); got != c.want {
			t.Errorf("%s: 期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// TestPersistOrchestrationRun_AllCompleted 全成功时 status 为 completed。
func TestPersistOrchestrationRun_AllCompleted(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	res := &orchestrate.RunResult{
		Goal:  "测试",
		Final: "ok",
		Tasks: []orchestrate.TaskResult{
			{TaskID: "research", Role: "research", Status: orchestrate.TaskCompleted, Content: "a"},
			{TaskID: "outline", Role: "outline", Status: orchestrate.TaskCompleted, Content: "b"},
		},
	}
	svc.persistOrchestrationRun(context.Background(), &Session{ID: 1}, 2, res, "ok", 1)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.runs) != 1 || repo.runs[0].Status != "completed" {
		t.Fatalf("全成功 status 应为 completed，实际 %+v", repo.runs)
	}
	if len(repo.runs[0].Tasks) != 2 {
		t.Errorf("任务数不符: %+v", repo.runs[0].Tasks)
	}
}

// TestPersistOrchestrationRun_Empty 空任务列表时也落库（run 级记录存在）。
func TestPersistOrchestrationRun_Empty(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	res := &orchestrate.RunResult{Goal: "测试", Final: "ok"}
	svc.persistOrchestrationRun(context.Background(), &Session{ID: 1}, 2, res, "ok", 1)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.runs) != 1 {
		t.Fatalf("期望 1 条入库记录，实际 %d", len(repo.runs))
	}
	if len(repo.runs[0].Tasks) != 0 {
		t.Errorf("空任务列表应落库为空数组: %+v", repo.runs[0].Tasks)
	}
}

// TestStreamOrchestrate_FallbackNoPlan 动态编排遇"无需编排"（P4-N）：
// planner 对简单问题回自然语言 → 回退单 Agent 直接应答（会话工具集在），
// 不再报"计划失败"、不渲染编排块，直接给回答并落库 user/assistant。
func TestStreamOrchestrate_FallbackNoPlan(t *testing.T) {
	repo := newFakeRepo()
	repo.sessions[1] = &Session{
		ID: 1, UserID: 1, Title: "测试会话", Status: SessionStatusActive,
		Config: SessionConfig{Mode: "orchestrate", OrchestratePlan: "dynamic", Model: "test-model"},
	}
	streamCalls := 0
	p := &llm.MockProvider{
		// 动态 planner 的唯一一次非流式调用：模型判定无需编排，回了自然语言。
		ChatFn: func(_ *llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "这个简单，我能看到文档生成工具。"}, nil
		},
		// 回退后的直接应答走流式（会话工具集在，含 render_document）。
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			streamCalls++
			return llm.NewSliceStream([]llm.StreamEvent{{Content: "能，我有 render_document 工具。"}}), nil
		},
	}
	svc, err := newTestService(repo, p)
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	obs := &agent.StreamObserver{OnContent: func(string) {}}
	res, err := svc.streamOrchestrate(context.Background(), repo.sessions[1], 1, "你的文档生成工具你能看到吗？", obs)
	if err != nil {
		t.Fatalf("streamOrchestrate err: %v", err)
	}
	if res.Content != "能，我有 render_document 工具。" {
		t.Errorf("回退直接应答内容 = %q", res.Content)
	}
	if streamCalls == 0 {
		t.Error("直接应答应走流式 ChatStream")
	}
	// 落库：user + assistant 两条（无编排摘要 system 消息）。
	msgs, err := repo.ListMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("应落库 user+assistant 共 2 条，实际 %d", len(msgs))
	}
	if msgs[0].Role != string(schema.RoleUser) || msgs[1].Role != string(schema.RoleAssistant) {
		t.Errorf("落库角色异常: %s / %s", msgs[0].Role, msgs[1].Role)
	}
}

// TestBuildOrchestrator_PlanModes 验证固定/动态两种规划模式装配（P4-J）：
//   - fixed（默认）：固定教研模板，4 个子任务直接执行 + 1 次 aggregator 最终
//     汇总 = 5 次 LLM 调用，无 planning 调用；
//   - dynamic：LLMPlanner 动态分解，先产生一次 planning 调用；mock 输出
//     非 JSON 时判定"无需编排"（ErrNoPlan），回退空计划不报错（P4-N）。
func TestBuildOrchestrator_PlanModes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		plan      string
		wantCalls int
		wantErr   bool
	}{
		{"fixed 固定教研模板", "fixed", 5, false},
		{"dynamic 动态分解（mock 非 JSON → 判定无需编排，回退不报错）", "dynamic", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			calls := 0
			p := &llm.MockProvider{
				// 子任务已流式执行（ChatStream），planner/聚合器仍非流式（Chat）。
				// 两条链路都要计数，才能反映真实调用次数。
				ChatFn: func(_ *llm.Request) (*llm.Response, error) {
					calls++
					return &llm.Response{Content: "完成"}, nil
				},
				ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
					calls++
					return llm.NewSliceStream([]llm.StreamEvent{{Content: "完成"}}), nil
				},
			}
			svc, err := newTestService(repo, p)
			if err != nil {
				t.Fatalf("newTestService 失败: %v", err)
			}
			sess := &Session{ID: 1, UserID: 1, Config: SessionConfig{OrchestratePlan: tc.plan}}
			o := svc.buildOrchestrator(sess, nil)
			runCtx := llm.WithHeader(ctx, "X-User-Id", "1")
			_, err = o.Run(runCtx, "围绕「教学」设计教案")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Errorf("LLM 调用次数 = %d, want %d（固定模板 4 次子任务 + 1 次汇总；动态多 1 次 planning）", calls, tc.wantCalls)
			}
		})
	}
}

// TestRunSubTask_ToolsInjected 编排子任务工具注入（P4-J 子任务工具化）：
// 子任务按会话配置装配工具（calculator），模型请求工具调用 → 工具执行 →
// 结果回填并完成；工具调用写入审计（归属当前会话）。
func TestRunSubTask_ToolsInjected(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	step := 0
	p := &llm.MockProvider{
		// 子任务已统一流式执行（RunStreamWithObserver → ChatStream）。
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			step++
			if step == 1 {
				return llm.NewSliceStream([]llm.StreamEvent{
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "tc1", Name: "calculator", Arguments: `{"a":1,"b":1,"op":"+"}`}}},
				}), nil
			}
			return llm.NewSliceStream([]llm.StreamEvent{{Content: "结果是 2"}}), nil
		},
	}
	svc, err := newTestService(repo, p)
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	svc.autoApproveTools = true // 工具审批本地放行（approveToolCall），与主会话一致

	sess := &Session{
		ID: 9, UserID: 11, AgentID: "default",
		Config: SessionConfig{EnabledTools: []string{"calculator"}},
	}
	task := orchestrate.TaskSpec{ID: "calc", Role: "worker", Goal: "计算 1+1"}

	res, err := svc.runSubTask(ctx, sess, task, "", nil)
	if err != nil {
		t.Fatalf("runSubTask: %v", err)
	}
	if res.Status != orchestrate.TaskCompleted {
		t.Fatalf("子任务应完成，实际 %+v", res)
	}
	if !strings.Contains(res.Content, "2") {
		t.Errorf("子任务结果应包含工具输出，实际 %q", res.Content)
	}

	// 工具调用审计归属当前会话（session 9）。
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var found bool
	for _, a := range repo.audits {
		if a.Tool == "calculator" && a.SessionID == 9 && a.UserID == 11 {
			found = true
		}
	}
	if !found {
		t.Error("子任务工具调用未写入审计（audit_tool_calls）")
	}
}

// TestRunSubTask_ModelFollowsSession 子任务模型跟随会话所选模型。
func TestRunSubTask_ModelFollowsSession(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	var lastReqModel string
	p := &llm.MockProvider{
		ChatStreamFn: func(req *llm.Request) (llm.Stream, error) {
			lastReqModel = req.Model
			return llm.NewSliceStream([]llm.StreamEvent{{Content: "ok"}}), nil
		},
	}
	svc, err := newTestService(repo, p)
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	sess := &Session{ID: 1, UserID: 1, Config: SessionConfig{Model: "session-model"}}
	_, err = svc.runSubTask(ctx, sess, orchestrate.TaskSpec{ID: "t", Role: "worker", Goal: "任务"}, "", nil)
	if err != nil {
		t.Fatalf("runSubTask: %v", err)
	}
	if lastReqModel != "session-model" {
		t.Errorf("子任务应使用会话模型，实际 %q", lastReqModel)
	}
}

// TestRunSubTask_ObserverKind 子任务过程事件 kind 透传（P4-N）：
// subObs 收到的思考/工具/正文增量按 kind 区分下发（reasoning / tool_start /
// tool_end / text），前端按 TaskID 分流渲染；验证 runner→runSubTask→framework
// 的回调链路完整（缺失 OnReasoning 挂接会静默丢思考事件，即"输出停止"根因）。
func TestRunSubTask_ObserverKind(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	step := 0
	p := &llm.MockProvider{
		ChatStreamFn: func(_ *llm.Request) (llm.Stream, error) {
			step++
			if step == 1 {
				// 第一轮流：思考增量 + 工具调用（模型先思考再决定调工具）。
				return llm.NewSliceStream([]llm.StreamEvent{
					{Reasoning: "先分析输入"},
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "tc1", Name: "calculator", Arguments: `{"a":1,"b":1,"op":"+"}`}}},
				}), nil
			}
			return llm.NewSliceStream([]llm.StreamEvent{{Content: "结果是 2"}}), nil
		},
	}
	svc, err := newTestService(repo, p)
	if err != nil {
		t.Fatalf("newTestService 失败: %v", err)
	}
	svc.autoApproveTools = true

	sess := &Session{
		ID: 9, UserID: 11, AgentID: "default",
		Config: SessionConfig{EnabledTools: []string{"calculator"}},
	}
	task := orchestrate.TaskSpec{ID: "calc", Role: "worker", Goal: "计算 1+1"}

	// subObs 收集事件，模拟 runner 中的透传闭包（kind 由 backend 侧映射）。
	var got []agent.TaskStatusEvent
	subObs := &agent.StreamObserver{
		OnContent:    func(delta string) { got = append(got, agent.TaskStatusEvent{Kind: "text", Content: delta}) },
		OnReasoning:  func(delta string) { got = append(got, agent.TaskStatusEvent{Kind: "reasoning", Content: delta}) },
		OnToolCall:   func(call schema.ToolCall) { got = append(got, agent.TaskStatusEvent{Kind: "tool_start", Content: call.Name}) },
		OnToolResult: func(call schema.ToolCall, _ *schema.ToolResult, _ error) { got = append(got, agent.TaskStatusEvent{Kind: "tool_end", Content: call.Name}) },
	}
	res, err := svc.runSubTask(ctx, sess, task, "", subObs)
	if err != nil {
		t.Fatalf("runSubTask: %v", err)
	}
	if res.Status != orchestrate.TaskCompleted {
		t.Fatalf("子任务应完成，实际 %+v", res)
	}

	// 事件序列：reasoning → tool_start → tool_end → text（最终回答）。
	kinds := make([]string, 0, len(got))
	for _, ev := range got {
		kinds = append(kinds, ev.Kind)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"reasoning", "tool_start", "tool_end", "text"} {
		if !strings.Contains(joined, want) {
			t.Errorf("事件序列 %q 缺少 kind=%q（思考/工具增量丢失）", joined, want)
		}
	}
	// 工具事件须携带工具名（前端渲染"正在调用 xxx"）。
	for _, ev := range got {
		if (ev.Kind == "tool_start" || ev.Kind == "tool_end") && ev.Content != "calculator" {
			t.Errorf("%s 事件应携带工具名 calculator，实际 %q", ev.Kind, ev.Content)
		}
	}
}

// TestSubtaskTimeout 子任务超时解析（P4-L 角色级差异化）：
//   - 内置角色池超时符合设计（content 最长 1800s，research/outline/review/worker 各异）；
//   - 角色级非零时覆盖全局值（content 给满、research 收紧）；
//   - 角色级为 0 时跟随全局；双 0 表示不限制。
func TestSubtaskTimeout(t *testing.T) {
	want := map[string]int{"research": 1200, "outline": 1500, "content": 1800, "review": 1200, "worker": 1200}
	for role, sec := range want {
		if got := orchestrationRoles[role].TimeoutSec; got != sec {
			t.Errorf("角色 %s TimeoutSec = %d, want %d", role, got, sec)
		}
	}
	// 角色级非零覆盖全局值：长角色（content）给满、短角色（research）收紧。
	if got := subtaskTimeout(30*time.Minute, orchestrationRoles["content"]); got != 30*time.Minute {
		t.Errorf("content 角色超时应为 30min，实际 %v", got)
	}
	if got := subtaskTimeout(30*time.Minute, orchestrationRoles["research"]); got != 20*time.Minute {
		t.Errorf("research 角色超时应收紧为 20min，实际 %v", got)
	}
	// 角色级为 0：跟随全局。
	if got := subtaskTimeout(10*time.Minute, roleSpec{}); got != 10*time.Minute {
		t.Errorf("角色级为 0 应跟随全局 10min，实际 %v", got)
	}
	// 全局 0 + 角色 0：不限制。
	if got := subtaskTimeout(0, roleSpec{}); got != 0 {
		t.Errorf("双 0 应不限制，实际 %v", got)
	}
}
