# web 接口文档

> 阅读对象：需要在 web 项目里开发功能、或想在其它前端复用这套封装的开发者。
> web 是纯前端（React），它**不对外提供 HTTP 服务**——它消费 backend 的接口（见 [backend.md](./backend.md)）。
> 本文讲的"接口"是 web 内部暴露给组件使用的**模块级 API**（函数/Store/事件）。
>
> 状态标记：✅ 已实现（P2-G 交付，测试全绿）

---

## 1. 子项目定位：什么时候需要 web？

web 是用户在浏览器里使用的**网页客户端**：登录/注册、会话侧栏、聊天（SSE 打字机）。技术栈 **React 19 + TypeScript(strict) + Vite 8 + Tailwind v4 + zustand**。

- 开发运行：`cd web && npm run dev` → `http://localhost:3000`。
- 它只对接 gateway `:8080`（`VITE_API_BASE_URL` 可改）。
- **desktop（Tauri）复用同一套代码**：web 检测到 Tauri 环境会自动切换 token 存储后端，无需改代码。

一句话：**web 是前端的唯一代码库，浏览器和桌面端共用。**

## 2. 模块概览

| 模块 | 文件 | 职责 | 状态 |
|---|---|---|---|
| **服务器地址** | `src/lib/settings.ts` | 服务器地址默认+自填：读/写/校验，api 与 sse 动态读取 | ✅ |
| **API 客户端** | `src/lib/api.ts` | 调后端 HTTP：axios 封装、token 注入、401 自动刷新 | ✅ |
| **SSE 客户端** | `src/lib/sse.ts` | 流式对话：fetch + ReadableStream 手动解析 | ✅ |
| **token 存储** | `src/lib/storage.ts` | 双后端：Tauri 安全存储 / localStorage 回退 | ✅ |
| **类型定义** | `src/types/api.ts` | 与后端 JSON 一一对应的 TS 类型 | ✅ |
| **auth store** | `src/stores/auth.ts` | 登录态：user/status + 持久化 | ✅ |
| **chat store** | `src/stores/chat.ts` | 会话/消息/流式状态管理 + 复制/删除轮/重生成/分支/版本切换/重命名 | ✅ |
| **气泡操作区** | `src/components/chat/MessageActions.tsx` | 消息气泡下按钮区（复制/重生成/分支/删除/版本切换），scope 两分类标准化注册（every / last-assistant） | ✅ |
| **思考过程气泡** | `src/components/chat/ThinkingBlock.tsx` | 助手气泡上方可收起折叠块：思考文本 + 工具调用/返回可视化（颜色/字体区分，流式中展开、完成后自动收起；标题随内容动态显示"思考过程/工具调用/过程"，无思考也渲染统一占位"本次未产生思考过程"） | ✅ |
| **会话配置弹窗** | `src/components/chat/SessionConfigDialog.tsx` | 会话配置（工具权限白名单 + 思考模式开关/推理强度），保存走 PATCH | ✅ |
| **输入区** | `src/components/chat/ChatInput.tsx` | 输入区（Enter 发送/停止）+ InputAction 注册式动作骨架（含"会话配置"齿轮入口），新增输入功能按注册管线扩展 | ✅ |
| **记住密码** | `src/lib/remember.ts` | 跨端统一 localStorage（base64 仅混淆、非加密，XSS 风险已注释）；**按门户域隔离**存储（`agent.remembered_credentials.<agentId>`） | ✅ |
| **游客会话 ID** | `src/lib/guest.ts` | 游客 UUID 生成/读取/清除（getGuestId/hasGuestId/clearGuestId） | ✅ |
| **角色工具** | `src/lib/roles.ts` | 角色判定：isAdminRole / isSuperAdmin / canManageUsers（含测试） | ✅ |
| **侧栏（游客态）** | `src/components/chat/SessionSidebar.tsx` | 会话列表 + 底部用户区：游客隐藏退出、显示登录入口；管理员显示"管理端"盾牌 | ✅ |
| **登录页** | `src/pages/LoginPage.tsx` | 门户登录（地址即门户 `/login/:agentId`）+ 记住密码（按域）+ 服务器地址 + 返回游客模式入口 + 按角色跳转 | ✅ |
| **UI 组件** | `src/components/` | 页面级与通用组件 | ✅ |

---

## 3. 模块：服务器地址设置（`src/lib/settings.ts`，✅ 已实现）

### 3.1 模块定位

解决"客户端连哪台服务器"的问题。**默认连本机**（`http://localhost:8080`），用户可在登录页底部"服务器地址"区修改并持久化到 localStorage（Tauri 的 WebView2 同样持久）。**部署时可用 `VITE_API_BASE_URL` 构建变量覆盖初始默认值**（如打成面向学生的安装包时默认填部署机地址）。

