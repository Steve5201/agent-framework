# backend 接口文档

> 阅读对象：需要对接后端、或在 web/desktop 端实现调用的开发者。
> 本文只覆盖**对外**的 HTTP 接口（gateway :8080）。服务间 gRPC 契约见 `backend/internal/proto/`。
>
> 状态标记：
> - ✅ **已实现**（P2 交付，单测全绿）
> - 🚧 **规划中，未实现**（严禁按已实现调用）

---

## 1. 子项目定位：什么时候需要 backend？

backend 是整套系统里**真正干活的进程**：接收前端请求、校验身份、调度大模型、持久化数据。

- 前端（web / desktop）**只需要对接 gateway 一个入口**（`http://localhost:8080`），不需要关心后端内部拆成了几个服务。
- 端口约定：gateway `8080`（对外）、auth `8081`、agent `8082`、llm-gateway `8083`、knowledge `8084`、rag `8085`。**除 8080 外均为内网 gRPC，不对外开放。**
- 调用链路：浏览器/桌面端 ──HTTP──▶ gateway ──gRPC──▶ auth/agent ──HTTP(OpenAI 协议)──▶ llm-gateway ──▶ DeepSeek。

一句话：**前端只认识 8080，8080 负责把请求翻译给内部服务。**

## 2. 模块概览

| 模块 | 端口 | 协议 | 对外暴露 | 状态 |
|---|---|---|---|---|
| **gateway** | 8080 | HTTP/JSON | ✅ 全部对外接口 | ✅ 已实现 |
| **auth-service** | 8081 | gRPC | ❌（经 gateway 透传） | ✅ 已实现 |
| **agent-service** | 8082 | gRPC | ❌（经 gateway 透传） | ✅ 已实现 |
| **llm-gateway** | 8083 | HTTP/OpenAI | ⚠️ 内网调用（agent 上游） | ✅ 已实现 |
| knowledge / rag | 8084~8085 | gRPC | ❌ | 🚧 规划中，未实现 |

## 3. 通用约定（所有接口都适用）

### 3.1 认证方式

需要认证的接口必须带请求头：

```
Authorization: Bearer <access_token>
```

- access token 由 `POST /v1/auth/login` / `POST /v1/auth/refresh` 下发，有效期 **15 分钟**（可在 `backend/.env` 调）。
- **gateway 自己校验 JWT**，不会把 access token 转发给下游服务；下游只信任 gateway 注入的 `x-user-id`——前端无需关心，也不用把 user_id 放进请求体。
- token 过期返回 `401 UNAUTHENTICATED`；前端应在收到 401 后用 refresh token 静默刷新并重放请求（web 端已内置该逻辑）。

免认证（白名单）接口：注册、登录、刷新、登出、健康检查、接口文档。

### 3.2 全链路追踪 request_id

建议（非强制）每个请求带上：

```
X-Request-Id: <uuid>
```

gateway 会把该 ID 贯穿到内部 gRPC 调用与日志。不带时 gateway 自动生成。**排障时拿着 request_id 去各服务日志里 grep 即可串起整条链路**。

### 3.3 错误响应体（对外契约）

所有非 2xx 响应统一为：

```json
{
  "code": 40401,
  "message": "会话不存在",
  "request_id": "a1b2c3..."
}
```

| 字段 | 说明 |
|---|---|
| `code` | 整型业务码，见 3.4 错误码表 |
| `message` | 面向调用方的可读信息（不含内部细节） |
| `request_id` | 全链路追踪 ID |

### 3.4 错误码表

| 业务码 | HTTP | 字符串码 | 含义 | 常见场景 |
|---|---|---|---|---|
| 40001 | 400 | `INVALID_ARGUMENT` | 参数非法 | JSON 解析失败、字段格式错误、非法会话 ID |
| 40002 | 400 | `FAILED_PRECONDITION` | 前置条件不满足 | 状态不允许当前操作 |
| 40101 | 401 | `UNAUTHENTICATED` | 未认证 / token 无效 | 没带 token、token 过期、token 类型不符 |
| 40301 | 403 | `PERMISSION_DENIED` | 无权限（RBAC） | 已登录但角色不允许 |
| 40401 | 404 | `NOT_FOUND` | 资源不存在 | 会话不存在；**非本人的资源也返回 404**（防枚举探测） |
| 40901 | 409 | `ALREADY_EXISTS` | 资源冲突 | 用户名已注册、技能重名新建 |
| 40902 | 409 | `VERSION_CONFLICT` | 版本冲突 | 技能同版本号但内容不同（需覆盖或改版本号） |
| 42901 | 429 | `RESOURCE_EXHAUSTED` | 限流 / 配额 | IP 或用户维度限流、LLM token 月配额超限 |
| 49901 | 499 | `CANCELLED` | 请求被取消 | 客户端断开、服务端取消 |
| 50001 | 500 | `INTERNAL` | 内部错误（兜底） | 未预期的服务端错误 |
| 50301 | 503 | `UNAVAILABLE` | 服务不可用 | 上游服务（auth/agent/llm）不可达或过载 |
| 50401 | 504 | `DEADLINE_EXCEEDED` | 超时 | 调用上游超时 |

