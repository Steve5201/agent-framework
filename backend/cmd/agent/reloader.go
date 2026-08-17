// reloader.go —— 管理端热加载（agent 侧）。
//
// 管理端（gateway /v1/admin/*）把 skill/MCP 配置直接落盘到共享路径（多租户按域隔离）：
//   - 技能：<skills>/<agent_id>/<name>/SKILL.md；
//   - MCP：<AGENT_MCP_CONFIG_FILE 目录>/<agent_id>/<文件名> 的 JSON 数组文件。
//
// 本组件监听本智能体实例（AGENT_ID）的上述路径，变更后重建工具注册表并热替换
// （agentsvc.ReplaceRegistry），从而保存即生效、免重启。进行中的会话仍使用旧
// 注册表（注册表只读共享），不受影响。
//
// 触发通道（双保险）：
//  1. fsnotify 事件：本地 go run / Linux 原生环境下事件及时可靠。
//  2. 定时快照轮询：Docker Desktop 的 bind mount 上 fsnotify 收不到宿主机写入
//     事件（实测 hot reload applied 从不出现），轮询兜底——每 3s 对比技能目录
//     文件签名与 MCP 文件 modtime，变化即重建。两者共用同一 rebuild 逻辑。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/agentsvc"
	"github.com/Steve5201/agent-backend/internal/config"
)

const (
	// reloadDebounce fsnotify 事件防抖窗口：合并一段连续变更，避免频繁重建。
	reloadDebounce = 300 * time.Millisecond
	// pollInterval 快照轮询间隔：与前端资源轮询节奏一致，Docker 场景下保存后
	// 至多一个周期内生效。
	pollInterval = 3 * time.Second
)

// hotReloader 热加载核心：fsnotify 事件与快照轮询共用同一 rebuild 逻辑。
type hotReloader struct {
	svc       *agentsvc.Service
	cfg       *config.Config
	log       *zap.Logger
	skillsDir string
	mcpFile   string

	mu       sync.Mutex // 保护 curClose / pending
	curClose func()
	pending  *time.Timer

	rebuildM sync.Mutex // rebuild 串行化（fsnotify debounce 与轮询可能并发触发）
}

func newHotReloader(svc *agentsvc.Service, cfg *config.Config, log *zap.Logger, seedCloser func()) *hotReloader {
	return &hotReloader{
		svc:       svc,
		cfg:       cfg,
		log:       log,
		skillsDir: agentSkillsDir(cfg),
		mcpFile:   agentMcpFile(cfg),
		curClose:  seedCloser,
	}
}

// rebuild 重建工具注册表并热替换；失败时保留旧注册表（对话不受影响）。
func (h *hotReloader) rebuild() {
	h.rebuildM.Lock()
	defer h.rebuildM.Unlock()
	reg, closeNew, err := buildToolRegistry(h.cfg, h.log)
	if err != nil {
		h.log.Error("hot reload: rebuild tool registry failed, 继续使用旧注册表", zap.Error(err))
		return
	}
	h.mu.Lock()
	old := h.curClose
	h.curClose = closeNew
	h.mu.Unlock()
	h.svc.ReplaceRegistry(reg)
	if old != nil {
		old() // 释放旧的 MCP 连接（skill 无连接）
	}
	h.log.Info("hot reload applied", zap.Int("tool_count", len(reg.Schemas())))
}

// touch 触发一次防抖重建（fsnotify 事件入口）。
func (h *hotReloader) touch() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending != nil {
		h.pending.Stop()
	}
	h.pending = time.AfterFunc(reloadDebounce, h.rebuild)
}

