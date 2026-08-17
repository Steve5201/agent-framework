// cleanup.go —— 工作区定期清理 job（模块三·防撑爆）。
//
// 背景：聊天上传文档（chat-files）与 rag 摄取临时目录（ingest）随时间在共享卷
// 累积。本 job 定期扫描 /work/users/<uid>/ 下的白名单子目录，删除超过 TTL
// （默认 7 天）未修改的会话文档目录与孤儿解析临时目录。
//
// 保守策略（只动白名单，绝不遍历用户目录全删）：
//   - chat-files/<sessionId>/：聊天上传文档落盘（模块二），整目录按 TTL 删除；
//   - ingest/<docID>/：rag 摄取临时目录，正常流程完成后即清理，残留即异常
//     中断的孤儿，按 TTL 兜底删除；
//   - 其它子目录（rag-media/ 持久媒体、file_ops 用户产物等）一律不动。
//     rag-media 无主目录需 DB 知识判断，由 rag-service 侧清理（见
//     rag.Service.CleanupOrphanMedia）。
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

// 工作区清理白名单子目录名（其余目录一律跳过）。
const (
	dirChatFiles = "chat-files" // 聊天上传文档（模块二）
	dirIngest    = "ingest"     // rag 摄取临时目录（孤儿）
)

// CleanStats 单轮清理统计（日志与测试断言用）。
type CleanStats struct {
	UsersScanned int   `json:"users_scanned"` // 扫描的用户目录数
	DirsDeleted  int   `json:"dirs_deleted"`  // 删除的过期目录数
	BytesFreed   int64 `json:"bytes_freed"`   // 释放字节数（仅统计白名单目录内文件）
}

// Cleaner 工作区清理器。
type Cleaner struct {
	WorkRoot string        // 工作区根（缺省 /work）
	TTL      time.Duration // 超过该时长未修改的目录删除；≤0 = 禁用（Run 直接返回零值）
	Log      *zap.Logger
}

// NewCleaner 构造清理器（WorkRoot 缺省 /work，Log 缺省 Nop）。
func NewCleaner(workRoot string, ttl time.Duration, log *zap.Logger) *Cleaner {
	if workRoot == "" {
		workRoot = "/work"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Cleaner{WorkRoot: workRoot, TTL: ttl, Log: log}
}

// Run 执行一轮清理：遍历 users/<uid>/，仅处理白名单子目录（chat-files/ingest）。
// 返回统计；users/ 目录不存在时视为无事可做（零值返回，不报错）。
func (c *Cleaner) Run(ctx context.Context) (CleanStats, error) {
	var stats CleanStats
	if c.TTL <= 0 {
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
	cutoff := time.Now().Add(-c.TTL)
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		// 只处理数字命名的用户目录（user_id），跳过其它残留。
		if _, err := strconv.ParseInt(ent.Name(), 10, 64); err != nil {
			continue
		}
		if err := c.cleanUserDirs(ctx, filepath.Join(usersDir, ent.Name()), cutoff, &stats); err != nil {
			c.Log.Warn("workspace cleanup: user dir failed",
				zap.String("user", ent.Name()), zap.Error(err))
		}
		stats.UsersScanned++
	}
	return stats, nil
}

// cleanUserDirs 处理单个用户目录下的白名单子目录。
func (c *Cleaner) cleanUserDirs(ctx context.Context, userDir string, cutoff time.Time, stats *CleanStats) error {
	for _, name := range []string{dirChatFiles, dirIngest} {
		base := filepath.Join(userDir, name)
		subs, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, sub := range subs {
			if !sub.IsDir() {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			dir := filepath.Join(base, sub.Name())
			latest, err := dirLatestMTime(dir)
			if err != nil || !latest.Before(cutoff) {
				continue // 无法判定或仍活跃：跳过
			}
			c.removeDir(ctx, dir, stats)
		}
	}
	return nil
}

// removeDir 删除目录并累计统计。相信 RemoveAll 的返回值（真实系统返回
// nil 即删除成功）；删除元数据在 Windows 延迟删除场景可能短暂未落盘，
// 但不影响最终结果，交给文件系统完成。
func (c *Cleaner) removeDir(ctx context.Context, dir string, stats *CleanStats) {
	size := dirSize(dir)
	if err := ctx.Err(); err != nil {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		c.Log.Warn("workspace cleanup: remove failed", zap.String("path", dir), zap.Error(err))
		return
	}
	stats.DirsDeleted++
	stats.BytesFreed += size
	c.Log.Info("workspace cleanup: removed expired dir",
		zap.String("path", dir), zap.Int64("bytes_freed", size))
}

// dirLatestMTime 返回目录内最新修改时间（递归含子目录，跳过符号链接）。
// 用于判定"最后一次活跃时间"，比目录自身 mtime 可靠（覆盖写同名文件不更新目录 mtime）。
// 注意跳过根目录自身 mtime：目录在创建/写入子项时被刷新为最新，若计入会把
// 任何新创建目录误判为活跃（即使其内容早已过期）。
func dirLatestMTime(dir string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可访问条目（如权限拒绝），不阻断清理
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if path == dir {
			return nil // 根目录自身 mtime 不计入
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

// dirSize 统计目录内全部文件字节数（递归，不含目录条目自身）。
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