> 前端处理口诀：`401` → 刷新重试；`429` → 提示稍后再试；`404/409` → 直接展示 message；其他 → 按 500 类处理并附 request_id。

### 3.5 限流与请求体限制

- 请求体上限 **1MB**（超大 body 直接 400）。
- 两层限流：**IP 维度**（最外层，防匿名刷注册/登录）+ **用户维度**（认证后按 user_id，防单用户刷爆下游）。默认速率见 `backend/.env` 的 `GATEWAY_GLOBAL_*` / `GATEWAY_USER_*`。
- LLM 另有**月 token 配额**（按用户），超限返回 429。

---

## 4. 模块：认证 auth（✅ 已实现）

### 4.1 模块定位

负责账号生命周期：注册、登录、令牌下发与轮换、登出吊销。**令牌安全设计**（P2-B）：

- **access / refresh 双令牌**：access 15 分钟短命（前端内存/本地持有），refresh 长期有效（进数据库，SHA-256 哈希存储）。
- **refresh 族轮换 + 单次使用**：每次刷新都会下发新 refresh 并作废旧 refresh；**同族 refresh 一旦被重复使用（重放攻击），整族吊销**。
- **登录失败限流**：按用户名内存限流（多副本部署需换 Redis）。

### 4.2 接口列表

| 方法 | 路径 | 认证 | 作用 |
|---|---|---|---|
| POST | `/v1/auth/register/{agent_id}` | 免 | 分智能体门户注册（裸 `/register` 已下线） |
| POST | `/v1/auth/login` | 免 | 管理员入口登录，**仅放行 `role=admin`** |
| POST | `/v1/auth/login/{agent_id}` | 免 | 智能体门户登录（首次自动绑定 agent 标签） |
| POST | `/v1/auth/refresh` | 免 | 用 refresh 换新令牌对 |
| POST | `/v1/auth/logout` | 免 | 吊销 refresh 族 |
| GET | `/v1/auth/me` | ✅ | 当前用户资料 |

> 网关白名单支持 `{agent_id}` 路径通配（`middleware.go skipRoute`），匿名入口不会被误判为"缺少访问令牌"。

### 4.3 注册

**作用**：创建一个新账号。用户名唯一，密码强校验。**仅分智能体门户入口**（`{agent_id}`）可用；管理员账号由管理员经 `/v1/admin/users` 创建，不开放自助注册。

**定义**：`POST /v1/auth/register/{agent_id}`（如 `/v1/auth/register/tutor`）

**请求体**：

```json
{
  "username": "student_01",
  "password": "passw0rd123"
}
```

**参数**：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `username` | string | ✅ | 3~32 字符，字母数字下划线 |
| `password` | string | ✅ | ≥8 位，须同时含字母与数字 |
| `agent_id`（路径） | string | ✅ | 智能体 ID，注册后写入 `agent` 标签 |

**成功响应**（`201 Created`）：

```json
{
  "user_id": "1",
  "username": "student_01",
  "tags": [{ "key": "agent", "value": "tutor" }]
}
```

**失败**：`409 ALREADY_EXISTS`（用户名已注册）、`400 INVALID_ARGUMENT`（格式不满足）。

> 注：注册不直接返回令牌，注册成功后需再调登录接口。

### 4.4 登录

**作用**：校验账号密码，下发 access/refresh 令牌对与用户信息。

**定义**：
- `POST /v1/auth/login` —— 管理员入口，**仅放行 `role=admin` 的账号**；普通账号返回 `403 PERMISSION_DENIED`（"该入口仅限管理员登录，请使用对应的智能体门户入口"），防止普通用户误登/越权管理端。
- `POST /v1/auth/login/{agent_id}` —— 智能体门户入口，任意有效账号可登录；用户尚无该 `agent` 标签时首次登录自动补写（绑定该智能体）。

**请求体**：

```json
{
  "username": "student_01",
  "password": "passw0rd123"
}
```

**成功响应**（`200 OK`）：

```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "a3f2c1...",
  "expires_in": 900,
  "user": {
    "id": "1",
    "username": "student_01",
    "role": "user",
    "tags": [{ "key": "agent", "value": "tutor" }]
  }
}
```

**参数**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `access_token` | string | JWT，15 分钟有效，请求时放 `Authorization: Bearer` |
| `refresh_token` | string | 单次使用，用于刷新/登出 |
| `expires_in` | int | access 有效期（秒） |
| `user` | object | 用户信息（`role`/`tags`） |

