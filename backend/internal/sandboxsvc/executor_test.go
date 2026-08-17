// executor_test.go —— sandboxsvc 执行器单测。
//
// 覆盖：默认值装配 / 参数校验 / 黑名单 / 白名单（执行前即拒绝，无需真实环境）；
// uidForUser 映射（不同用户不同 uid、溢出拒绝）；
// Linux 上（GOOS=linux）额外跑真实执行（unshare+prlimit+setpriv 链路）、
// 工作区属主为派生 uid、以及超时终止。
package sandboxsvc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	return NewExecutor(Config{
		WorkRoot:      t.TempDir(),
		MemoryLimitMB: 512,
		CPUSeconds:    60,
		NofileLimit:   1024,
		MaxTimeout:    5 * time.Second,
		AgentUID:      1000,
		AgentGID:      1001,
		UIDBase:       2000,
		Log:           zap.NewNop(),
	})
}

func TestNewExecutorDefaults(t *testing.T) {
	e := NewExecutor(Config{Log: zap.NewNop()})
	if e.cfg.WorkRoot != "/work" {
		t.Fatalf("WorkRoot 默认值错误: %q", e.cfg.WorkRoot)
	}
	if e.cfg.AgentUID != 100 || e.cfg.AgentGID != 101 || e.cfg.UIDBase != 2000 {
		t.Fatalf("AgentUID/AgentGID/UIDBase 默认值错误: %d/%d/%d", e.cfg.AgentUID, e.cfg.AgentGID, e.cfg.UIDBase)
	}
	if e.cfg.MaxTimeout != 300*time.Second {
		t.Fatalf("MaxTimeout 默认值错误: %v", e.cfg.MaxTimeout)
	}
}

// TestUIDForUser 用户→uid 映射：线性、互不碰撞、非法输入拒绝。
func TestUIDForUser(t *testing.T) {
	e := newTestExecutor(t)

	// 正常映射：uid = UIDBase + user_id，且 uid==gid（执行时 reuid=regid）。
	for _, tc := range []struct {
		user int64
		want int
	}{
		{1, 2001},
		{42, 2042},
		{999, 2999},
	} {
		got, err := e.uidForUser(tc.user)
		if err != nil {
			t.Fatalf("uidForUser(%d) 不应报错: %v", tc.user, err)
		}
		if got != tc.want {
			t.Errorf("uidForUser(%d) = %d, want %d", tc.user, got, tc.want)
		}
	}

	// 不同用户必须映射到不同 uid（强隔离前提）。
	a, errA := e.uidForUser(7)
	b, errB := e.uidForUser(8)
	if errA != nil || errB != nil {
		t.Fatalf("正常映射不应报错: %v/%v", errA, errB)
	}
	if a == b {
		t.Fatalf("不同用户映射到同一 uid: %d（碰撞会破坏隔离）", a)
	}

	// 非法输入：user_id<=0、超范围。
	if _, err := e.uidForUser(0); err == nil {
		t.Error("user_id=0 应报错")
	}
	if _, err := e.uidForUser(-1); err == nil {
		t.Error("负 user_id 应报错")
	}
	if _, err := e.uidForUser(int64(maxSandboxUID - e.cfg.UIDBase)); err == nil {
		t.Error("超 uid 池上限的 user_id 应报错")
	}
}

func TestExec_Validation(t *testing.T) {
	e := newTestExecutor(t)

	// user_id 必须为正整数（工作区按用户隔离）
	if _, err := e.Exec(context.Background(), ExecRequest{UserID: 0, Code: "echo hi"}); err == nil {
		t.Fatal("user_id=0 应报错")
	}
	// code 不能为空
	if _, err := e.Exec(context.Background(), ExecRequest{UserID: 1, Code: "  "}); err == nil {
		t.Fatal("空 code 应报错")
	}
	// 未知 language
	if _, err := e.Exec(context.Background(), ExecRequest{UserID: 1, Language: "ruby", Code: "puts 1"}); err == nil {
		t.Fatal("未知 language 应报错")
	}
}

func TestExec_Blacklist(t *testing.T) {
	e := newTestExecutor(t)
	for _, code := range []string{"rm -rf /", "sudo whoami", "mkfs.ext4 /dev/sda"} {
		if _, err := e.Exec(context.Background(), ExecRequest{UserID: 1, Code: code}); err == nil {
			t.Fatalf("黑名单命令应被拒绝: %s", code)
		}
	}
}