- 读：`getServerUrl()`——api/sse **每次发请求时动态读取**，保存后立即生效，无需刷新。
- 写：`setServerUrl(url)`——去首尾空格/去尾部斜杠 + 校验必须以 `http://` 或 `https://` 开头，不合法抛错。

### 3.2 接口

| 常量/函数 | 类型 | 说明 |
|---|---|---|
| `DEFAULT_SERVER_URL` | `string` | 初始默认值：`import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'` |
| `getServerUrl()` | `() => string` | 读当前生效地址（localStorage 无则默认值） |
| `setServerUrl(url)` | `(url: string) => void` | 校验并持久化；非法地址抛 `Error` |

### 3.3 使用示例（登录页）

```tsx
const [serverUrl, setServerUrl] = useState(getServerUrl())
function handleSaveServer() {
  try { persistServerUrl(serverUrl); setServerMsg('已保存，立即生效') }
  catch (err) { setServerMsg((err as Error).message) }
}
```

> 注意：桌面端 WebView2 的 CSP 已放行任意 `http:`/`https:`（含 ws/wss），所以自填任意部署机地址不会被拦；若收紧 CSP 白名单，自填范围随之受限。

---

## 4. 模块：API 客户端（`src/lib/api.ts`，✅ 已实现）

### 4.1 模块定位

所有 HTTP 调用的统一出口。它把"带 token、出错重试、刷新续期"这些横切逻辑收敛在一处，**组件永远不需要关心 token**。

**内置横切能力**：
- 每个请求自动注入 `Authorization: Bearer <token>` + `X-Request-Id: <uuid>`。
- **baseURL 逐请求动态设置**（取 `getServerUrl()`），支持运行时切换服务器。
- 收到 401 → 用 refresh token **单飞行刷新**（多个请求并发 401 只刷一次）→ 重放原请求。
- 刷新失败 → 清 token 并派发 `AUTH_EXPIRED_EVENT`（全应用登出）。
- 统一错误类型 `ApiError`（含 `code`/`requestId`/`status`）。

另导出 `getApiBase(): string`（即 `getServerUrl()`），供 sse.ts 等需要当前 baseURL 的模块使用。

### 4.2 认证函数

#### `register(username, password, agentId?): Promise<User>`
- **作用**：注册新账号。**仅分智能体门户可用**（`agentId` 必填）——管理员账号由管理员创建，裸注册入口已下线。
- **参数**：`username`（3~32 位字母数字下划线）、`password`（≥8 位含字母和数字，前端先校验）、`agentId`（如 `tutor`，走 `/v1/auth/register/{agent_id}`）。
- **返回**：`{ id, username, role?, tags? }`。
- **失败**：`409`（用户名已占用）、`400`（格式不合法）。

#### `login(username, password, agentId?): Promise<LoginResponse>`
- **作用**：登录。
  - `agentId` 非空 → `/v1/auth/login/{agent_id}`（智能体门户，首次登录自动绑定该智能体标签）；
  - `agentId` 为空 → `/v1/auth/login`（**管理员入口，只放行 `role=admin`**；普通账号会收到 403"该入口仅限管理员登录，请使用对应的智能体门户入口"）。
- **返回**：`{ access_token, refresh_token, expires_in, user }`。**调用方负责把令牌交给 auth store**（`applySession`），组件不直接碰 token。

#### `logout(refreshToken): Promise<void>`
- **作用**：吊销 refresh 族（后端立即失效）。失败时**忽略错误**（本地也要清）。

#### `fetchMe(): Promise<User>`
- **作用**：取当前用户（app 启动恢复登录态用）。

#### `refreshToken(): Promise<RefreshResponse>`
- **作用**：主动刷新令牌对（一般不需要直接调，401 拦截器已覆盖）。

### 3.3 会话函数

| 函数 | 作用 | 返回 |
|---|---|---|
| `createSession(title?)` | 新建会话 | `Session` |
| `listSessions(page?, pageSize?)` | 分页会话列表（空会话不返回） | `{ sessions, total }` |
| `getSession(id)` | 会话详情 | `Session` |
| `renameSession(id, title)` | 重命名会话（1~100 字符） | `Session` |
| `deleteSession(id)` | 删除会话（幂等） | `void` |
| `fetchMessages(id)` | 会话消息历史（seq 升序，含 `id`/`round_no`/`version`/`total_versions`） | `HistoryMessage[]` |
| `deleteMessage(sessionId, messageId)` | 删除一轮完整对话（后端删整轮 + 工具对；删空自动删会话） | `void` |
| `regenerate(sessionId, messageId)` | 重新生成该轮回答（旧版本保留） | `RegenerateResult`（含 `version`） |
| `setActiveVersion(sessionId, messageId, version)` | 切换该轮活跃版本 | `void` |
| `createBranch(sessionId, messageId)` | 基于该轮创建分支会话 | `Session` |
| `chat(id, content)` | 非流式对话 | `ChatResult`（含 token 统计） |
| `submitToolResult(sessionId, toolCallId, content, isError?)` | **阶段3·本地工具回填**：把本地工具执行结果发给后端，唤醒挂起的会话（见 5.3） | `void` |