**失败**：`401 UNAUTHENTICATED`（账号或密码错误——不区分具体是哪个，防探测）、`403 PERMISSION_DENIED`（普通账号走管理员入口）、`429 RESOURCE_EXHAUSTED`（登录失败限流）。同一用户名连续失败会触发限流。

### 4.5 刷新令牌

**作用**：access 过期后，用 refresh 换新令牌对（前端 401 重试的底层依据）。**每刷新一次，refresh 也换新**。

**定义**：`POST /v1/auth/refresh`

**请求体**：

```json
{
  "refresh_token": "a3f2c1..."
}
```

**成功响应**（`200 OK`）：

```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "b8d4e2...",
  "expires_in": 900
}
```

**注意**：
- 返回的 `refresh_token` 与旧的不一样（轮换）；前端**必须用新值覆盖旧的**。
- 同一个 refresh 被用两次 → **整族吊销**，此后任何该族的 refresh 都无效（前端务必"单飞行"刷新，不要并发刷）。

### 4.6 登出

**作用**：吊销 refresh 族，服务器端立即失效。

**定义**：`POST /v1/auth/logout`

**请求体**：

```json
{
  "refresh_token": "b8d4e2..."
}
```

**成功响应**：`204 No Content`（无响应体）。

### 4.7 当前用户

**作用**：验证 access 仍有效并取回最新用户资料（web 端启动时用它恢复登录态）。

**定义**：`GET /v1/auth/me`

**请求头**：`Authorization: Bearer <access_token>`

**成功响应**（`200 OK`）：

```json
{
  "id": "1",
  "username": "student_01",
  "role": "student"
}
```

**失败**：`401 UNAUTHENTICATED`（token 缺失/无效/过期）。

### 4.8 游客身份（阶段2，`X-Guest-ID`）

未登录用户以**游客**身份使用智能体对话（`/agent/:agentId` 无需登录）。

- **请求头**：`X-Guest-ID: <uuid>`（浏览器/桌面端 localStorage 生成的 UUID，持久化保持）。
- **身份派生**：auth 包 `GuestUserID(guestID)` 用 FNV-1a 把 UUID 哈希成**负整数 user_id**（负值空间专供游客，与真实用户 `> 0` 空间互不重叠）——会话落库、合并、限流全部复用 user_id 体系，无需另起炉灶。
- **传递链路**：gateway `RequireAuth` 解析 `X-Guest-ID` → gRPC metadata `x-user-id=<负数>` → agent-service 落库/查询 → **llm-gateway 也必须放行负值 user_id**（`parseUserID` 仅拒绝 0，负值各自独立限流桶与用量归属）。
- **会话合并**：登录成功后前端调 `POST /v1/agent/sessions/merge-guest`（body `{"guest_id":"<uuid>"}`），把游客会话归属到账号（合并失败不阻断登录）。

### 4.9 角色与智能体归属（阶段3，多租户）

| 角色 | 说明 | 智能体归属（JWT `agent_id` claim） |
|---|---|---|
| `super_admin` | 最高超管，管理全部智能体/用户/模块 | 无（资源操作经请求 `?agent_id=` 显式指定域） |
| `agent_admin` | 智能体超管，管理本智能体组 | 有（资源域强制锁定自身归属） |
| `admin` | 组内普通管理员，管理本组技能/MCP/KB/日志 | 有 |
| `user` | 普通用户（`users.tags` 软关联智能体） | 无（`tags[{key:"agent", value:<agent_id>}]`） |

- **用户-智能体软关联**：`users.tags` JSONB；authsvc `AgentScope()` 解析为归属域。
- **管理员判定**：`identity.IsAdminRole`（super_admin/agent_admin/admin 三者皆管理员）。
- **门户登录归属校验**（阶段3 收尾，防跨域改绑越权）：管理员经智能体门户 `POST /v1/auth/login/{agent_id}` 登录时，`AgentScope()` 必须与 `agent_id` 一致，否则 `403 PERMISSION_DENIED`（"该账号不归属于智能体 X，请核对智能体 ID 或改用管理员入口"）；`super_admin`（无归属）禁止走门户入口，强制走管理员入口；普通用户首次经门户登录自动补写 `agent` 标签不变。
- **管辖边界**：`/v1/admin/users` 仅 super_admin + agent_admin；`/v1/admin/agents` 仅 super_admin；`/v1/admin/logs` 全员（域锁定见 admin 文档 §4.2）。
- **历史迁移**（auth 迁移 000004）：旧的唯一 `admin` 账号自动升级为 `super_admin`，并建 `agents` 表（agent_id/name/owner_user_id）承载智能体注册表。

---

## 5. 模块：智能体会话 agent（✅ 已实现）

### 5.1 模块定位

会话与对话：创建/管理会话、保存消息历史、调用大模型回答（支持工具调用与流式输出）。

