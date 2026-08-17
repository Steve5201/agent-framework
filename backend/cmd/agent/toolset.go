// toolset.go —— 工具注册表构建（内置工具 + skill + MCP + kb_search）。
// 可被启动与热加载（reloader.go）复用：同一份构建逻辑保证两种路径行为一致。
package main

import (
	"context"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/Steve5201/agent-backend/internal/agentsvc"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/grpcx"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"github.com/Steve5201/agent-backend/internal/tools/kb"
	"github.com/Steve5201/agent-backend/internal/tools/mcp"
	"github.com/Steve5201/agent-backend/internal/tools/skill"
	agenttool "github.com/Steve5201/agent-framework/tool"
	"github.com/jackc/pgx/v5/pgxpool"
)

// agentSkillsDir 返回本智能体实例的技能根目录（<SkillsDir>/<AgentID>）。
// 多租户：管理端按 <root>/<agent_id>/<name> 落盘（adminsvc.SkillStore.For），
// 本实例只装配自己资源域下的技能。
func agentSkillsDir(cfg *config.Config) string {
	root := cfg.Agent.SkillsDir
	if root == "" {
		root = "skills" // 与 adminsvc 默认一致（本地 go run 均在 backend/ 下）
	}
	return filepath.Join(root, cfg.Agent.AgentID)
}

// agentMcpFile 返回本智能体实例的 MCP 配置文件
// （<McpConfigFile 目录>/<AgentID>/<文件名>）。
// 与 adminsvc.McpStore.For 的落盘规则保持一致：管理端写哪、agent 读哪。
func agentMcpFile(cfg *config.Config) string {
	file := cfg.Agent.McpConfigFile
	if file == "" {
		file = "mcp_servers.json"
	}
	return filepath.Join(filepath.Dir(file), cfg.Agent.AgentID, filepath.Base(file))
}

