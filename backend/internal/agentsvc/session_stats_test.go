// session_stats_test.go —— 管理端会话统计（数据管理模块）测试。
//
// 覆盖：service 层窗口参数校验与透传；gRPC 层身份校验、默认窗口、proto 转换。
// 使用 fakeRepo 注入统计结果（SQL 聚合逻辑属存储层，由集成测试覆盖）。
package agentsvc

import (
	"context"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

// TestAdminSessionStats_Validation service 层：窗口参数校验与透传。
func TestAdminSessionStats_Validation(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	// 窗口超界拒绝
	if _, err := svc.AdminSessionStats(context.Background(), 0); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("days=0 应拒绝, got %v", err)
	}
	if _, err := svc.AdminSessionStats(context.Background(), 91); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("days=91 应拒绝, got %v", err)
	}
	// 合法窗口透传
	repo.stats = &SessionStats{Total: 42}
	if _, err := svc.AdminSessionStats(context.Background(), 30); err != nil {
		t.Fatalf("days=30 应成功, got %v", err)
	}
	if repo.statsDays != 30 {
		t.Fatalf("days 应透传 30, got %d", repo.statsDays)
	}
}

// TestGrpc_AdminSessionStats gRPC 层：身份校验、缺省窗口、proto 转换。
func TestGrpc_AdminSessionStats(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	gs := NewGrpcServer(svc, zap.NewNop())

	// 无身份（防裸 gRPC 匿名直连）→ 拒绝
	if _, err := gs.AdminSessionStats(context.Background(), &agentv1.AdminSessionStatsRequest{Days: 7}); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Fatalf("无身份应拒绝, got %v", err)
	}

	// 带身份 + 缺省 days → 默认 30；proto 转换完整
	repo.stats = &SessionStats{
		Daily:   []SessionDayStat{{Date: "2026-08-12", Sessions: 3}},
		ByAgent: []SessionAgentStat{{AgentID: "tutor", Sessions: 3}},
		Total:   100,
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(metadataUserID, "1"))
	resp, err := gs.AdminSessionStats(ctx, &agentv1.AdminSessionStatsRequest{})
	if err != nil {
		t.Fatalf("AdminSessionStats: %v", err)
	}
	if repo.statsDays != 30 {
		t.Fatalf("缺省 days 应为 30, got %d", repo.statsDays)
	}
	if resp.TotalSessions != 100 || len(resp.Days) != 1 || resp.Days[0].Date != "2026-08-12" || resp.Days[0].Sessions != 3 {
		t.Fatalf("days 转换异常: %+v", resp)
	}
	if len(resp.Agents) != 1 || resp.Agents[0].AgentId != "tutor" || resp.Agents[0].Sessions != 3 {
		t.Fatalf("agents 转换异常: %+v", resp.Agents)
	}

	// nil 请求 → 拒绝
	if _, err := gs.AdminSessionStats(ctx, nil); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("nil 请求应拒绝, got %v", err)
	}
}