- **属主隔离**：每个会话都属于某个 user_id（由 gateway 注入，前端传不了别人）；**访问非本人会话一律返回 404**，不暴露"存在/不存在"。
- **工具调用**：agent 内置 `echo` / `calculator` 两个示例工具（framework 的 Tool 机制），消息里的 `tool_calls` 以 JSON 字符串返回。
- **流式**：`/chat/stream` 走 SSE，逐 token 推送（打字机效果）。

### 5.2 接口列表

| 方法 | 路径 | 认证 | 作用 |
|---|---|---|---|
| POST | `/v1/agent/sessions` | ✅ | 创建会话 |
| GET | `/v1/agent/sessions` | ✅ | 会话列表（分页，空会话不显示） |
| GET | `/v1/agent/sessions/{id}` | ✅ | 会话详情 |
| PATCH | `/v1/agent/sessions/{id}` | ✅ | 更新会话（重命名 和/或 配置） |
| DELETE | `/v1/agent/sessions/{id}` | ✅ | 删除会话 |
| GET | `/v1/agent/tools` | ✅ | 列出默认可用工具集（配置开关用） |
| GET | `/v1/agent/kbs` | ✅ | 列出当前资源域知识库（对话配置区勾选用，普通用户可访问） |
| GET | `/v1/agent/sessions/{id}/messages` | ✅ | 会话消息历史 |
| DELETE | `/v1/agent/sessions/{id}/messages/{mid}` | ✅ | 删除一轮完整对话（删空自动删会话） |
| POST | `/v1/agent/sessions/{id}/messages/{mid}/regenerate` | ✅ | 重新生成该轮回答（多版本保留） |
| POST | `/v1/agent/sessions/{id}/messages/{mid}/version` | ✅ | 切换该轮活跃版本 |
| POST | `/v1/agent/sessions/{id}/messages/{mid}/branch` | ✅ | 基于该轮创建分支会话 |
| POST | `/v1/agent/sessions/{id}/chat` | ✅ | 对话（非流式） |
| POST | `/v1/agent/sessions/{id}/chat/stream` | ✅ | 对话（SSE 流式） |

### 5.3 创建会话

**作用**：新建一个空会话，返回会话对象。

**定义**：`POST /v1/agent/sessions`

**请求体**：

```json
{
  "title": "高等数学答疑",
  "config": {
    "enabled_tools": ["calculator", "get_current_time"],
    "thinking": { "enabled": true, "reasoning_effort": "high" }
  }
}
```

- `title` 可省略（省略则自动命名为"新对话"）。
- `config` 可省略：`enabled_tools` = 工具白名单（空/缺省 = 全部工具启用）；`thinking` = 思考模式（`enabled=false` 关闭思考；`reasoning_effort` ∈ low/high/max，缺省 = 厂商默认 high）；`mode` = 运行模式（`single` 单智能体 / `orchestrate` 多智能体编排，缺省 = `single`）。

**成功响应**（`201 Created`）：

```json
{
  "session": {
    "id": "12",
    "user_id": "1",
    "title": "高等数学答疑",
    "created_at": "2026-08-04T10:00:00+08:00",
    "updated_at": "2026-08-04T10:00:00+08:00",
    "config": {
      "enabled_tools": ["calculator", "get_current_time"],
      "thinking": { "enabled": true, "reasoning_effort": "high" }
    }
  }
}
```

### 5.4 会话列表

**作用**：分页拉取当前用户的会话（按最近更新倒序）。

**定义**：`GET /v1/agent/sessions?page=1&page_size=20`

**查询参数**：

| 参数 | 默认 | 说明 |
|---|---|---|
| `page` | 1 | 页码，从 1 开始 |
| `page_size` | 20 | 每页条数，上限 100 |

**成功响应**（`200 OK`）：

```json
{
  "sessions": [
    {
      "id": "12",
      "user_id": "1",
      "title": "高等数学答疑",
      "created_at": "2026-08-04T10:00:00+08:00",
      "updated_at": "2026-08-04T10:00:00+08:00"
    }
  ],
  "total": 1
}
```

### 5.5 会话详情

**作用**：获取单个会话。`{id}` 是整型会话 ID。

**定义**：`GET /v1/agent/sessions/{id}`

**成功响应**：`200 OK`，`{"session": { ...同上... }}`。

**失败**：`404 NOT_FOUND`（会话不存在或非本人）。

### 5.6 删除会话

**作用**：删除会话及其全部消息（幂等——重复删除仍返回成功）。

**定义**：`DELETE /v1/agent/sessions/{id}`

**成功响应**：`204 No Content`。

### 5.7 消息历史

**作用**：返回会话全部消息（seq 升序），前端用于历史回看与上下文恢复。

**定义**：`GET /v1/agent/sessions/{id}/messages`

**成功响应**（`200 OK`）：