**`Session` 结构**：`{ id, user_id, title, created_at, updated_at, config }`（时间 RFC3339 字符串）。

**`SessionConfig` 结构**：`{ enabled_tools?: string[], thinking?: { enabled: boolean, reasoning_effort?: string } }`——`enabled_tools` 空/缺省 = 全部工具启用；`thinking.reasoning_effort` ∈ low/high/max，缺省 = 厂商默认 high。

**`ToolInfo` 结构**：`{ name, description, external? }`——`external` 阶段3 新增：`true` = 本地工具（走桌面端执行 + 回填，见 5.3）；缺省 `false`（服务器工具）。

**`HistoryMessage` 结构**：`{ role, content, tool_call_id, tool_calls, round_no, version, total_versions }`，`tool_calls` 是 JSON 字符串（空串=无）。

**`RegenerateResult` 结构**：`{ content, rounds, tool_calls, total_tokens, version }`。

**`ChatResult` 结构**：`{ content, rounds, tool_calls, prompt_tokens, completion_tokens, total_tokens }`。

### 3.4 事件

| 事件 | 触发时机 | 监听方式 |
|---|---|---|
| `AUTH_EXPIRED_EVENT` | refresh 失败，登录态彻底失效 | `window.addEventListener('auth-expired', ...)` |

`ProtectedRoute` 已监听该事件自动登出并跳转登录页。

---

## 5. 模块：SSE 客户端（`src/lib/sse.ts`，✅ 已实现）

### 5.1 模块定位

实现流式对话客户端。**不用原生 `EventSource` 的原因**：它无法携带自定义 `Authorization` 头（我们的 token 走 Bearer）。因此用 `fetch` + `ReadableStream` 手动解析 SSE 帧。

### 5.2 接口

#### `streamChat(sessionId, content, handlers, signal?): Promise<void>`

- **作用**：发起流式对话，按事件回调。
- **参数**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `sessionId` | string | 目标会话 ID |
| `content` | string | 用户消息 |
| `handlers.onDelta` | `(content: string) => void` | 回答文本增量回调（累加即打字机） |
| `handlers.onReasoning` | `(content: string) => void` | **思考内容增量**（DeepSeek `reasoning_content`，累加成"思考过程"气泡） |
| `handlers.onToolCall` | `(toolCallId: string, name: string, arguments: string) => void` | **工具调用开始**（参数已按 index 拼装完整；**`toolCallId` 阶段3 新增**——本地工具回填凭据，见 4.3） |
| `handlers.onToolResult` | `(toolCallId: string, name: string, content: string, error: string) => void` | **工具执行返回**（`error` 非空 = 失败；`toolCallId` 与对应调用一致） |
| `handlers.onDone` | `(d: SSEDoneEvent) => void` | 流结束回调（含 `rounds/tool_calls/tokens` 统计） |
| `handlers.onError` | `(e: {message: string}) => void` | 流中错误回调 |
| `signal` | `AbortSignal?` | 传入可中断（"停止生成"按钮） |

- **内置行为**：30s 空闲超时兜底断开（`onError` 提示）；用户 `abort` 静默返回（不触发 `onError`）；**401 自修复**——收到 401（access token 过期）时自动刷新 token 并重试一次（刷新失败则抛错，错误由调用方提示，不写入对话）。
- **事件顺序语义**：`reasoning`（思考）→ `tool_call`（决定调用）→ `tool_result`（真实返回）→ `reasoning`（继续思考）→ … → `delta`（最终回答），即模型真实的"想→做→想"循环；工具调没调、返回什么由**真实执行事件**决定，杜绝模型幻觉。

**示例**（chat store 里的用法）：

```ts
await streamChat(
  sessionId,
  content,
  {
    onDelta: (d) => appendContent(d),          // 打字机
    onReasoning: (d) => appendThinkingText(d), // 思考气泡增量
    onToolCall: (toolCallId, name, args) => pushSegment({ kind: 'tool-call', toolCallId, name, arguments: args }),
    onToolResult: (toolCallId, name, content, error) => pushSegment({ kind: 'tool-result', toolCallId, name, content, error: !!error }),
    onDone: (d) => set({ content, stats: d, streaming: false }),
    onError: (e) => set({ error: e.message, streaming: false }),
  },
  signal,
)
```

---

## 5.3 本地工具代理（阶段3，`src/lib/localTools.ts` + `src/components/chat/LocalToolModal.tsx`，✅ 已实现）

