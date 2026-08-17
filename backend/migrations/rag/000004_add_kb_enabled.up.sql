-- 知识库启用状态（P3 反馈：知识库也纳入资源启停体系）。
-- 存量知识库默认启用（true），保证升级不改变现有行为。
ALTER TABLE knowledge_bases ADD COLUMN enabled boolean NOT NULL DEFAULT true;