// buildToolRegistry 构建完整工具注册表（内置工具 + skill + MCP），
// 返回注册表与释放函数（关闭 MCP 子进程/连接）。热加载时重复调用。
//
// pool 用于装配保护区磁盘配额执行器（模块三：file_ops 写 protected/ 前校验）；
// 可为 nil（本地/无 DB 场景降级为不校验）。
func buildToolRegistry(cfg *config.Config, pool *pgxpool.Pool, log *zap.Logger) (*agenttool.Registry, func(), error) {
	skillsDir := agentSkillsDir(cfg)
	extraProviders := []tools.ToolProvider{
		// 技能：扫描本智能体技能目录（<AGENT_SKILLS_DIR>/<AGENT_ID>），
		// 目录不存在 = 零技能。
		skill.NewProvider(skillsDir, log),
	}

	// 保护区磁盘配额执行器（模块三）：用户工作区 protected/ 是唯一永不清除的
	// 空间，须按角色默认 + 单用户覆盖校验软上限；临时/散落内容由清理器 TTL 回收。
	var diskQuota builtin.CheckDiskQuota
	if pool != nil {
		store := agentsvc.NewDiskQuotaStore(pool)
		enforcer := agentsvc.NewDiskQuotaEnforcer(store, agentsvc.RoleDiskQuota{
			User:       cfg.Agent.DiskQuotaUserMB,
			Admin:      cfg.Agent.DiskQuotaAdminMB,
			AgentAdmin: cfg.Agent.DiskQuotaAgentAdminMB,
			SuperAdmin: cfg.Agent.DiskQuotaSuperAdminMB,
		}, log)
		diskQuota = enforcer.Check
		log.Info("保护区磁盘配额已装配",
			zap.Int64("user_mb", cfg.Agent.DiskQuotaUserMB),
			zap.Int64("admin_mb", cfg.Agent.DiskQuotaAdminMB),
			zap.Int64("agent_admin_mb", cfg.Agent.DiskQuotaAgentAdminMB),
			zap.Int64("super_admin_mb", cfg.Agent.DiskQuotaSuperAdminMB))
	}

	// 知识库检索（P3-A6）：AGENT_RAG_ADDR 非空才装配 kb_search 工具。
	// rag 是软依赖——拨号失败仅降级（不注册 kb 工具），不影响其它工具与对话。
	var ragConn *grpc.ClientConn
	if addr := cfg.Agent.RagAddr; addr != "" {
		conn, err := grpcx.Dial(context.Background(), addr)
		if err != nil {
			log.Warn("rag-service 连接失败，kb_search 工具不可用（不影响其它功能）",
				zap.String("rag_addr", addr), zap.Error(err))
		} else {
			ragConn = conn
			extraProviders = append(extraProviders,
				kb.NewProvider(ragv1.NewRagServiceClient(conn), cfg.Agent.AgentID, log))
			log.Info("kb_search 工具已装配",
				zap.String("agent_id", cfg.Agent.AgentID), zap.String("rag_addr", addr))
		}
	}

	mcpProvider, mcpCfgs, err := loadMcpProvider(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	if mcpProvider != nil {
		extraProviders = append(extraProviders, mcpProvider)
	}

	reg, err := agentsvc.DefaultToolSet(
		agentsvc.WithWebSearchBackend(cfg.Agent.WebSearchBackend),
		agentsvc.WithCodeExecAllowlist(cfg.Agent.CodeExecAllowlist),
		agentsvc.WithSandboxURL(cfg.Agent.SandboxURL),
		agentsvc.WithSkillsRoot(skillsDir), // 与 skill.NewProvider 同源：@skills/ 只读资源
		agentsvc.WithDiskQuota(diskQuota),  // 保护区磁盘配额（模块三；nil = 不校验）
		agentsvc.WithProviders(extraProviders...),
	)
	if err != nil {
		if mcpProvider != nil {
			_ = mcpProvider.Close()
		}
		if ragConn != nil {
			_ = ragConn.Close()
		}
		return nil, nil, err
	}

	closeFn := func() {
		if ragConn != nil {
			_ = ragConn.Close()
		}
		if mcpProvider != nil {
			_ = mcpProvider.Close()
		}
	}
	if len(mcpCfgs) > 0 {
		log.Info("mcp servers enabled",
			zap.Int("server_count", len(mcpCfgs)),
			zap.Strings("servers", mcpNames(mcpCfgs)))
	}
	return reg, closeFn, nil
}

// loadMcpProvider 按优先级加载 MCP server 配置（多租户：按 <AgentID> 分文件）：
//   - 本域配置文件（<AGENT_MCP_CONFIG_FILE 目录>/<AGENT_ID>/<文件名>，管理端写入）
//     存在 → 用文件（含空数组）；
//   - 文件缺失/损坏 → 回退环境变量 JSON（AGENT_MCP_SERVERS_JSON，老配置方式）；
//   - 都没有 → 返回 nil（不启用 MCP）。
//
// 返回 provider（nil = 不启用）、生效的配置列表、错误。
func loadMcpProvider(cfg *config.Config, log *zap.Logger) (*mcp.Provider, []mcp.ServerConfig, error) {
	var cfgs []mcp.ServerConfig

	mcpFile := agentMcpFile(cfg)
	if data, err := os.ReadFile(mcpFile); err == nil {
		if parsed, perr := mcp.ParseServersJSON(data); perr != nil {
			// 配置文件损坏：记 ERROR 并回退环境变量，保证 agent 不因管理端误写而宕机。
			log.Error("MCP 配置文件解析失败，回退 AGENT_MCP_SERVERS_JSON",
				zap.String("file", mcpFile), zap.Error(perr))
		} else {
			cfgs = parsed
		}
	} // 文件不存在 → 回退
	if cfgs == nil && cfg.Agent.McpServersJSON != "" {
		parsed, err := mcp.ParseServersJSON([]byte(cfg.Agent.McpServersJSON))
		if err != nil {
			return nil, nil, err
		}
		cfgs = parsed
	}
	if len(cfgs) == 0 {
		return nil, nil, nil
	}
	// 过滤已禁用的 server（管理端 enabled=false），工具不注册。
	var enabled []mcp.ServerConfig
	for _, c := range cfgs {
		if c.IsEnabled() {
			enabled = append(enabled, c)
		} else {
			log.Info("mcp server disabled，跳过", zap.String("server", c.Name))
		}
	}
	if len(enabled) == 0 {
		return nil, nil, nil
	}
	return mcp.NewProvider(enabled, log), enabled, nil
}

func mcpNames(cfgs []mcp.ServerConfig) []string {
	names := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		names = append(names, c.Name)
	}
	return names
}