### 模块定位

外部工具（`external=true`，当前仅 `local_shell`）由**桌面客户端**在本机执行。前端链路：SSE 收到 `tool_call`（带 `tool_call_id`）→ 若是本地工具 → `handleLocalToolCall` 分流：

- **浏览器环境**：无 Tauri 运行时 → 直接 `submitToolResult(sessionId, toolCallId, '请使用桌面客户端...', true)` 回填失败，agent 不会挂起 120s 等桌面端；
- **桌面环境 + 非自由模式**：弹 `LocalToolModal` 确认弹窗（命令全文展示）→ 用户点"允许"→ `invoke('local_shell_execute', { command, cwd })` 本机执行 → `submitToolResult` 回填 `{content, is_error}` 唤醒会话；点"拒绝"→ 回填失败结果；
- **桌面环境 + 自由模式**（本地个人化开关 `agent.free_mode`，仅桌面端显示，每次开启都弹风险警告）：跳过确认、直接执行，且不限超时（`runLocalShell` 传 `timeoutSecs=-1`）。

### 接口

| 导出 | 作用 |
|---|---|
| `LOCAL_TOOL_NAMES: Set<string>` | 本地工具名集合（当前 `{local_shell}`） |
| `isTauri(): boolean` | 是否运行在 Tauri 环境（`'__TAURI_INTERNALS__' in window`） |
| `runLocalShell(command, cwd?, timeoutSecs?)` | 动态 import `@tauri-apps/api/core` 调 `local_shell_execute`；`timeoutSecs`：`>0` 强制超时 / `0` 默认 / `-1` 不限超时（自由模式） |
| `isFreeMode() / setFreeMode(on)` | `src/lib/freeMode.ts`：读写本地偏好 `agent.free_mode`（仅本机、与服务器/角色无关） |
| `FreeModeDialog` | `src/components/chat/config/FreeModeDialog.tsx`：配置按钮区注册的弹窗（仅桌面端 visible），开启时弹窗内风险提示 + 二次确认 |
| `useChatStore.handleLocalToolCall(toolCallId, name, args)` | chat store 导出：解析参数 → 浏览器降级回填 / 桌面弹窗（自由模式直接执行） |
| `useChatStore.resolveLocalCall(allow)` | 桌面确认回调：执行 + 回填（或拒绝回填） |

### 组件

`LocalToolModal`（挂在 `ChatPage`，仅 Tauri 环境 + 有待确认本地调用时显示）：展示工具名、完整命令（只读代码块）、"拒绝 / 允许"两按钮；`finally` 清空挂起项。

### 事件触发点

chat store 的 `streamChat` handler `onToolCall` 中检测 `LOCAL_TOOL_NAMES.has(name)` → 调用 `handleLocalToolCall`；`onToolResult` 带 `tool_call_id` 用于工具段关联。

---

## 6. 模块：token 存储（`src/lib/storage.ts`，✅ 已实现）

### 6.1 接口

| 函数 | 作用 |
|---|---|
| `getAccessToken(): Promise<string \| null>` | 读 access token |
| `getRefreshToken(): Promise<string \| null>` | 读 refresh token |
| `setTokens(access, refresh): Promise<void>` | 写令牌对 |
| `clearTokens(): Promise<void>` | 清空令牌 |
| `isTauri(): boolean` | 是否运行在 Tauri 环境（`__TAURI_INTERNALS__`），供 UI 判断是否显示"退出应用"等桌面专属按钮 |

### 6.2 行为说明

- **双后端自动切换**：检测到 Tauri 环境（`__TAURI_INTERNALS__`）→ 走 Rust 命令读写应用配置目录；否则 → localStorage。
- 所有函数**异步**（Tauri IPC 是异步的）；浏览器回退路径内部仍是同步 localStorage，行为一致。
- 组件层永远走 `auth store` 或 `api.ts`，一般**不需要直接调 storage**。

---

## 7. 模块：状态管理（stores，✅ 已实现）

### 7.1 `auth store`（`src/stores/auth.ts`）

| 成员 | 类型 | 说明 |
|---|---|---|
| `user` | `User \| null` | 当前用户 |
| `status` | `'loading' \| 'authed' \| 'guest'` | 登录态 |
| `applySession(access, refresh, user)` | async | 登录成功后写入令牌与用户 |
| `logout()` | async | 吊销 refresh + 清本地 |
| `hydrate()` | async | app 启动时恢复登录态（有 refresh → 拉 /me） |

持久化：`user`/`status` 经 zustand `persist` 存 localStorage（key `agent.auth`）。

### 7.2 `chat store`（`src/stores/chat.ts`）

