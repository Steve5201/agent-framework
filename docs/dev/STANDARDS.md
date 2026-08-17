# 开发标准（Development Standards）

> 本文件定义智能体框架 monorepo 的**通用开发规范**，重点覆盖管理端（admin panel）
> 及其"文件态配置平面"。新增模块/接口/配置前**先读本文件**，保证只增不改、不破坏既有契约。

---

## 1. Monorepo 结构

```
d:\Agent
├── backend/     Go 微服务群（go.work 联合 framework 模块）
│   └── internal/{authsvc,agentsvc,gatewaysvc,adminsvc,tools,...}
├── framework/   可复用 Agent 框架（Go 库，工具注册表/工具调用/权限）
├── web/         React + TypeScript + Vite（前端，端口 3000）
├── desktop/     Tauri 2 桌面端
├── deploy/      docker-compose / Dockerfile / .env.example
└── docs/        文档（api / dev / ops）
```

端口约定：**后端 8080（gateway 唯一入口）**、前端 3000、desktop 1420。

## 2. 通用约束

1. **密钥一律走环境变量**（`os.Getenv("DEEPSEEK_API_KEY")` 等），代码中严禁硬编码密钥。
2. **API 优先**：接口契约先定（`docs/api/*.md`），前后端按契约联调。
3. **错误统一**：全部走 `internal/errors`（`apperr`）——`code/message/request_id` 结构，
   禁止裸 `fmt.Errorf` 泄漏给客户端。
4. **日志统一**：`go.uber.org/zap`。业务事件（创建/删除）记 Info + 关键字段。
5. **输入校验在边界**：REST handler 层做校验；存储层也防御（如 `dirPath` 正则防穿越）。
6. 新增代码**必须带单元测试**；破坏性变更需先与网关路由/gRPC 契约对齐。

## 3. 管理端模块化规范

**目标**：新增模块只增不改，不影响既有模块。

### 3.1 Module 接口契约

```go
// backend/internal/adminsvc/adminsvc.go
type Module interface {
    Key() string            // 唯一标识，同时是 REST 前缀 /v1/admin/<key>
    Name() string           // 侧边栏显示名
    Description() string    // 一句话说明
    Implemented() bool      // false = 占位（前端渲染"规划中"）
    Register(mux *http.ServeMux, s *Service)
}
```

### 3.2 新增一个模块的步骤

1. 在 `adminsvc` 新建 `<key>.go`：存储层（`xxxStore`）+ 模块层（`xxxModule`，实现 `Module`）。
2. `NewService` 里注册一行（追加，不动其它行）。
3. 前端：`web/src/App.tsx` 加路由页；侧边栏由 `/v1/admin/modules` 自动渲染。
4. 写 `docs/api/admin.md` 对应接口小节。

### 3.3 已实现 / 占位模块

| Key | 模块 | 状态 |
|---|---|---|
| skills | 技能管理 | ✅ |
| mcp | MCP 管理 | ✅ |
| kb / agents / users / data | 知识库/智能体/用户/数据 | 🚧 占位 |

占位模块用 `PlaceholderModule`（元信息 + 空 Register），前端渲染"规划中"卡片。

## 4. 文件态配置平面（配置管理范式）

管理端是**配置即文件**的管理层，agent 侧热加载生效：

- **技能**：`<skills>/<name>/SKILL.md`（Anthropic Agent Skills）。
- **MCP**：`mcp_servers.json`（JSON 数组，字段见 `mcp.ServerConfig`）。

**热加载语义**（`backend/cmd/agent/reloader.go`）：

- agent 用 fsnotify 监听上述路径，300ms 防抖后重建工具注册表；
- `agentsvc.ReplaceRegistry` 用 `RWMutex` **整体换表**——新会话用新表，
  进行中会话持旧引用不受影响；
- 配置文件损坏**不致命**：记 ERROR 后回退到环境变量配置，agent 继续运行。

**配置优先级**：文件（`AGENT_MCP_CONFIG_FILE`）优先 → 环境变量 JSON（`AGENT_MCP_SERVERS_JSON`）回退。
管理端与 agent 必须指向**同一份**文件（共享卷），否则"管理端保存但 agent 看不到"。

## 5. 安全约束

| 项 | 约束 |
|---|---|
| 管理端鉴权 | `/v1/admin/*` 由 gateway `RequireAdmin` 拦截（JWT `role == "admin"`）；adminsvc 不重复校验 |
| 初始管理员 | auth 启动 `EnsureAdmin` 播种，**仅创建、不覆盖**；默认 `admin/Admin@2026`，生产必须用 `AUTH_ADMIN_USERNAME/AUTH_ADMIN_PASSWORD` 覆盖 |
| 命名校验 | 技能名 / MCP server 名：`^[A-Za-z0-9][A-Za-z0-9_-]{0,49}$`（防路径穿越；frontmatter name 必须与目录名一致） |
| 请求体 | 管理端 JSON body ≤ 1MB（`maxBodyBytes`）；SKILL.md ≤ 64KB |
| MCP 权限 | 工具确认级别 `default_permission`：L0 纯计算 / L1 只读 / L2 写(需确认) / L3 危险 |
| 文件写 | 原子写（tmp + rename）；Windows 下目标存在先删后改 |

## 6. 环境变量清单（新增配置在此登记）

| 变量 | 服务 | 默认 | 说明 |
|---|---|---|---|
| `DEEPSEEK_API_KEY` | llm-gateway | — | 上游 LLM 密钥 |
| `JWT_SECRET` | auth/gateway | — | JWT 签名密钥（两端必须一致） |
| `AUTH_ADMIN_USERNAME` | auth | `admin` | 初始超级管理员用户名 |
| `AUTH_ADMIN_PASSWORD` | auth | `Admin@2026` | 初始超级管理员密码（生产必改） |
| `ADMIN_SKILLS_DIR` | gateway | `skills/` | 管理端技能写入目录 |
| `ADMIN_MCP_CONFIG_FILE` | gateway | `mcp_servers.json` | 管理端 MCP 配置写入文件 |
| `AGENT_SKILLS_DIR` | agent | `skills/` | agent 技能加载目录 |
| `AGENT_MCP_CONFIG_FILE` | agent | 空 | agent MCP 配置文件（优先） |
| `AGENT_MCP_SERVERS_JSON` | agent | 空 | MCP 配置环境变量（回退） |

**约定**：`ADMIN_*`（gateway 侧写入）与 `AGENT_*`（agent 侧读取）默认值对齐，
本地 go run 均在 `backend/` 下运行时天然指向同一份文件。

## 7. 测试与验证

- 后端：`cd backend && go test ./...`（新增模块必须带存储层单测，见 `adminsvc/skills_test.go`、`mcp_test.go`）。
- 前端：`cd web && npm run build`（`tsc -b` 先跑类型检查）。
- 回归要点：`GET /{$}` 根路径精确匹配已注册，勿改回 `GET /`（与 `/v1/admin/` 子树冲突）。
