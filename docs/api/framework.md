# framework 接口文档（P1 已实现）

> **这是什么**：可复用的通用 Agent 框架，一个纯 Go 库（`github.com/Steve5201/agent-framework`）。
> **阅读对象**：要用本框架开发 Agent 应用的开发者（假设具备 Go 基础）。
> **配套**：[README.md](./README.md)（索引）、[ARCHITECTURE.md](../ARCHITECTURE.md)（设计原理）。

---

## 第 0 章 子项目介绍

### 作用

framework 把"做一个会思考的 Agent"所需的能力做成开箱即用的库：

| 能力 | 说明 |
|---|---|
| 大模型接入 | 统一接口对接 DeepSeek / OpenAI / Kimi / 智谱等一切 OpenAI 兼容厂商 |
| 工具调用 | 注册工具 → LLM 自主决定调用 → 框架执行并回填结果（Function Calling） |
| 记忆 | 会话内滑动窗口（短期）+ 跨会话长期记忆 |
| 多轮对话 | 自动"想 → 做 → 想"循环，直到给出答案 |
| 工具安全 | L0~L3 权限分级，L2/L3 强制用户确认 |

### 定位

- **零业务耦合**：框架只提供通用能力，不含任何业务代码。各类具体场景
  通过"注册工具 + 配置"接入，框架本身不知道业务存在。
- **接口与实现分离**：模型厂商、记忆存储都是可替换的接口；换实现不换代码。
- **核心层零环境依赖**：框架只接收显式参数，不读环境变量（密钥从哪来由调用方决定）。

### 什么时候需要用到

- 你要开发一个"能调用工具、有记忆、支持多轮对话"的 Agent 应用时；
- 你要对接多家大模型厂商、且希望切换厂商不改业务代码时；
- 你要把 AI 能力嵌进 Web / 桌面 / 服务端程序时。

### 使用前提

- Go 1.22+；大模型厂商的 API Key（DeepSeek 用 `deepseek-v4-flash` / `deepseek-v4-pro`）。

---

## 第 1 章 30 秒上手

看一个最小可运行示例，先建立整体印象（完整代码见 [../../framework/examples/cli](../../framework/examples/cli)）：

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/Steve5201/agent-framework/agent"
    "github.com/Steve5201/agent-framework/llm"
    "github.com/Steve5201/agent-framework/schema"
    "github.com/Steve5201/agent-framework/tool"
)

func main() {
    apiKey := os.Getenv("DEEPSEEK_API_KEY") // 密钥由调用方提供

    // ① 接模型（OpenAI 兼容协议，一个入口吃遍各厂商）
    provider, _ := llm.NewOpenAICompatible(llm.Config{
        Name:    "deepseek",
        BaseURL: llm.DeepSeekBaseURL,    // https://api.deepseek.com
        APIKey:  apiKey,
        Model:   llm.DeepSeekFlashModel, // deepseek-v4-flash
    })

    // ② 注册工具（LLM 需要时才会调用）
    reg := tool.NewRegistry()
    _ = reg.Register(tool.CalculatorTool{})

    // ③ 配置 Agent（人格 + 能力 + 记忆策略）
    cfg := schema.AgentConfig{
        Model:        llm.DeepSeekFlashModel,
        SystemPrompt: "你是教学助手。",
        MaxRounds:    8,
    }

    // ④ 创建会话并对话（自动完成"调 LLM → 执行工具 → 再调 LLM"）
    s, _ := agent.NewSession(cfg, provider, reg)
    res, _ := s.Run(context.Background(), "12*13等于几")
    fmt.Println(res.Content) // LLM 会调用 calculator 算出 156 再回答
}
```

对应到下文模块：①=llm 包，②=tool 包，③=schema 包，④=agent 包。

---

## 第 2 章 模块地图

framework 由 5 个包组成，依赖关系单向（`←` 表示"依赖"）：

```
schema（公共契约，零依赖）
   ← llm / tool / memory（三个能力包，只依赖 schema）
        ← agent（编排层，依赖以上全部）
             ← examples（示例，最外层）
```

| 包 | 一句话职责 | 什么时候用到 |
|---|---|---|
| schema | 公共类型：消息、工具、配置、权限 | 几乎总在用——它是其它包的类型来源 |
| llm | 大模型接入 | 接模型、发请求、收流式输出时 |
| tool | 工具注册、校验、执行 | 给 Agent 添加"能力"时 |
| memory | 短期/长期记忆 | 需要"记住上下文"时（默认已内置） |
| agent | 会话与消息循环 | 组装整个 Agent 时（必用） |

下面按依赖顺序逐模块展开。

---

## 第 3 章 `schema` 包 —— 公共契约

### 模块介绍

**作用**：定义全框架共用的类型——消息、工具说明书、工具调用、配置、权限分级。
**定位**：零依赖的"契约层"，其它包都依赖它；它与 OpenAI 协议天然对齐，
保证消息在"框架内部"和"厂商 HTTP 协议"之间无需转换逻辑。
**什么时候用到**：任何时候——自定义工具要实现 `ToolSchema`、组装会话要填
`AgentConfig`、读取结果要碰 `Message`。它不需要单独"初始化"，直接引用类型。

---

### 3.1 `type Role string` —— 消息角色

**作用**：标记一条消息是谁说的。LLM 看到不同角色会有不同的对待方式
（system 是指令，必须优先服从；user 是用户输入；assistant 是模型自己的话）。

**定义**：

```go
type Role string

