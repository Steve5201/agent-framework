// files.go —— agent 服务本地媒体静态端点（/files）。
//
// 背景（本地媒体渲染交叉项）：浏览器无法直接读取服务器本地文件路径，
// 前端 <img>/<video> 只能加载 HTTP URL。模型按渲染协议输出本地媒体时，
// 用 <files 基址>/files/<工作目录内相对路径> 生成 URL，由本端点提供内容。
//
// 安全边界：与 file_ops 工具完全一致（tools.ResolveWorkPath）——只允许
// 访问智能体工作目录（os.Getwd()，容器里为 /app）内的文件，越界 403；
// 且只服务"文件"，不开放目录列表，避免泄露目录结构。
package main

import (
	"net/http"
	"os"
	"strings"

	"github.com/Steve5201/agent-backend/internal/tools"
	"go.uber.org/zap"
)

// filesHandler 提供工作目录内文件的只读访问。
// 跨域：媒体渲染与下载按钮需要浏览器跨端口加载，放行任意来源
// （本地工具场景可接受；如暴露到公网需加鉴权）。
type filesHandler struct {
	log *zap.Logger
}

// ServeHTTP 实现 http.Handler。
func (h filesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 预检（下载按钮走 fetch，浏览器可能先发 OPTIONS）。
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, "/files/")
	if rel == "" || rel == r.URL.Path {
		http.Error(w, "missing file path, usage: /files/<relative path>", http.StatusBadRequest)
		return
	}

	full, err := tools.ResolveWorkPath(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		// 不提供目录列表，只服务文件（防目录结构泄露）。
		http.Error(w, "directory listing is not allowed", http.StatusForbidden)
		return
	}

	h.log.Debug("files serve", zap.String("path", rel), zap.Int64("size", info.Size()))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeFile(w, r, full)
}

// 编译期断言：filesHandler 实现 http.Handler。
var _ http.Handler = filesHandler{}
