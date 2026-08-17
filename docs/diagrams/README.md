# 图解集（diagrams）

本项目架构 / 设计模式 / 模块功能的 SVG 图册，采用 **主图 + 子图** 组织：
主图给全局视角，每个模块块标注「详见 xx.svg」；子图深入模块内部，顶部标注所属主图区块。

## 主图

| 文件 | 内容 |
|---|---|
| [system-overview.svg](system-overview.svg) | 全栈总览：客户端（web / desktop）→ gateway → 五微服务 → framework 能力层 → 基础设施（PostgreSQL / 外部 LLM / Ollama）；每块标注子图链接 |

> 已嵌入 [docs/ARCHITECTURE.md](../ARCHITECTURE.md) 第 0 节。

## 子图 · 模块功能

| 文件 | 内容 |
|---|---|
| [architecture-microservices.svg](architecture-microservices.svg) | 微服务拓扑、端口、内部调用关系（gRPC / HTTP）、DB 连线与契约要点 |
| [framework-agent-core.svg](framework-agent-core.svg) | Agent 核心循环：记忆 → 压缩 → 调 LLM → 工具执行 → 回填；流式 SSE 与打断持久化 |
| [framework-orchestration.svg](framework-orchestration.svg) | 多智能体编排：planner / executor / aggregator、教研 DAG、失败降级、文档生成衔接 |
| [rag-pipeline.svg](rag-pipeline.svg) | RAG 链路：解析（docx/pdf/pptx）→ 分块 → embedding → pgvector 入库 → kb_search 检索注入 |
| [sandbox-security.svg](sandbox-security.svg) | 沙盒隔离：uid 降权、网络隔离、资源限制（prlimit）、能力与文件系统、协作权限模型 |
| [data-flow-message.svg](data-flow-message.svg) | 一次对话 SSE 事件序列、消息落库字段、版本 / 分支 / 摘要 |

## 子图 · 设计模式

| 文件 | 内容 |
|---|---|
| [patterns-architecture.svg](patterns-architecture.svg) | 微服务 + DDD 分层、依赖倒置（Repository 接口注入）、API 优先、go.work 多模块 |
| [patterns-messaging.svg](patterns-messaging.svg) | 追加式消息日志、round_no / version 状态机、截断回滚事务、上下文降级 |
| [patterns-security.svg](patterns-security.svg) | 认证（JWT/Refresh/游客合并）、RBAC、工具权限 L0-L3、审计双通道、密钥管理 |

## 存量图（早期归档）

| 文件 | 内容 | 关联文档 |
|---|---|---|
| [doc-generation.svg](doc-generation.svg) | 文档生成链路（DocumentSpec → 沙盒渲染 → 产物落盘 → 前端下载） | `docs/api/backend.md` |
| [html-docgen.svg](html-docgen.svg) | HTML 中间层文档生成（净化 → iframe 预览 → PDF） | `docs/api/backend.md` |
| [p4-orchestration.svg](p4-orchestration.svg) | 多智能体编排流程（planner / executor / aggregator / DAG） | `docs/PROGRESS.md` P4 |
| [remaining-plan.svg](remaining-plan.svg) | 剩余路线图（P6+ 待办） | `docs/PROGRESS.md` P6 |

## 呈现规范

- 统一尺寸 `1280x800`，配色沿用系统品牌色，字号不小于 12px。
- 每图顶部标题栏标注所属层级（主图 / 模块功能 / 设计模式）与版本日期。
- 主图模块块内嵌「详见 xx.svg」文本链接；子图回链主图区块。
- `ARCHITECTURE.md` 嵌入主图，其余子图按需在各 API / 模块文档中引用。
