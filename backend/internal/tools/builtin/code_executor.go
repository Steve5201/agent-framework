package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

const (
	// defaultExecTimeout code_executor 默认超时（沙盒默认执行时长）。
	defaultExecTimeout = 60 * time.Second
	// maxExecTimeout 允许的最大超时（防止模型把超时调到离谱值）。
	// 300s 覆盖 matplotlib 字体扫描（单次可达几十秒）与复杂脚本构建。
	maxExecTimeout = 300 * time.Second
	// maxExecOutput 输出截断上限（16KB），避免刷爆上下文。
	maxExecOutput = 16 << 10
)

// CodeExecutorTool L3 代码执行工具：在受限环境下运行 shell / python 代码。
//
// 约束（需求约束）：
//  1. 默认超时 60s（可传 timeout_seconds 覆盖，上限 60s）；
//  2. 危险命令黑名单（rm/sudo/mkfs/dd/fdisk/shutdown/reboot 等），命中即拒绝；
//  3. 工作目录 = 智能体工作目录（与 file_ops 一致），代码只影响该目录。
//
// 说明：本工具是"命令级第一道防线"，真正的进程级沙盒隔离属于 P4 宿主层
// 职责；当前在容器内以非 root 用户运行，天然无法执行系统级破坏操作。
type CodeExecutorTool struct {
	// Root 工作目录；空 = os.Getwd()。
	Root string
	// DefaultTimeout 默认超时（测试可注入短超时）；空 = 60s。
	DefaultTimeout time.Duration
	// Allowlist 命令白名单（正则列表）。非空时仅命中任一正则的命令可执行，
	// 其它一律拒绝（供用户把"无所谓的安全命令"加入白名单后免确认执行）；
	// 空 = 不限制（仅黑名单拦截危险命令）。
	Allowlist []string
	// SandboxURL 沙盒服务地址（阶段2·容器沙盒）。非空时代码执行委托给
	// 独立 sandbox-service（禁网络 + 资源限制 + 每用户独立工作区），
	// 本工具只做前置校验（黑名单/白名单）与结果格式化；空 = 进程内本地执行
	// （本地开发无 sandbox 容器时降级，行为与旧版一致）。
	SandboxURL string
}

// codeExecutorArgs 执行参数。
type codeExecutorArgs struct {
	Language    string `json:"language"`        // shell | python
	Code        string `json:"code"`            // 要执行的代码/命令
	TimeoutSecs int    `json:"timeout_seconds"` // 可选，默认 60，上限 60
}

// sandboxExecRequest / sandboxExecResponse：与 sandbox-service POST /v1/exec 契约。
// 字段与 backend/internal/sandboxsvc 保持一一对应（不引入额外依赖）。
type sandboxExecRequest struct {
	UserID      int64  `json:"user_id"`
	Language    string `json:"language"`
	Code        string `json:"code"`
	TimeoutSecs int    `json:"timeout_seconds"`
}

type sandboxExecResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Error      string `json:"error"` // 沙盒侧拒绝（黑名单/白名单/参数校验）时的说明
}

// blacklistItem 危险命令黑名单条目。
type blacklistItem struct {
	re   *regexp.Regexp
	name string
}

// dangerousCmds 危险命令黑名单（正则 + 说明）。
// \b 词边界：避免误伤常见子串（如 "former" 不会命中 rm）。
// 说明：这是第一道静态过滤；执行本身仍在容器非 root 沙盒环境内。
var dangerousCmds = []blacklistItem{
	{regexp.MustCompile(`\brm\b`), "rm（删除文件/目录）"},
	{regexp.MustCompile(`\bsudo\b`), "sudo（提权）"},
	{regexp.MustCompile(`\bmkfs\b`), "mkfs（格式化磁盘）"},
	{regexp.MustCompile(`\bdd\b`), "dd（可能破坏磁盘）"},
	{regexp.MustCompile(`\bfdisk\b`), "fdisk（分区工具）"},
	{regexp.MustCompile(`\bshutdown\b`), "shutdown"},
	{regexp.MustCompile(`\breboot\b`), "reboot"},
	{regexp.MustCompile(`\bhalt\b`), "halt"},
	{regexp.MustCompile(`\bpoweroff\b`), "poweroff"},
	{regexp.MustCompile(`\binit\b`), "init（系统初始化命令）"},
	{regexp.MustCompile(`\bpasswd\b`), "passwd（改密码）"},
	{regexp.MustCompile(`\buseradd\b`), "useradd（建用户）"},
	{regexp.MustCompile(`\bgroupadd\b`), "groupadd（建用户组）"},
	{regexp.MustCompile(`:\(\)\{\s*:`), "fork 炸弹"},
	// 向块设备（磁盘/分区）重定向写入是危险操作；但 /dev/null、/dev/zero 等
	// 字符设备（丢弃输出、取随机数）是常见安全用法（如 `2>/dev/null`），必须放行。
	{regexp.MustCompile(`>\s*/dev/(sd[a-z]\d*|hd[a-z]\d*|vd[a-z]\d*|nvme\d+n\d+(p\d+)?|mmcblk\d+(p\d+)?|(loop|ram|md)\d+|mapper/[^\s"']+|sr\d+)`), "向块设备重定向写入（磁盘/分区）"},
}

