package adminsvc

// 智能体默认会话配置读写（P2-AI 智能体管理模块）。
//
// 默认配置是"文件态配置平面"的一环：落盘到与 MCP 配置同目录的
// agent_defaults.json（<McpConfigFile 目录>/<agent_id>/agent_defaults.json），
// agent 每次新建会话实时读取（见 cmd/agent/domainview.go），保存即生效、
// 无需 fsnotify 热加载。
//
// REST 契约（鉴权由 gateway RequireAdmin 保证）：
//
//	GET /v1/admin/agents/{id}/defaults     读取默认配置 {defaults: {...}}（无文件 → 空对象）
//	PUT /v1/admin/agents/{id}/defaults     写入/清空默认配置（body 全空 = 删除文件）
//
// 写入语义与 agentsvc.AgentDefaults 对齐（复用同一 JSON 形状）：
//   - 只写 body 中显式出现的字段；缺省字段保持无默认；
//   - body 为空对象/空数组 = 清空该智能体的默认配置（删除文件）；
//   - thinking.reasoning_effort 仅接受 low/high/max（与 agentsvc.validateConfig 一致）。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/agentsvc"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// defaultsFileName 智能体默认会话配置文件名（与 agentsvc.DefaultsFileName 一致）。
// 两处变更必须同步——这是文件态配置平面的契约。
const defaultsFileName = agentsvc.DefaultsFileName

// defaultsFileFor 返回指定域的默认会话配置文件路径。
// agentID 须已通过 agentIDRe 白名单校验（防目录穿越）；空值视同默认域。
// 与 MCP 配置同目录：<McpConfigFile 目录>/<agentID>/agent_defaults.json
// （cmd/agent/domainview.go defaultsFileFor 与之一致）。
func (s *Service) defaultsFileFor(agentID string) string {
	return filepath.Join(filepath.Dir(s.mcp.For(agentID).file), defaultsFileName)
}

// handleAdminGetAgentDefaults GET /v1/admin/agents/{id}/defaults。
// 先经 auth-service 校验智能体存在与调用者权限；无默认配置文件 → 返回空对象（非 404）。
func (s *Service) handleAdminGetAgentDefaults(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	agentID, err := s.checkAgentAccess(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	def, err := s.readAgentDefaults(agentID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": def})
}

// handleAdminPutAgentDefaults PUT /v1/admin/agents/{id}/defaults。
// body 为 agentsvc.AgentDefaults JSON；空对象/空数组 = 删除默认配置文件（无默认）。
func (s *Service) handleAdminPutAgentDefaults(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	agentID, err := s.checkAgentAccess(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	def, err := decodeAgentDefaultsBody(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.writeAgentDefaults(agentID, def); err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin update agent defaults",
			zap.String("agent_id", agentID),
			zap.Bool("cleared", def.IsEmpty()))
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": def})
}

// checkAgentAccess 校验智能体存在与调用者权限，返回白名单校验后的 agentID。
// 白名单（agentIDRe）在文件路径拼接前强制执行，防目录穿越；
// 存在性与 scope 权限由 auth-service GetAgent 服务端校验（CanManageAgent）。
func (s *Service) checkAgentAccess(ctx context.Context, rawID string) (string, error) {
	if !agentIDRe.MatchString(rawID) {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）")
	}
	if _, err := s.auth.GetAgent(ctx, &authpb.GetAgentRequest{Id: rawID}); err != nil {
		return "", err
	}
	return rawID, nil
}

// decodeAgentDefaultsBody 解析默认配置请求体。
// 空 body / 空对象 / 空数组 → 零值（清空语义）；非法 JSON / 非法
// reasoning_effort → 明确错误（配置错误应暴露给操作者，不静默吞掉）。
func decodeAgentDefaultsBody(r *http.Request) (agentsvc.AgentDefaults, error) {
	var def agentsvc.AgentDefaults
	if err := decodeJSON(r, &def); err != nil {
		return def, err
	}
	if def.Thinking != nil {
		e := def.Thinking.ReasoningEffort
		if e != "" && e != "low" && e != "high" && e != "max" {
			return def, apperr.New(apperr.CodeInvalidArgument, "thinking.reasoning_effort 仅支持 low/high/max")
		}
	}
	if len(def.KBIDs) > 100 {
		return def, apperr.New(apperr.CodeInvalidArgument, "kb_ids 数量超出上限（≤100）")
	}
	// 管理员级字段范围校验（防配置错误导致会话装配异常/死循环）。
	if def.MaxRounds != 0 && def.MaxRounds < 1 {
		return def, apperr.New(apperr.CodeInvalidArgument, "max_rounds 必须 ≥ 1（0 = 不设置）")
	}
	if def.MaxRounds > 100 {
		return def, apperr.New(apperr.CodeInvalidArgument, "max_rounds 超出上限（≤100）")
	}
	if def.MaxMessages != 0 && def.MaxMessages < 2 {
		return def, apperr.New(apperr.CodeInvalidArgument, "max_messages 必须 ≥ 2（0 = 不设置）")
	}
	if def.MaxThinkingRounds != 0 && def.MaxThinkingRounds < 1 {
		return def, apperr.New(apperr.CodeInvalidArgument, "max_thinking_rounds 必须 ≥ 1（0 = 不设置）")
	}
	if def.MaxThinkingRounds > 100 {
		return def, apperr.New(apperr.CodeInvalidArgument, "max_thinking_rounds 超出上限（≤100）")
	}
	// mode：运行模式仅允许 single / orchestrate（空 = single）。
	if m := def.Mode; m != "" && m != "single" && m != "orchestrate" {
		return def, apperr.New(apperr.CodeInvalidArgument, "mode 仅支持 single 或 orchestrate")
	}
	// orchestrate_plan：编排方案仅允许 fixed / dynamic（空 = fixed）。
	if p := def.OrchestratePlan; p != "" && p != "fixed" && p != "dynamic" {
		return def, apperr.New(apperr.CodeInvalidArgument, "orchestrate_plan 仅支持 fixed 或 dynamic")
	}
	return def, nil
}

// readAgentDefaults 读取该域默认配置；缺文件/空内容 = 零值（无默认）。
// 文件损坏（非法 JSON）→ 明确错误（与 MCP 配置损坏同语义，需人工修复）。
func (s *Service) readAgentDefaults(agentID string) (agentsvc.AgentDefaults, error) {
	data, err := os.ReadFile(s.defaultsFileFor(agentID))
	if err != nil {
		if os.IsNotExist(err) {
			return agentsvc.AgentDefaults{}, nil
		}
		return agentsvc.AgentDefaults{}, apperr.Wrap(apperr.CodeInternal, "读取默认配置失败", err)
	}
	def, err := agentsvc.ParseDefaultsJSON(data)
	if err != nil {
		return agentsvc.AgentDefaults{}, apperr.Wrap(apperr.CodeInternal, "默认配置文件损坏: "+err.Error(), err)
	}
	return def, nil
}

// writeAgentDefaults 写入该域默认配置（原子写：临时文件 + rename）。
// 零值（全字段空）→ 删除配置文件（无默认）；目录不存在时自动创建。
func (s *Service) writeAgentDefaults(agentID string, def agentsvc.AgentDefaults) error {
	file := s.defaultsFileFor(agentID)
	if def.IsEmpty() {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return apperr.Wrap(apperr.CodeInternal, "删除默认配置失败", err)
		}
		return nil
	}
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "序列化默认配置失败", err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "创建默认配置目录失败", err)
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "写入默认配置失败", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return apperr.Wrap(apperr.CodeInternal, "落盘默认配置失败", err)
	}
	return nil
}
