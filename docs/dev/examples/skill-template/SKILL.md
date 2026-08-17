---
name: example-skill
description: 演示模板：当用户要求演示技能开发/询问技能规范时使用
metadata:
  version: 1.0.0
license: MIT
---

# 技能模板示例

这是 Agent Skill 的**最小完整模板**，用于演示：

1. **frontmatter**：`name`（= 工具名前缀，`-` 会转 `_`）+ `description`（写给模型判断何时调用，必填）+ `metadata.version`（多版本管理唯一标识，推荐 x.y.z）。
2. **正文**：给模型的执行指引——什么时候触发、按什么步骤执行。

## 目录结构（本技能）

```
example-skill/
├── SKILL.md            ← 本文件
├── ref/guide.md        ← 可被正文相对引用的说明文档
└── scripts/hello.sh    ← 模型可经 code_executor 执行的脚本
```

## 使用指引

1. 当用户问"技能怎么写"时，直接引用 `ref/guide.md` 中的规范说明回答；
2. 当用户要求演示技能执行效果时，用 `code_executor` 运行 `scripts/hello.sh`（参数示例：`sh scripts/hello.sh <名称>`），并把输出回给用户；
3. 本技能不修改任何文件系统状态，属只读演示。

## 注意

- `description` 必须具体到"什么场景调用"，否则模型无法判断；
- 正文空、缺 description 的技能会被 agent 跳过（日志 `SKILL.md 解析失败`）；
- 上传到管理端的版本号冲突会返回 `409`，可 overwrite 覆盖。