const (
    RoleSystem    Role = "system"    // 系统指令（Agent 身份/行为边界）
    RoleUser      Role = "user"      // 用户输入
    RoleAssistant Role = "assistant" // 模型回答
    RoleTool      Role = "tool"      // 工具执行结果回填
)
```

**使用要点**：
- `system` 通常放对话最前面，只出现一次；
- `tool` 消息必须带 `ToolCallID`，且与对应的 assistant 工具调用指令成对出现。

---

### 3.2 `type Message struct` —— 对话消息

**作用**：对话历史的基本单位。一条消息 = 角色 + 内容 +（可选的）工具调用/结果关联。

**定义**：

```go
type Message struct {
    Role        Role
    Content     string
    ToolCallID  string       // 仅 role=tool 时使用：关联到具体工具调用
    ToolCalls   []ToolCall   // 仅 role=assistant 时使用：模型发出的工具调用指令
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Role` | `Role` | 消息角色（见 3.1） |
| `Content` | `string` | 文本内容。工具结果消息里就是执行结果文本 |
| `ToolCallID` | `string` | 仅 tool 消息：与模型下发的调用 ID 配对（协议要求） |
| `ToolCalls` | `[]ToolCall` | 仅 assistant 消息：本轮模型要调用的工具指令列表 |

**方法**：`IsToolResult() bool` —— 判断这条消息是否是工具结果（`Role == RoleTool`）。
在展示历史、统计调用时常用。

**演示**（构造对话历史）：

```go
msgs := []schema.Message{
    {Role: schema.RoleSystem, Content: "你是教学助手"},
    {Role: schema.RoleUser, Content: "12*13?"},
}
// 遍历时判断工具结果：
for _, m := range msgs {
    if m.IsToolResult() {
        fmt.Println("工具结果:", m.Content)
    }
}
```

---

### 3.3 `type PermissionLevel int` —— 工具权限分级

**作用**：给工具标注危险等级，是框架安全机制的第一道闸门。
级别越高越危险，执行前越需要人工确认。

**定义**：

```go
type PermissionLevel int

const (
    PermissionL0Pure      PermissionLevel = iota // 纯计算，无副作用
    PermissionL1Read                             // 只读操作
    PermissionL2Write                            // 写操作（需确认）
    PermissionL3Dangerous                        // 危险操作（需确认）
)
```

**方法**：`String() string`（转为 "L0"~"L3" 文本，用于日志/展示）；
`RequiresApproval() bool`（L2/L3 返回 `true`，其余返回 `false`）。

**使用要点**：L2/L3 工具即使注册了，若会话未提供确认回调也会被框架拒绝执行
（见 agent 包 `WithApprovalFunc`）。**安全默认：宁可不执行，不静默执行。**

---

### 3.4 `type ToolSchema struct` —— 工具说明书

**作用**：给 LLM 看的"工具说明书"，也是框架校验参数、判定权限的依据。
LLM 依据 Description 决定"什么时候该调这个工具"。

**定义**：

```go
type ToolSchema struct {
    Name        string
    Description string
    Parameters  json.RawMessage // JSON Schema 对象
    Required    []string        // 必填参数名
    Permission  PermissionLevel
    External    bool            // 阶段3：是否由宿主外部执行（本地工具）
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Name` | `string` | 工具名，必须唯一；LLM 用它发起调用 |
| `Description` | `string` | 写给 LLM 的功能说明——决定了它"何时调用"，务必写清楚适用条件 |
| `Parameters` | `json.RawMessage` | 参数定义，JSON Schema 格式（`{"type":"object","properties":{...}}`） |
| `Required` | `[]string` | 必填参数名列表（缺失时 `ValidateArgs` 拒绝） |
| `Permission` | `PermissionLevel` | 权限级别（见 3.3） |
| `External` | `bool` | **阶段3 新增**：`true` = 本地工具，由宿主外部执行——agent 循环不会在本进程执行，而是派发给 `AsyncRunner` 并挂起等待 `Session.SubmitToolResult` 回填（见 7.x）；`false`（默认）= 服务器内置工具，走审批 + `Execute` 原路径 |

**方法**：`RequiresApproval() bool` —— 该工具是否需要用户确认（等价于
`Permission.RequiresApproval()`）。

**演示**（声明一个天气工具）：

```go
ts := schema.ToolSchema{
    Name:        "get_weather",
    Description: "查询指定城市的天气。当用户询问天气时使用。",
    Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
    Required:    []string{"city"},
    Permission:  schema.PermissionL1Read, // 只读，无需确认
}
```

---

### 3.5 `type ToolCall struct` —— 工具调用指令

**作用**：LLM 发出的"我想调用某个工具"的指令。包含调用 ID、工具名、参数 JSON。
框架据此执行工具，并用 `ID` 把结果配对回填。

**定义**：

```go
type ToolCall struct {
    ID        string          // 调用唯一标识（结果回填时配对）
    Name      string          // 要调用的工具名（对应 ToolSchema.Name）
    Arguments json.RawMessage // 参数 JSON（由 LLM 生成，需校验）
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `ID` | `string` | 厂商生成的一次性调用 ID；tool 结果消息必须携带它才能配对 |
| `Name` | `string` | 工具名，注册表按它找到具体工具 |
| `Arguments` | `json.RawMessage` | 参数原始 JSON（LLM 生成，不可信，执行前必须校验） |

**演示**（流式场景下手动构造并解析参数）：

```go
call := schema.ToolCall{
    ID:        "call_abc",
    Name:      "calculator",
    Arguments: json.RawMessage(`{"a":12,"b":13,"op":"*"}`),
}
var p struct{ A, B float64; Op string }
_ = json.Unmarshal(call.Arguments, &p) // 注意：生产环境用 tool.ValidateArgs 先校验
```

---

### 3.6 `type ToolResult struct` —— 工具执行结果

**作用**：工具执行的产出，封装后回填给 LLM（作为 role=tool 消息）。
`IsError` 让框架能区分"工具正常返回"与"工具执行失败"，两者都可继续对话。

**定义**：

```go
type ToolResult struct {
    ToolCallID string
    Name       string
    Content    string // 结果文本（回填给 LLM）
    IsError    bool   // 是否执行失败
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `ToolCallID` | `string` | 对应 `ToolCall.ID`，配对用 |
| `Name` | `string` | 工具名 |
| `Content` | `string` | 结果文本；失败时是错误说明 |
| `IsError` | `bool` | `true` 表示工具执行失败（失败原因已含在 Content） |

---

### 3.7 `type AgentConfig struct` —— Agent 配置

**作用**：创建会话的"总配置"——模型、人格、行为边界、记忆策略。
**位置**：交给 `agent.NewSession`。

**定义**：

```go
type AgentConfig struct {
    Model                string
    SystemPrompt         string
    Tools                []ToolSchema
    MaxRounds            int
    Memory               MemoryConfig
    ExternalExecTimeout  time.Duration // 阶段3：外部工具挂起超时（zero = 默认 120s）
}
```

**字段说明**：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `Model` | `string` | ✅ | 模型名（如 `llm.DeepSeekFlashModel`）；为空时 NewSession 直接报错 |
| `SystemPrompt` | `string` | 否 | 系统指令：定义 Agent 人格与行为边界 |
| `Tools` | `[]ToolSchema` | 否 | 工具说明书（一般由注册表提供，可不填） |
| `MaxRounds` | `int` | ✅ | 消息循环上限（>0，防死循环）；建议 >= 8 |
| `Memory` | `MemoryConfig` | 否 | 记忆策略 |
| `ExternalExecTimeout` | `time.Duration` | 否 | **阶段3 新增**：外部工具（`ToolSchema.External`）挂起等待回填的超时；`0` = 默认 120s。超时未回填则本次工具调用按失败结束、会话继续（保护无事件通知路径不无限挂起） |

**方法**：`Validate() error` —— 校验 Model 非空、MaxRounds>0。`NewSession` 内部会自动调用。

**演示**：

```go
cfg := schema.AgentConfig{
    Model:        "deepseek-v4-flash",
    SystemPrompt: "你是智能助手，回答用中文，数学计算必须用 calculator 工具。",
    MaxRounds:    8,
    Memory:       schema.MemoryConfig{MaxMessages: 20},
}
if err := cfg.Validate(); err != nil {
    log.Fatal(err) // 配置错误尽早暴露
}
```

---

### 3.8 `type MemoryConfig struct` —— 记忆配置

**作用**：配置会话的记忆行为。挂在 `AgentConfig.Memory` 下。

**定义**：

```go
type MemoryConfig struct {
    MaxMessages int  // 短期记忆窗口最大消息数（<=0 时默认 20）
    UseLongTerm bool // 是否启用长期记忆（P3 生效）
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `MaxMessages` | `int` | 滑动窗口上限；`<=0` 时 NewSession 内部使用默认值 20 |
| `UseLongTerm` | `bool` | 长期记忆开关（P1 仅占位，P3 生效；届时配合 agent.WithLongTermMemory） |

---

## 第 4 章 `llm` 包 —— 模型接入

### 模块介绍

**作用**：把"调用大模型"抽象成统一接口。一套 `OpenAICompatible` 客户端对接
所有提供 OpenAI 兼容端点的厂商；流式/非流式双支持；自动重试。
**定位**：模型层。上层 agent 只面向 `Provider` 接口编程，换厂商不改业务代码。
**什么时候用到**：任何要"发消息给模型"的时刻——初始化 Provider、直接调用
`Chat`/`ChatStream`、读取用量。

---

### 4.1 `func NewOpenAICompatible(cfg Config) (*OpenAICompatible, error)` —— 构造模型客户端

**作用**：创建连接某家厂商的客户端。**这是接入一切 OpenAI 兼容厂商的唯一入口**——
DeepSeek / OpenAI / Kimi / 智谱 / 通义 只是 `BaseURL` 和 `Model` 不同。

**定义**：

```go
type Config struct {
    Name        string
    BaseURL     string
    APIKey      string
    Model       string
    Timeout     time.Duration
    MaxRetries  int
    Temperature *float64
    TopP        *float64
    MaxTokens   *int
}

func NewOpenAICompatible(cfg Config) (*OpenAICompatible, error)
```

**参数说明**（`Config` 字段）：

| 字段 | 类型 | 必填 | 作用与意义 |
|---|---|---|---|
| `Name` | `string` | 否 | 供应商名，用于日志/路由识别；空则显示 `openai-compatible` |
| `BaseURL` | `string` | ✅ | 协议端点（不带 `/chat/completions`）。空值直接报错 |
| `APIKey` | `string` | ✅ | 接入密钥，**由调用方传入**（建议从环境变量读）。空值直接报错 |
| `Model` | `string` | 否 | 默认模型名；请求未指定模型时使用。空则兜底 `gpt-3.5-turbo` |
| `Timeout` | `time.Duration` | 否 | 非流式请求整体超时；默认 60s。流式请求不受它限制（由 ctx 控制） |
| `MaxRetries` | `int` | 否 | 可重试错误（429/5xx/网络）的最大重试次数；默认 3 |
| `Temperature` | `*float64` | 否 | 默认采样温度 0~2；`nil` = 不覆盖（用服务端默认） |
| `TopP` | `*float64` | 否 | 默认核采样 0~1；`nil` = 不覆盖 |
| `MaxTokens` | `*int` | 否 | 默认生成 token 上限；`nil` = 不限制 |

**返回**：`(*OpenAICompatible, error)`。配置非法（BaseURL/APIKey 为空）时返回错误。

**演示**（DeepSeek）：

```go
provider, err := llm.NewOpenAICompatible(llm.Config{
    Name:    "deepseek",
    BaseURL: llm.DeepSeekBaseURL,    // https://api.deepseek.com
    APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
    Model:   llm.DeepSeekFlashModel, // deepseek-v4-flash
})
if err != nil {
    log.Fatal("初始化模型失败:", err)
}
```

**演示**（换其它厂商只需改 BaseURL + Model）：

```go
// OpenAI
llm.NewOpenAICompatible(llm.Config{
    BaseURL: "https://api.openai.com",
    APIKey:  os.Getenv("OPENAI_API_KEY"),
    Model:   "gpt-4o-mini",
})
// Kimi（月之暗面）
llm.NewOpenAICompatible(llm.Config{
    BaseURL: "https://api.moonshot.cn",
    APIKey:  os.Getenv("MOONSHOT_API_KEY"),
    Model:   "moonshot-v1-8k",
})
```

---

### 4.2 DeepSeek 连接常量

**作用**：免去手写 DeepSeek 的端点与模型名字符串，防止拼错、防硬编码。

**定义**：

```go
const (
    DeepSeekBaseURL    = "https://api.deepseek.com"
    DeepSeekFlashModel = "deepseek-v4-flash" // 高速低成本，通用对话推荐
    DeepSeekProModel   = "deepseek-v4-pro"   // 旗舰推理，复杂分析/Agent 任务
)
```

**注意**：旧模型名 `deepseek-chat` / `deepseek-reasoner` 已于 2026-07-24 停用，
请使用 V4 架构的新模型名。

---

### 4.3 `type Provider interface` —— 模型接入统一契约

**作用**：定义"一个模型厂商 = 三个方法"。agent 层只依赖此接口，
因此**接入协议不兼容的厂商（如 Anthropic）时，新写一个实现此接口的类型即可，
agent 层零改动**。

**定义**：

```go
type Provider interface {
    Name() string                                                       // 供应商名（日志/路由）
    Chat(ctx context.Context, req *Request) (*Response, error)          // 非流式：等完整响应
    ChatStream(ctx context.Context, req *Request) (Stream, error)       // 流式：返回迭代器
}
```

**参数说明**：

| 参数 | 说明 |
|---|---|
| `ctx` | 控制请求生命周期（超时、取消） |
| `req` | 调用请求（见 4.4） |

**返回约定**：`Chat` 返回完整 `Response`；`ChatStream` 返回 `Stream` 迭代器，
**调用方必须负责 `Close()`**。

---

### 4.4 `type Request struct` —— 一次模型调用请求

**作用**：描述"这次问模型什么"——模型、消息、工具、采样参数。
`Model` 为空时自动使用客户端默认模型。

**定义**：

```go
type Request struct {
    Model       string
    Messages    []schema.Message
    Tools       []schema.ToolSchema
    Stream      bool
    Temperature *float64
    TopP        *float64
    MaxTokens   *int
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Model` | `string` | 本次模型名；空 = 用客户端默认 |
| `Messages` | `[]schema.Message` | 对话消息（system 放最前） |
| `Tools` | `[]schema.ToolSchema` | 工具说明书集；非空时 LLM 才可能发起工具调用 |
| `Stream` | `bool` | 是否流式（配合 ChatStream 使用） |
| `Temperature` | `*float64` | 采样温度 0~2；nil = 用客户端默认，再 nil = 服务端默认 |
| `TopP` | `*float64` | 核采样 0~1；取值逻辑同上 |
| `MaxTokens` | `*int` | 本次生成 token 上限；取值逻辑同上 |

**优先级**：`请求级 > 客户端 Config 默认 > 不发字段（服务端默认）`。

**演示**（直接调用 Provider，绕过 agent）：

```go
highTemp := 1.2
maxTok := 2048
resp, err := provider.Chat(ctx, &llm.Request{
    Model:       llm.DeepSeekFlashModel,
    Messages:    []schema.Message{{Role: schema.RoleUser, Content: "给一个创意点子"}},
    Temperature: &highTemp, // 更有创造性
    MaxTokens:   &maxTok,
})
if err != nil { /* 处理 */ }
fmt.Println(resp.Content)
```

---

### 4.5 `type Response struct` —— 非流式响应

**作用**：一次 `Chat` 调用的完整结果。

**定义**：

```go
type Response struct {
    Content      string
    ToolCalls    []schema.ToolCall
    FinishReason string
    Usage        Usage
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `Content` | `string` | 模型回答文本 |
| `ToolCalls` | `[]schema.ToolCall` | 模型请求调用的工具（未调用则为空） |
| `FinishReason` | `string` | 结束原因：`stop`（正常）/`tool_calls`（要调工具）/`length`（超限） |
| `Usage` | `Usage` | token 用量（见 4.6） |

**使用要点**：`FinishReason == "tool_calls"` 表示模型还没答完，它想要工具结果，
需要执行工具后把结果带回再问一次——这正是 agent 包帮你做的。

---

### 4.6 `type Usage struct` —— token 用量

**作用**：统计一次请求的 token 消耗，供成本核算、配额控制（llm-gateway 用）。

**定义**：

```go
type Usage struct {
    PromptTokens     int // 输入 token 数
    CompletionTokens int // 输出 token 数
    TotalTokens      int // 合计
}
```

---

### 4.7 `type Stream interface` + `type StreamEvent struct` —— 流式输出

**作用**：模型逐 token 输出时，通过迭代器逐个事件消费，实现"打字机效果"。
**什么时候用到**：聊天界面需要边生成边显示时；或需要尽早看到模型开始回答时。

**定义**：

```go
type Stream interface {
    Next() (StreamEvent, error) // 返回 io.EOF 表示正常结束
    Close() error               // 释放连接，必须调用
}

type StreamEvent struct {
    Content   string          // 本次文本增量（可能为空）
    ToolCalls []ToolCallDelta // 本次工具调用增量（可能为空）
    Usage     *Usage          // 流结束时模型可能附带用量
    Done      bool            // 流是否已结束
}
```

**参数/返回说明**：

| 项 | 说明 |
|---|---|
| `Content` | 每个事件只含一小段文本，调用方拼接即为完整回答 |
| `ToolCalls` | 工具参数是**分片**下发的（见 4.8），需要拼装 |
| `Next()` | 迭代到结尾返回 `errors.Is(err, io.EOF)` |

**演示**（标准流式消费模式）：

```go
st, err := provider.ChatStream(ctx, req)
if err != nil { /* 处理 */ }
defer st.Close() // 别忘了释放

var sb strings.Builder
for {
    ev, err := st.Next()
    if errors.Is(err, io.EOF) {
        break // 正常结束
    }
    if err != nil {
        return fmt.Errorf("流中断: %w", err)
    }
    sb.WriteString(ev.Content)
    fmt.Print(ev.Content) // 逐 token 输出
}
```

---

### 4.8 `type ToolCallDelta struct` —— 流式工具调用增量

**作用**：流式场景下，模型把工具参数切成多片逐段下发。调用方必须按 `Index`
分组、按顺序拼接完整 JSON 后才能真正解析执行。

**定义**：

```go
type ToolCallDelta struct {
    Index     int    // 并行多工具调用时的序号（分组的 key）
    ID        string // 调用 ID（通常首片携带）
    Name      string // 工具名（通常首片携带）
    Arguments string // 参数 JSON 的增量片段
}
```

**字段说明**：

| 字段 | 说明 |
|---|---|
| `Index` | 同一次工具调用的分片共享同一 Index；并行调用时每个工具一个 Index |
| `ID` / `Name` | 只在第一片出现，后续分片为空 |
| `Arguments` | 把同一 Index 的所有分片顺序拼接，才得到完整参数 JSON |

**注意**：你通常不需要自己处理拼装——agent 包的 `RunStream` 已内置完整拼装逻辑。
此类型主要供高级场景（自研编排）使用。

---

### 4.9 `type MockProvider struct` —— 测试替身

**作用**：内存版 Provider，用于单测/演示时**不花钱、不联网**地模拟模型回复。
**什么时候用到**：写测试时替代真实模型；演示框架行为时。

**定义**：

```go
type MockProvider struct {
    Name_        string
    Content      string
    ToolCalls    []schema.ToolCall
    Events       []StreamEvent
    Err          error
    ChatFn       func(*Request) (*Response, error)
    ChatStreamFn func(*Request) (Stream, error)
}
```

**字段说明**：

| 字段 | 说明 |
|---|---|
| `Content` | 非流式的固定回答文本 |
| `ToolCalls` | 非流式的固定工具调用指令（模拟"模型要调工具"） |
| `Events` | 流式的固定事件序列 |
| `Err` | 模拟调用失败 |
| `ChatFn` / `ChatStreamFn` | 自定义行为（优先级最高），实现"第一次要调工具、第二次给答案"等场景 |

**演示**（模拟"先调工具再回答"的模型）：

```go
p := &llm.MockProvider{}
calls := 0
p.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
    calls++
    if calls == 1 {
        return &llm.Response{ToolCalls: []schema.ToolCall{{ID: "c1", Name: "calculator", Arguments: json.RawMessage(`{"a":1,"b":2,"op":"+"}`)}}}, nil
    }
    return &llm.Response{Content: "答案是 3"}, nil
}
```

---

## 第 5 章 `tool` 包 —— 工具

### 模块介绍

**作用**：管理 Agent 的"能力单元"——注册工具、校验参数、带权限地执行。
**定位**：LLM 不能直接运行代码，它只能"看到"说明书（ToolSchema）并发调用请求；
本包把"请求"变成"真实的执行结果"。
**什么时候用到**：给 Agent 添加新能力时（实现 `Tool` 接口并注册）；
执行工具调用时（`Registry.Execute`）。

---

### 5.1 `type Tool interface` —— 工具契约

**作用**：定义一个工具 = 实现两个方法：`Schema()`（说明书，给 LLM 看）+
`Execute()`（执行逻辑，LLM 传参）。框架对"工具到底是什么"零假设。

**定义**：

```go
type Tool interface {
    Schema() schema.ToolSchema                                      // 工具说明书
    Execute(ctx context.Context, args json.RawMessage) (string, error) // 执行，返回结果文本
}
```

**参数说明**：

| 方法 | 参数 | 说明 |
|---|---|---|
| `Execute` | `args json.RawMessage` | LLM 生成的参数 JSON，**不可信**，先校验再使用 |
| `Execute` 返回 | `(string, error)` | 结果文本（将回填给 LLM）；error 表示执行失败 |

**使用要点**：
- `Execute` 尽量做成纯函数，避免副作用；有副作用/写外部状态的工具，
  在 `Schema().Permission` 中声明更高的级别；
- 参数解析前先调用 `tool.ValidateArgs` 校验（见 5.3）。

**演示**（自定义一个天气工具）：

```go
type WeatherTool struct{}

func (WeatherTool) Schema() schema.ToolSchema {
    return schema.ToolSchema{
        Name:        "get_weather",
        Description: "查询城市天气。用户问天气时使用。",
        Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
        Required:    []string{"city"},
        Permission:  schema.PermissionL1Read,
    }
}

func (WeatherTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
    var p struct{ City string `json:"city"` }
    if err := json.Unmarshal(args, &p); err != nil {
        return "", err
    }
    return "上海今天晴，25℃", nil // 真实场景改为调用天气 API
}

// 注册：reg.Register(WeatherTool{})
```

---

### 5.2 `func NewRegistry() *Registry` + Registry 方法 —— 工具注册表

**作用**：按名字存放工具的"字典"，提供注册、查找、批量导出说明书、执行闭环。
**什么时候用到**：组装 Agent 时创建，把工具逐个 `Register` 进去。

**定义**：

```go
func NewRegistry() *Registry

// Registry 方法：
func (r *Registry) Register(t Tool) error                                  // 注册；重名报错
func (r *Registry) Get(name string) (Tool, error)                          // 按名查找
func (r *Registry) Schemas() []schema.ToolSchema                           // 全部说明书（发给 LLM）
func (r *Registry) Names() []string                                        // 全部工具名
func (r *Registry) Execute(ctx context.Context, call schema.ToolCall, approved bool) (*schema.ToolResult, error)
```

**参数说明**：

| 方法 | 参数 | 说明 |
|---|---|---|
| `Register` | `t Tool` | 注册；工具名重复返回错误 |
| `Execute` | `call` | 一次工具调用指令 |
| `Execute` | `approved` | 用户确认结果：`false` 时 L2/L3 工具被拒绝（`true` 表示已确认） |

**`Execute` 行为约定**（开发者最容易混淆的点）：
- 返回 **error**：工具未注册 / 未获确认 / 参数非法——应中止流程；
- 返回 **`*ToolResult{IsError:true}` 且 err 为 nil**：工具本身执行失败，
  失败原因已回填到 Content——**可继续对话**，让 LLM 知道"没跑通"并调整策略。

**演示**：

```go
reg := tool.NewRegistry()
_ = reg.Register(tool.CalculatorTool{})
_ = reg.Register(WeatherTool{})

for _, s := range reg.Schemas() { // 发给 LLM 的说明书
    fmt.Println(s.Name, "—", s.Description)
}

result, err := reg.Execute(ctx, schema.ToolCall{
    ID: "c1", Name: "calculator",
    Arguments: json.RawMessage(`{"a":12,"b":13,"op":"*"}`),
}, true /* 已确认 */)
if err != nil {
    log.Fatal(err) // 参数非法/工具不存在
}
fmt.Println(result.Content) // "156"
```

---

### 5.3 `func ValidateArgs(ts schema.ToolSchema, args json.RawMessage) error` —— 参数校验

**作用**：在执行前校验 LLM 传来的参数——必填项存在、类型正确。
**为什么必须**：LLM 生成的 JSON 不可信，可能缺字段、类型错（如把 "12" 传成字符串），
直接执行会导致 panic 或脏数据。

**定义**：

```go
func ValidateArgs(ts schema.ToolSchema, args json.RawMessage) error
```

**参数说明**：

| 参数 | 说明 |
|---|---|
| `ts` | 工具的说明书（其中的 `Parameters` 定义参数约束，`Required` 定义必填） |
| `args` | LLM 传来的参数 JSON |

**返回**：`nil` 表示通过；否则返回描述具体问题的 error（缺哪个必填、哪个字段类型不符）。

**演示**（自定义工具内自校验）：

```go
func (WeatherTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
    if err := tool.ValidateArgs(WeatherTool{}.Schema(), args); err != nil {
        return "", fmt.Errorf("参数非法: %w", err)
    }
    // 校验通过后再解析
    var p struct{ City string `json:"city"` }
    _ = json.Unmarshal(args, &p)
    return "上海晴 25℃", nil
}
```

---

### 5.4 内置工具

| 工具 | 说明 | 权限 |
|---|---|---|
| `CalculatorTool{}` | 四则运算（`{"a":..,"b":..,"op":"+|-|*|/"}`） | L0 |
| `EchoTool{}` | 原样回显输入文本 | L0 |

实现模板见 [../../framework/tool/example.go](../../framework/tool/example.go)，是自定义工具的最佳参照。

---

## 第 6 章 `memory` 包 —— 记忆

### 模块介绍

**作用**：让 Agent"记得住"。上下文窗口装不下无限历史，记忆负责"什么保留、什么丢弃"。
**定位**：分两层——短期（会话内滑动窗口）+ 长期（跨会话持久记忆）。
**什么时候用到**：使用 agent 包时短期记忆自动内置（无需手动创建）；
需要跨会话持久记忆时，创建长期记忆实现并注入（见 6.6）。

---

### 6.1 `type Memory interface` —— 短期记忆接口

**作用**：会话内对话历史的滑动窗口接口。agent 包默认使用 `ShortTermMemory` 实现。

**定义**：

```go
type Memory interface {
    Add(msg schema.Message)
    Recent() []schema.Message
    Trim() int
    Len() int
    Clear()
}
```

**方法说明**：

| 方法 | 说明 |
|---|---|
| `Add` | 追加一条消息，超限自动裁剪最旧 |
| `Recent` | 返回窗口内全部消息的**副本**（外部修改不影响内部） |
| `Trim` | 手动裁剪超限消息，返回丢弃条数；**配对保护**——裁剪边界若落在 role=tool 消息上，会向前回溯保留其配对的 assistant(tool_calls)，绝不切开"工具调用声明 ↔ 工具结果"配对（2026-08-10 修复：此前连续裁剪曾切开配对，导致请求出现孤立 tool，OpenAI 兼容协议返回 400；配对保护允许窗口短暂超限） |
| `Len` | 当前消息数 |
| `Clear` | 清空全部（如开启新话题） |

---

### 6.2 `func NewShortTermMemory(maxMessages int, protected int) (*ShortTermMemory, error)` —— 短期记忆实现

**作用**：滑动窗口短期记忆。`protected` 参数保护开头的 system 指令，
保证 Agent 不会"忘记自己是谁"。

**参数说明**：

| 参数 | 说明 |
|---|---|
| `maxMessages` | 窗口上限，必须 > 0 |
| `protected` | 开头保护的条数（配置了 system 建议传 1）；必须 <= maxMessages，否则报错 |

**演示**：

```go
mem, err := memory.NewShortTermMemory(10, 1) // 窗口 10 条，保护开头 1 条
if err != nil { log.Fatal(err) }

mem.Add(schema.Message{Role: schema.RoleSystem, Content: "你是教学助手"})
mem.Add(schema.Message{Role: schema.RoleUser, Content: "你好"})
msgs := mem.Recent()   // [system, user]
fmt.Println(mem.Len()) // 2
mem.Clear()            // 清空
```

---

### 6.3 `type MemoryEntry struct` —— 长期记忆条目

**作用**：长期记忆不是"一句话"，而是一段可管理的结构化记录：
内容 + 来源 + 标签 + 时间戳，便于检索、筛选、溯源。

**定义**：

```go
type MemoryEntry struct {
    ID        string
    Content   string
    Source    string
    Tags      []string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**字段说明**：

| 字段 | 说明 |
|---|---|
| `ID` | 唯一标识；`Remember` 传空表示由实现自动生成 |
| `Content` | 记忆内容，如"用户偏好 Go 语言" |
| `Source` | 来源（conversation / user_profile / knowledge...），便于溯源筛选 |
| `Tags` | 标签，如 ["preference"]，用于分类检索 |
| `CreatedAt` / `UpdatedAt` | 时间戳（零值由实现填充；UpdatedAt 由实现维护） |

---

### 6.4 `type LongTermMemory interface` —— 长期记忆接口

**作用**：跨会话持久记忆的契约。**要用长期记忆功能，实现这 6 个方法即可接入框架**
（注入方式见 6.6）。检索匹配策略由实现决定：关键词匹配（内置实现）
或语义检索（P3 接 pgvector）。

**定义**：

```go
type LongTermMemory interface {
    Remember(ctx context.Context, entry MemoryEntry) (string, error)
    Recall(ctx context.Context, query string, limit int) ([]MemoryEntry, error)
    Get(ctx context.Context, id string) (MemoryEntry, error)
    List(ctx context.Context) ([]MemoryEntry, error)
    Forget(ctx context.Context, id string) error
    Clear(ctx context.Context) error
}
```

**方法说明**：

| 方法 | 作用 | 参数/返回要点 |
|---|---|---|
| `Remember` | 存一条记忆 | `entry.ID` 空 → 自动生成并返回新 ID；重复 ID → 覆盖更新 |
| `Recall` | 检索相关记忆 | `limit` <=0 表示不限条数 |
| `Get` | 按 ID 读单条 | 不存在返回 `ErrNotFound`（见 6.5） |
| `List` | 列出全部 | 实现按更新时间倒序 |
| `Forget` | 删除 | 不存在返回 `ErrNotFound` |
| `Clear` | 清空全部 | — |

---

### 6.5 哨兵错误 `ErrNotFound`

**作用**：所有实现统一用 `errors.Is(err, memory.ErrNotFound)` 判断"这条记忆不存在"，
无论底层是内存、文件还是数据库。

**演示**：

```go
_, err := mem.Get(ctx, "no-such-id")
if errors.Is(err, memory.ErrNotFound) {
    fmt.Println("没有这条记忆")
}
```

---

### 6.6 内置实现与注入 Agent

**三种实现（从"空"到"真"）**：

| 实现 | 行为 | 适用场景 |
|---|---|---|
| `NoopLongTermMemory{}` | 所有操作成功但无效果 | 默认值 / 测试 / 暂不需要长期记忆 |
| `NewInMemoryLongTermMemory()` | 内存 map + 关键词检索；进程退出即丢 | 单机原型 / 单测 / 本地演示 |
| `NewFileLongTermMemory(path)` | JSON 文件持久化；重启不丢 | 单机轻量部署 |

**演示**（创建 + 使用 + 注入 Agent）：

```go
// 文件版（持久化）；接口用法与内存版完全一致
mem, err := memory.NewFileLongTermMemory("data/mem.json")
if err != nil { log.Fatal(err) }
ctx := context.Background()

id, _ := mem.Remember(ctx, memory.MemoryEntry{
    Content: "用户偏好 Go 语言",
    Source:  "conversation",
    Tags:    []string{"preference"},
})
hits, _ := mem.Recall(ctx, "Go 偏好", 5) // 按命中相关度返回

// 注入 Agent 会话（不传则默认 Noop，什么都不做）
s, _ := agent.NewSession(cfg, provider, reg,
    agent.WithLongTermMemory(memory.NewInMemoryLongTermMemory()),
)
```

**注意**：P1 阶段 `Session` 支持注入长期记忆；对话中**自动**写入/读取长期记忆的
编排在 P3 落地（届时 `AgentConfig.Memory.UseLongTerm=true` 生效）。

---

## 第 7 章 `agent` 包 —— 会话与消息循环

### 模块介绍

**作用**：框架的"大脑"。把 schema / llm / tool / memory 串成完整的
"调 LLM → 执行工具 → 结果回填 → 再调 LLM"循环，直到模型给出答案。
**定位**：编排层。开发者与框架交互的主要入口。
**什么时候用到**：所有场景——创建会话、发消息、拿结果。

---

### 7.1 `func NewSession(cfg schema.AgentConfig, provider llm.Provider, reg *tool.Registry, opts ...Option) (*Session, error)` —— 创建会话

**作用**：创建一次会话。一个 `Session` 对应一段连续对话，多次调用
`Run`/`RunStream` 之间自动共享历史与记忆。

**参数说明**：

| 参数 | 说明 |
|---|---|
| `cfg` | Agent 配置（见 3.7）；非法配置直接报错 |
| `provider` | 模型 Provider（见 4.3）；为 nil 报错 |
| `reg` | 工具注册表（见 5.2）；为 nil 报错 |
| `opts` | 可选行为配置（见 7.2） |

**演示**：

```go
s, err := agent.NewSession(cfg, provider, reg)
if err != nil { log.Fatal("创建会话失败:", err) }
```

---

### 7.2 Option 配置项 —— 定制会话行为

**作用**：`NewSession` 的可选参数，按需定制安全确认、长期记忆与外部工具派发。

**定义**：

```go
func WithApprovalFunc(f func(schema.ToolCall) bool) Option
func WithLongTermMemory(m memory.LongTermMemory) Option
func WithAsyncRunner(r AsyncRunner) Option // 阶段3：外部工具派发器
```

**`WithApprovalFunc` 说明**：
- 作用：设置 L2/L3 工具的"用户确认回调"，返回 `true` 才允许执行；
- **默认 nil → 所有 L2/L3 工具一律拒绝**（安全设计，不是 bug）；
- 参数 `call`：本次待确认的工具调用（可读 Name/Arguments 展示给用户）。

**演示**（终端确认）：

```go
s, _ := agent.NewSession(cfg, provider, reg,
    agent.WithApprovalFunc(func(call schema.ToolCall) bool {
        fmt.Printf("执行工具 %s？[y/N] ", call.Name)
        var ans string
        fmt.Scanln(&ans)
        return strings.EqualFold(ans, "y")
    }),
)
```

**`WithLongTermMemory` 说明**：注入长期记忆实现（见 6.6）；默认 Noop。

**`WithAsyncRunner` 说明**（阶段3）：注入外部工具派发器。当 agent 循环遇到 `ToolSchema.External=true` 的工具时，走"外部异步执行"路径（见 7.6）：
1. 在 `Session` 挂起表注册该调用（`callID → 结果通道`）；
2. 调 `AsyncRunner.Dispatch(ctx, call)` 把调用派发给宿主（如桌面端确认 + 本机执行）；
3. agent 循环挂起等待回填通道或 `ExternalExecTimeout` 超时；
4. 宿主执行完成后调 `Session.SubmitToolResult(callID, result)` 唤醒。

**`AsyncRunner` 接口定义**：

```go
type AsyncRunner interface {
    // Dispatch 派发一次外部工具调用给宿主。实现方通常把 call 转成事件
    // 推给客户端（SSE/WS/REST 回调皆可），并在宿主完成时调用
    // session.SubmitToolResult(call.ID, result) 回填。
    Dispatch(ctx context.Context, call schema.ToolCall) error
}
```

**默认 nil → 外部工具被拒绝**（安全设计）：`execExternal` 在无派发器时返回错误，工具调用按失败结束，会话不挂起。

---

### 7.3 `func (s *Session) Run(ctx, userInput string) (*Result, error)` —— 非流式对话

**作用**：发一条用户消息，自动完成整个工具循环，**等完整回答返回**。
**什么时候用到**：不需要打字机效果时（命令行、批处理、服务端）。

**参数说明**：

| 参数 | 说明 |
|---|---|
| `ctx` | 控制超时/取消 |
| `userInput` | 用户输入文本 |

**返回**：`(*Result, error)`。错误示例：达到 `MaxRounds` 未收敛、LLM 调用失败。

**演示**：

```go
res, err := s.Run(ctx, "请计算 12*13 和 8/4")
if err != nil { log.Fatal(err) }
fmt.Println("AI:", res.Content)
```

---

### 7.4 `func (s *Session) RunStream(ctx, userInput string, contentFn func(string)) (*Result, error)` —— 流式对话

**作用**：流式处理用户消息，`contentFn` 逐个接收文本增量（打字机效果）。
流式同样完整支持工具调用（内部自动拼装分片参数）。

**参数说明**：

| 参数 | 说明 |
|---|---|
| `ctx` | 控制超时/取消 |
| `userInput` | 用户输入文本 |
| `contentFn` | 每个文本增量的回调；传 nil 表示忽略增量 |

**演示**：

```go
res, err := s.RunStream(ctx, "讲个笑话", func(part string) {
    fmt.Print(part) // 逐 token 输出
})
fmt.Println()      // 输出换行
if err != nil { /* 处理 */ }
```

---

### 7.5 `func (s *Session) History() []schema.Message` —— 查看会话历史

**作用**：返回当前会话的消息历史（含 system），用于调试、前端展示、上下文分析。

**演示**：

```go
for _, m := range s.History() {
    fmt.Printf("[%s] %s\n", m.Role, m.Content)
}
```

---

### 7.6 外部工具异步执行（阶段3，`Schema.External` / `AsyncRunner` / `SubmitToolResult`）

**作用**：支持"由宿主外部执行的工具"（如桌面端本机命令）。与内置工具"审批 → `Execute` 同步执行"不同，外部工具走 **挂起 → 派发 → 异步回填** 路径。

**执行流程**（`execTool` 检测 `ts.External` → 走 `execExternal`）：

```
agent 循环                     宿主（如桌面端）                 client
   │                                │                            │
   ├─ 注册挂起项(callID→chan)       │                            │
   ├─ Dispatch(ctx, call) ─────────►│（弹确认窗，展示命令全文）    │
   │  （挂起等待 回填通道）          │── invoke 本机执行 ──────────►│
   │                                │◄── 执行结果 ────────────────│
   │                                │（调 session.SubmitToolResult)│
   │◄─ 回填结果 / ExternalExecTimeout（默认120s）超时              │
   ├─ 结果回填历史 role=tool ───────► 继续推理（或本次工具失败）     │
```

**`func (s *Session) SubmitToolResult(callID string, result *schema.ToolResult)`**：
- 作用：宿主执行完外部工具后回填结果，唤醒挂起等待的 agent 循环；
- **线程安全**：可从任意 goroutine 调用（不要求与运行 `Run`/`RunStream` 的 goroutine 相同）；
- 结果投递后挂起项即被移除；未知 `callID`（已超时/已回填/会话已结束）静默忽略（幂等安全）。

**超时语义**：等待时间由 `AgentConfig.ExternalExecTimeout` 控制（默认 120s）。超时未回填 → 本次工具调用按失败结束、会话继续——**绝不无限挂起**。

**演示**（注册一个本地工具 + 注入派发器）：

```go
// 1) 声明外部工具
localShell := schema.ToolSchema{
    Name:        "local_shell",
    Description: "在本机执行 shell 命令。当用户要求操作其本机时使用。",
    Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
    Required:    []string{"command"},
    External:    true, // 阶段3：走外部异步执行路径
}

// 2) 注入派发器（把调用推给宿主；宿主完成时调用 SubmitToolResult 回填）
s, _ := agent.NewSession(cfg, provider, reg,
    agent.WithAsyncRunner(asyncRunner), // AsyncRunner 接口见 7.2
)
```

---

### 7.7 `type Result struct` —— 一次对话的统计结果

**作用**：`Run`/`RunStream` 的返回，除回答外还带统计信息（成本、轮数、工具次数）。

**定义**：

```go
type Result struct {
    Content   string    // 最终回答
    Usage     llm.Usage // 累计 token 用量（成本核算）
    Rounds    int       // 消息循环轮数
    ToolCalls int       // 实际执行的工具调用次数
}
```

**字段说明**：

| 字段 | 说明 |
|---|---|
| `Content` | 模型最终回答文本 |
| `Usage` | 累计 token 用量（见 4.6） |
| `Rounds` | 消息循环轮数（多步任务 > 1） |
| `ToolCalls` | 实际执行的工具次数（诊断 Agent 是否"真的用上了工具"） |

---

## 第 8 章 完整示例汇总

### 8.1 真实聊天（流式 + 工具调用）

```go
// 完整可运行代码见 ../../framework/examples/cli/main.go
apiKey := os.Getenv("DEEPSEEK_API_KEY")
provider, _ := llm.NewOpenAICompatible(llm.Config{
    Name: "deepseek", BaseURL: llm.DeepSeekBaseURL,
    APIKey: apiKey, Model: llm.DeepSeekFlashModel,
})
reg := tool.NewRegistry()
_ = reg.Register(tool.CalculatorTool{})

cfg := schema.AgentConfig{
    Model:        llm.DeepSeekFlashModel,
    SystemPrompt: "你是教学助手，数学计算必须用 calculator 工具。",
    MaxRounds:    8,
}
s, _ := agent.NewSession(cfg, provider, reg)

res, _ := s.RunStream(ctx, "帮我算 12*13", func(part string) { fmt.Print(part) })
fmt.Println() // 换行
```

### 8.2 记忆全演示（不联网）

```go
// 完整可运行代码见 ../../framework/examples/memory-demo/main.go
mem, _ := memory.NewFileLongTermMemory("data/mem.json")
id, _ := mem.Remember(ctx, memory.MemoryEntry{Content: "用户偏好 Go", Tags: []string{"preference"}})
hits, _ := mem.Recall(ctx, "Go", 5)
for _, e := range hits { fmt.Println(e.ID, e.Content) }
```

### 8.3 单元测试（用 Mock 不花钱）

```go
provider := &llm.MockProvider{Content: "你好"}
s, _ := agent.NewSession(cfg, provider, reg)
res, err := s.Run(ctx, "在吗")
// 断言 res.Content == "你好"
```

---

## 第 9 章 注意事项（踩坑预警）

| 场景 | 注意 |
|---|---|
| API Key | 框架只接收显式参数；获取方式由调用方决定（建议 `os.Getenv`），禁止硬编码 |
| 接入新厂商 | 只需改 `Config.BaseURL` + `Model`，无需新增函数；协议不兼容才需新写 `Provider` |
| 流式必须 Close | `ChatStream` 返回的 `Stream` 用完务必 `Close()`；`Next()` 返回 `io.EOF` 即结束 |
| 工具参数不可信 | 自定义工具 `Execute` 内先 `ValidateArgs` 再解析 |
| L2/L3 被"拒绝" | 未提供 `WithApprovalFunc` 时 L2/L3 工具被静默拒绝——是安全设计，不是 bug |
| MaxRounds 过小 | 多步任务建议 >= 8，否则"工具还没用完就报错" |
| 多轮对话 | 同一 `Session` 连续 `Run` 自动共享历史，无需手动拼接 |
| 采样参数 | `Temperature`/`TopP`/`MaxTokens` 均为指针：`nil`=不覆盖；优先级 Request > Config > 服务端默认 |
| 旧模型名 | DeepSeek 旧模型 `deepseek-chat`/`deepseek-reasoner` 已停用，用 `deepseek-v4-*` |

---

*版本 v0.2 · P1 已实现。接口变更时同步更新本文件。*
