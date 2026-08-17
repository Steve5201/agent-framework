// media_cleanup.go —— 无主 rag-media 目录定期清理（模块三·防撑爆）。
//
// 背景：文档删除时 rag 已 best-effort 清理
// <SandboxWorkRoot>/rag-media/<docID>/（公共只读区，P3-A8）；因目录占用/中断等
// 偶发失败会残留孤儿目录（DB 已无对应文档）。本 job 兜底：扫描媒体目录，docID
// 不在 DB 且超过宽限期（TTL，默认 7 天）未修改 → 删除。
//
// 安全设计：
//   - 有主目录（docID 仍在 DB）直接跳过，无论其 mtime 多旧——媒体目录摄取后
//     不再更新，不能按 mtime 近似孤儿，必须依赖 DB 判空；
//   - TTL 判定用"目录内最新文件 mtime"（而非目录自身），仅作删除后宽限，
//     防 docID 复用（删文档→重新摄取同 ID）的误删；
//   - 只动 rag-media 白名单目录，绝不影响 file_ops 等用户产物。
package rag

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// mediaDirName rag-media 目录名（媒体产物根，共享卷 /work 下）。
const mediaDirName = "rag-media"

// CleanupOrphanMedia 清理无主媒体目录，返回删除数。
// 扫描两个区域：
//  1. 公共只读区 <mediaRoot>/rag-media/<docID>/（当前落盘路径）；
//  2. 历史遗留 <mediaRoot>/users/<uid>/rag-media/<docID>/（P3-A8 迁移前的旧布局，
//     迁移后不再产生新文件，此处仅做一次性兜底清理）。
func (s *Service) CleanupOrphanMedia(ctx context.Context, ttl time.Duration) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	ids, err := s.store.ListAllDocumentIDs(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-ttl)
	deleted := 0
	for _, base := range s.mediaBases() {
		n, err := s.cleanupMediaBase(base, ids, cutoff)
		if err != nil {
			s.log.Warn("无主媒体清理失败", zap.String("base", base), zap.Error(err))
			continue
		}
		deleted += n
	}
	return deleted, nil
}

// mediaBases 返回待扫描的媒体根目录列表。
func (s *Service) mediaBases() []string {
	bases := []string{filepath.Join(s.mediaRoot, mediaDirName)}
	usersDir := filepath.Join(s.mediaRoot, "users")
	if entries, err := os.ReadDir(usersDir); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			bases = append(bases, filepath.Join(usersDir, ent.Name(), mediaDirName))
		}
	}
	return bases
}

// cleanupMediaBase 清理单个媒体根目录下的无主 docID 子目录。
func (s *Service) cleanupMediaBase(base string, ids map[string]struct{}, cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	deleted := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if _, ok := ids[ent.Name()]; ok {
			continue // 有主：文档仍存在，跳过
		}
		dir := filepath.Join(base, ent.Name())
		latest, err := ragMediaLatestMTime(dir)
		if err != nil || !latest.Before(cutoff) {
			continue // 无法判定或仍在宽限期内
		}
		if err := os.RemoveAll(dir); err != nil {
			s.log.Warn("无主媒体清理失败", zap.String("path", dir), zap.Error(err))
			continue
		}
		deleted++
		s.log.Info("无主媒体已清理", zap.String("path", dir), zap.Time("latest_mtime", latest))
	}
	return deleted, nil
}

// RunMediaCleanup 定时执行无主媒体清理（cmd/rag 装配）；interval≤0 立即返回。
// 每轮仅在有清理产出时记录 Info，避免周期空转刷日志。
func (s *Service) RunMediaCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ttl := s.mediaCleanupTTL
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.CleanupOrphanMedia(ctx, ttl)
			if err != nil {
				s.log.Warn("无主媒体清理失败", zap.Error(err))
				continue
			}
			if n > 0 {
				s.log.Info("无主媒体清理完成", zap.Int("deleted", n))
			}
		}
	}
}

// ragMediaLatestMTime 媒体目录内最新 mtime（跳过根目录自身与符号链接）。
func ragMediaLatestMTime(dir string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可访问条目
		}
		if d.Type()&fs.ModeSymlink != 0 || path == dir {
			return nil
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
