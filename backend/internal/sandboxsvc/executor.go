// Package sandboxsvc 提供独立容器沙盒服务（阶段2·L1+L2）：
//
// 代码执行从 agent 容器迁出，统一由 sandbox-service 执行，进程级隔离：
//  1. 每用户独立工作区  /work/users/<user_id>，且每用户映射独立 uid
//     （uid = UIDBase + user_id，uid==gid，目录 2775 = 属主派生 uid +
//     属组 app 组，setgid 继承），文件系统级强隔离：用户 A 的脚本读不到
//     用户 B 的目录（other=0，OS 权限拒绝），agent 经 app 组权限协作读写；
//  2. unshare -n 新建网络命名空间 → 执行进程无任何网卡，天然禁网；
//  3. prlimit 资源限制（CPU 时间 / 虚拟内存 / 打开文件数上限）；
//  4. setpriv 降权到该用户的专属 uid（即使代码尝试提权也无法越过容器能力）；
//  5. 超时终止（默认 60s，上限 60s，杀死整个进程组）；
//  6. 危险命令黑名单 + 命令白名单（与 code_executor 同一套静态校验，纵深防御）。
//
// 本服务只监听 compose 内部网络（不发布宿主端口），仅 agent 可调用。
package sandboxsvc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"go.uber.org/zap"
)

const (
	// maxOutputBytes 单次执行输出上限（与 code_executor 一致，防刷爆上下文）。
	maxOutputBytes = 16 << 10
	// defaultTimeout 默认执行超时。
	defaultTimeout = 30 * time.Second
)

// ExecRequest POST /v1/exec 请求体（与 builtin.sandboxExecRequest 契约一致）。
type ExecRequest struct {
	UserID      int64    `json:"user_id"`         // 必填：>0，用于划分独立工作区
	Language    string   `json:"language"`        // shell | python（profile 模式必须为空）
	Code        string   `json:"code"`            // 要执行的命令/源码（profile 模式必须为空）
	TimeoutSecs int      `json:"timeout_seconds"` // 可选，默认 60，上限 MaxTimeout（profile 模式放宽到其专属上限）
	Profile     string   `json:"profile"`         // 可选：预置解析器名（parse_pdf|parse_docx|parse_pptx），与 language/code 互斥
	Args        []string `json:"args"`            // profile 模式的脚本参数（相对用户工作区的 input/out/media 路径）
	// 以下为按请求覆盖（0/false = 回退服务实例 Config 默认），供会话级沙盒配置
	// 动态切换（agent_admin 设定，见 agentsvc.SessionConfig.Sandbox*）。禁网为
	// 默认安全基线：除非显式 network_enabled=true，一律 unshare -n 禁网。
	NetworkEnabled bool `json:"network_enabled,omitempty"`
	MemoryMB       int64 `json:"memory_mb,omitempty"`       // 虚拟内存上限（MB），0 = 回退全局
	CPUSeconds     int64 `json:"cpu_seconds,omitempty"`     // CPU 时间上限（秒），0 = 回退全局
	NofileLimit    int64 `json:"nofile_limit,omitempty"`    // 最大打开文件数，0 = 回退全局
	MaxTimeoutSecs int   `json:"max_timeout_secs,omitempty"` // 单次执行最大超时（秒），0 = 回退全局
}

// ExecResult 执行结果。
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Error      string `json:"error,omitempty"` // 沙盒侧拒绝（黑名单/白名单/参数校验）
}