```json
{
  "messages": [
    {
      "id": "56",
      "role": "user",
      "content": "帮我算一下 2+3",
      "reasoning": "",
      "tool_call_id": "",
      "tool_calls": "",
      "round_no": 1,
      "version": 0,
      "total_versions": 1
    },
    {
      "id": "57",
      "role": "assistant",
      "content": "",
      "reasoning": "用户要计算 2+3，我需要调用计算器工具",
      "tool_call_id": "",
      "tool_calls": "[{\"name\":\"calculator\",\"arguments\":\"{\\\"a\\\":2,\\\"b\\\":3}\"}]",
      "round_no": 1,
      "version": 0,
      "total_versions": 1
    },
    {
      "id": "58",
      "role": "tool",
      "content": "5",
      "reasoning": "",
      "tool_call_id": "call_1",
      "tool_calls": "",
      "round_no": 1,
      "version": 0,
      "total_versions": 1
    }
  ]
}
```

**字段**：

| 字段 | 说明 |
|---|---|
| `id` | 数据库主键（BIGSERIAL 转字符串），删除/定位用 |
| `role` | `user` / `assistant` / `tool` / `system` |
| `content` | 文本内容（assistant 纯工具调用时可能为空） |
| `reasoning` | assistant 消息的**思考内容**（DeepSeek `reasoning_content`，与 `content` 同级）；前端"思考过程"气泡数据源，也是工具轮回传上游的必需上下文 |
| `tool_call_id` | tool 消息关联的调用 ID（非 tool 消息为空串） |
| `tool_calls` | JSON 字符串数组（assistant 的工具调用计划）；空串 = 无 |
| `round_no` | 轮次序号（每轮从 user 提问开始；重生成/分支/版本切换定位用） |
| `version` | 该条回答的版本号（0=初始回答，重新生成递增） |
| `total_versions` | 该轮回答的版本总数（前端切换 UI 用） |

> **思考内容与工具上下文（重要）**：思考内容会被框架**持久化**并在后续请求中**回传给模型**。DeepSeek 官方规则：无工具调用的轮次回传会被忽略；**一旦本轮发生过工具调用，后续请求必须带上该轮 `reasoning_content`，否则返回 400**。因此框架统一保存并携带——模型在上下文里能看到自己真实的思考链与真实的工具返回结果，从机制上杜绝"幻觉声称调用了工具"。

> **轮次模型**：每轮 = 一次 user 提问 + 该轮 assistant 回答（含工具调用对）。一轮内多次重新生成 → 多个 `version`，同一时刻只有一个活跃版本展示在列表里（其余 `hidden`，数据保留可切换）。

### 5.8 删除一轮完整对话

**作用**：删除**整轮对话**（该轮 user 提问 + assistant 回答 + 工具调用对全部删除）——用于整段清理"无意义、污染上下文"的对话。删除后消息不再出现在历史加载与后续对话上下文中。

> 若删除后会话已**没有任何消息**，会话被自动删除（空会话不保留，列表不再出现）。

**定义**：`DELETE /v1/agent/sessions/{id}/messages/{mid}`

**路径参数**：

| 参数 | 说明 |
|---|---|
| `id` | 会话 ID（整型） |
| `mid` | 消息 ID（`messages` 接口返回的 `id` 字段，整型） |

**成功响应**：`204 No Content`。

**错误**：`40001`（参数非法）／`40401`（会话不存在或非本人）／`50001`（内部错误）。

### 5.9 重命名会话

**作用**：修改会话标题（侧栏显示）。标题 1~100 字符。

**定义**：`PATCH /v1/agent/sessions/{id}`

**路径参数**：`id` 会话 ID。

**请求体**（`title` 与 `config` 至少提供其一，可同时给出）：

```json
{
  "title": "高数答疑·定积分",
  "config": {
    "enabled_tools": ["get_current_time"],
    "thinking": { "enabled": false, "reasoning_effort": "max" }
  }
}
```

- `config.enabled_tools`：工具白名单，空 = 全部启用（当前默认工具集：`echo`、`calculator`、`get_current_time`、`kb_search`（装配了 rag 时），以 `GET /v1/agent/tools` 为准）。
- `config.thinking.enabled=false`：关闭思考模式（模型直接回答，不产生思考过程，省 token）。
- `config.thinking.reasoning_effort` ∈ `low | high | max`（deepseek-v4-pro 支持 high/max；flash 支持 low/high/max；缺省 = 厂商默认 high）。
- `config.kb_ids`（P3-A6 新增）：会话限定的知识库 ID 列表（用 `GET /v1/agent/kbs` 拉取当前域清单）；空/缺省 = 检索当前智能体域全部知识库。`kb_search` 按此限定默认检索范围（模型显式传 `kb_ids` 时优先）；rag 侧强制校验 kb 归属。

**成功响应**（`200 OK`）：返回更新后的会话对象：

```json
{
  "session": { "id": "12", "user_id": "1", "title": "高数答疑·定积分", "created_at": "...", "updated_at": "...",
    "config": { "enabled_tools": ["get_current_time"], "thinking": { "enabled": false, "reasoning_effort": "max" } } }
}
```

