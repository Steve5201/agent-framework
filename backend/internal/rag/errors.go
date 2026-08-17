package rag

import "errors"

// 领域错误：存储层/服务层统一返回，gRPC server 映射为 status codes。
var (
	// ErrNotFound 记录不存在（映射 gRPC NotFound）。
	ErrNotFound = errors.New("rag: 记录不存在")
	// ErrNameExists 知识库/文档名冲突（映射 gRPC AlreadyExists）。
	ErrNameExists = errors.New("rag: 名称已存在")
	// ErrInvalidArgument 参数不合法（映射 gRPC InvalidArgument）。
	ErrInvalidArgument = errors.New("rag: 参数不合法")
	// ErrUnsupportedFileType 不支持的文档格式（映射 gRPC InvalidArgument）。
	ErrUnsupportedFileType = errors.New("rag: 不支持的文档格式（一期支持 md/txt/html）")
	// ErrNotConfigured 向量模型供应商未配置（映射 gRPC Unavailable）。
	// 服务照常启动，检索/摄取相关接口返回此错误，向前端给出明确提示。
	ErrNotConfigured = errors.New("rag: 向量模型供应商未配置，检索功能暂不可用（请部署本地 Ollama 或设置 SILICONFLOW_API_KEY）")
)