| 成员 | 类型 | 说明 |
|---|---|---|
| `sessions` / `sessionsTotal` | `Session[]` / `number` | 会话列表与总数 |
| `activeId` | `string \| null` | 当前会话 |
| `messages` | `ChatMessage[]` | 当前会话消息（含流式中间态） |
| `sending` | `boolean` | 是否正在流式输出 |
| `regeneratingId` | `string \| null` | 正在重新生成的消息 ID（按钮 loading 态） |
| `selectSession(id)` | async | 切换会话并加载历史（并行刷新会话列表） |
| `sendMessage(content)` | async | 发消息（无会话先建会话 → 流式增量更新 → done 后重拉历史回填 serverId → 刷新列表）；**失败移除 assistant 占位气泡，错误不进对话**；流式期间 `onReasoning`/`onToolCall`/`onToolResult` 实时累积"思考过程"分段 |
| `deleteMessage(id)` | async | 删除一轮完整对话；删空后会话自动从列表移除（后端已软删，拉历史 404 视为空会话，不报错） |
| `copyMessage(id)` | async | 复制消息纯文本到剪贴板 |
| `regenerateMessage(id)` | async | 重新生成该轮回答（隐藏旧版、生成新版，成功后重拉历史） |
| `switchVersion(id, version)` | async | 切换该轮活跃版本并重拉历史 |
| `branchMessage(id)` | async | 基于该轮创建分支会话并跳转 |
| `renameSession(id, title)` | async | 重命名会话并更新侧栏标题 |
| `stopStreaming()` | - | 中止当前流并结束"流式中"状态（保留已收内容） |

**`ChatMessage` 结构**：`{ role, content, serverId?, toolCallId?, toolNames?, thinking?, status?, stats?, roundNo?, version?, totalVersions? }`——`serverId`（后端主键，仅历史消息有）决定消息是否可操作；`thinking?: ThinkingSegment[]`（"思考过程"分段：`{kind:'text'}` 思考文本 / `{kind:'tool-call',name,arguments}` 工具调用 / `{kind:'tool-result',name,content,error}` 工具返回，按"想→做→想"顺序）；`roundNo`/`version`/`totalVersions` 由历史消息透传，供重生成/分支/版本切换使用。

> **历史回放合并**：一轮工具对话在后端是 4 条消息（user → assistant(带 tool_calls) → tool → assistant 最终回答），chat store 的 `fromHistory` 会把中间 assistant 的思考+工具调用与 tool 消息的工具返回**合并进最终 assistant 的 `thinking` 分段**（与流式事件到达顺序一致），渲染成一个"思考过程 + 一个回答"。

### 7.3 气泡操作区（`src/components/chat/MessageActions.tsx`，✅ 已实现）

消息气泡下方的**标准化按钮区**（用户与助手气泡共用），按钮只有两种 **scope**（见 `MessageActionScope`，后期新增按钮也只会有这两种）：

- **`every`（所有气泡都显示）**：复制、删除本轮。
- **`last-assistant`（只在最后一条助手气泡下显示）**：重新生成、分支——它们基于"当前上下文末端"工作，中间轮次展示无意义，主流智能体均如此。

**标准化开发管线**：新增一个气泡按钮 = 定义一个 `MessageActionDef`（`{ key, icon, label, scope, visible?, disabled?, loading?, onClick }`）并注册进 `buildActions`；组件统一负责渲染与 scope 过滤（`every` 恒显示，`last-assistant` 叠加"本消息是最后一条助手气泡"判断 + `visible` 附加条件）——**改功能不动 UI，改 UI 不动业务**，业务全部在 chat store。

- **版本切换**：assistant 且 `totalVersions > 1` 时出现下拉（v 1/n），切换调用 `switchVersion`。
- 自定义操作集可通过 `actions` prop 覆盖默认（组件预留扩展点）。
- **新对话气泡同样可操作**：流式正常完成（收到 done 事件）后，前端重拉历史回填 `serverId`/`roundNo`/`version`，因此新产生的气泡同样具备删除/重生成/分支能力；用户主动停止时后端不落库，不刷新（保留已显示内容）。

### 7.4 思考过程气泡（`src/components/chat/ThinkingBlock.tsx`，✅ 已实现）

助手气泡上方的**可收起过程折叠块**（需求 9），渲染模型"想→做→想"循环的完整过程。折叠块**标题随内容动态变化**：有思考文本 → "思考过程"；关闭思考但发生了工具调用 → "工具调用 · N 次"（不再误导为思考）；都没有 → "过程"。

- **分段渲染**（颜色/字体明显区分，一眼可辨工具调没调）：
  - `text` 思考文本：灰色小字，与回答正文区分；
  - `tool-call` 工具调用：**琥珀色块**（"工具调用 · name" + 参数 JSON 美化），由真实 `tool_call` 事件/历史渲染；
  - `tool-result` 工具返回：**绿色块**（成功）/ **红色块**（`error`，标注"失败"），由真实 `tool_result` 事件/历史渲染。
