// cleanup.go —— 工作区定期清理 job（模块三·防撑爆）。
//
// 背景：聊天上传文档（chat-files）、rag 摄取临时目录（ingest）与智能体产出的
// 散落内容（file_ops 创建的工作文件、chat-docs 渲染文档等）随时间在共享卷
// 累积。本 job 定期扫描 /work/users/<uid>/ 下的内容，按归属分三档处理：
//
//   - 系统临时区（chat-files/、ingest/）：按短期 TTL（默认 7 天）整目录删除；
//   - 保护区（protected/）：永不清除——用户显式要求或智能体确认保留的长期
//     资产落盘于此（有独立磁盘配额封顶，见 agent 侧 file_ops 配额校验）；
//   - rag-media/（用户目录内旧布局）：沙盒侧永不动——无主媒体需要 DB 知识
//     判断（文档是否仍存在），由 rag-service 侧按 DB 判空清理（见
//     rag.Service.CleanupOrphanMedia），沙盒若按时间清理会把"文档仍在引用"
//     的媒体误删；
//   - 其余一切散落内容（AI 产物：chat-docs 渲染文档、file_ops 工作文件等）：
//     按长期 TTL（默认 30 天）清理——可再生/低价值，配前端 404 兜底降级。
//
// TTL 判定基于"目录内最新文件 mtime"（而非目录自身 mtime）：重传同名文档覆盖
// 文件时目录 mtime 不变，按文件 mtime 才不会误删活跃会话。
package sandboxsvc

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// 用户工作区条目归属：
//   - 短期临时区（chat-files/ingest）：TTL 兜底清理；
//   - 保护区（protected）：永不清（用户/AI 显式保留资产，配额封顶）；
//   - 持久媒体（rag-media）：沙盒侧永不动（rag 侧 DB 判空清理）。
const (
	dirChatFiles = "chat-files" // 聊天上传文档（模块二）：短期 TTL
	dirIngest    = "ingest"     // rag 摄取临时目录（孤儿）：短期 TTL
	dirProtected = "protected"  // 保护区：永不清（用户/AI 显式保留的长期资产）
	dirRagMedia  = "rag-media"  // 持久媒体：沙盒侧永不动（rag 侧按 DB 判空清理）
)

// CleanStats 单轮清理统计（日志与测试断言用）。
type CleanStats struct {
	UsersScanned int   `json:"users_scanned"` // 扫描的用户目录数
	DirsDeleted  int   `json:"dirs_deleted"`  // 删除的过期条目数（目录或文件）
	BytesFreed   int64 `json:"bytes_freed"`   // 释放字节数
}

// Cleaner 工作区清理器。
type Cleaner struct {
	WorkRoot string        // 工作区根（缺省 /work）
	TTL      time.Duration // 短期 TTL：系统临时区（chat-files/ingest）整目录删除
	LongTTL  time.Duration // 长期 TTL：散落 AI 产物（file_ops 工作文件、chat-docs 等）
	Log      *zap.Logger
}

// NewCleaner 构造清理器（WorkRoot 缺省 /work，Log 缺省 Nop）。
// 两个 TTL 均 ≤0 时 Run 直接返回零值（清理禁用）。
func NewCleaner(workRoot string, ttl, longTTL time.Duration, log *zap.Logger) *Cleaner {
	if workRoot == "" {
		workRoot = "/work"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Cleaner{WorkRoot: workRoot, TTL: ttl, LongTTL: longTTL, Log: log}
}

// Run 执行一轮清理：遍历 users/<uid>/，按条目归属分档清理（见包注释）。
// 返回统计；users/ 目录不存在时视为无事可做（零值返回，不报错）。
func (c *Cleaner) Run(ctx context.Context) (CleanStats, error) {
	var stats CleanStats
	if c.TTL <= 0 && c.LongTTL <= 0 {
		return stats, nil
	}
	usersDir := filepath.Join(c.WorkRoot, "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, err
	}
	shortCutoff := time.Now().Add(-c.TTL)
	longCutoff := time.Now().Add(-c.LongTTL)
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		// 只处理数字命名的用户目录（user_id），跳过其它残留。
		if _, err := strconv.ParseInt(ent.Name(), 10, 64); err != nil {
			continue
		}
		if err := c.cleanUserEntries(ctx, filepath.Join(usersDir, ent.Name()), shortCutoff, longCutoff, &stats); err != nil {
			c.Log.Warn("workspace cleanup: user dir failed",
				zap.String("user", ent.Name()), zap.Error(err))
		}
		stats.UsersScanned++
	}
	return stats, nil
}