**错误**：`40001`（标题长度非法 / 配置非法：未注册工具名、非法 reasoning_effort、两者皆空）／`40401`（会话不存在或非本人）。

> **自动命名**：会话标题仍为"新对话"时，首轮对话结束后后端自动用首条用户消息（前 24 字符、换行压平）重命名，前端无需额外处理。

### 5.10 列出默认可用工具集

**作用**：返回当前默认工具集（名称 + 描述），供前端"会话配置"弹窗渲染工具开关（勾选 = 白名单）。

**定义**：`GET /v1/agent/tools`

**成功响应**（`200 OK`）：

```json
{
  "tools": [
    { "name": "echo", "description": "...", "external": false },
    { "name": "calculator", "description": "...", "external": false },
    { "name": "get_current_time", "description": "...", "external": false },
    { "name": "kb_search", "description": "检索知识库，带来源引用返回相关片段（装配 rag 时出现，L1 只读）", "external": false },
    { "name": "local_shell", "description": "...", "external": true }
  ]
}
```

**字段**：

| 字段 | 说明 |
|---|---|
| `name` | 工具名（Function Calling 调用名） |
| `description` | 工具描述（模型据此决定何时调用） |
| `external` | **阶段3 新增**：`true` = 本地工具，由宿主外部执行（如 `local_shell` 走桌面端执行 + 上行回填，见 5.16）；`false` = 服务器内置工具，agent 进程内/沙盒执行 |

### 5.10.1 列出当前资源域知识库

**作用**：返回当前资源域的知识库清单（轻量字段），供对话配置区"知识库"弹窗勾选（会话级 `kb_ids`）。
普通用户可访问（区别于 `GET /v1/admin/kb` 仅管理员）；**资源域由后端按用户身份锁定**——最高超管可随 `agent_id` 跟随切换的智能体，其它用户强制用自身 JWT 归属（防越权枚举其它域知识库）。

**定义**：`GET /v1/agent/kbs?agent_id=tutor`

**查询参数**：

| 参数 | 说明 |
|---|---|
| `agent_id` | 资源域（超管可指定，跟随切换智能体）；缺省/`*` = 默认域 `tutor`；其它用户忽略此参数 |

**成功响应**（`200 OK`）：

```json
{
  "agent_id": "tutor",
  "bases": [
    { "id": "kb_1", "name": "高数上册", "description": "课程讲义", "doc_count": 12 },
    { "id": "kb_2", "name": "物理实验", "description": "", "doc_count": 5 }
  ]
}
```

**错误**：`50300`（rag-service 未接入）／`40001`（非法的智能体 ID）。

> **会话级 kb_ids 语义**：`PATCH /v1/agent/sessions/{id}` 的 `config.kb_ids` 配置会话检索范围（空 = 检索当前域全部知识库）。`kb_search` 工具在模型未显式传 `kb_ids` 时，默认按会话配置的知识库检索；rag 侧强制校验 kb 归属（越出本域一律 404）。

### 5.11 重新生成该轮回答

**作用**：以该轮**之前**的上下文重新生成回答。旧版本**不删除**（数据保留、暂时隐藏），新回答作为新版本落库；该轮**之后**的消息也会暂隐藏（同一轮新版本对应的后续尚未生成）。用户可调用 5.12 切换展示哪个版本。

> 重新生成失败时自动回滚（恢复原版本与后续消息），不污染数据。

**定义**：`POST /v1/agent/sessions/{id}/messages/{mid}/regenerate`

**路径参数**：`id` 会话 ID；`mid` 该轮内任意消息 ID。

**成功响应**（`200 OK`）：

```json
{
  "content": "2 + 3 = 5（换一种解法）",
  "rounds": 2,
  "tool_calls": 1,
  "total_tokens": 180,
  "version": 1
}
```

`version` = 新生成版本号（初始回答为 0，每次重新生成递增）。

**错误**：`40401`（会话/消息不存在或非本人）／`50001`。

### 5.12 切换该轮活跃版本

**作用**：切换某轮展示的版本（重新生成过的轮次）。切换后该轮显示指定版本；若该版本对应不同的后续轮次，后续消息自动按该版本分支恢复。

**定义**：`POST /v1/agent/sessions/{id}/messages/{mid}/version`

**路径参数**：`id` 会话 ID；`mid` 该轮内任意消息 ID。

**请求体**：

```json
{
  "version": 0
}
```

`version` 为目标版本号（来自 `messages` 接口的 `version` 字段；0=初始回答）。

**成功响应**：`204 No Content`。

**错误**：`40001`（参数非法）／`40401`（会话/消息不存在或非本人）。

### 5.13 基于该轮创建分支会话