// Config 沙盒执行器配置（均由环境变量注入，见 cmd/sandbox）。
type Config struct {
	// WorkRoot 每用户工作区根目录（容器内，默认 /work）。
	WorkRoot string
	// MemoryLimitMB 单进程虚拟内存上限（RLIMIT_AS），0 = 不限制。
	MemoryLimitMB int64
	// ProfileMemoryLimitMB profile 模式（可信预置解析/渲染脚本，非用户输入）
	// 的虚拟内存上限（RLIMIT_AS），0 = 跟随 MemoryLimitMB。
	// P4-J 修复：matplotlib/numpy 渲染公式/图片的虚拟内存需求远超普通代码，
	// 512MB 限制下脚本 syscall 风暴卡死（60s 超时）；可信脚本单独放宽。
	ProfileMemoryLimitMB int64
	// CPUSeconds 单进程 CPU 时间上限（RLIMIT_CPU），0 = 不限制。
	CPUSeconds int64
	// NofileLimit 单进程最大打开文件数（RLIMIT_NOFILE），0 = 不限制。
	NofileLimit int64
	// MaxTimeout 单次执行允许的最大超时；默认 60s。
	MaxTimeout time.Duration
	// Allowlist 命令白名单（正则）；空 = 仅黑名单拦截。
	Allowlist []string
	// AgentUID agent 容器内 app 用户 uid（SANDBOX_AGENT_UID，默认 100）。
	// 用户工作区以「组协作」方式授权给 agent（无 POSIX ACL 的文件系统
	// 如 Docker Desktop bind mount 上 ACL 不可用）：用户目录属主 = 派生 uid，
	// 属组 = AgentGID（app 组），mode 2775（setgid 继承组）→ agent 经组权限
	// 协作读写，而沙盒用户之间按独立 uid + other=0 强隔离。
	AgentUID int
	// AgentGID agent 容器内 app 用户 gid（SANDBOX_AGENT_GID，默认 101，
	// 与镜像内 addgroup -S app 分配的 gid 一致）。
	AgentGID int
	// UIDBase 沙盒执行用户 uid 池起点（SANDBOX_UID_BASE，默认 2000）。
	// 每个用户映射 uid = UIDBase + user_id（uid == gid，不取模避免碰撞）：
	// 用户目录属主即该 uid，其它用户（uid/group 不同、other=0）被 OS 拒绝。
	UIDBase int
	// ParsersDir 预置解析脚本目录（SANDBOX_PARSERS_DIR，默认 /opt/rag-parsers，
	// 镜像构建期 COPY backend/scripts/parsers）。profile 模式的脚本与 Go 代码
	// 同仓版本管理，非用户输入，故跳过 code 黑名单/白名单校验。
	ParsersDir string
	// Log 日志实例。
	Log *zap.Logger
}

// maxSandboxUID 沙盒执行用户 uid 上限（保留 60000 以上给系统/nobody 等）。
const maxSandboxUID = 60000

// Executor 沙盒执行器。
type Executor struct {
	cfg Config
}

// NewExecutor 构造执行器（WorkRoot 缺省 /work，AgentUID 缺省 100，UIDBase 缺省 2000）。
func NewExecutor(cfg Config) *Executor {
	if cfg.WorkRoot == "" {
		cfg.WorkRoot = "/work"
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = 300 * time.Second
	}
	if cfg.AgentUID <= 0 {
		cfg.AgentUID = 100
	}
	if cfg.AgentGID <= 0 {
		cfg.AgentGID = 101
	}
	if cfg.UIDBase <= 0 {
		cfg.UIDBase = 2000
	}
	if cfg.ParsersDir == "" {
		cfg.ParsersDir = "/opt/rag-parsers"
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	return &Executor{cfg: cfg}
}

// uidForUser 将业务 user_id 映射为沙盒执行 uid（uid == gid）。
// 直接线性映射（不取模）保证任意两个用户 uid 互不碰撞，杜绝跨用户访问。
func (e *Executor) uidForUser(userID int64) (int, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("sandbox: user_id 必须为正整数")
	}
	uid := int64(e.cfg.UIDBase) + userID
	if uid >= maxSandboxUID {
		return 0, fmt.Errorf("sandbox: user_id=%d 超出可映射 uid 上限（< %d）", userID, maxSandboxUID-int64(e.cfg.UIDBase))
	}
	return int(uid), nil
}