// relevant 判断事件是否落在本智能体资源域内（技能目录或其子目录、MCP 文件）。
func (h *hotReloader) relevant(ev fsnotify.Event) bool {
	p := filepath.Clean(ev.Name)
	if p == filepath.Clean(h.mcpFile) {
		return true
	}
	root := filepath.Clean(h.skillsDir)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// ---------------------------------------------------------------------------
// 快照轮询兜底
// ---------------------------------------------------------------------------

// fsSnapshot 资源域当前状态签名：MCP 文件 modtime/size + 技能目录全量文件签名。
type fsSnapshot struct {
	mcpModTime time.Time
	mcpSize    int64
	skillsSig  string
}

// takeSnapshot 采集当前签名（每轮询周期调用，文件量小，成本可忽略）。
func (h *hotReloader) takeSnapshot() fsSnapshot {
	var snap fsSnapshot
	if fi, err := os.Stat(h.mcpFile); err == nil {
		snap.mcpModTime = fi.ModTime()
		snap.mcpSize = fi.Size()
	}
	snap.skillsSig = treeSignature(h.skillsDir)
	return snap
}

// treeSignature 递归收集目录内全部文件的相对路径+大小+修改时间，排序后拼接签名。
// 任何文件新增/删除/内容变更都会改变签名（比只比较目录结构更可靠）。
func treeSignature(root string) string {
	type entry struct {
		rel string
		sig string
	}
	var entries []entry
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fi, ferr := d.Info()
		if ferr != nil {
			return nil
		}
		entries = append(entries, entry{rel, fi.ModTime().Format("20060102150405.000000000") + "|" + fmt.Sprint(fi.Size())})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.rel)
		sb.WriteByte(':')
		sb.WriteString(e.sig)
		sb.WriteByte(';')
	}
	return sb.String()
}

// poll 定时快照轮询：签名变化即重建，保证 Docker bind mount 场景下保存免重启生效。
func (h *hotReloader) poll(ctx context.Context) {
	last := h.takeSnapshot()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := h.takeSnapshot()
			if !reflect.DeepEqual(cur, last) {
				last = cur
				h.log.Info("hot reload: snapshot changed, rebuilding")
				h.rebuild()
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 装配入口
// ---------------------------------------------------------------------------

// startReloader 启动热加载（fsnotify + 快照轮询），返回 stop 函数。
func startReloader(svc *agentsvc.Service, cfg *config.Config, log *zap.Logger, seedCloser func()) func() {
	h := newHotReloader(svc, cfg, log, seedCloser)

	// 快照轮询兜底：fsnotify 不可靠（Docker bind mount）时依然保存即生效。
	ctx, cancel := context.WithCancel(context.Background())
	go h.poll(ctx)

	// 确保技能目录与 MCP 配置文件存在（fsnotify 需要先 Add 成功才能监听）。
	if err := os.MkdirAll(h.skillsDir, 0o755); err != nil {
		log.Warn("ensure skills dir failed", zap.String("dir", h.skillsDir), zap.Error(err))
	}
	ensureMcpConfigFile(h.mcpFile)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn("hot reload fsnotify disabled, 仅快照轮询生效", zap.Error(err))
		return func() {
			cancel()
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.curClose != nil {
				h.curClose()
			}
		}
	}
	if err := watchTree(watcher, h.skillsDir, log); err != nil {
		log.Warn("watch skills dir failed", zap.String("dir", h.skillsDir), zap.Error(err))
	}
	// MCP 配置文件：只监听文件本身（不监听父目录，避免 /app 工作区内
	// code_executor/file_ops 写文件误触发）。
	if err := watcher.Add(h.mcpFile); err != nil {
		log.Warn("watch mcp config file failed", zap.String("file", h.mcpFile), zap.Error(err))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if !h.relevant(ev) {
					continue
				}
				// 新技能目录出现 / 配置被原子替换（rename）后，需重新纳入监听。
				_ = watchTree(watcher, h.skillsDir, log)
				_ = watcher.Add(h.mcpFile)
				h.touch()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Warn("hot reload watcher error", zap.Error(err))
			}
		}
	}()

	log.Info("hot reload started",
		zap.String("skills_dir", h.skillsDir),
		zap.String("mcp_config_file", h.mcpFile))

	return func() {
		cancel() // 停快照轮询
		_ = watcher.Close()
		<-done
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.curClose != nil {
			h.curClose()
		}
	}
}

// watchTree 递归监听 root 下所有子目录（含 root 本身）。
func watchTree(w *fsnotify.Watcher, root string, log *zap.Logger) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 目录被删除等场景直接跳过，不中断遍历
		}
		if d.IsDir() {
			if err := w.Add(path); err != nil {
				log.Warn("watch dir failed", zap.String("dir", path), zap.Error(err))
			}
		}
		return nil
	})
}

// ensureMcpConfigFile 确保 MCP 配置文件存在（不存在则写入空列表），
// 使 agent 能直接监听该文件（而非父目录，避免误触发）。
func ensureMcpConfigFile(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		// 写失败不致命：agent 仍可按环境变量配置启动；仅影响监听。
		return
	}
}
