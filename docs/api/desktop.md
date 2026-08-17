# desktop 接口文档

> 阅读对象：需要在桌面端加能力（Rust 侧命令、窗口/托盘行为）、或在 web 里调用 Tauri 命令的开发者。
> desktop 是 **Tauri 2 + Rust 壳**，不写 UI——它加载 web 的构建产物（见 [web.md](./web.md)）。
> 本文讲的"接口"是 desktop 暴露给前端调用的 **invoke 命令**与系统集成行为。
>
> 状态标记：✅ 已实现（P2-H 交付，`cargo check` 零警告）；🚧 规划中，未实现

---

## 1. 子项目定位：什么时候需要 desktop？

desktop 是**桌面客户端外壳**：把浏览器里的 web 应用装进系统窗口，提供托盘常驻、系统通知，并把 token 存到浏览器够不到的位置。

- 前端完全复用 web/（dev 加载 `:3001`，打包嵌入 `web/dist`），**desktop 目录里没有任何 UI 代码**。
- 运行开发：`cd desktop && npm run tauri dev`（自动启动 web dev server(:3001) + 编译 Rust + 开窗口）。
- 打包：`npm run tauri build` → 生成安装包（Windows: NSIS）。

一句话：**web 负责"长什么样"，desktop 负责"像个桌面软件"。**

## 2. 架构与模块概览

```
web (React) ──invoke──▶ Rust 命令（desktop/src-tauri/src/commands.rs）
     │  ──HTTP :8080──▶ gateway（业务数据）
```

| 模块 | 文件 | 职责 | 状态 |
|---|---|---|---|
| **入口装配** | `src/lib.rs` | 窗口事件（关闭=最小化到托盘）、插件注册、命令注册 | ✅ |
| **系统托盘** | `src/tray.rs` | 托盘图标/菜单/单击唤起 | ✅ |
| **自定义命令** | `src/commands.rs` | `app_info` + `tokens_*` 登录态存储 + `app_exit` 退出 + `open_external` 外链打开（`remember_credentials_*` 已停用，命令保留兼容，前端统一 localStorage，见 6.1） | ✅ |
| **系统通知** | tauri-plugin-notification | 首次隐藏托盘时的引导通知 | ✅ |
| 本地文件接入 | - | 拖拽/文件选择器 | 🚧 规划中 |

## 3. 模块：登录态安全存储（`commands.rs`，✅ 已实现）

### 3.1 模块定位

解决"web 的 localStorage 在桌面端不安全"的问题。token 落盘到**应用配置目录** `%APPDATA%/com.agentframework.desktop/session.json`（Linux/macOS 为各自 app_config_dir），与 WebView2 数据目录隔离。

前端 `src/lib/storage.ts` 检测到 Tauri 环境后自动改走这三条命令，**组件层完全无感**。

### 3.2 命令接口

所有命令由前端 `invoke` 调用，参数/返回值自动 JSON 序列化。

#### `tokens_get(): Promise<TokenPair | null>`

- **作用**：读取已存的登录态令牌。
- **返回**：

```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "a3f2c1..."
}
```

- 从未登录（文件不存在）返回 `null`。

#### `tokens_set(tokens: TokenPair): Promise<void>`

- **作用**：写入令牌对。
- **参数**：`{ access_token, refresh_token }`。
- **实现细节**：先写 `session.json.tmp` 再 `rename`——原子落盘，中途崩溃不会留半截文件。
- **失败**：目录不可写时返回错误字符串（前端已兜底回退 localStorage）。

#### `tokens_clear(): Promise<void>`

- **作用**：删除会话文件（登出时调用）。文件不存在时静默成功。

### 3.3 前端调用示例（`web/src/lib/storage.ts`）

```ts
const { invoke } = await import('@tauri-apps/api/core')
invoke('tokens_get')                                  // 读
invoke('tokens_set', { tokens: { access_token, refresh_token } }) // 写
invoke('tokens_clear')                                // 清
```

### 3.4 安全模型说明

| 项 | 说明 |
|---|---|
| 隔离性 | token 在 WebView2 之外，浏览器 JS/其他站点**读不到** |
| 明文落盘 | 与 tauri-plugin-store 等价，防"浏览器级窃取"够用 |
| 加固方向 | 防本机进程直接读文件 → 升级 Windows Credential Manager（keyring crate），🚧 规划中 |

---

## 4. 模块：系统托盘与窗口行为（`tray.rs` / `lib.rs`，✅ 已实现）

### 4.1 行为约定

| 行为 | 说明 |
|---|---|
| 关闭主窗口 | **不退出**——隐藏到托盘（后台常驻），任务栏消失 |
| 首次隐藏（仅一次） | 发一条系统通知，提示"已最小化到托盘：右键托盘图标可退出，或在侧栏点击电源按钮" |
| 托盘菜单"显示主窗口" | 显示并聚焦窗口 |
| 托盘菜单"退出" | 设置退出标志 → 真正结束进程 |
| **左键单击**托盘图标 | 显示并聚焦窗口（仅左键弹起触发；右键只弹菜单，不唤起窗口） |
| 右键托盘图标 | 弹出托盘菜单（"显示主窗口"/"退出"）——不会同时唤起窗口 |
| 侧栏"退出应用"按钮（⚡） | 调 `app_exit` 命令，真正结束进程 |