// Exec 执行一次代码（或预置解析脚本 profile），返回结构化结果。
// 所有校验（参数/黑名单/白名单）先于任何进程创建完成，命中即返回 Error。
func (e *Executor) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	// ---- 1. 参数校验 ----
	if req.UserID <= 0 {
		return nil, fmt.Errorf("sandbox: user_id 必须为正整数（按用户隔离工作区）")
	}

	// profile 模式：执行镜像预置解析脚本（脚本非用户输入，跳过 code 黑名单/白名单）。
	profileMode := strings.TrimSpace(req.Profile) != ""
	if !profileMode {
		if strings.TrimSpace(req.Code) == "" {
			return nil, fmt.Errorf("sandbox: code 不能为空")
		}

		// ---- 2. 静态安全校验（与 agent 侧 code_executor 双重拦截） ----
		if hit := builtin.CheckDangerousCommand(req.Code); hit != "" {
			return nil, fmt.Errorf("sandbox: 代码包含被禁命令 %s，已拒绝执行（危险命令黑名单）", hit)
		}
		if len(e.cfg.Allowlist) > 0 && !builtin.MatchAllowlist(req.Code, e.cfg.Allowlist) {
			return nil, fmt.Errorf("sandbox: 命令不在白名单内，已拒绝执行（未命中任一允许规则）")
		}
	}

	// ---- 3. 解释器 / 预置解析脚本 ----
	// runArgs 统一为「最终要执行的命令 + 参数」：
	//   profile 模式：<cmd> <ParsersDir>/<script> <args...>
	//   普通模式：    <interp> -c <code>
	var runArgs []string
	profileMaxTimeout := time.Duration(0)
	if profileMode {
		spec, ok := parserProfiles[req.Profile]
		if !ok {
			return nil, fmt.Errorf("sandbox: 未知 profile %q（可用：parse_pdf|parse_docx|parse_pptx|render_docx|render_pptx|render_pdf）", req.Profile)
		}
		if req.Language != "" {
			return nil, fmt.Errorf("sandbox: profile 模式不接受 language（%q）", req.Language)
		}
		if strings.TrimSpace(req.Code) != "" {
			return nil, fmt.Errorf("sandbox: profile 模式不接受 code")
		}
		if len(req.Args) != spec.ArgCount {
			return nil, fmt.Errorf("sandbox: profile %s 需要 %d 个参数（input/out/media），收到 %d",
				req.Profile, spec.ArgCount, len(req.Args))
		}
		runArgs = append(runArgs, spec.Cmd, filepath.Join(e.cfg.ParsersDir, spec.Script))
		runArgs = append(runArgs, req.Args...)
		profileMaxTimeout = spec.MaxTimeout
	} else {
		switch req.Language {
		case "shell", "":
			runArgs = []string{"sh", "-c", req.Code}
		case "python":
			runArgs = []string{"python3", "-c", req.Code}
		default:
			return nil, fmt.Errorf("sandbox: 未知 language %q（仅支持 shell|python）", req.Language)
		}
	}

	// ---- 4. 每用户独立工作区 + 独立 uid（系统级强制隔离） ----
	// 用户目录 users/<user_id> 属主 = 派生 uid（UIDBase+user_id），属组 = app 组，
	// mode 2775（setgid）：沙盒执行进程是属主 → 可写；agent 经组权限协作读写；
	// 其它用户 uid/group 均不匹配且 other=0 → 被 OS 拒绝。
	wsUid, err := e.uidForUser(req.UserID)
	if err != nil {
		return nil, err
	}
	ws := filepath.Join(e.cfg.WorkRoot, "users", strconv.FormatInt(req.UserID, 10))
	if err := e.ensureWorkspace(ws, wsUid); err != nil {
		return nil, err
	}
	// profile 模式：修正 ingest 临时目录权限，让降权解析进程能写 out.json。
	if profileMode {
		e.prepareProfileDirs(wsUid, req.Args)
	}

	// ---- 5. 超时（profile 模式放宽到其专属上限，普通执行仍受 MaxTimeout 约束） ----
	timeout := defaultTimeout
	if req.TimeoutSecs > 0 {
		timeout = time.Duration(req.TimeoutSecs) * time.Second
	}
	maxT := e.cfg.MaxTimeout
	if req.MaxTimeoutSecs > 0 {
		maxT = time.Duration(req.MaxTimeoutSecs) * time.Second
	}
	if profileMaxTimeout > maxT {
		maxT = profileMaxTimeout
	}
	if timeout > maxT {
		timeout = maxT
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// ---- 6. 构建命令：unshare -n（禁网）→ prlimit（资源上限）→ setpriv（降权） ----
	args := e.buildCommandArgs(req, profileMode, runArgs, wsUid)
	cmd := exec.CommandContext(execCtx, args[0], args[1:]...)
	cmd.Dir = ws
	setProcAttr(cmd) // 独立进程组：超时/异常时整体终止子进程树

	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)

	// 超时（或父 ctx 取消）→ 终止整个进程组，避免孤儿进程。
	timedOut := execCtx.Err() == context.DeadlineExceeded
	if timedOut && cmd.Process != nil {
		killProcessGroup(cmd)
	}

	res := &ExecResult{
		Stdout:     builtin.TruncateOutput(out.String()),
		Stderr:     builtin.TruncateOutput(errb.String()),
		DurationMs: dur.Milliseconds(),
		TimedOut:   timedOut,
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	if timedOut {
		res.Error = fmt.Sprintf("执行超时（%s），已在沙盒内终止进程组", timeout)
	} else if runErr != nil {
		res.Error = fmt.Sprintf("执行失败：%v", runErr)
	}
	e.cfg.Log.Info("sandbox exec done",
		zap.Int64("user_id", req.UserID),
		zap.String("language", req.Language),
		zap.Int("exit_code", res.ExitCode),
		zap.Int64("duration_ms", res.DurationMs),
		zap.Bool("timed_out", timedOut))
	return res, nil
}

