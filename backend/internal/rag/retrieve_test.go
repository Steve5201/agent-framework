package rag

import (
	"testing"
)

func mkHits(ids ...string) []SearchHit {
	out := make([]SearchHit, 0, len(ids))
	for _, id := range ids {
		out = append(out, SearchHit{ChunkID: id})
	}
	return out
}

// TestRRFFuse_RankMerging 验证 RRF 融合：两路排名加权，同 chunk 分相加。
func TestRRFFuse_RankMerging(t *testing.T) {
	// primary: [A,B,C]（rank 1,2,3）；secondary: [B,D]（rank 1,2）
	// A:1/61  B:1/61+1/62  C:1/63  D:1/62 → B > A > D > C
	got := rrfFuse(mkHits("A", "B", "C"), mkHits("B", "D"), 4)
	wantOrder := []string{"B", "A", "D", "C"}
	if len(got) != len(wantOrder) {
		t.Fatalf("融合结果数 %d != %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ChunkID != want {
			t.Fatalf("第 %d 位应为 %s，实际 %s", i, want, got[i].ChunkID)
		}
	}
	// B 的融合分必须显著高于单路命中（A/D）。
	if got[0].Score <= got[1].Score {
		t.Errorf("B 的融合分 %f 应高于 A %f", got[0].Score, got[1].Score)
	}
}

// TestRRFFuse_DedupAndTopK 同 chunk 两路均命中只保留一份，topK 截断。
func TestRRFFuse_DedupAndTopK(t *testing.T) {
	got := rrfFuse(mkHits("X", "Y", "Z"), mkHits("X"), 2)
	if len(got) != 2 {
		t.Fatalf("topK=2 应只返回 2 条，实际 %d", len(got))
	}
	if got[0].ChunkID != "X" {
		t.Errorf("X 两路命中融合分最高，应为第一，实际 %s", got[0].ChunkID)
	}
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h.ChunkID] {
			t.Fatalf("重复 chunk %s", h.ChunkID)
		}
		seen[h.ChunkID] = true
	}
}

// TestRRFFuse_StableOrdering 融合分相等时保持输入先后（稳定排序）。
func TestRRFFuse_StableOrdering(t *testing.T) {
	// primary=[P,Q] secondary=[R,S]：P 与 R 同分（rank1）、Q 与 S 同分（rank2）。
	// 同分按 order 先后 → [P, R, Q, S]。
	got := rrfFuse(mkHits("P", "Q"), mkHits("R", "S"), 4)
	want := []string{"P", "R", "Q", "S"}
	for i, w := range want {
		if got[i].ChunkID != w {
			t.Fatalf("第 %d 位应为 %s，实际 %s（%+v）", i, w, got[i].ChunkID, got)
		}
	}
}

// TestRRFFuse_EmptyInput 空输入返回空结果。
func TestRRFFuse_EmptyInput(t *testing.T) {
	if got := rrfFuse(nil, nil, 5); len(got) != 0 {
		t.Fatalf("空输入应返回空，实际 %d", len(got))
	}
}

// TestRRFFuse_SingleSource 仅一路命中时不报错且保序。
func TestRRFFuse_SingleSource(t *testing.T) {
	got := rrfFuse(mkHits("A", "B", "C"), nil, 5)
	if len(got) != 3 || got[0].ChunkID != "A" {
		t.Fatalf("单路命中应保持原序，实际 %+v", got)
	}
}
