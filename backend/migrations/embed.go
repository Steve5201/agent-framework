// Package migrations 将各服务的 SQL 迁移文件嵌入二进制，
// 使服务启动时可自动执行迁移（单二进制部署，无需额外 CLI）。
//
// 目录约定（与 scripts/migrate.ps1 一致）：
//
//	migrations/
//	auth/   auth-service 迁移
//	agent/  agent-service 迁移
//	llm/    llm-gateway 迁移
//	rag/    rag-service 迁移（P3-A）
package migrations

import "embed"

//go:embed auth/*.sql agent/*.sql llm/*.sql rag/*.sql
var FS embed.FS