// buildCommandArgs 组装沙盒执行命令参数：unshare -n（禁网）→ prlimit（资源
// 上限）→ setpriv（降权）。网络开关默认禁网（安全基线）；仅当请求显式
// network_enabled=true 时跳过 unshare -n（放行联网）。放行由会话级管理员配置
// 驱动（如 fetch_url_render 需 chromium 渲染外网动态页），普通 code 执行保持禁网。
// 资源限制取"请求覆盖 > 全局配置"，0 = 该限制不设置。
func (e *Executor) buildCommandArgs(req ExecRequest, profileMode bool, runArgs []string, wsUid int) []string {
	var args []string
	if !req.NetworkEnabled {
		args = []string{"unshare", "-n", "--"}
	}
	prlimitArgs := []string{"prlimit"}
	cpu := e.cfg.CPUSeconds
	if req.CPUSeconds > 0 {
		cpu = req.CPUSeconds
	}
	if cpu > 0 {
		prlimitArgs = append(prlimitArgs, fmt.Sprintf("--cpu=%d", cpu))
	}
	// profile 模式执行的是镜像内可信脚本（解析/渲染，非用户代码）：内存上限
	// 单独放宽（ProfileMemoryLimitMB），避免 matplotlib/numpy 渲染公式/图片时
	// 虚拟内存不足导致 syscall 风暴卡死；普通代码执行仍按 MemoryLimitMB 收紧。
	memLimit := e.cfg.MemoryLimitMB
	if profileMode && e.cfg.ProfileMemoryLimitMB > 0 {
		memLimit = e.cfg.ProfileMemoryLimitMB
	}
	if req.MemoryMB > 0 {
		memLimit = req.MemoryMB
	}
	// render_pdf / fetch_render（Chromium headless）与 RLIMIT_AS 存在兼容性缺陷
	// （P5-HTML 实测）：Alpine/musl 下只要设置 RLIMIT_AS（与值大小无关，
	// 2/4/8GB 均触发），chromium 内部 CHECK 即崩溃（Trace/breakpoint trap），只能
	// 不设置。chromium 渲染也会 mmap 大量虚拟地址空间。故跳过 --as 限制；
	// 降权/禁网/nofile/cpu/超时仍生效。
	if profileMode && (req.Profile == "render_pdf" || req.Profile == "fetch_render") {
		memLimit = 0
	}
	if memLimit > 0 {
		prlimitArgs = append(prlimitArgs, fmt.Sprintf("--as=%d", memLimit<<20))
	}
	nofile := e.cfg.NofileLimit
	if req.NofileLimit > 0 {
		nofile = req.NofileLimit
	}
	if nofile > 0 {
		prlimitArgs = append(prlimitArgs, fmt.Sprintf("--nofile=%d", nofile))
	}
	prlimitArgs = append(prlimitArgs, "--")
	args = append(args, prlimitArgs...)
	args = append(args,
		"setpriv",
		fmt.Sprintf("--reuid=%d", wsUid),
		fmt.Sprintf("--regid=%d", wsUid),
		"--clear-groups", "--",
	)
	args = append(args, runArgs...)
	return args
}

