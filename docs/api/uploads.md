# 技能 / MCP 上传标准与模板（uploads）

> **面向对象**：管理员——准备上传到管理端（`/v1/admin/*`）的技能包与本地 MCP 代码包。
> **说明**：上传/新建/测试/删除全部位于 `/v1/admin/*` 下，**仅管理员可操作，普通用户无权限**。
> **配套**：[admin.md](./admin.md)（接口契约）· [uploads.md 本文件]（打包标准 + 模板 + 排查）

---

## 1. 技能（Skill）上传标准

### 1.1 目录结构（Anthropic Agent Skills 格式）

```
<技能名>/
├── SKILL.md              ← 必填：frontmatter + 正文使用指引
├── ref/                  ← 可选：被 SKILL.md 相对引用的文档
│   └── guide.md
├── scripts/              ← 可选：模型用 code_executor 执行的脚本
├── assets/               ← 可选：静态资源（图片等，经 /files 渲染）
└── requirements.txt      ← 可选：脚本依赖说明
```

- zip 内可**任意嵌套**（`ref/`、`docs/`、`scripts/`、`assets/` 等子目录），
  上传后**原始目录结构原样保留**——SKILL.md 里的相对引用（如 `ref/guide.md`）可直接命中。
- SKILL.md 可位于 zip 任意层级（取最浅的）；也允许顶层散落文件并存。

### 1.2 SKILL.md 标准模板

```markdown
---
name: 技能名            # 必填：与上传技能名一致（支持中文），也是目录名
description: 一句话说明什么时候使用这个技能（模型据此判断是否调用）
license: MIT            # 可选
metadata:
  version: 1.0.0        # 必填：x.y.z 语义版本号，版本管理的唯一标识
---

# 技能名

给模型的执行指引：什么时候触发、按什么步骤执行、可引用同目录下的脚本与数据文件。
```

### 1.3 校验规则（哪些情况会被拒绝）

| 检查项 | 失败结果 |
|---|---|
| 缺 `name`（且无法从包裹目录名/文件名回退） | `400` 拒绝上传 |
| 缺 `description` / 正文为空 | `400` 拒绝上传/新建 |
| 缺 `metadata.version` 或不是 `x.y.z` 格式 | `400` 拒绝上传/新建 |
| frontmatter `name` 与最终技能名不一致 | `400` 拒绝 |
| 目录名含非法字符（`/`、`..`、`.` 等） | `400` 拒绝（防路径穿越） |
| SKILL.md > 64KB / zip > 10MB | `400` 拒绝 |
| 版本号在"当前 + 全部历史"中已存在且内容不同 | `409 VERSION_CONFLICT`（可 `overwrite` 覆盖） |

> **"解析成功还是失败"的语义**：上传/新建时校验**不通过 → 直接拒绝（不创建）**；
> 已存在的技能目录若 SKILL.md 被手工改坏，会以 `valid:false` 出现在列表（供修复），但**不会自动创建成功**。
> 前端编辑器里的"frontmatter 实时解析"只是**打字预览**，保存/上传以后端权威校验为准。

### 1.4 版本管理语义（版本号唯一）

- 版本身份 = **名字 + 版本号**：同一名字下同一版本号只能有一份（含当前与全部历史）；
- 版本号不同 → 发布新版本（旧内容进历史）；同版本号但内容不同 → 冲突，覆盖或拒绝；
- 回滚 = `POST /v1/admin/skills/{name}/versions/{semver}/restore`。

---

## 2. 本地 MCP 上传标准（把开发好的 MCP 传到服务器本地运行）

> 管理端"新建 Server"只支持**远程 http** 模式；**本地（stdio）MCP 一律通过「上传本地 MCP」**创建。

### 2.1 目录结构

```
<server名>/
├── main.py        ← 入口（也可用 server.py / mcp_server.py / app.py / index.js 等）
├── utils/         ← 被入口 import 的模块
└── requirements.txt
```

上传后解压到服务器 `mcp-servers/<server名>/`，自动注册为：

```json
{
  "name": "<server名>",
  "transport": "stdio",
  "command": "python3",
  "args": ["main.py"],
  "cwd": "mcp-servers/<server名>"
}
```

