package rag

import (
	"context"
	"sort"
)

// Retrieve 混合检索（A4）：向量召回 + 关键词召回 → RRF 融合 → topK。
//
// 对标大厂标准"双路召回 + 融合精排"：
//   - 向量路：query 向量（hnsw 余弦相似度）召回 topK*4；
//   - 关键词路：query 原文（pg_trgm 三元组相似度，中文弱分词下的可靠兜底）召回 topK*4；
//   - 融合：Reciprocal Rank Fusion（k=60）对两路排名加权合并，结果按融合分降序。
//
// 说明：一期不接 Rerank（二期在融合结果上再做精排）；Score 为 RRF 融合分，
// 无绝对语义（仅排序用）。kbIDs 为 nil/空 = 搜索全部知识库。
func (s *Store) Retrieve(ctx context.Context, queryVec []float32, query string, kbIDs []string, topK int, minScore float64) ([]SearchHit, error) {
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}
	if minScore < 0 {
		minScore = 0
	}

	// 向量召回：扩充召回池，给 RRF 更多候选。
	vectorHits, err := s.VectorSearch(ctx, queryVec, kbIDs, topK*4, minScore)
	if err != nil {
		return nil, err
	}
	// 关键词召回：pg_trgm 下限 0.05（过低会噪声，过高会漏召回）。
	keywordHits, err := s.KeywordSearch(ctx, query, kbIDs, topK*4, 0.05)
	if err != nil {
		return nil, err
	}
	return rrfFuse(vectorHits, keywordHits, topK), nil
}

// rrfFuse Reciprocal Rank Fusion：score = Σ 1/(k+rank)，k=60。
// 两路按各自排名加权合并，按 chunk_id 去重（同 chunk 取高者信息合并）。
func rrfFuse(primary, secondary []SearchHit, topK int) []SearchHit {
	const k = 60
	type entry struct {
		hit   SearchHit
		score float64
	}
	merged := map[string]*entry{}
	order := make([]string, 0, len(primary)+len(secondary))

	add := func(hits []SearchHit) {
		for i, h := range hits {
			e, ok := merged[h.ChunkID]
			if !ok {
				e = &entry{hit: h}
				merged[h.ChunkID] = e
				order = append(order, h.ChunkID)
			}
			e.score += 1 / (k + float64(i+1))
		}
	}
	add(primary)
	add(secondary)

	// 融合分降序稳定排序。
	sort.SliceStable(order, func(i, j int) bool {
		return merged[order[i]].score > merged[order[j]].score
	})

	out := make([]SearchHit, 0, topK)
	for _, id := range order {
		if len(out) >= topK {
			break
		}
		e := merged[id]
		e.hit.Score = e.score
		out = append(out, e.hit)
	}
	return out
}
