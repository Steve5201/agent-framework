-- 000001_create_kb.up.sql —— RAG 知识库三表（P3-A1）
-- 依赖：pgvector 扩展（compose 使用 pgvector/pgvector:pg16 镜像自带）。

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- 知识库
CREATE TABLE IF NOT EXISTS knowledge_bases (
    id          TEXT PRIMARY KEY,             -- kb_<时间戳base36>
    name        TEXT NOT NULL UNIQUE,         -- 知识库名（1~50 字符）
    description TEXT NOT NULL DEFAULT '',
    doc_count   INTEGER NOT NULL DEFAULT 0,   -- 文档数（含失败，冗余便于列表展示）
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 文档 + 摄取状态机（queued → processing → succeeded|failed）
CREATE TABLE IF NOT EXISTS documents (
    id           TEXT PRIMARY KEY,            -- doc_<时间戳base36>
    kb_id        TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    file_name    TEXT NOT NULL,
    file_type    TEXT NOT NULL DEFAULT '',    -- md|txt|html|pdf|docx
    content      BYTEA NOT NULL DEFAULT '',   -- 原始文件字节（worker 异步摄取用）
    status       TEXT NOT NULL DEFAULT 'queued',
    content_hash TEXT NOT NULL DEFAULT '',    -- sha256：同内容重传幂等跳过
    chunk_count  INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kb_id, file_name)
);
CREATE INDEX IF NOT EXISTS idx_documents_kb_id ON documents(kb_id);

-- 分块（向量 + 关键词双索引）
CREATE TABLE IF NOT EXISTS chunks (
    id         TEXT PRIMARY KEY,              -- chunk_<时间戳base36>
    doc_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id      TEXT NOT NULL,                 -- 冗余便于按知识库过滤检索
    seq        INTEGER NOT NULL DEFAULT 0,    -- 文档内分块序号
    content    TEXT NOT NULL,
    embedding  vector(1024) NOT NULL,         -- BGE-M3 1024 维（与 RAG_EMBEDDING_DIM 一致）
    metadata   JSONB NOT NULL DEFAULT '{}'::jsonb, -- 来源/标题/章节/页码
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chunks_kb_id ON chunks(kb_id);
-- 向量近似最近邻（余弦距离）
CREATE INDEX IF NOT EXISTS idx_chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_cosine_ops);
-- 关键词检索：pg_trgm 三元组 GIN（中文弱分词下的可靠兜底）
CREATE INDEX IF NOT EXISTS idx_chunks_content_trgm ON chunks USING gin (content gin_trgm_ops);