**作用**：把该轮（含）之前的所有可见历史复制到一个**新会话**（标题 = 原标题 + "（分支）"），用户可在新分支继续对话，不影响原会话。适合"想换个方向继续"的场景。

**定义**：`POST /v1/agent/sessions/{id}/messages/{mid}/branch`

**路径参数**：`id` 会话 ID；`mid` 分支点消息 ID（复制到该轮为止）。

**成功响应**（`200 OK`）：

```json
{
  "session": { "id": "99", "user_id": "1", "title": "高数答疑·定积分（分支）", "created_at": "...", "updated_at": "..." }
}
```

**错误**：`40401`（会话/消息不存在或非本人）／`50001`。

### 5.14 对话（非流式）

**作用**：一次问答，等待模型完整回答后一次性返回（适合不需要打字机的场景，如测试/批量）。

**定义**：`POST /v1/agent/sessions/{id}/chat`

**请求体**：

```json
{
  "content": "帮我算一下 2+3"
}
```

**成功响应**（`200 OK`）：

```json
{
  "content": "2 + 3 = 5",
  "rounds": 2,
  "tool_calls": 1,
  "prompt_tokens": 128,
  "completion_tokens": 45,
  "total_tokens": 173
}
```

**字段**：

| 字段 | 说明 |
|---|---|
| `content` | 最终回答文本 |
| `rounds` | 模型调用轮数（含工具调用往返） |
| `tool_calls` | 工具调用次数 |
| `prompt_tokens` / `completion_tokens` / `total_tokens` | 本次消耗的 token 统计（与 llm-gateway 计费对齐） |

**失败**：`404`（会话不存在）、`429`（配额超限）、`503`（上游不可用）。

### 5.15 对话（SSE 流式）

**作用**：逐 token 流式返回（打字机效果）。与 5.14 同参数，但响应为 SSE 事件流。

**定义**：`POST /v1/agent/sessions/{id}/chat/stream`

**请求体**：同 5.14 `{"content": "..."}`。

**响应头**：`Content-Type: text/event-stream`；`X-Accel-Buffering: no`（提示 Nginx 关闭缓冲）。

**事件格式**（每帧以空行分隔）：

```
data: {"type":"reasoning","content":"用户要计算 2+3，先调用计算器"}

data: {"type":"tool_call","name":"calculator","arguments":"{\"a\":2,\"b\":3}","tool_call_id":"call_00_x1y2z3"}

data: {"type":"tool_result","name":"calculator","content":"5","tool_call_id":"call_00_x1y2z3"}

data: {"type":"reasoning","content":"得到 5，直接回答"}

data: {"type":"delta","content":"2 + 3 = 5"}

event: done
data: {"type":"done","rounds":2,"tool_calls":1,"prompt_tokens":128,"completion_tokens":45,"total_tokens":173}

```

**事件类型**：

| 事件 | 含义 | data 字段 |
|---|---|---|
| `reasoning` | 思考内容增量（`reasoning_content`，与 `delta` 独立到达） | `{type, content}` |
| `delta`（默认事件） | 回答文本增量（打字机效果） | `{type, content}` |
| `tool_call` | 一次工具调用开始（参数已按 index 拼装完整；**`tool_call_id` 用于本地工具执行后回填，见 5.16**） | `{type, name, arguments, tool_call_id}` |
| `tool_result` | 工具执行返回（`error` 非空 = 执行失败；`tool_call_id` 与对应 `tool_call` 一致） | `{type, name, content, error, tool_call_id}` |
| `task_status` | 多智能体编排进度（仅 `mode=orchestrate` 触发）：子任务开始/结束、整体完成/失败 | `{type, task_type, task_id, status, error, total_tokens}` |
| `done` | 正常结束 | `{type, rounds, tool_calls, prompt_tokens, completion_tokens, total_tokens}` |
| `error` | 流中出错 | `{message}` |

> **顺序语义**：事件按模型真实的"想→做→想"循环到达——`reasoning`（思考）→ `tool_call`（决定调用）→ `tool_result`（真实返回）→ `reasoning`（继续思考）→ … → `delta`（最终回答）。前端据此渲染"思考过程"折叠气泡（工具调没调、返回什么由真实执行事件决定，杜绝幻觉）。
>
> **编排模式语义**：`mode=orchestrate` 时不产生 `reasoning`/`tool_call`/`tool_result`（子任务是纯文本角色），改为持续下发 `task_status` 事件。`task_type` ∈ `task_started` / `task_finished` / `run_completed` / `run_failed`；`status` ∈ `running` / `completed` / `failed` / `skipped`。前端据此渲染子任务节点状态流（research→outline→content→review），最终回答仍以一次 `delta` + `done` 收尾。

**心跳**：每 **15 秒**发一行注释 `: keepalive`（可忽略，仅用于保活防代理掐断）。

