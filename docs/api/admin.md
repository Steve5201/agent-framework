# 管理端接口文档（Admin Panel，`/v1/admin/*`）

> 对应后端模块：`backend/internal/adminsvc`（文件态配置平面 + 模块化设计）。
>
> **阅读对象**：管理端前端（`web/src/pages/admin/*`）与后续管理模块开发者。

---

## 1. 模块定位

管理端是智能体系统的**配置管理层**，采用「文件态配置平面」设计：

- **技能（skill）**：直接落盘为 `<技能目录>/<name>/SKILL.md`（Anthropic Agent Skills 格式）；
- **MCP Server**：整份列表写入一个 JSON 配置文件（`mcp_servers.json`）；
- agent 用 **fsnotify** 监听上述路径，变更后**防抖重建工具注册表并热替换**——
  **管理端保存即生效，无需重启**；进行中的会话持有旧注册表引用不受影响。

## 2. 模块化与扩展约定

每个功能模块实现 `adminsvc.Module` 接口后，在 `NewService` 注册一行即可：

```go
type Module interface {
    Key() string            // 模块标识，同时作为 REST 前缀 /v1/admin/<key>
    Name() string           // 侧边栏显示名
    Description() string    // 一句话说明
    Implemented() bool      // false = 占位模块（前端渲染"规划中"）
    Register(mux *http.ServeMux, s *Service)
}
```

| Key | 名称 | 状态 |
|---|---|---|
| agents | 智能体管理 | ✅ 已实现（P3-A，/v1/admin/agents*） |
| skills | 技能管理 | ✅ 已实现 |
| kb | 知识库管理 | ✅ 已实现（P3-A5/A3b） |
| mcp | MCP 管理 | ✅ 已实现 |
| users | 用户管理 | ✅ 已实现（P2-A，/v1/admin/users*） |
| models | 大模型管理 | ✅ 已实现（/v1/admin/models*） |
| data | 数据管理（运营分析：会话/用量） | ✅ 已实现 |
| logs | 操作日志 | ✅ 已实现 |

**新增模块三件事**：实现 `Module` 接口 → `NewService` 注册一行 → 前端加路由页。
**只增不改**，不影响已有模块。前端侧边栏由 `GET /v1/admin/modules` 动态渲染，后端注册即出现。

## 3. 鉴权与错误约定

- **鉴权**：全部 `/v1/admin/*` 由 gateway 的 `RequireAdmin` 中间件强制校验
  （JWT `role` 经 `identity.IsAdminRole` 判定），adminsvc 自身不校验权限。
  普通用户/游客访问返回 403。
- **角色分层（阶段3·多租户）**：
  - `super_admin`（最高超管，无智能体归属）：完整权限；资源操作经 `?agent_id=` 显式指定域（缺省回退默认域 `tutor`）；可管理任意智能体/用户/全部模块；
  - `agent_admin`（智能体超管）：只能管理**自己智能体组**——资源域锁定 JWT 归属（忽略 `agent_id` 参数，防越权）；可创建组内 user/admin、管理组内资源；无 agents/data 模块；
  - `admin`（组内普通管理员）：域同样锁定；无用户管理/智能体管理/数据管理。
- **资源域参数**：`?agent_id=<id>`（字母/数字/中划线，≤64 字符）——仅 super_admin 生效，agent_admin/admin 由后端强制锁定自身归属，越域请求被忽略或 404。
- **请求体上限**：1MB（防超大 body）。
- **统一错误体**（与 gateway 其它接口同构）：

```json
{ "code": "NOT_FOUND", "message": "技能不存在", "request_id": "..." }
```

| code | HTTP 状态 | 说明 |
|---|---|---|
| INVALID_ARGUMENT | 400 | 参数不合法（命名/格式/内容校验失败） |
| UNAUTHENTICATED | 401 | 未登录或 token 失效 |
| PERMISSION_DENIED | 403 | 非管理员访问 / 无该模块权限 |
| NOT_FOUND | 404 | 资源不存在 / 越域访问（同 404 防枚举） |
| ALREADY_EXISTS | 409 | 资源已存在（创建冲突） |
| VERSION_CONFLICT | 409 | 版本冲突（同版本号但内容不同，需覆盖或改版本号） |
| INTERNAL | 500 | 内部错误 |

