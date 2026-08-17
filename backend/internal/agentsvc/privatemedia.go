// privatemedia.go —— 聊天上传解析产物的"用户私有媒体"迁移。
//
// 背景：聊天上传文档（pdf/docx/pptx）解析出的媒体文件（图片等）默认落在
// 公共区 rag-media/<docID>/——与知识库解析的公共媒体混放。聊天上传属于
// 用户私有产物，应放到用户自己的工作目录。
//
// 策略：解析完成后把媒体从公共 rag-media/<docID>/ 移到
// users/<uid>/chat-files/<sid>/media/，并把注入正文/工作区文件里的引用前缀
// （rag-media/<docID>/）改写为私有路径前缀。迁移采用"先落公共区、再移动"，
// 而非让沙盒直接写私有目录——解析脚本的引用路径推导（media_rel_prefix）
// 硬编码 parent/leaf 两级结构，私有路径会推导出错，移动改写更稳。
//
// 权限：迁移后把媒体目录收紧为 0750、文件 0640（仍属 app 属主，agent /files
// 可读；不再世界可读，区别于公共 rag-media 的 0777/0644）。
package agentsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Steve5201/agent-backend/internal/rag/ingest"
	"go.uber.org/zap"
)

// migrateChatMedia 把解析出的媒体迁入用户私有工作区并收紧权限。
//
// 返回（oldPrefix, newPrefix）：调用方用 oldPrefix 把注入正文/落盘全文中的
// 引用改写为 newPrefix（如 rag-media/x_123/pdf/ → users/<uid>/chat-files/<sid>/media/）。
// 无媒体或迁移失败时返回空串，调用方跳过改写（引用保持原样，不阻断上传）。
func (s *Service) migrateChatMedia(userID, sessionID int64, doc *ingest.ParsedDoc) (oldPrefix, newPrefix string, _ error) {
	if len(doc.Media) == 0 {
		return "", "", nil
	}
	// 工作区根与落盘/沙盒必须一致：优先配置的 workRoot，未配置时回退进程
	// 工作目录（容器内 /app = 沙盒 /work，同一宿主目录）。此前直接判空跳过，
	// 导致默认部署（未设 AGENT_WORK_ROOT）下媒体永远留在公共区 rag-media/。
	root := s.effectiveWorkRoot()
	// 同一解析的所有媒体共享同一 docID 目录：旧前缀取其公共父目录。
	first := filepath.FromSlash(doc.Media[0].Path)
	oldPrefix = filepath.ToSlash(filepath.Dir(first)) + "/"

	mediaRoot := filepath.Join("users", strconv.FormatInt(userID, 10),
		"chat-files", strconv.FormatInt(sessionID, 10), "media")
	newPrefix = filepath.ToSlash(mediaRoot) + "/"
	dstDir := filepath.Join(root, mediaRoot)
	if err := ensureGroupWritableDir(dstDir); err != nil {
		return "", "", fmt.Errorf("创建私有媒体目录失败: %w", err)
	}

	moved := 0
	used := make(map[string]bool, len(doc.Media))
	for i, m := range doc.Media {
		src := filepath.Join(root, filepath.FromSlash(m.Path))
		name := filepath.Base(filepath.FromSlash(m.Path))
		// 同名文件去重（解析产物可能嵌套同名图片）。
		if used[name] {
			name = fmt.Sprintf("%d_%s", i, name)
		}
		used[name] = true
		dst := filepath.Join(dstDir, name)
		if err := os.Rename(src, dst); err != nil {
			s.log.Warn("迁移聊天媒体失败，保留公共区路径",
				zap.String("media", m.Path),
				zap.Error(err))
			continue
		}
		// 收紧权限：媒体文件不再世界可读（区别于公共 rag-media 的 0644）。
		_ = os.Chmod(dst, 0o640)
		doc.Media[i].Path = filepath.ToSlash(filepath.Join(mediaRoot, name))
		moved++
	}
	if moved == 0 {
		return "", "", nil
	}
	// 清理已迁移走的公共区目录（尽力而为：非空/无权限时残留由公共区策略兜底）。
	_ = os.Remove(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(oldPrefix, "/"))))
	s.log.Info("聊天媒体已迁入用户私有目录",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.Int("moved", moved),
		zap.String("prefix", newPrefix))
	return oldPrefix, newPrefix, nil
}