**前端解析要点**（web 端 `src/lib/sse.ts` 已实现）：
- 按 `\r?\n\r?\n` 切帧，逐行处理 `event:` / `data:`；
- `data:` 是 JSON 行，按 `type` 分发；`error` 事件即终止；
- 收到 `done` 后连接结束；连接空置超时（前端设 30s 兜底）需自行断开。

**失败**：SSE 头发出后无法改状态码，**流建立前的错误以 `error` 事件形式下发**（`data: {"message":"..."}`）；建立后的网络错误同前。

### 5.16 回填外部工具执行结果（本地工具，阶段3）

**作用**：agent 调用 **external 本地工具**（`/v1/agent/tools` 中 `external=true`，如 `local_shell`）后，会话会**挂起等待结果**。桌面客户端收到 SSE 的 `tool_call` 事件（带 `tool_call_id`）→ 弹出确认弹窗 → 在本机执行 → 调用本接口回填结果，唤醒 agent 继续推理。

**定义**：`POST /v1/agent/sessions/{id}/tool-results`

**路径参数**：`id` 会话 ID。

**请求体**：

```json
{
  "tool_call_id": "call_00_iqR84MYK0xIl1jWrOSM28468",
  "content": "hello-e2e",
  "is_error": false
}
```

**字段**：

| 字段 | 说明 |
|---|---|
| `tool_call_id` | 必填。来自 SSE `tool_call` 事件的 `tool_call_id` |
| `content` | 工具执行结果文本（stdout+stderr+退出码，失败时可为错误说明） |
| `is_error` | 可选，默认 `false`。`true` = 执行失败（如命令非零退出 / 被拒） |

**成功响应**：`204 No Content`。

**错误**：`40001`（缺 `tool_call_id`）／`40401`（会话不存在/非本人/**该工具调用未挂起或已超时**，前端提示"该工具调用已过期"）。

> **设计要点**：
> - agent 挂起超时默认 120s（`AgentConfig.ExternalExecTimeout`，框架层可配），超时未回填则本次工具调用按失败结束，会话继续；
> - **属主校验**：非本用户回填统一返回 NotFound（防枚举探测他人会话）；
> - **幂等**：投递成功后挂起项即被移除，重复回填走 NotFound；
> - 浏览器环境无桌面端时，前端收到本地工具 `tool_call` 会**立即回填失败**（提示"请使用桌面客户端"），避免等待超时。

---

## 6. 其他模块

### 6.1 llm-gateway（✅ 已实现，内网服务）

- 端口 `8083`，HTTP/OpenAI 协议，**只有 agent-service 通过内网调用**，前端不要直接连。
- 职责：统一接 DeepSeek（真实 `DEEPSEEK_API_KEY` 只存在这里）、用量落库、token 月配额、成本统计。
- 需要真实密钥的联调密钥注入方式：环境变量 `DEEPSEEK_API_KEY`（**严禁写死在代码里**）。

### 6.2 knowledge / rag

- **knowledge**（8084）：文档/课件知识库管理（🚧 规划中，未实现）。
- **rag**（8085）：✅ 已实现（P3）。检索增强生成：pgvector 混合检索（向量 + 关键词 RRF 融合），知识库/文档管理走 `/v1/admin/kb/*`（见 `api/admin.md`）。agent 装配了 `kb_search` 工具（见 5.10），对话时模型可主动检索知识库——装配由 agent 侧环境变量 `AGENT_RAG_ADDR`（如 `rag:8085`）控制，非空才拨号装配；rag 不可用时降级不阻断对话。

---

## 7. 运维端点

| 方法 | 路径 | 认证 | 作用 |
|---|---|---|---|
| GET | `/healthz` | 免 | 健康检查（含版本号），Docker healthcheck 依赖它 |
| GET | `/v1/openapi.yaml` | 免 | OpenAPI 契约文件（机器可读） |
| GET | `/swagger/ui` | 免 | Swagger UI 在线调试页 |

---

## 附录：联调环境速查

1. 后端一键起：`docker compose -f deploy/docker-compose.yml up -d`（或本机 `go run ./cmd/gateway` 等逐个起）。
2. 冒烟脚本：`powershell -File scripts/smoke.ps1`（10 步全链路自检，`-SkipModel` 跳过真实模型调用省钱）。
3. 前后端联调：后端 `:8080` + web `:3000`（`VITE_API_BASE_URL` 默认指向 8080）。
4. 桌面端联调：`cd desktop && npm run tauri dev`（自动复用 web 构建产物）。
5. **CORS 白名单**：由 `deploy/.env` 的 `GATEWAY_CORS_ORIGINS` 控制（web dev `localhost:3000`、Tauri dev `localhost:1420/3001`、**桌面端打包版 `http(s)://tauri.localhost`** 四者都在默认值里）。修改后需重启 gateway 容器（`docker compose up -d gateway`）生效。**漏配桌面端 origin 时桌面端所有请求被浏览器拦截，表现为"无法连接服务器（http://localhost:8080）"**。