- **命名校验（技能名 & MCP server 名通用）**：`^[\p{L}\p{N}][\p{L}\p{N}_-]{0,49}$`
  （**支持中文**/字母/数字/下划线/连字符，首字符为中文/字母/数字，1~50 字符）。
  该规则排除点号与路径分隔符 → **天然防目录穿越**。
- 技能工具名 = `skill_` + 净化名：中文等非 ASCII 名自动哈希兜底成唯一 ASCII 工具名
  （如 `skill_数据分析_a1b2c3d4`），保证 LLM 工具名合法且互不冲突。

---

## 4. 接口清单

### 4.1 模块清单

#### GET `/v1/admin/modules`

返回**当前角色可见**的管理模块及实现状态（管理端侧边栏/首页渲染用）。
阶段3·角色裁剪：`super_admin` 全量（含 agents/data）；`agent_admin` 隐藏 agents/data；
`admin` 仅自身域模块（skills/mcp/kb/logs）。

```json
{
  "modules": [
    { "key": "skills", "name": "技能管理", "description": "…", "implemented": true },
    { "key": "kb", "name": "知识库管理", "description": "…", "implemented": true },
    { "key": "logs", "name": "日志管理", "description": "…", "implemented": true },
    { "key": "agents", "name": "智能体管理", "description": "…", "implemented": true },
    { "key": "users", "name": "用户管理", "description": "…", "implemented": true },
    { "key": "data", "name": "数据管理", "description": "…", "implemented": false }
  ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| key | string | 模块标识（= 前端路由 `/admin/<key>`） |
| name | string | 显示名 |
| description | string | 一句话说明 |
| implemented | bool | false = 占位模块，前端渲染"规划中" |

已实现模块一览：

| 模块 key | 端点 | 可见角色 |
|---|---|---|
| skills | `/v1/admin/skills*` | 全部管理员（按资源域隔离） |
| mcp | `/v1/admin/mcp*` | 全部管理员（按资源域隔离） |
| kb | `/v1/admin/kb*` | 全部管理员（按资源域隔离） |
| logs | `/v1/admin/logs` | 全部管理员（agent_admin/admin 锁定本域，super_admin 全量） |
| users | `/v1/admin/users*` | super_admin + agent_admin |
| agents | `/v1/admin/agents*` | 仅 super_admin |
| data | `/v1/admin/data/overview` | 仅 super_admin |

### 4.2 日志管理（阶段4，`/v1/admin/logs`）

管理端写操作审计（skill/mcp/kb/users/agents 的 POST/PUT/DELETE 均被
`WithAudit` 中间件记录；GET 只读不记录）。数据源为**文件态 JSONL**：
`<LogsDir>/<agentID>/audit.jsonl`（`ADMIN_LOGS_DIR` 环境变量，默认工作目录 `admin-logs/`）。

#### GET `/v1/admin/logs`

查询日志，分页 + 过滤。权限同资源隔离模型：
- `agent_admin` / `admin`：锁定自身归属域（忽略 `agent_id` 参数）；
- `super_admin`：不传 `agent_id` = 扫描全部域，传了则只看指定域。

Query 参数：

| 参数 | 类型 | 说明 |
|---|---|---|
| agent_id | string | 目标域（仅 super_admin 生效，缺省全部域） |
| action | string | 动作前缀过滤，如 `skills` 或 `skills.update`；空 = 不过滤 |
| user_id | int | 操作者用户 ID 过滤；0 = 不过滤 |
| page | int | 页码（1-based，默认 1） |
| page_size | int | 每页条数（默认 50，上限 200） |

响应：

```json
{
  "items": [
    {
      "ts": "2026-08-09T10:00:00+08:00",
      "user_id": 7,
      "role": "agent_admin",
      "actor_agent": "tutor",
      "target_agent": "tutor",
      "action": "skills.create",
      "method": "POST",
      "path": "/v1/admin/skills",
      "status": 201,
      "request_id": "…",
      "latency_ms": 12
    }
  ],
  "page": 1,
  "page_size": 50,
  "total": 128
}
```

### 4.3 技能管理（`/v1/admin/skills`）

> **打包标准与模板见 [uploads.md](./uploads.md) 第 1 节**（SKILL.md 模板 / 目录结构 / 校验规则 / 版本语义）。

技能对象（`Skill`）：

| 字段 | 类型 | 说明 |
|---|---|---|
| name | string | 技能名（= 目录名 = 工具名来源） |
| description | string | SKILL.md frontmatter 的 description |
| license | string | 可选的许可证信息 |
| semver | string | frontmatter 语义版本号（`metadata.version`/`version`，可选，如 `1.0.0`） |
| tool_name | string | 注册的工具名（`skill_<净化名>`） |
| content | string | SKILL.md 完整内容（frontmatter + 正文） |
| file_count | number | 目录内其它文件数（不含 SKILL.md 与内部文件） |
| updated_at | string | SKILL.md 修改时间（RFC3339） |
| valid | boolean | 解析是否通过（无效技能在列表仍可见，供修复） |
| error | string | valid=false 时的失败原因 |
| enabled | boolean | 是否启用（禁用 = agent 不注册该技能工具，热加载生效） |
| version | number | 当前生效版本号（内部槽序号，仅兜底展示） |
| versions | array | 历史版本列表 `[{semver, updated_at, size}]`，按语义版本号倒序 |

**版本语义（版本号以 SKILL.md frontmatter 的 `metadata.version`/`version` 为准，
x.y.z 语义版本号；版本身份 = 名字 + 版本号）**：
- **同一技能（name）下同一版本号只能有一份**（含当前与全部历史）；
- 上传/新建/更新：版本号在"当前 + 历史"中均未出现 → 发布新版本（旧内容入历史）；
  版本号已存在且内容相同 → 幂等；**版本号已存在但内容不同 → `409 VERSION_CONFLICT`**，
  `?overwrite=true` 时覆盖该版本（同版本号只保留新内容一份，原当前版本入历史）；
- 新建与上传要求 frontmatter **必须**提供合法语义版本号（缺失/格式非法 → `400` 拒绝）；
- 回滚 = 把某历史版本号置为当前（原当前入历史），**被回滚的版本号不再存在于历史**；
- 历史无版本号的旧技能允许"无版本号兼容编辑"（不做冲突判定）。

#### GET `/v1/admin/skills` — 列表

`200`：`{ "skills": [ Skill, … ] }`。目录不存在 = 空数组；单个技能无效不中断列表。

#### POST `/v1/admin/skills` — 新建

请求体：`{ "name": "emoji-helper", "content": "---\nname: emoji-helper\nmetadata:\n  version: 1.0.0\n…" }`

- 校验：命名规则（**支持中文**）+ SKILL.md ≤ 64KB + frontmatter 必填
  name/description/**version（x.y.z）** + **frontmatter name 必须与目录名一致**
  （防止"目录名 A、工具名 B"错位）。
- `201`：`{ "skill": Skill }`；**重名 → `409 ALREADY_EXISTS`**（同名新版本请走
  更新/上传流程，二者均按版本号语义处理）。

#### POST `/v1/admin/skills/upload` — 上传 zip 技能包

`multipart/form-data`：仅 `file`（zip）。zip ≤ 10MB。**技能名与版本号由后端从
包内 SKILL.md 自动提取，无需（也不允许）手工填写**：
- 名称来源：frontmatter `name` → zip 内包裹目录名 → 上传文件名（去 `.zip`），
  仍为空则 `400` 拒绝；
- 版本：frontmatter `metadata.version`/`version` 必须为合法语义版本号（x.y.z），
  缺失/非法 → `400` 拒绝；
- **保留 zip 原始目录结构**：SKILL.md 可位于任意层级（取最浅），其下全部子目录
  （docs/、ref/、scripts/ 等）与跨目录相对引用原样保留；
- 防 zip-slip：拒绝绝对路径、盘符路径与 `../` 越界条目（跨平台加固）；
- 同名上传：版本不同 = 发布新版本；同版本不同内容 → `409 VERSION_CONFLICT`，
  `?overwrite=true` 时覆盖（保留历史版本与启用状态）；
- `201`：`{ "skill": Skill }`

#### GET `/v1/admin/skills/{name}` — 详情

`200`：`{ "skill": Skill }`；不存在 → `404`。

#### PUT `/v1/admin/skills/{name}` — 全量更新

请求体：`{ "content": "---\nname: …\nmetadata:\n  version: 1.1.0\n…" }`
（仅 content；**name 以路径为准，不可改名**）。

- 按版本号语义处理：版本不同 = 发布新版本；同版本同内容 = 幂等；
  **同版本不同内容 → `409 VERSION_CONFLICT`**，`?overwrite=true` 时覆盖；
  历史无版本号的旧技能允许"无版本号兼容编辑"（不做冲突判定）。
- `200`：`{ "skill": Skill }`；不存在 → `404`。

#### PATCH `/v1/admin/skills/{name}/enabled` — 启用/禁用

请求体：`{ "enabled": false }`。禁用 = agent 移除该技能工具（热加载）。

- `200`：`{ "skill": Skill }`；不存在 → `404`。

#### POST `/v1/admin/skills/{name}/versions/{version}/restore` — 回滚版本

`{version}` 为语义版本号（如 `1.0.0`）。回滚到该版本：内容写回当前 SKILL.md，
原当前版本入历史（**同版本号只留一份**，被回滚的版本号从历史移除）。

- `200`：`{ "skill": Skill }`；技能或版本不存在 → `404`；当前已是该版本 → 幂等返回。

#### DELETE `/v1/admin/skills/{name}` — 删除

`204` No Content；不存在 → `404`。

### 4.4 MCP Server 管理（`/v1/admin/mcp-servers`）

> **标准模板（Python/Node 本地 MCP + 远程 http + 标准 mcpServers 格式）见 [uploads.md](./uploads.md) 第 2~3 节**。
> 约定：**新建仅支持远程 http**；本地 MCP 一律通过「上传本地 MCP」（第 4.3 节 upload 接口）。

MCP server 配置对象（与 `backend/internal/tools/mcp.ServerConfig` 一致）：

| 字段 | 类型 | 说明 |
|---|---|---|
| name | string | server 名（命名规则同上；工具名前缀 `mcp_<name>_` 由此而来） |
| enabled | boolean | 是否启用（缺省 = 启用；false = agent 不注册其工具，热加载生效） |
| transport | string | `stdio`（缺省）\| `http` |
| command | string | stdio：启动命令（如 `npx`） |
| args | string[] | stdio：命令参数 |
| cwd | string | stdio：子进程工作目录（可选；Claude/trae/workbuddy 标准字段） |
| env | object | stdio：子进程环境变量 |
| url | string | http：远程 endpoint |
| headers | object | http：请求头 |
| timeout_seconds | number | 单次工具调用超时秒数；0 = 跟随上游 |
| default_permission | string | 工具确认级别：`L0`\|`L1`\|`L2`（缺省）\|`L3` |
| discovered_tools | string[] | 最近一次"测试连接/启用"成功发现的工具名（展示用） |
| discovery_error | string | 最近一次"测试连接/启用"失败原因（展示用） |

权限级别语义（`framework/schema/tool.go`）：
`L0` 纯计算 · `L1` 只读 · `L2` 写操作（需确认） · `L3` 危险（执行脚本/联网/删除）。

**启用是真实动作**：`PATCH .../enabled` 置 true 时会**实际连接 server 并 tools/list 发现工具**——
连接失败返回 `400`（不启用，`discovery_error` 记录原因）；成功则把发现结果持久化展示。

**配置方式**：支持结构化为表单（前端）或**直接编辑 JSON**。JSON 兼容三种业界格式：
1. 单 server 对象（上表字段）；
2. JSON 数组（多个 server）；
3. **标准 `mcpServers` 对象**（Claude Desktop / trae / workbuddy）：
   `{ "mcpServers": { "<name>": { "command": "…", "args": […], "cwd": "…" } } }`，
   也接受无包装的裸对象 `{ "<name>": { … } }`（name 取 key）。
   粘贴/导入含多 server 的 JSON 时前端**批量创建**（已存在自动跳过）。
后端 `mcp.ParseServersJSON` 同样兼容上述格式（配置文件 / `AGENT_MCP_SERVERS_JSON` 均可直接使用标准格式）。

#### GET `/v1/admin/mcp-servers` — 列表

`200`：`{ "servers": [ … ] }`。配置文件不存在或为空 = 空数组；
文件内容损坏（非法 JSON）→ `500`（需人工修复，防静默丢配置）。

#### POST `/v1/admin/mcp-servers` — 新建

请求体：完整 `McpServer` 对象。transport 缺省补 `stdio`。
- `201`：`{ "server": … }`；重名 → `409`；必填项缺失（stdio 缺 command / http 缺 url）→ `400`。

#### POST `/v1/admin/mcp-servers/test` — 测试一段尚未保存的配置

请求体：`McpServer`（走标准校验）。实际连接并 tools/list 发现工具，不持久化。
- `200`：`{ "tools": […], "error": "" }`；连接失败时 `error` 为原因（仍 200，tools 为空）。

#### POST `/v1/admin/mcp-servers/upload` — 上传本地 MCP 代码包

`multipart/form-data`：`file`（zip，≤10MB）+ `name`（可选，默认取 zip 文件名）+ `entry`（可选入口文件）。
把开发好的 MCP 上传到服务器本地运行：解压到 `mcp-servers/<name>/`，自动定位入口
（`main.py`/`server.py`/`mcp_server.py`/`app.py`/`index.js` 等，取最浅），按后缀选解释器
（py→python3，js→node，sh→sh），注册为 stdio server（`command=解释器, args=[入口], cwd=代码目录`）。
同名覆盖代码与配置。
- `201`：`{ "server": … }`；无入口/名字非法/zip 非法 → `400`。运行需容器内有对应解释器与依赖。

#### GET `/v1/admin/mcp-servers/{name}` — 详情

`200`：`{ "server": … }`；不存在 → `404`。

#### PUT `/v1/admin/mcp-servers/{name}` — 全量更新

请求体：完整 `McpServer` 对象。**name 以路径为准，不可改名**；body 中 name 与路径不一致 → `400`。
- `200`：`{ "server": … }`；不存在 → `404`。

#### POST `/v1/admin/mcp-servers/{name}/test` — 测试已保存的 server

实际连接并 tools/list 发现工具，结果持久化到 `discovered_tools`/`discovery_error`。
- `200`：`{ "server": …, "tools": […], "error": "" }`；连接失败 `error` 非空（200）；不存在 → `404`。

#### PATCH `/v1/admin/mcp-servers/{name}/enabled` — 启用/禁用

请求体：`{ "enabled": false }`。禁用 = agent 移除该 server 全部工具（热加载）。
**置 true = 真实连接验证**：连接/发现失败 → `400`（不启用）；成功则返回含 `discovered_tools` 的 server。
- `200`：`{ "server": … }`；不存在 → `404`。

#### DELETE `/v1/admin/mcp-servers/{name}` — 删除

`204` No Content；不存在 → `404`。

### 4.5 知识库管理（`/v1/admin/kb*`，P3-A5/A3b）

> 知识库走**数据库（PostgreSQL + pgvector）**而非文件态配置平面（大数据量 + 向量）。摄取为异步：上传即入队（queued），后台 worker 解析 → 分块 → 向量化 → 落库。

**支持的文档格式（P3-A3b）**

| 格式 | 解析方式 | 备注 |
|---|---|---|
| `.md / .txt / .html / .markdown` | Go 原生 | 无需沙盒 |
| `.xlsx` | Go 原生（excelize） | 每 sheet 转 Markdown 表格段 |
| `.pdf` | 沙盒（PyMuPDF） | 扫描版（无文本层）会被拒绝 |
| `.docx` | 沙盒（pandoc） | OMML 公式自动转 `$LaTeX$` |
| `.pptx` | 沙盒（python-pptx） | 图片/视频/音频提取保留 |
| `.doc` | 拒绝 | 老版二进制，提示"请另存为 .docx" |
| 其它 | 拒绝 | 明确错误提示 |

> `.pdf/.docx/.pptx` 需 rag 配置 `RAG_SANDBOX_URL`（docker 部署默认已配）；未配置时返回"需启用解析沙盒"。
> 单篇文档 ≤20MB；媒体文件提取到 `rag-media/<docID>/`，chunk 正文保留 `![alt](path)` 占位。

**端点**

#### GET `/v1/admin/kb` — 知识库列表（按资源域隔离）
- 响应：`{ "bases": [{ "id", "name", "description", "doc_count", "created_at" }] }`

#### POST `/v1/admin/kb` — 新建知识库
- 请求体：`{ "name" (1~50 字符), "description" (≤200，可选) }`
- 校验失败 `400`；名称重复 `409`。

#### GET `/v1/admin/kb/{id}?page=&page_size=` — 知识库详情（含文档分页）
- 响应：`{ "id", "name", "description", "total", "documents": [{ "doc_id", "file_name", "status", "chunk_count", "error", "updated_at" }] }`
- `status ∈ { queued, processing, succeeded, failed }`；`error` 为失败原因（含扫描版 PDF 提示、`.doc` 拒绝原因等）。

#### POST `/v1/admin/kb/{id}/documents` — 上传文档（multipart，字段 `file`）
- 入队成功 `201`；格式不支持/超 20MB → `400`；知识库不存在 → `404`。
- 摄取状态异步推进，前端轮询详情页可见。

#### DELETE `/v1/admin/kb/{id}/documents/{doc_id}` — 删除文档
- `204`；不存在 → `404`。

#### DELETE `/v1/admin/kb/{id}` — 删除知识库（含其下全部文档与分块）
- `204`；不存在 → `404`。

### 4.6 数据管理（运营分析台，`/v1/admin/data/overview`）

平台运营数据的**只读观测台**（无任何写操作，仅 super_admin 可见）。数据源为
三个只读服务端聚合：
- agent-service `AdminSessionStats`（gRPC）：会话统计（agent 库 `sessions`）；
- llm-gateway `GET /v1/usage/overview`（HTTP，携带 `X-Admin-Token`）：用量/成本（llm 库 `usage_logs`）；
- auth-service `AdminGetUsersByIds`（gRPC）：Top 用户 user_id → 用户名回填。

#### GET `/v1/admin/data/overview?days=N`

Query 参数：

| 参数 | 类型 | 说明 |
|---|---|---|
| days | int | 统计窗口天数（1..90，缺省 30） |

响应（`dataOverview`）：

```json
{
  "sessions": {
    "days": [ { "date": "2026-08-12", "sessions": 5 } ],
    "agents": [ { "agent_id": "tutor", "sessions": 5 } ],
    "total_sessions": 123
  },
  "usage": {
    "summary": { "calls": 100, "success": 95, "failed": 5, "dau": 7, "total_tokens": 5000, "cost_usd": 1.23 },
    "daily": [ { "date": "2026-08-12", "calls": 20, "success": 19, "failed": 1, "dau": 3, "total_tokens": 1000, "cost_usd": 0.2 } ],
    "by_model": [ { "key": "deepseek", "calls": 100, "total_tokens": 5000, "cost_usd": 1.23 } ],
    "by_agent": [ { "key": "tutor", "calls": 90, "total_tokens": 4500, "cost_usd": 1.0 } ],
    "by_user": [ { "user_id": 1, "calls": 60, "total_tokens": 3000, "cost_usd": 0.8 } ]
  },
  "user_names": { "1": "zhangsan" }
}
```

| 字段 | 说明 |
|---|---|
| sessions.days | 按日新建会话数（`sessions.created_at` 分组，status=1 有效会话；generate_series 补全 0 值完整日序列） |
| sessions.agents | 会话按智能体域分布（`''` = 管理端域，会话数倒序） |
| sessions.total_sessions | 全量累计有效会话数 |
| usage.summary | 窗口内总调用/成功/失败/去重活跃用户（DAU）/Token/成本 |
| usage.daily | 按日调用明细（含 DAU，0 值补全） |
| usage.by_model | 按模型聚合（成本 $ 口径） |
| usage.by_agent | 按智能体域聚合 |
| usage.by_user | 按用户聚合（调用降序，Top 10） |
| user_names | Top 用户 user_id → username（回填失败仅告警，不阻断主数据） |

数据口径：活跃用户（DAU）= 当日 ≥1 次成功调用去重 user_id；成功率 = success / calls。

错误：`days` 非 1..90 → `400`；任一主数据源失败 → `500`（llm-gateway 非 200 亦视为
内部错误）；无管理员身份 → `401`。

---

## 5. 生效链路（免重启的机制）

```
管理端保存/删除
   │  写文件
   ▼
<技能目录>/<name>/SKILL.md  或  mcp_servers.json   （文件态配置平面）
   │  fsnotify 事件
   ▼
agent 防抖(300ms) → rebuild() → 新 tool.Registry
   │  RWMutex 整体换表（ReplaceRegistry）
   ▼
新会话用新工具表；进行中会话持旧引用不受影响
```

配置文件路径由环境变量指定（文件优先于环境变量 JSON，防管理端误写导致 agent 宕机）：

| 环境变量 | 作用方 | 默认值 |
|---|---|---|
| `ADMIN_SKILLS_DIR` | gateway 管理端 | 工作目录下 `skills/` |
| `ADMIN_MCP_CONFIG_FILE` | gateway 管理端 | 工作目录下 `mcp_servers.json` |
| `AGENT_SKILLS_DIR` | agent | 工作目录下 `skills/` |
| `AGENT_MCP_CONFIG_FILE` | agent | 空（回退 `AGENT_MCP_SERVERS_JSON`） |

部署时 gateway 与 agent 必须指向**同一份**技能目录与 MCP 配置文件
（见 `deploy/docker-compose.yml`：共享 `./data/agent:/app` 卷）。
