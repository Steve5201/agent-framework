# 工具开发管线（扩展智能体能力）

> **面向对象**：需要给智能体增加新能力的开发者（本仓库维护者 / 三方开发者）。
> **目标**：一条"标准工具开发管线"——复用统一注册逻辑，按需选路，尽量不动核心源码。
> **配套**：[uploads.md](../api/uploads.md)（Skill/MCP 打包上传标准与模板）· [ARCHITECTURE.md](../ARCHITECTURE.md)（整体架构）

---

## 1. 三条路径总览

```
                      framework/tool.Registry（统一注册表）
                      注册 = reg.Register(t) 一行
                              │
       ┌──────────────────────┼──────────────────────┐
       ▼                      ▼                      ▼
  路径一：内置 Go 工具    路径二：Skill 技能       路径三：MCP 外部服务
  （改代码，仓库内）      （纯文件，免改代码）      （标准协议，免改代码）
  - 实现 tool.Tool 接口    - SKILL.md + 脚本/资源    - 远程 http 服务
  - DefaultToolSet 注册    - 放 AGENT_SKILLS_DIR     - 本地 stdio 进程
  - 可挂接"能力"开关       - fsnotify 热加载免重启   - 管理端配置 / 上传
  - 适合系统级 / 强耦合     - 适合知识型技能          - 适合复用第三方能力
  - 参考 describe_image     - 工具名 skill_<名称>     - 工具名 mcp_<server>_<工具>
```

| 对比 | 路径一 内置 Go 工具 | 路径二 Skill | 路径三 MCP |
|---|---|---|---|
| 需要改 Go 源码 | ✅ 是 | ❌ 否 | ❌ 否 |
| 是否需要重启/重部署 | ✅ 重建 agent 镜像 | ❌ 热加载（秒级生效） | ❌ 热加载（秒级生效） |
| 三方开发者可独立交付 | ❌ 需合入本仓库 | ✅ zip 上传管理端 | ✅ 任意 MCP 服务 |
| 能力边界 | 完全可控（权限分层 L0-L3） | 由正文指引约束 | 由 `default_permission` 约束 |
| 最佳场景 | 系统级工具（搜索/文件/计算） | 领域知识型工具（出题/翻译规则） | 复用社区生态（GitHub/天气/数据库…） |

> **推荐顺序**：能用 Skill/MCP 解决的，**不写 Go 代码**；只有需要深度访问 agent 内部能力
> （工作区文件、沙盒、视觉管线）时才走路径一。

---

## 2. 路径一：内置 Go 工具

### 2.1 最小示例（hello_world）

`backend/internal/agentsvc/tools.go` 新增：

```go
type helloWorldTool struct{}

// helloWorldArgs 参数结构（字段名 = JSON 键）。
type helloWorldArgs struct {
	Name string `json:"name"` // 必填：打招呼对象
}

func (t helloWorldTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "hello_world",
		Description: "向指定对象打招呼（示例工具）。当用户要求示例/演示时使用。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "打招呼的对象"}
			},
			"required": ["name"]
		}`),
		Required:   []string{"name"},
		Permission: schema.PermissionL0Pure, // 无副作用 → L0
	}
}

func (t helloWorldTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p helloWorldArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("hello_world: 参数解析失败: %w", err)
	}
	return "你好，" + p.Name + "！", nil
}