// ensureWorkspace 确保用户工作区目录链存在且权限模型正确（档位 B：每用户独立 uid）。
//
// 权限模型（组协作方案：Docker Desktop bind mount 不支持 POSIX ACL/setgid，
// 统一走纯权限位，见 fixUserDir）：
//   - 中间层 users/：root:AgentGID 0771 —— app 组可读写（可创建用户目录），
//     沙盒执行用户（--clear-groups）仅可穿透（o+x）不可列。
//   - 用户目录 users/<user_id>：属主 = 派生 uid（uid == gid），属组 = app 组，
//     mode 2770（setgid 继承组；bind mount 不生效时退化为读互通、各写各的）。
//     其它沙盒用户 uid/group 不匹配、other=0 → 被 OS 权限拒绝（文件系统级强隔离）。
//   - 绝不能修改 WorkRoot 本身 —— 它是与 agent/gateway 共享的卷根（容器内挂为
//     /app）。gateway 原子写 mcp_servers.json（tmp+rename）、agent file_ops 写
//     卷根都依赖 app 用户对卷根可写；若 chown 为 root:0711，会直接导致它们写
//     文件 permission denied（实测：管理端启用 MCP 返回 500，即档位 B 回归）。
//
// 兼容旧卷迁移：目录属主不是派生 uid 时，一次性全量 chown -R 到 uid:AgentGID，
// 把历史文件（可能属主为 app 100）归到用户 uid 下，此后属主一致、不再重复。
// 非 Linux（本地调试）无 chown 语义：记录 WARN 但不阻断。
func (e *Executor) ensureWorkspace(ws string, uid int) error {
	rel, err := filepath.Rel(e.cfg.WorkRoot, ws)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sandbox: 工作区路径越界: %s", ws)
	}

	// 1. 逐层创建：中间层 0711（可穿透不可列），末层先 0700（稍后统一纠偏）。
	segs := strings.Split(rel, string(filepath.Separator))
	cur := e.cfg.WorkRoot
	for i, seg := range segs {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		mode := os.FileMode(0o711)
		if i == len(segs)-1 {
			mode = 0o700
		}
		if err := os.MkdirAll(cur, mode); err != nil {
			return fmt.Errorf("sandbox: 创建工作区目录失败 %s: %w", cur, err)
		}
	}

	// 2. 中间层 users/ 属主纠正为 root:AgentGID 0771：root 可管理；app 组
	//    成员（agent uid 100）可读可写，能在此创建用户目录（file_ops 首次
	//    写文件时）；沙盒执行用户（其它 uid/gid，--clear-groups）仅可穿透。
	//    注意：绝不能修改 WorkRoot 本身 —— 它是与 agent/gateway 共享的卷根
	//    （容器内挂为 /app）。gateway 原子写 mcp_servers.json（tmp+rename）、
	//    agent file_ops 写卷根都依赖 app 用户对卷根可写；若 chown 为 root:0711，
	//    会直接导致它们写文件 permission denied（实测：管理端启用 MCP 返回 500）。
	parent := filepath.Dir(ws)
	if _, err := os.Lstat(parent); err == nil {
		_ = os.Chown(parent, 0, e.cfg.AgentGID)
		_ = os.Chmod(parent, 0o771)
	}

	// 3. 末层用户目录：属主/ACL/权限位纠偏。
	return e.fixUserDir(ws, uid)
}

// fixUserDir 维护用户目录属主（派生 uid:app 组）与 2770 权限（setgid 继承组）。
//
// 组协作模型（无 ACL 文件系统——如 Docker Desktop bind mount——的通用方案）：
//   - 属主 = 派生 uid（uid == gid）：沙盒执行进程（setpriv 到该 uid）是属主，
//     拥有全部权限；其它沙盒用户（不同 uid，且非 app 组成员）other=0 →
//     目录本身即被 OS 拒绝（文件系统级强隔离，B 无法进入 A 的目录）。
//   - 属组 = app 组（AgentGID）：agent 容器内 app 用户（uid 100, gid=AgentGID）
//     经组权限 rwx 读写用户目录（创建/删除文件）；setgid 在 Linux 上使新建
//     文件继承 app 组（agent 可经组权限协作改文件），Docker Desktop bind mount
//     不支持 setgid 时退化为"读互通、各写各的"（仍不破坏隔离）。
func (e *Executor) fixUserDir(ws string, uid int) error {
	needMigrate := true
	if st, err := os.Lstat(ws); err == nil {
		if fileOwnerID(st) == uid && fileGroupID(st) == e.cfg.AgentGID {
			needMigrate = false // 属主+属组均正确：已迁移过
		}
	}
	if needMigrate {
		// 首次创建或旧卷迁移：全量属主纠正（chown -R 需 root 主进程）。
		if err := e.recursiveChown(ws, uid); err != nil {
			e.cfg.Log.Warn("workspace recursive chown failed", zap.String("path", ws), zap.Int("uid", uid), zap.Int("gid", e.cfg.AgentGID), zap.Error(err))
		}
	}
	if err := os.Chmod(ws, 0o2770); err != nil {
		e.cfg.Log.Warn("workspace chmod failed", zap.String("path", ws), zap.Error(err))
	}
	return nil
}