- **展开/收起**：流式中（`streaming=true`）保持展开并显示 spinner，实时展示思考与工具过程；**思考完成 + 对话完成（`streaming` 变 false）自动收起**，用户可点击头部展开查看。
- **无思考统一占位**：无思考且非流式时显示"本次未产生思考过程"（而非整块消失，观感统一）；流式中暂无分段显示"正在思考…"。
- **数据源**：流式 = chat store 累积 `onReasoning`/`onToolCall`/`onToolResult` 事件；历史回放 = `fromHistory` 合并后端 `reasoning`/`tool_calls`/tool 消息。
- **props**：`segments: ThinkingSegment[]`、`streaming?: boolean`。

### 7.5 富媒体渲染协议（`src/components/chat/RichContent.tsx`，需求 9，✅ 已实现）

**设计思想**：前端与后端约定"渲染协议"，协议写入系统提示词（backend `agentsvc/prompt.go` 的 `BuildSystemPrompt`）——模型知道"前端用什么框架、有哪些图表、怎么给数据设属性"，按约定格式输出，前端只按协议渲染。**无需为每种图表单独适配代码**，新图表类型 = 模型输出标准 ECharts option，前端零改动。

助手消息正文走 `RichContent`（react-markdown + remark-gfm + **remark-math** + rehype-raw + **rehype-katex** + **rehype-sanitize 受限 HTML 白名单**），协议：

| 内容 | 模型输出格式 | 前端渲染 |
|---|---|---|
| 富文本 | 标准 Markdown（GFM：标题/加粗/斜体/列表/表格/引用/链接/图片）；对齐/字体等用受限 HTML（`<p align="center">`、`<span style="color:#888">`） | Markdown 原生渲染；HTML 经 sanitize 白名单（仅 `align`/`style`/`className` 及 `center`/`video`/`audio`/`figure` 等，防 XSS） |
| 公式 | LaTeX：行内 `$x^2$`、独立 `$$…$$` | **KaTeX**（remark-math + rehype-katex）；**白名单需放行 MathML 标签**（math/mrow/mfrac…，否则公式数学结构被剥） |
| 图表 | ````echarts` 代码块 + 标准 ECharts option JSON | **ECharts 按需注册 + 懒加载**（`echarts/core` 只注册 bar/line/pie/radar/heatmap/scatter/funnel/gauge/treemap/graph/sankey/candlestick/boxplot + 常用组件；`React.lazy` 代码分割，仅出现图表块才下载 chunk）；非法 JSON 显示"解析失败"占位；流式中 JSON 未完整显示"生成中"占位；ResizeObserver 自适应容器；右上角"下载图表图片"（`getDataURL` 白底 2x PNG） |
| 内联矢量图 | ````svg` 代码块 + 完整 SVG 代码 | `InlineSVG`：`sanitizeSVG` 剥离 script/`on*` 事件/`javascript:` 链接后内联渲染（净化防注入）；右上角"下载 SVG"（序列化回文本，保留矢量可编辑） |
| 图片 | Markdown `![描述](url)`，url 可为网络地址或本地文件路径 | `<img loading="lazy">` + 下载按钮（`downloadUrl`：fetch 转 Blob 下载，跨域无 CORS 退化新窗口打开） |
| 视频 | Markdown `![描述](url.mp4)`（url 以 `.mp4/.webm/.ogg/.mov/.m4v` 结尾）或 ````video` + url 代码块 | 自动 `<video controls>` + 下载按钮；`isVideoUrl` 忽略查询串/锚点 |
| 媒体尺寸与对齐 | 图片/视频：HTML 属性控制——`<p align="center"><img src="…" width="360" /></p>`、`<video src="…" width="480" controls>`；echarts：option 顶层 `"__media": {"width": 520, "height": 300, "align": "center"}`；svg：根元素 `width`/`height` + 代码块标签 ````svg align=center```` | img/video 透传 `width/height/style`；`<p align>` 包裹居中（渲染器为 inline-block 尊重 text-align）；EChart 剥离 `__media` 后 setOption（宽高/居中生效）；InlineSVG 按对齐加 `text-center`/`text-right` 块级包裹（`rehypeMoveCodeMeta` 把代码块 `meta` 从 `data` 移到 `properties` 绕过 sanitize 丢 data 的问题） |

- **XSS 防线**：HTML 白名单（只放行对齐/样式/媒体/公式最小集合）+ SVG 净化双保险；`style`/`className` 允许但标签受控。
- **宽松 JSON 容错**（`parseEChartsOption`，应对模型输出习惯）：严格 `JSON.parse` 失败后，字符串感知地剥离 `//` 与 `/* */` 注释、尾逗号、单引号字符串再试——**不误伤字符串内 `http://`**（状态机跳过字符串字面量）；仍失败显示"解析失败"占位。
- **测试**：`RichContent.test.tsx`（7 例：Markdown 富文本/下载按钮/图片视频/内联 SVG/echarts 非法 JSON/HTML 白名单/script 剥离/KaTeX）+ `rich.test.ts`（7 例：isVideoUrl/parseEChartsOption 严格与宽松 5 类/sanitizeSVG）；**懒加载在 jsdom 不 resolve → 测试里 `vi.mock('./EChart')`**。
- **代码块复制按钮（2026-08-14）**：普通代码块 pre 右上角悬浮 `CodeCopyButton`（非 echarts/svg/video/doc 专属块），点击复制原文，成功后图标切对勾 + `msg-copy-pop` pop 动画（1.2s 复位）；`MessageActions.tsx` 气泡复制按钮同款对勾动画（`copied` state + 1.2s 复位）；CSS 在 `index.css` `.msg-code-copy` / `@keyframes msg-copy-pop`。
- **CSS**：`index.css` `.rich-content` 块（h1-h6 字号字重、列表、表格边框+横向滚动、blockquote、引用、img/video 圆角）；KaTeX 样式来自 `katex/dist/katex.min.css`（字体随构建输出）。

