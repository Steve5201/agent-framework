# Agent Skill 规范速查

## frontmatter 字段

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | ✅（缺省回退目录名） | 技能名，注册为 `skill_<名称>` 工具（`-` 转 `_`） |
| `description` | ✅ | 一句话说明什么时候使用，模型据此判断是否调用 |
| `license` | — | 授权声明 |
| `metadata.version` | 推荐 | `x.y.z` 语义版本号，多版本管理唯一标识 |

## 校验规则（管理端上传）

- 缺 name/description、正文为空 → 400 拒绝；
- 版本号同版本内容不同 → 409（可 overwrite）；
- 目录名含 `/` `..` `.` 等 → 400 拒绝（防路径穿越）；
- SKILL.md > 64KB / zip > 10MB → 400 拒绝。

## 引用约定

- 同目录相对引用直接可读（`ref/guide.md`、`scripts/x.sh`）；
- `@skills/<技能名>/...` 为 file_ops 的虚拟路径，可读技能资源。

详见 `docs/api/uploads.md` 第 1 节（打包标准与模板）。