func TestExec_Allowlist(t *testing.T) {
	e := newTestExecutor(t)
	e.cfg.Allowlist = []string{`^echo `}
	if _, err := e.Exec(context.Background(), ExecRequest{UserID: 1, Code: "cat /etc/hostname"}); err == nil {
		t.Fatal("不在白名单内的命令应被拒绝")
	}
	// 命中白名单则进入执行阶段（非 Linux 上 unshare 不存在会报"命令不存在"，
	// 说明已越过白名单校验，而不是被白名单拦截）。
	if runtime.GOOS == "linux" {
		res, err := e.Exec(context.Background(), ExecRequest{UserID: 1, Code: "echo ok"})
		if err != nil {
			t.Fatalf("白名单内命令应放行: %v", err)
		}
		if !strings.Contains(res.Stdout, "ok") {
			t.Fatalf("输出异常: %+v", res)
		}
	}
}

// TestExec_RealLinux 真实执行链路（仅 Linux）：验证 unshare/prlimit/setpriv
// 可用、输出正确返回、工作区按用户隔离创建、以及超时终止。
func TestExec_RealLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("沙盒完整隔离仅支持 Linux（unshare/prlimit/setpriv）")
	}
	e := newTestExecutor(t)

	res, err := e.Exec(context.Background(), ExecRequest{
		UserID: 42, Language: "shell", Code: "echo sandbox-ok",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Stdout, "sandbox-ok") {
		t.Fatalf("stdout 异常: %+v", res)
	}
	// 工作区目录已按用户隔离创建（/work/users/42），且属主为派生 uid（2000+42=2042）、
	// 属组为 app 组（AgentGID=1001）、mode 2770（setgid 继承组，agent 经组权限协作）。
	ws := filepath.Join(e.cfg.WorkRoot, "users", strconv.Itoa(42))
	st, statErr := os.Stat(ws)
	if statErr != nil {
		t.Fatalf("用户工作区未创建: %v", statErr)
	}
	if owner := fileOwnerID(st); owner != 2042 {
		t.Fatalf("工作区属主错误: uid=%d, want 2042（用户独立 uid 未生效）", owner)
	}
	if fileGroupID(st) != 1001 {
		t.Fatalf("工作区属组错误: gid=%d, want 1001（app 组协作未生效）", fileGroupID(st))
	}
	if perm := st.Mode().Perm(); perm != 0o770 {
		t.Fatalf("工作区权限错误: %o, want 770（agent 经组权限协作读写，用户间 other=0）", perm)
	}
	if st.Mode()&os.ModeSetgid == 0 {
		t.Fatal("工作区缺少 setgid 位（新建文件应继承 app 组）")
	}
	// 中间层 users/ 应为 root:app 组 0771（可穿透不可列；app 组可创建用户目录），
	// 且执行进程以派生 uid 运行。
	usersDir := filepath.Join(e.cfg.WorkRoot, "users")
	if st, err := os.Stat(usersDir); err != nil {
		t.Fatalf("users 层缺失: %v", err)
	} else if owner := fileOwnerID(st); owner != 0 {
		t.Fatalf("users 层属主应为 root: uid=%d", owner)
	} else if fileGroupID(st) != 1001 {
		t.Fatalf("users 层属组应为 app 组 1001: gid=%d", fileGroupID(st))
	} else if perm := st.Mode().Perm(); perm != 0o771 {
		t.Fatalf("users 层权限错误: %o, want 771（app 组可创建用户目录）", perm)
	}
	// 执行进程 uid 应为 2042（非 root、非 app 100）：验证 setpriv 降权到用户专属 uid。
	if out, err := e.Exec(context.Background(), ExecRequest{
		UserID: 42, Language: "shell", Code: "id -u",
	}); err != nil || strings.TrimSpace(out.Stdout) != "2042" {
		t.Fatalf("执行进程 uid 应为 2042: out=%+v err=%v", out, err)
	}

	// 超时：1 秒内应超时并终止进程组
	start := time.Now()
	_, err = e.Exec(context.Background(), ExecRequest{
		UserID: 42, Language: "shell", Code: "sleep 30", TimeoutSecs: 1,
	})
	if err != nil {
		t.Fatalf("超时执行不应返回传输错误: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("超时未生效，sleep 未被及时终止")
	}
}

func TestExec_Python(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 Linux 上验证 python3 解释器")
	}
	e := newTestExecutor(t)
	res, err := e.Exec(context.Background(), ExecRequest{
		UserID: 1, Language: "python", Code: "print(6*7)",
	})
	if err != nil {
		t.Fatalf("python 执行失败: %v", err)
	}
	if !strings.Contains(res.Stdout, "42") {
		t.Fatalf("python 输出异常: %+v", res)
	}
}