// cleanUserEntries 处理单个用户目录下的全部条目：
//   - protected/、rag-media/：跳过（保护区 + rag 侧管理的持久媒体）；
//   - chat-files/、ingest/：短期 TTL，逐子目录判定；
//   - 其余散落目录/文件：长期 TTL 整条目判定。
func (c *Cleaner) cleanUserEntries(ctx context.Context, userDir string, short, long time.Time, stats *CleanStats) error {
	entries, err := os.ReadDir(userDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := ent.Name()
		if name == dirProtected || name == dirRagMedia {
			continue // 保护区 + 持久媒体：沙盒侧永不动
		}
		full := filepath.Join(userDir, name)
		if ent.IsDir() && (name == dirChatFiles || name == dirIngest) {
			if err := c.cleanExpiredSubdirs(ctx, full, short, stats); err != nil {
				return err
			}
			continue
		}
		// 散落 AI 产物（目录或散落文件）：长期 TTL 整条目判定。
		if err := c.removeIfExpired(ctx, full, ent.IsDir(), long, stats); err != nil {
			return err
		}
	}
	return nil
}

// cleanExpiredSubdirs 清理临时区根目录（chat-files/ingest）下超过短期 TTL
// 未修改的子目录（聊天会话目录 / 摄取孤儿目录）。
func (c *Cleaner) cleanExpiredSubdirs(ctx context.Context, base string, cutoff time.Time, stats *CleanStats) error {
	subs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		if err := c.removeIfExpired(ctx, filepath.Join(base, sub.Name()), true, cutoff, stats); err != nil {
			return err
		}
	}
	return nil
}

// removeIfExpired 条目（目录按"目录内最新文件 mtime"，文件按自身 mtime）超
// 过 cutoff 未修改时删除；无法判定（IO 错误）或仍活跃则跳过。
func (c *Cleaner) removeIfExpired(ctx context.Context, full string, isDir bool, cutoff time.Time, stats *CleanStats) error {
	latest, err := entryLatestMTime(full, isDir)
	if err != nil || !latest.Before(cutoff) {
		return nil
	}
	c.removeEntry(ctx, full, stats)
	return nil
}

// entryLatestMTime 返回条目"最后一次内容活跃"时间：
//   - 文件：自身 mtime；
//   - 目录：目录内最新文件的 mtime（递归，跳过符号链接）。
//
// 只统计文件 mtime，忽略目录条目自身 mtime：目录 mtime 在子项增删/父级写入时
// 会被刷新为较新时刻，不代表内容活跃度——若计入，chat-docs/<fileID>/ 这类
// "父目录创建时刻较新、内部文件已过期"的结构会被误判为活跃而永不清理。
func entryLatestMTime(path string, isDir bool) (time.Time, error) {
	if !isDir {
		fi, err := os.Stat(path)
		if err != nil {
			return time.Time{}, err
		}
		return fi.ModTime(), nil
	}
	var latest time.Time
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可访问条目（如权限拒绝），不阻断清理
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

// removeEntry 删除条目（目录或文件）并累计统计。相信 RemoveAll 的返回值
// （真实系统返回 nil 即删除成功）；删除元数据在 Windows 延迟删除场景可能
// 短暂未落盘，但不影响最终结果，交给文件系统完成。
func (c *Cleaner) removeEntry(ctx context.Context, full string, stats *CleanStats) {
	size := dirSize(full)
	if err := ctx.Err(); err != nil {
		return
	}
	if err := os.RemoveAll(full); err != nil {
		c.Log.Warn("workspace cleanup: remove failed", zap.String("path", full), zap.Error(err))
		return
	}
	stats.DirsDeleted++
	stats.BytesFreed += size
	c.Log.Info("workspace cleanup: removed expired entry",
		zap.String("path", full), zap.Int64("bytes_freed", size))
}

// dirSize 统计条目内全部文件字节数（递归，不含目录条目自身；文件则返回自身大小）。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