**本地媒体渲染（内置工具集交叉项，backend 侧）**：浏览器无法直接读取服务器本地文件路径，agent HTTP 服务新增只读端点 `GET /files/<工作目录内相对路径>`（`cmd/agent/files.go`，与 file_ops 工具同边界、只服务文件不放行目录列表、CORS `*`）。配置 `AGENT_FILES_BASE_URL`（如 `http://localhost:8182`）后，系统提示词注入协议第 7 条，模型用 `![描述](http://localhost:8182/files/路径/图.png)` / `![视频](…/视频.mp4)` 输出本地媒体，前端 img/video 渲染**零改动**。

### 7.6 消息链接点击（外部链接打开，`src/lib/external.ts`，✅ 已实现）

**背景**：桌面端（Tauri WebView2）点击 `<a href>` 默认在当前 webview 内导航，整个界面会被目标网页替换（应用"消失"）。

- **规则**：`isExternalLink(href)` 判定 `http/https/mailto/tel` → 拦截默认跳转，改调 `openExternal(href)`：
  - Tauri 环境 → `invoke('open_external', { url })`，由 Rust 侧交给**系统默认浏览器**打开；
  - 浏览器环境 → `window.open(url, '_blank', 'noopener,noreferrer')` 新标签页。
- 相对路径/站内锚点保留浏览器默认行为。
- **同一出口**：图片/视频跨域下载失败的回退（`rich.ts downloadUrl`）也走 `openExternal`，不再直接 `window.open`。
- **测试**：`external.test.ts`（isExternalLink 6 例 + openExternal 2 例）+ `RichContent.test.tsx` 链接 4 例。

### 7.7 登录页：门户登录 + 服务器地址 + 游客模式（`src/pages/LoginPage.tsx`，✅ 已实现）

**门户化（阶段3，P2-Z 后）**：**地址即门户**——`/login/:agentId`（如 `/login/tutor`），无需输入智能体 ID；直达 `/login`（无参数）由 App 路由重定向默认门户 `/agent/tutor`（页面内再兜底一次 `Navigate`）。