// TestExec_Profile 预置解析脚本（profile）模式：参数校验 + 超时上限放宽。
// 校验类错误在执行前即返回 Error（任意平台可测）；真实执行仅在 Linux。
func TestExec_Profile(t *testing.T) {
	e := newTestExecutor(t)

	// 未知 profile 拒绝
	if _, err := e.Exec(context.Background(), ExecRequest{
		UserID: 1, Profile: "parse_nope", Args: []string{"a", "b", "c"},
	}); err == nil || !strings.Contains(err.Error(), "未知 profile") {
		t.Fatalf("未知 profile 应报错: %v", err)
	}
	// profile 模式不接受 language / code
	if _, err := e.Exec(context.Background(), ExecRequest{
		UserID: 1, Profile: "parse_pdf", Language: "shell", Code: "", Args: []string{"a", "b", "c"},
	}); err == nil {
		t.Fatal("profile + language 应报错")
	}
	if _, err := e.Exec(context.Background(), ExecRequest{
		UserID: 1, Profile: "parse_pdf", Code: "echo hi", Args: []string{"a", "b", "c"},
	}); err == nil {
		t.Fatal("profile + code 应报错")
	}
	// 参数个数不匹配拒绝（各 profile 要求 3 个：input/out/media）
	for name, spec := range parserProfiles {
		if _, err := e.Exec(context.Background(), ExecRequest{
			UserID: 1, Profile: name, Args: []string{"only_one"},
		}); err == nil {
			t.Fatalf("profile %s 参数个数不足应报错", name)
		}
		_ = spec
	}

	// render 系列（P4-D 文档渲染）：恰好 2 个参数（spec.json + 输出文件）。
	// 参数合法时只可能进入执行阶段（本地无 unshare 属执行期错误，err 为 nil），
	// 因此可用 err==nil 断言校验放行；参数过多应被拒绝。
	for _, name := range []string{"render_docx", "render_pptx", "render_pdf"} {
		spec := parserProfiles[name]
		if spec.ArgCount != 2 {
			t.Fatalf("profile %s ArgCount 应为 2，实际 %d", name, spec.ArgCount)
		}
		if _, err := e.Exec(context.Background(), ExecRequest{
			UserID: 1, Profile: name, Args: []string{"spec.json", "out." + name[len("render_"):]},
		}); err != nil {
			t.Fatalf("profile %s 参数合法（2 个）不应返回校验错误: %v", name, err)
		}
		if _, err := e.Exec(context.Background(), ExecRequest{
			UserID: 1, Profile: name, Args: []string{"a", "b", "c"},
		}); err == nil {
			t.Fatalf("profile %s 参数过多应报错", name)
		}
	}
	// user_id 校验仍生效（profile 模式同样按用户隔离工作区）
	if _, err := e.Exec(context.Background(), ExecRequest{
		UserID: 0, Profile: "parse_pdf", Args: []string{"a", "b", "c"},
	}); err == nil {
		t.Fatal("profile 模式 user_id=0 应报错")
	}

	// 解析 profile 的专属超时上限应放宽（默认 120s > 普通 60s/测试 5s），
	// 即请求 timeout 超过普通 MaxTimeout 时仍不被截断到 MaxTimeout。
	if runtime.GOOS == "linux" {
		spec := parserProfiles["parse_pdf"]
		if spec.MaxTimeout <= e.cfg.MaxTimeout {
			t.Fatalf("profile 专属超时应大于普通 MaxTimeout: %v <= %v", spec.MaxTimeout, e.cfg.MaxTimeout)
		}
		// 真实执行：脚本路径指向容器内 /opt/rag-parsers，测试环境通常不存在 →
		// 报"文件不存在"属执行期错误（非校验错误），且会进入执行阶段
		// （unshare 可用）。这里只需验证校验放行、超时不截断。
		start := time.Now()
		_, err := e.Exec(context.Background(), ExecRequest{
			UserID: 1, Profile: "parse_pdf",
			Args: []string{"in.pdf", "out.json", "media"}, TimeoutSecs: 200,
		})
		if err != nil {
			t.Fatalf("参数合法时不应返回校验错误: %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("profile 执行不应被普通 MaxTimeout 截断")
		}
	}
}