> **退出途径共两条**：托盘菜单"退出" / 侧栏电源按钮。二者等效，都先置 `QUITTING` 标志再 `app.exit(0)`，因此不会触发"关闭=隐藏"逻辑。

### 4.2 实现要点

- 关闭事件在 `on_window_event` 里被拦截：非退出状态调 `api.prevent_close()` 改为隐藏。
- 全局 `QUITTING: AtomicBool` 区分"关窗"（隐藏）与"退出"（结束）——托盘"退出"先置位再 `app.exit(0)`。
- `HIDDEN_ONCE: AtomicBool` 保证首次隐藏通知只发一次（重启后重置）。

---

## 5. 模块：通用命令（`commands.rs`，✅ 已实现）

### 5.1 `app_info(): Promise<AppInfo>`

- **作用**：返回应用版本与运行平台。
- **返回**：

```json
{
  "version": "0.1.0",
  "platform": "windows"
}
```

- 用途：前端展示版本号、按平台做差异化（如快捷键提示）。

### 5.2 `app_exit(): Promise<void>`

- **作用**：真正退出整个桌面应用（前端"退出应用"电源按钮调用，与托盘菜单"退出"等效）。
- **实现**：置位 `QUITTING` 标志 → `app.exit(0)`——退出标志让"关闭=隐藏"逻辑放行，进程正常结束。
- **前端调用**（侧栏电源按钮，仅 Tauri 环境显示）：

```ts
const { invoke } = await import('@tauri-apps/api/core')
await invoke('app_exit')
```

### 5.3 `local_shell_execute(command, cwd?): Promise<LocalExecResult>`（阶段3·本地工具代理）

- **作用**：在本机执行 shell 命令并返回输出。配合后端"外部工具异步执行"机制：前端收到 SSE 的 `tool_call` 事件（`external=true` 的本地工具，如 `local_shell`）→ 弹确认弹窗 → 用户允许后调用本命令 → 把结果经 `POST /v1/agent/sessions/{id}/tool-results` 回填给 agent-service 唤醒挂起会话。
- **参数**：
  - `command: string`：完整命令文本（Windows 走 `cmd /C`，Linux/macOS 走 `sh -c`）；
  - `cwd?: string`：可选工作目录（空字符串 = 不设置，使用默认）。
- **返回**：

```json
{ "content": "hello-e2e", "is_error": false }
```

| 字段 | 说明 |
|---|---|
| `content` | stdout + stderr 合并输出；命令无输出时占位"（命令执行完成，无输出）"；非零退出附 `（退出码: N）` |
| `is_error` | 非零退出码 = `true` |

- **安全与超时**：
  - **命令全文在确认弹窗中展示**，用户点"允许"后才执行（拒绝 = 前端回填 `is_error: true` 的失败结果）；
  - 默认 **30s 超时**，超时 `try_wait` 轮询 + `kill` 终止子进程；可用环境变量 `LOCAL_EXEC_TIMEOUT_SECS` 覆盖（供单测）；
  - 执行在 `spawn_blocking` 线程中运行，不阻塞 Tauri 异步运行时/主线程；双管道读取线程避免子进程输出过多导致管道阻塞。
- **前端调用**（`web/src/lib/localTools.ts`）：

```ts
const { invoke } = await import('@tauri-apps/api/core')
const res: LocalExecResult = await invoke('local_shell_execute', { command, cwd })
```

- **注意**：浏览器环境无此命令（`'__TAURI_INTERNALS__' in window` 为 false）→ 前端对本地工具直接回填失败"请使用桌面客户端"，不会等待 120s 超时。

### 5.4 `open_external(url): Promise<void>`（外部链接打开）

- **作用**：用**系统默认浏览器**打开外部链接，防止 WebView2 在当前窗口内导航把整个应用界面替换成目标网页（聊天消息里出现 `https://www.example.edu.cn` 这类链接时，此前点击会导致界面"消失"）。
- **参数**：`url: string`。
- **触发链路**：web 端 `web/src/lib/external.ts` 的 `openExternal()`：
  1. 检测到 Tauri 环境 → `invoke('open_external', { url })`；
  2. 浏览器环境 → `window.open(url, '_blank', 'noopener,noreferrer')` 新标签页。
- **安全**：`validate_external_url` 仅放行 `http/https/mailto/tel` 白名单协议，且长度 ≤ 2048——`file://`、`javascript:`、`cmd://` 等一律拒绝（防命令注入），非法链接直接返回错误、不触发系统调用。
- **跨平台实现**：Windows `cmd /C start "" "<url>"`；macOS `open`；Linux `xdg-open`。
- **前端调用示例**：

