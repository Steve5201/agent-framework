package orchestrate

import (
	"context"
	"testing"
)

// noPlanPlanner 返回 ErrNoPlan 的假 planner（验证"无需编排"回退路径）。
type noPlanPlanner struct{}

func (noPlanPlanner) Plan(context.Context, string) (*TaskGraph, error) {
	return nil, ErrNoPlan
}

// TestRun_NoPlanFallback 验证 planner 判定无需编排时：
// Run 返回空结果且不报错、不调用聚合器——executor/agg 传 nil 即可证明
// 未被触碰（若被调用会 nil 解引用 panic）。
func TestRun_NoPlanFallback(t *testing.T) {
	o := NewOrchestrator(noPlanPlanner{}, nil, nil)
	res, err := o.Run(context.Background(), "你能看到文档工具吗")
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if res == nil || len(res.Tasks) != 0 || res.Final != "" {
		t.Fatalf("空计划应返回空 RunResult: %+v", res)
	}
}

// TestRunStream_NoPlanFallback 同上，验证流式入口 RunStream 的同一回退行为。
func TestRunStream_NoPlanFallback(t *testing.T) {
	o := NewOrchestrator(noPlanPlanner{}, nil, nil)
	var got []string
	res, err := o.RunStream(context.Background(), "今天天气怎么样", func(d string) {
		got = append(got, d)
	})
	if err != nil {
		t.Fatalf("RunStream err: %v", err)
	}
	if res == nil || len(res.Tasks) != 0 || res.Final != "" {
		t.Fatalf("空计划应返回空 RunResult: %+v", res)
	}
	if len(got) != 0 {
		t.Fatalf("空计划不应下发任何内容增量: %v", got)
	}
}