// 编译期断言：确保实现了 tool.Tool 接口。
var _ tool.Tool = helloWorldTool{}
```

### 2.2 注册（三种形态）

| 形态 | 做法 | 示例 |
|---|---|---|
| 无状态内置工具 | `DefaultToolSet` 内 `reg.Register(helloWorldTool{})` | calculator / echo / get_current_time |
| 依赖 Service 实例 | 实现 `type xxxTool struct{ svc *Service }`，在 `NewService` / `ReplaceRegistry` 注册（`ensureXxxRegistered`） | describe_image / read_document |
| 外部能力源 | 实现 `tools.ToolProvider`，经 `WithProviders(...)` 注入 | skill / mcp |

### 2.3 权限分层（Schema.Permission，决定是否弹确认）

| 级别 | 语义 | 适用 |
|---|---|---|
| `PermissionL0Pure` | 纯计算、无副作用 | 计算器、回显、时间 |
| `PermissionL1Read` | 只读访问（读文件/查询） | 文件读取、搜索 |
| `PermissionL2Write` | 修改状态/写文件，需用户确认 | 文件写入 |
| `PermissionL3Dangerous` | 执行脚本/删除/联网，高危 | 代码执行、shell |

> `AGENT_AUTO_APPROVE_TOOLS=true`（个人部署默认）会放行 L2/L3 并记审计；生产可关闭改人工审批。

### 2.4 挂接"能力"开关（可选）

若工具应作为**可配置能力**（用户在会话配置区开关），在 `backend/internal/agentsvc/resources.go` 的 `defaultCapabilities` 追加：

```go
{id: "hello", name: "打招呼", description: "演示用示例能力", tools: []string{"hello_world"}},
```

之后：勾选该能力 → 工具注入模型工具集；未勾选 → 过滤。与 search/vision/doc 同语义。

### 2.5 测试要求

`backend/internal/agentsvc/*_test.go` 至少覆盖：

1. **Schema**：工具已注册（`reg.Get("hello_world")`）、参数必填项正确；
2. **Execute**：正常入参 → 正确输出；非法参数 → 报错；
3. **能力过滤**（若挂了能力）：`TestService_Chat_XxxCapabilityToggle` 勾选注入/未勾选过滤（参照 `TestService_Chat_DocCapabilityToggle`）。

---

## 3. 路径二：Skill 技能（免改代码）

### 3.1 规范（Anthropic Agent Skills）

```
<技能名>/
├── SKILL.md              ← 必填：frontmatter + 正文使用指引
├── ref/guide.md          ← 可选：被 SKILL.md 相对引用的文档
├── scripts/              ← 可选：模型用 code_executor 执行的脚本
└── assets/               ← 可选：静态资源（图片等，经 /files 渲染）
```

`SKILL.md` frontmatter（必填 `name`+`description`，正文是给模型的执行指引）：

```markdown
---
name: emoji-helper
description: 需要把中文文案转成 Emoji 表情时使用
metadata:
  version: 1.0.0        # 推荐：x.y.z，多版本管理唯一标识
---
按以下规则将输入转换为 Emoji：开心→😄，生气→😡，其余保持原样。
```

### 3.2 两种部署方式

1. **直接放目录**（本地/容器）：放到 `AGENT_SKILLS_DIR`（容器内 `/app/skills` = 宿主机 `deploy/data/agent/skills/`），agent 监听目录**热加载**，工具名 `skill_<名称>`（`-` 转 `_`，如 `skill_emoji_helper`）；
2. **管理端上传**（推荐给三方开发者）：打包 zip 上传 `/v1/admin/skills`，含版本管理与回滚，见 [uploads.md](../api/uploads.md) 第 1 节。

### 3.3 模板

完整可运行模板见 `docs/dev/examples/skill-template/`（含 `ref/` 与 `scripts/` 的引用方式）。

---

## 4. 路径三：MCP 外部服务（免改代码）

### 4.1 三种接入形态

| 形态 | 做法 | 工具名 |
|---|---|---|
| 远程 http | 管理端"新建 Server"填 url + headers | `mcp_<server>_<tool>` |
| 本地 stdio | 管理端上传本地 MCP 代码包（自动注册 command/args/cwd） | `mcp_<server>_<tool>` |
| 环境变量直配 | `AGENT_MCP_SERVERS_JSON` / `AGENT_MCP_CONFIG_FILE`（文件优先） | `mcp_<server>_<tool>` |

### 4.2 最小示例（Python stdio）

`docs/api/uploads.md` 第 2.2 节有完整模板（`FastMCP` 两行注册一个工具）。核心：

```python
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("demo")

@mcp.tool()
def add(a: int, b: int) -> int:
    """两个整数相加"""
    return a + b

if __name__ == "__main__":
    mcp.run()  # stdio
```

### 4.3 权限与失败语义

- `default_permission` 决定该 server 工具的确认级别（缺省 L2）；
- 单 server 连接失败**仅跳过该 server**，不影响 agent 启动；工具调用失败结果回填给模型自主调整。

---

## 5. 验证清单

- [ ] 工具在 `GET /v1/agent/tools`（或会话思考过程）出现；
- [ ] 若挂能力：未勾选时工具被过滤（参考 `TestService_Chat_DocCapabilityToggle`）；
- [ ] 权限正确：L2/L3 工具按 `AGENT_AUTO_APPROVE_TOOLS` 语义确认/放行；
- [ ] Skill/MCP 改动后**无需重启**（fsnotify 热加载），刷新会话即生效。
