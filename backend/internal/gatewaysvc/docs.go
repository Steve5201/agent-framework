// docs.go —— 内嵌 OpenAPI 文档与 Swagger UI（P2-55）。
//
// openapi.yaml 随二进制嵌入（无外部文件依赖），Swagger UI 页面从
// CDN 加载（浏览文档需联网；接口定义本身离线可用）。
package gatewaysvc

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed openapi.yaml swagger.html
var docsFS embed.FS

// docsHandlers 返回文档静态文件处理器（挂载到 /v1/ 与 /swagger/ 路径）。
// 仅读取 embed.FS，不做任何鉴权（运维查看用，不含敏感信息）。
func docsHandlers() http.Handler {
	return http.FileServerFS(docsFS)
}

// _ 编译期断言：docsFS 为只读文件系统。
var _ fs.ReadFileFS = docsFS
