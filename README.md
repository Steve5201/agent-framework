# agent-framework

一个**可复用的通用智能体框架** Monorepo：零业务耦合的 Go Agent 核心库 + 微服务化的完整后端 + React 前端 + Tauri 桌面端。

只需"注册工具 + 配置"，即可快速产出具备不同人格与能力的智能体应用。

## 特性

- **零业务耦合核心库**（`framework/`）：Agent 会话、流式输出、Function Calling 工具调用、短期/长期记忆、多 Agent 编排，均为接口化设计，可替换实现不换代码
- **微服务后端**（`backend/`）：Gateway 对外统一 HTTP 入口，Auth / Agent / LLM-Gateway / RAG / Admin 等微服务拆分，gRPC 内部通信
- **完整管理端**：用户 / 智能体 / 技能（Anthropic Agent Skills）/ MCP / 模型 / 知识库 / 配额 / 审计日志
- **对话增强**：多轮对话、流式打字机、文档解析渲染（PDF/DOCX/PPTX）、公式渲染、外部链接安全打开
- **React 前端**（`web/`）+ **Tauri 桌面端**（`desktop/`）：同一套 Web 构建产物，桌面端提供系统托盘、凭据加密存储、本地工具代理
- **安全设计**：登录防爆破、JWT + Refresh Token、密码自助修改后强制下线、L0-L3 工具权限分级、操作审计、用户工作区强隔离沙盒

## 目录结构

```
.
├── backend/     Go 微服务（Gateway :8080 + 五微服务），gRPC + HTTP 双栈
├── framework/   可复用 Agent 核心库（独立 Go module，零业务耦合）
├── web/         React 前端（:3000），Vite + TypeScript
├── desktop/     Tauri 2 桌面端（复用 web 构建产物）
├── deploy/      Docker Compose 编排 + 环境变量模板
├── scripts/     dev-up / smoke / rebuild / publish 等开发运维脚本
└── docs/        架构与 API 文档
```

## 快速开始（本地开发）

前置条件：Go 1.26+、Docker、Node.js 18+。

```bash
# 1. 复制环境变量模板并填入真实值
Copy-Item deploy\.env.example deploy\.env   # PowerShell
#   必填：DB_PASSWORD / DEEPSEEK_API_KEY / JWT_SECRET

# 2. 一键拉起全部服务（自动重建镜像）
.\scripts\dev-up.ps1 -Rebuild
```

服务就绪后：

- Web 前端：http://localhost:3000
- API 入口 / Swagger：http://localhost:8080 / http://localhost:8080/swagger/ui
- 健康检查：http://localhost:8080/healthz

默认超级管理员 `admin / Admin@2026`（生产环境必须通过 `AUTH_ADMIN_PASSWORD` 修改）。

仅体验框架核心库（无需 Docker）：

```bash
cd framework
$env:DEEPSEEK_API_KEY='your-key'
go run ./examples/cli        # 终端交互式聊天（流式输出 + 计算器工具）
go run ./examples/memory-demo
```

## 从零部署（服务器）

### 1. 准备

- 一台 Linux 服务器（推荐 2C4G+），安装 Docker 与 Docker Compose
- 阿里云/腾讯云等安全组放行 `8080`（HTTP API）、`8182`（文件）端口
- 一个 DeepSeek API Key（必填）；可选硅基流动 Key（云端 embedding）

### 2. 部署

```bash
git clone git@github.com:Steve5201/agent-framework.git
cd agent-framework

# 配置环境变量
cp deploy/.env.example deploy/.env
#   必填：DB_PASSWORD / DEEPSEEK_API_KEY / JWT_SECRET（openssl rand -hex 32 生成）
#   可选：SILICONFLOW_API_KEY（生产推荐，切换云端 embedding）

# 修改 GATEWAY_CORS_ORIGINS 加入你的公网访问源
# 例：浏览器直接访问 http://<你的公网IP>:8080 需加该来源，
#     桌面端（打包版 webview）需保留 http(s)://tauri.localhost

# 构建并启动
docker compose -f deploy/docker-compose.yml up -d --build
```

### 3. 访问

| 入口 | 地址 |
|---|---|
| 后端 API / Swagger | `http://<公网IP>:8080` / `http://<公网IP>:8080/swagger/ui` |
| Web 前端 | 本地 `npm run build` 后部署，或桌面端直接连接 |

桌面端打包：见 `desktop/`（Tauri 2），产物要求 Windows 10/11 + WebView2。

### 4. 运维

```bash
.\scripts\smoke.ps1          # 端到端冒烟（注册→登录→对话→改密）
.\scripts\rebuild.ps1        # 改代码后重建镜像
.\scripts\publish-images.ps1 # 打包并传输镜像到服务器
```

## 环境变量

所有密钥一律通过环境变量注入，**禁止硬编码**。完整键列表见 [deploy/.env.example](deploy/.env.example)。

## 文档

- [架构设计](docs/ARCHITECTURE.md)
- [开发标准](docs/dev/STANDARDS.md)
- [Framework API](docs/api/framework.md) / [Backend API](docs/api/backend.md) / [Web API](docs/api/web.md) / [Desktop API](docs/api/desktop.md)

## 开发

- 后端测试：`cd backend && go test ./...`
- 框架测试：`cd framework && go test ./...`
- 前端测试：`cd web && npm test`
- proto 重新生成：`.\scripts\gen-proto.ps1`

## License

[Apache-2.0](LICENSE) © agent-framework contributors