- **注册**：仅普通门户显示注册入口（注册即该门户普通用户，走 `/v1/auth/register/{agent_id}`）；**超管门户 `/login/*` 隐藏注册**（超管账号仅由最高超管在管理端创建，后端亦拒绝 `*` 门户注册）；`*` 门户登录限全门户归属者（越权防护见 P2-Z）。
- **管理员归属校验**：管理员账号经门户登录必须归属该门户（后端校验，非本人 403"该账号不归属于智能体 X"）；super_admin（无归属）禁止走门户入口，强制走管理员入口 `/v1/auth/login`。
- **记住密码**（`src/lib/remember.ts`，P2-AA/AC 后）：**统一走 localStorage**（base64 仅混淆、**非加密**，XSS 泄露风险已注释——浏览器无法接入系统凭据库）；**按门户域隔离**存储（`agent.remembered_credentials.<agentId>`，含 `*` 域），旧单域 key 一次性迁移；挂载时按当前域回填用户名/密码并勾选，取消勾选清除当前域凭据。
- **按角色跳转（落地角色归属会话域，P2-AG）**：已登录/登录成功后用 `isAdminRole` 判定——管理员 → `/agent/{getHomeScope(user)}` 角色归属会话域（`super_admin` → `/agent/*` 全部域；`agent_admin` / `admin` → `/agent/{绑定域}`）；普通用户 → `/agent/{agentId}` 对应智能体门户；登录成功后先合并游客会话（`mergeGuestSessions`，失败不阻断）。修复背景：原一律跳 `/admin/chat`（管理端域）与管理员会话实际归属脱节，曾致"登录后首屏空列表、需手动切 `*` 域才出现"。
- **归属域兜底（`getHomeScope`，方案 C）**：`src/lib/roles.ts` 新增 `getHomeScope(user)`——超管→`*`、agent_admin/admin→绑定域、普通用户/游客→`''`。管理端对话域（`ChatPage mode="admin"`）无记忆时按它回退会话域（`loadRememberedAgent() || getHomeScope(user)`）；`chat.ts createSession` 对管理端空串域按角色归属回退（超管→`*`→默认域 tutor、绑定域管理员→绑定域），**不再产生 `agent_id=''` 的孤儿会话**。测试：`roles.test.ts`（getHomeScope 4 例）+ `chat.createSession.test.ts`（管理端空串回退 3 例）+ `ChatPage.test.tsx`（已登录超管/绑定域管理员回退 2 例）。
- **返回游客模式**：表单下方「以游客身份继续（不登录）」按钮 → `/agent/{agentId}`，本地游客会话不丢失（覆盖"从游客模式进登录页想返回"与"登录页兜底"场景）。
- **门户域预验（严格多租户域守卫）**：前端切换/进入 `/agent/:agentId` 前调用 `GET /v1/agent/domains/{id}`（`src/lib/agent.ts getDomain`），孤儿域（`exists=false`）与已停用域（`status=0`）直接拒绝并提示"智能体不存在或已停用"；登录/注册遇后端 `404`（域不存在/未创建）或 `403`（域已停用）同样给出对应提示。
- **服务器地址**：`agent.server_url` 持久化（见第 3 节），保存立即生效。
- **测试**：`LoginPage.test.tsx`（表单/错误提示/门户注册显隐/服务器地址/记住密码域隔离）。

### 7.8 游客模式（阶段2，`ChatPage` / `SessionSidebar` / `ChatInput`，✅ 已实现）

- **触发**：未登录访问 `/agent/:agentId` 即为游客（`status === 'guest'`）；管理端域 `/admin/chat` 始终要求登录（AdminGuard）。
- **能力裁剪**：游客隐藏输入区配置按钮（`canConfigure={!isGuest}`）、侧栏底部显示"游客模式 + 登录"入口（**没有退出登录按钮**）；顶部游客提示条 + "登录/注册"入口（`/login/{agentId}`）。
- **对话**：`sse.ts` 无 token 时读 `getGuestId()` 注入 `X-Guest-ID` 请求头；后端 FNV-1a 派生负整数 user_id 落库（llm-gateway 已放行负值身份）；会话与真实用户隔离。
- **登录后合并**：`mergeGuestSessions(guestId)` 把游客会话归属到账号（失败不阻断登录）。
- **退出登录落地**：退出登录后仍是游客模式——智能体域留在原智能体继续对话、管理端落地默认 `/agent/tutor`。

---

## 8. 前端功能扩展标准（开发管线）

新增前端功能统一走两条**注册式管线**（数据驱动，组件零业务逻辑），避免"为每个功能单独写一套 UI 逻辑"导致代码膨胀与不一致。

### 9.1 输入区动作（`ChatInput.tsx` 的 `InputAction[]`）

新增一个输入区功能（如上传文件 / skill / mcp / 知识库）：

1. **后端先具备能力**：新增 RPC → gateway 暴露 HTTP → `src/lib/api.ts` 加请求函数 → chat store 加 action（新功能的数据/状态收在 store）；
2. **注册动作**：定义一个 `InputAction`（`key` 唯一 / `label` / `icon` / `onClick` / `visible` 可见条件）加入 `actions` 数组；
3. **次级 UI 组件化**：弹窗、文件选择器等由动作自身组件承载（参照 `SessionConfigDialog` 的 `key` 重挂载模式管理内部状态）。

### 9.2 气泡按钮（`MessageActions.tsx` 的 `MessageActionDef`）

新增一个气泡按钮：定义一个 `MessageActionDef` 并注明 `scope`（只有 `every` 与 `last-assistant` 两种），叠加 `visible` 附加条件；组件统一渲染与过滤。

> 两条管线的共同约定：**业务收在 store、定义收在注册表、组件只渲染**；新增功能只加定义，不动骨架。

---

## 9. 开发约定速查

1. 新增后端接口调用：先在 `src/types/api.ts` 定义类型 → 在 `api.ts` 加函数（走既有拦截器）→ 在 store/组件中使用。
2. 所有跨组件状态走 zustand，**不要用 props 层层透传**。
3. 组件测试用 vitest + Testing Library；新函数建议配测试（参考 `src/lib/sse.test.ts`）。
4. 主题变量在 `src/index.css`（`@theme inline`），深浅色自动切换。