// prepareProfileDirs 修正解析脚本的 ingest 临时目录权限 + 预创建公共媒体区（P3-A3b/A8）。
//
// 1. ingest 目录：rag 以 app 组身份创建 ingest/<docID>（默认 0755，属主 app），
// 而解析进程经 setpriv --clear-groups 降权到派生 uid（无任何附属组）——对 ingest
// 目录属 other，没有写权限，脚本写 out.json 会 PermissionError（实测复现）。
// 沙盒主进程是 root：执行前把 ingest 目录属主纠正为 派生uid:app组、mode 2770——
// 解析进程（属主）可写 out.json；rag（app 组）仍可读回产物并清理目录；其它
// 沙盒用户 other=0 保持强隔离。
//
// 2. 媒体公共只读区：媒体从用户私有目录 users/<uid>/rag-media 迁到共享卷根的
// rag-media/（所有用户可经 agent /files 读取）。/work 属主为 app:app 且其他用户
// 不可写，降权解析进程无权创建 /work/rag-media —— 由主进程（root）预创建并
// chmod 0777，解析进程才能在其中建 <docID> 子目录（脚本 ensure_media_dir 再
// 把子目录也设为 0777，文件 0644 全用户可读）。非 Linux 本地调试无 chown 语义：
// 记录 WARN 不阻断（后续脚本会带明确 stderr 报错）。
func (e *Executor) prepareProfileDirs(uid int, args []string) {
	if len(args) < 2 {
		return
	}
	ingestDir := filepath.Dir(args[1]) // out.json 所在目录 = rag 建的 ingest 临时目录
	if _, err := os.Lstat(ingestDir); err == nil {
		if err := os.Chown(ingestDir, uid, e.cfg.AgentGID); err != nil {
			e.cfg.Log.Warn("prepare profile ingest dir chown failed",
				zap.String("dir", ingestDir), zap.Int("uid", uid), zap.Int("gid", e.cfg.AgentGID), zap.Error(err))
			return
		}
		if err := os.Chmod(ingestDir, 0o2770); err != nil {
			e.cfg.Log.Warn("prepare profile ingest dir chmod failed", zap.String("dir", ingestDir), zap.Error(err))
		}
	}
	// 媒体公共区父目录：args[2] = <WorkRoot>/rag-media/<docID>，父级 = <WorkRoot>/rag-media。
	if len(args) >= 3 && args[2] != "" {
		mediaParent := filepath.Dir(args[2])
		if err := os.MkdirAll(mediaParent, 0o777); err != nil {
			e.cfg.Log.Warn("prepare profile media dir mkdir failed", zap.String("dir", mediaParent), zap.Error(err))
		} else if err := os.Chmod(mediaParent, 0o777); err != nil {
			e.cfg.Log.Warn("prepare profile media dir chmod failed", zap.String("dir", mediaParent), zap.Error(err))
		}
	}
}

// recursiveChown 递归修正工作区属主为 uid:AgentGID（工具 chown，非 Linux 不可用则报错）。
func (e *Executor) recursiveChown(ws string, uid int) error {
	_, err := exec.LookPath("chown")
	if err != nil {
		return fmt.Errorf("chown 不可用: %w", err)
	}
	return exec.Command("chown", "-R", fmt.Sprintf("%d:%d", uid, e.cfg.AgentGID), ws).Run()
}
