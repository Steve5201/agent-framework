// Package rag RAG 系统框架核心（P3-A）。
//
// 设计定位：RAG = 独立微服务（cmd/rag），对智能体暴露唯一标准接口
// （proto/rag/v1），管理端走 REST（gateway adminsvc kb 模块再包 gRPC）。
// 框架内部实现黑盒、环节可插拔：EmbeddingProvider / Chunker / Retriever
// 均为接口，换实现不影响外部程序。
package rag

import (
	"strconv"
	"time"
)

// 文档摄取状态机（与 proto IngestStatus 对齐）。
const (
	StatusQueued     = "queued"     // 排队等待摄取
	StatusProcessing = "processing" // 处理中（worker 已抢单）
	StatusSucceeded  = "succeeded"  // 成功（chunks 已就绪）
	StatusFailed     = "failed"     // 失败（error 记录原因）

	// DefaultAgentID 默认智能体域：存量数据与未显式指定 agent 的资源归属（多租户兜底）。
	DefaultAgentID = "tutor"
)

// KnowledgeBase 知识库。
type KnowledgeBase struct {
	ID          string
	Name        string
	Description string
	AgentID     string // 所属智能体域（阶段3·多租户）
	Enabled     bool   // 启用状态（false = 停用，不可被会话/普通用户检索引用）
	DocCount    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Document 文档 + 摄取状态。
type Document struct {
	ID          string
	KBID        string
	FileName    string
	FileType    string
	Content     []byte // 原始文件字节（列表查询不加载）
	Status      string
	ContentHash string // sha256（十六进制）：同内容重传幂等跳过
	ChunkCount  int
	Error       string
	Attempt     int // 摄取失败自动重试次数（0=首次；上限 RAG_INGEST_MAX_ATTEMPTS）
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Chunk 分块（含向量）。
type Chunk struct {
	ID        string
	DocID     string
	KBID      string
	Seq       int
	Content   string
	Embedding []float32
	Metadata  map[string]any
}

// SearchHit 检索命中的 chunk（带知识库名与融合分，供引用溯源）。
type SearchHit struct {
	ChunkID  string
	DocID    string
	KBID     string
	KBName   string
	Content  string
	Source   string
	Score    float64
	Metadata map[string]any
}

// idCounter 简易自增（进程内），避免同一纳秒创建多个对象 ID 冲突。
var idSeq uint64

// genID 生成形如 <prefix>_<base36时间戳><自增> 的短 ID。
func genID(prefix string) string {
	idSeq++
	ts := strconv.FormatInt(time.Now().UnixNano(), 36)
	n := strconv.FormatUint(idSeq, 36)
	return prefix + "_" + ts + n
}