- **入口自动检测**：`main.py / server.py / mcp_server.py / app.py / entrypoint.py / index.js / server.js / mcp_server.js / entrypoint.js / app.js`，取最浅；也可在 `entry` 字段显式指定。
- **解释器按后缀**：`.py → python3`，`.js/.mjs/.cjs → node`，`.sh → sh`。
- **`cwd` 存相对路径**（相对服务器工作目录 `docker=/app`，本地=`backend/`），保证子进程一定能读到上传的代码。
- **zip ≤ 10MB**；同名上传覆盖代码与配置。

### 2.2 Python MCP 最小模板（`main.py`）

```python
"""本地 MCP 最小示例：注册一个 add 工具。运行前：pip install mcp"""
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("<server名>")

@mcp.tool()
def add(a: int, b: int) -> int:
    """两个整数相加"""
    return a + b

if __name__ == "__main__":
    mcp.run()  # 默认 stdio 传输
```

### 2.3 Node MCP 最小模板（`index.js`）

```js
// 运行前：npm install @modelcontextprotocol/sdk
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

const server = new McpServer({ name: "<server名>", version: "1.0.0" });

server.tool("add", { a: { type: "number" }, b: { type: "number" } },
  async ({ a, b }) => ({ content: [{ type: "text", text: String(a + b) }] }));

const transport = new StdioServerTransport();
await server.connect(transport);
```

### 2.4 运行与验证

1. 上传后回到 MCP 列表 → 点「测试连接」（真实连接 + tools/list）；
2. 测试通过 → 点「启用」（**启用也是真实连接验证**，连不上会报错且不启用）；
3. 启用后列表显示"发现 N 个工具"（悬停看工具名），agent 热加载注册 `mcp_<server名>_<工具名>`。

> **常见失败**：服务器容器内没有对应解释器（镜像只装了 python3，无 node）→ node 类 MCP 会连接失败；
> 或入口缺少依赖（`requirements.txt` 未安装）→ 需先在服务器安装。

### 2.5 依赖怎么处理（重要）

**当前实现：上传只解压代码，不自动执行 `pip install` / `npm install`。**
依赖有两条路：

1. **随包携带**：把依赖目录（`site-packages`/`node_modules`/vendor）打进 zip；或代码自包含（只用标准库）。
2. **服务器预先安装**：进服务器容器安装后再「测试连接」：
   ```bash
   # agent 容器内（上传代码在 /app/mcp-servers/<name>/）
   docker compose exec agent sh -c "pip install -r /app/mcp-servers/calculator-mcp/requirements.txt"
   ```

> 容器当前只装 python3（无 node）；自动安装依赖可作为后续增强（见 PROGRESS 待办）。

---

## 3. 远程 MCP（http）标准配置

管理端"新建 Server"填写：

```json
{
  "name": "weather",
  "transport": "http",
  "url": "https://mcp.example.com/mcp",
  "headers": { "Authorization": "Bearer xxx" },
  "default_permission": "L1"
}
```

也可在 JSON 模式直接粘贴**标准 `mcpServers` 格式**（Claude/trae/workbuddy 通用，
多 server 时前端批量创建）：

```json
{
  "mcpServers": {
    "journal-crawler": {
      "command": "d:\\PyCharm\\projects\\Soup\\.venv\\Scripts\\python.exe",
      "args": ["d:\\PyCharm\\projects\\Soup\\mcp_server.py"],
      "cwd": "d:\\PyCharm\\projects\\Soup"
    }
  }
}
```

---

## 4. 常见问题（FAQ）

| 问题 | 原因 / 处理 |
|---|---|
| 上传技能提示"缺版本号" | SKILL.md frontmatter 没有 `metadata.version`（或非 x.y.z），补齐后重传 |
| 前端预览显示"缺 name"但上传成功 | 前端预览只是启发式（尽力与后端 YAML 对齐）；**以后端校验为准**（成功即 valid） |
| 上传同名同版本成功/失败不符预期 | 版本号相同且内容不同 → `409`；覆盖需 `overwrite=true`（前端会弹确认） |
| 技能文件"拍平"在同一级 | 当前代码已保留嵌套结构；若仍看到，说明**后端还是旧镜像** → `scripts/rebuild.ps1` 重建 |
| MCP 启用报"无法连接" | 真实连接失败：解释器缺失 / 依赖未装 / url 不对 / 超时。修好后重试 |
| 上传的 MCP 代码读不到 | `cwd` 已是相对服务器工作目录的路径；确认 `ADMIN_MCP_SERVERS_DIR` 与 agent 工作目录一致 |