```ts
import { openExternal, isExternalLink } from '@/lib/external'
// RichContent 渲染 Markdown 链接：isExternalLink 命中则拦截默认跳转
if (isExternalLink(href)) {
  e.preventDefault()
  void openExternal(href)
}
```

---

## 6. 登录设置：门户配置页 + 服务器地址

桌面端**没有浏览器地址栏**，但有专门的**门户配置页 `/portal`**（P2-Z 后）：

1. **首次运行**（Tauri 且未配置门户）：`HomeRedirect` 强制进入 `/portal` 配置页——选择一个智能体门户（如 `tutor`）；
2. **随时切换**：侧栏底部常驻「**门户配置**」按钮（仅 Tauri 显示）回到 `/portal`；
3. 配置存 localStorage（`agent.portal_agent`），配置后与浏览器端一致运行在具体门户；
4. 再下方是「**服务器地址**」输入框 + 保存按钮：默认 `http://localhost:8080`，部署到其它机器时改成目标地址，保存后**立即生效**；
5. **同域不登出（2026-08-10 修复）**：提交**当前已在使用的门户**时仅关闭并跳转，**不退出登录**；只有真正更换门户才 `logout()` 清登录态（域间登录态隔离，见 PROGRESS P2-AE）。

> **多租户体验（单包通用，与用户确认）**：桌面端**不按智能体分别打包安装包**——一个安装包内，通过「门户配置页」选择要进入的智能体域并记住（`agent.portal_agent`），与浏览器端逻辑完全一致；每个智能体如同独立系统（管理员/用户/资源域全部隔离，见 ARCHITECTURE.md §10.2）。URL 直达 `/login/:agentId` 在浏览器端同样支持。
> 若登录页内容超出窗口高度，卡片可上下滚动（设置区始终可达，不会被裁掉）。
> 管理员与普通用户的入口隔离：管理端登录接口只放行管理员角色账号，普通账号在管理员入口登录会被拒绝并提示改用智能体门户。

### 6.1 记住密码（✅ 已实现，P2-AA 后统一 localStorage）

- 登录页「**记住密码**」勾选后：**桌面端与浏览器行为完全一致**——前端 `web/src/lib/remember.ts` **统一走 localStorage**（base64 仅混淆、**非加密**，XSS 泄露风险已在代码注释注明），**不再调用 Rust 命令**；
- **按门户域隔离**（P2-AC）：凭据按 `agent.remembered_credentials.<agentId>` 分域保存（含 `*` 域），切换门户登录无需重新输入账号密码；旧单域 key 自动一次性迁移；
- 勾选后下次打开自动回填用户名密码（不自动提交，仍需点登录）；取消勾选即清除当前域已存凭据；
- 历史说明：P2-T 曾实现桌面端走系统凭据库（`remember_credentials_*`，keyring crate），但打包版命令未生效导致读写失败，P2-AA 起统一回退 localStorage——**Rust 命令保留但不再被前端调用**；
- **排查"记住密码没效果"**：代码链路完整；桌面端最常见的复现原因是运行**旧构建**——`frontendDist` 指向 `web/dist`，仅在 `tauri build` 时重建。验证步骤：① 确认 web 已 `npm run build`；② 重新 `npm run tauri build` 打包再安装；③ 勾选"记住密码"登录一次 → 关闭重开 → 应自动回填。

### 6.2 游客模式（✅ 已实现）

- 未登录进入桌面端 = 游客模式（同浏览器）：对话可用、无配置区、无退出登录按钮，底部显示「登录」入口；
- 登录页提供「以游客身份继续（不登录）」返回游客模式；
- 退出登录后落地游客模式：智能体域留在原智能体、管理端落地默认 `/agent/tutor`，保证任何时候都能以游客身份继续使用。

---

## 7. 模块：系统通知（✅ 已实现，用于首次隐藏引导）

- 插件 `tauri-plugin-notification` 已在入口注册、capabilities 已授权。
- **业务用途**：首次关闭窗口隐藏到托盘时发一条系统通知，告知用户退出途径（托盘右键 / 侧栏电源按钮）——防止"程序还在跑但找不到退出入口"。
- 若需在其它场景（如"长时间回答完成"提醒）使用，示例：

```ts
const { sendNotification } = await import('@tauri-apps/plugin-notification')
sendNotification({ title: '智能体助手', body: '回答完成' })
```

---

## 8. 工程约定速查

1. 新增 Rust 命令：`commands.rs` 加 `#[tauri::command] fn ...` → `lib.rs` 的 `generate_handler!` 注册 → web 端 `invoke` 调用。**前后端类型要保持一致**（serde 自动 JSON 序列化）。
2. 新插件：`Cargo.toml` 加 crate → `lib.rs` 注册 → `capabilities/default.json` 加权限。
3. 图标重生成：改 `desktop/app-icon.png` 后执行 `npm run tauri icon app-icon.png`。
4. 代码质量：`cargo check`（零警告目标）;改动命令后跑 web 的 build/test 确认 IPC 契约没被破坏。
5. 打包：`npm run tauri build`（Windows 首次会下载 NSIS 等打包器）。