// Schema 实现 Tool 接口。
func (t *CodeExecutorTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "code_executor",
		Description: "执行代码（需用户确认）：在你的用户工作区（当前工作目录）内运行代码并返回输出。language 支持 shell（sh 命令）或 python（python3 解释器）；code 必填，为要执行的命令或源码。timeout_seconds 可选（默认 60 秒，上限 300 秒）。安全约束：禁止 rm、sudo、mkfs、dd、fdisk、shutdown、reboot 等危险命令，命中即拒绝执行。路径约定：一律用相对当前工作目录的相对路径（如 python3 build.py、ls .），你的当前目录 = 用户工作区，写出的文件即交付给用户的文件；不要探索父目录或绝对路径（如 ls /work/users/、find /work、cd /work）——沙盒对你的工作区父目录无读权限，这些必然失败；也不要依赖 2>/dev/null 吞掉错误输出，否则看不到失败原因。仅当用户明确要求计算/处理数据/跑脚本时使用；代码失败时把错误信息原样报告给用户。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"language":{"type":"string","description":"解释器：shell（sh 命令）或 python（python3）"},
				"code":{"type":"string","description":"要执行的命令或源代码"},
				"timeout_seconds":{"type":"integer","description":"超时秒数，默认 60，上限 300"}
			}
		}`),
		Required:   []string{"language", "code"},
		Permission: schema.PermissionL3Dangerous,
	}
}

// Execute 实现 Tool 接口：黑名单/白名单检查 → 选解释器 → 带超时执行 → 返回输出。
// 执行位置（阶段2）：配置了 SandboxURL 时委托给独立 sandbox-service
// （禁网络 + 资源限制 + 每用户独立工作区，真正的进程级隔离在 sandbox 容器内）；
// 未配置时在进程内本地执行（本地开发降级，行为与旧版一致）。
func (t *CodeExecutorTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p codeExecutorArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("code_executor: 参数解析失败: %w", err)
	}
	if strings.TrimSpace(p.Code) == "" {
		return "", fmt.Errorf("code_executor: code 不能为空")
	}

	// 1. 危险命令黑名单过滤（需求约束；与 sandbox 侧双重校验，纵深防御）。
	if hit := CheckDangerousCommand(p.Code); hit != "" {
		return "", fmt.Errorf("code_executor: 代码包含被禁命令 %s，已拒绝执行（危险命令黑名单）", hit)
	}

	// 1.5 命令白名单（若配置）：非空时仅命中任一正则的命令可执行，其余拒绝。
	// 用途：用户把"确认过安全的命令"加入白名单，命中即可自动执行、无需审批。
	if len(t.Allowlist) > 0 && !MatchAllowlist(p.Code, t.Allowlist) {
		return "", fmt.Errorf("code_executor: 命令不在白名单内，已拒绝执行（未命中任一允许规则）")
	}

	// 2. 选择解释器；缺解释器时给出明确错误（模型可据此告知用户）。
	var bin string
	switch p.Language {
	case "shell", "":
		bin = "sh"
	case "python":
		bin = "python3"
	default:
		return "", fmt.Errorf("code_executor: 未知 language %q（仅支持 shell|python）", p.Language)
	}

	// 3. 超时控制（需求约束：默认 60s，可覆盖、上限 60s）。
	timeout := t.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	if p.TimeoutSecs > 0 {
		timeout = time.Duration(p.TimeoutSecs) * time.Second
	}
	if timeout > maxExecTimeout {
		timeout = maxExecTimeout
	}

	// 4. 沙盒路径（阶段2）：配置了 sandbox 服务时，代码执行委托给独立容器。
	if t.SandboxURL != "" {
		uid, _ := UserIDFromContext(ctx)
		return t.remoteExec(ctx, uid, p.Language, p.Code, timeout)
	}

	// 5. 本地降级执行（无 sandbox 容器：本地开发 / 单机部署）。
	// 工作目录同样按用户隔离到 <root>/users/<uid>，与沙盒侧 /work/users/<uid>
	// 保持同一路径语义（file_ops 也如此），避免"同一用户两个工作区"。
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("code_executor: 环境缺少 %s 解释器，无法执行 %s 代码（容器镜像需安装 python3）", bin, p.Language)
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, path, "-c", p.Code)
	root := t.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	if uid, ok := UserIDFromContext(ctx); ok {
		root = filepath.Join(root, "users", strconv.FormatInt(uid, 10))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("code_executor: 创建用户工作区失败: %w", err)
	}
	cmd.Dir = root

	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	text := formatExecOutput(exitCode, out.String(), errb.String())
	if execCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("code_executor: 执行超时（%s），已终止进程\n%s", timeout, text)
	}
	if runErr != nil {
		return text + fmt.Sprintf("\n执行失败：%v\n", runErr), nil
	}
	return text, nil
}

// remoteExec 委托 sandbox-service 执行代码（阶段2·容器沙盒）。
//
// 说明（同步模型）：当前工具循环是"同步阻塞"——本请求 HTTP 阻塞等待
// sandbox 执行完成并回传结果。模型视角与本地执行一致，只是执行环境换到
// 独立沙盒容器。工具级"并行执行"（慢工具等待期间模型继续）是 future
// work，接口与审计/tool_call_id 设计已按并行兼容预留（见 PROGRESS）。
func (t *CodeExecutorTool) remoteExec(ctx context.Context, userID int64, language, code string, timeout time.Duration) (string, error) {
	body, err := json.Marshal(sandboxExecRequest{
		UserID:      userID,
		Language:    language,
		Code:        code,
		TimeoutSecs: int(timeout.Seconds()),
	})
	if err != nil {
		return "", fmt.Errorf("code_executor: 构造沙盒请求失败: %w", err)
	}
	// 请求上下文比执行超时略长：保证 sandbox 侧超时（含进程清理）时
	// 客户端仍能读到最终响应，而不是连接被提前掐断。
	execCtx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(execCtx, http.MethodPost,
		strings.TrimSuffix(t.SandboxURL, "/")+"/v1/exec", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("code_executor: 构造沙盒请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("code_executor: 沙盒服务不可达（%s）: %v（部署时请确认 sandbox 容器健康且 AGENT_SANDBOX_URL 正确）", t.SandboxURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("code_executor: 沙盒执行请求失败（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var r sandboxExecResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxExecOutput+4096)).Decode(&r); err != nil {
		return "", fmt.Errorf("code_executor: 解析沙盒响应失败: %w", err)
	}
	text := formatExecOutput(r.ExitCode, r.Stdout, r.Stderr)
	if r.Error != "" {
		// 沙盒侧拒绝（黑名单命中/参数校验失败）时 Error 描述性完整，直接返回；
		// 执行失败（exit != 0）时 Error 只是 "执行失败：exit status N"，
		// 真正的 traceback 在 stdout/stderr 里——必须带上，否则模型看不到
		// 失败原因，只能靠试错重跑（浪费 token 且易跑偏）。
		if r.ExitCode != 0 || r.TimedOut {
			return text + fmt.Sprintf("\n沙盒执行失败：%s\n", r.Error), nil
		}
		return "", fmt.Errorf("code_executor: 沙盒拒绝执行: %s", r.Error)
	}
	if r.TimedOut {
		return "", fmt.Errorf("code_executor: 执行超时（%d 秒），已在沙盒内终止进程\n%s", int(timeout.Seconds()), text)
	}
	return text, nil
}

// formatExecOutput 统一格式化执行结果（本地/沙盒共用），保证对模型输出一致。
func formatExecOutput(exitCode int, stdout, stderr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "退出码：%d\n", exitCode)
	if stdout != "" {
		b.WriteString("标准输出：\n")
		b.WriteString(TruncateOutput(stdout))
	}
	if stderr != "" {
		b.WriteString("标准错误：\n")
		b.WriteString(TruncateOutput(stderr))
	}
	return b.String()
}

// CheckDangerousCommand 检查代码是否命中危险命令黑名单；命中返回被禁命令名，
// 否则空串。供 code_executor 与 sandbox-service 双重校验（纵深防御）。
func CheckDangerousCommand(code string) string {
	for _, item := range dangerousCmds {
		if item.re.MatchString(code) {
			return item.name
		}
	}
	return ""
}

// MatchAllowlist 检查代码是否命中任一白名单正则。
func MatchAllowlist(code string, patterns []string) bool {
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(code) {
			return true
		}
	}
	return false
}

// TruncateOutput 截断输出到上限，避免刷爆 LLM 上下文。
func TruncateOutput(s string) string {
	if len(s) <= maxExecOutput {
		return s
	}
	return s[:maxExecOutput] + fmt.Sprintf("\n……（输出超过 %d 字节，已截断）", maxExecOutput)
}

// 编译期断言：CodeExecutorTool 实现 Tool 接口。
var _ tool.Tool = (*CodeExecutorTool)(nil)
