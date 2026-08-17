package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// localShellTool 阶段3·本地 shell 执行工具（External=true，外部代理执行）。
//
// 与 code_executor（沙盒内执行，服务端完成）不同：本工具在"用户本地
// 计算机"执行，框架进程触达不到——因此声明 External=true，agent 循环
// 将其派发给 AsyncRunner 挂起等待，经 agent-service → gateway 下发到
// 桌面客户端，用户在确认弹窗批准后执行，结果经上行 API 回填。
//
// 浏览器环境无法执行本地命令：前端检测到本地工具调用时会立即回填
// "请使用桌面客户端"的失败结果，agent 据此给出降级答复（不长时间挂起）。
type LocalShellTool struct{}

// localShellArgs 本地 shell 参数。
type localShellArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

// Schema 实现 Tool 接口。
//
// 重要：命令在用户本机执行，当前桌面客户端运行在 Windows 上，解析器是
// cmd.exe（非 Unix shell）。模型容易生成 cat/ls/pwd/rm 等 Unix 命令，在
// cmd 下直接退出码 1 失败——因此描述里必须明确目标环境与可用命令，
// 这是"命令看起来失败"的最常见根因（见桌面端实测记录）。
func (LocalShellTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "local_shell",
		Description: "在用户本地计算机执行一条 shell 命令（需桌面客户端弹窗确认后才执行）。仅当用户明确要求操作其本机（查看文件、运行程序、git 操作等）时调用；服务器与代码沙盒内不可用。执行环境为用户的 Windows 电脑，解析器是 cmd.exe（不是 Unix/Linux shell），必须使用 Windows 兼容命令：type（读文本文件）、dir（列目录）、cd /d（切目录）、echo、where、findstr、tasklist、git 等；严禁使用 cat、ls、pwd、grep、rm、touch 等 Unix 命令（会直接失败）。路径可用反斜杠或正斜杠，含空格时用双引号包裹；多命令用 && 或 || 连接。先确认再执行：不确定路径是否存在时，先执行 dir <目录> 查看，再读取目标文件。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string","description":"要在用户本机执行的命令（整行，包含参数）。Windows cmd 语法，例如：dir \"D:\\projects\"、type \"C:\\a.txt\"、git -C \"D:\\repo\" status"},
				"cwd":{"type":"string","description":"工作目录（可选，缺省为用户主目录）"}
			},
			"required":["command"]
		}`),
		Required:   []string{"command"},
		Permission: schema.PermissionL2Write,
		External:   true, // 声明需外部（桌面客户端）执行
	}
}

// Execute 实现 Tool 接口：External=true 的工具不会走到这里（框架直接
// 派发给 AsyncRunner），此方法仅防御性实现，不应被调用。
func (LocalShellTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("local_shell: 需桌面客户端执行，不应在服务端直接调用")
}

// 编译期断言：确保 LocalShellTool 实现了 Tool 接口。
var _ tool.Tool = LocalShellTool{}
