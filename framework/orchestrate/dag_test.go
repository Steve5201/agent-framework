package orchestrate

import (
	"fmt"
	"reflect"
	"testing"
)

// spec 便捷构造 TaskSpec。
func spec(id string, deps ...string) TaskSpec {
	return TaskSpec{ID: id, Role: "worker", Goal: "目标 " + id, Deps: deps}
}

// nested 构造 depth 层嵌套 subgraph 的任务（每层单任务，ID 复用 "n"）。
func nested(depth int) TaskSpec {
	t := TaskSpec{ID: "n", Role: "worker", Goal: "g"}
	for i := 0; i < depth; i++ {
		t = TaskSpec{ID: "n", Role: "worker", Goal: "g", Subgraph: []TaskSpec{t}}
	}
	return t
}

func TestNewTaskGraph_OK(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b", "a"),
		spec("c", "a"),
		spec("d", "b", "c"),
	})
	if err != nil {
		t.Fatalf("合法 DAG 不应报错: %v", err)
	}
	if g.Len() != 4 {
		t.Fatalf("Len() = %d, want 4", g.Len())
	}
}

func TestNewTaskGraph_DuplicateID(t *testing.T) {
	_, err := NewTaskGraph([]TaskSpec{spec("a"), spec("a")})
	if err == nil {
		t.Fatal("重复 ID 应报错")
	}
}

func TestNewTaskGraph_MissingDep(t *testing.T) {
	_, err := NewTaskGraph([]TaskSpec{spec("a", "nope")})
	if err == nil {
		t.Fatal("依赖不存在的任务应报错")
	}
}

func TestNewTaskGraph_Cycle(t *testing.T) {
	_, err := NewTaskGraph([]TaskSpec{spec("a", "b"), spec("b", "a")})
	if err == nil {
		t.Fatal("环依赖应报错")
	}
}

func TestNewTaskGraph_InvalidTask(t *testing.T) {
	_, err := NewTaskGraph([]TaskSpec{{ID: "", Role: "", Goal: ""}})
	if err == nil {
		t.Fatal("空 ID 任务应报错")
	}
}

func TestReadyTasks(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b", "a"),
		spec("c", "a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 初始：只有 a 就绪
	if got := g.ReadyTasks(map[string]bool{}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("初始就绪 = %v, want [a]", got)
	}
	// a 完成后：b、c 就绪（按定义顺序）
	if got := g.ReadyTasks(map[string]bool{"a": true}); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("a 完成后就绪 = %v, want [b c]", got)
	}
	// 全部完成后：无就绪
	if got := g.ReadyTasks(map[string]bool{"a": true, "b": true, "c": true}); len(got) != 0 {
		t.Fatalf("全部完成后就绪 = %v, want 空", got)
	}
}

func TestTask_NotFound(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{spec("a")})
	if _, err := g.Task("nope"); err == nil {
		t.Fatal("取不存在的任务应报错")
	}
}

// TestTaskGraph_Subgraph_DeepNesting 递归深度上限校验（P4-J 阶段3）。
func TestTaskGraph_Subgraph_DeepNesting(t *testing.T) {
	// 3 层嵌套（root 深度 0 → 最内 subgraph 深度 3）合法。
	if _, err := NewTaskGraph([]TaskSpec{nested(3)}); err != nil {
		t.Fatalf("3 层嵌套应合法: %v", err)
	}
	// 4 层嵌套（最内 subgraph 深度 4）超过 MaxSubgraphDepth。
	if _, err := NewTaskGraph([]TaskSpec{nested(4)}); err == nil {
		t.Fatal("4 层嵌套应超深失败")
	}
}

// TestTaskGraph_Subgraph_TotalLimit 全树任务总数上限校验（含子图）。
func TestTaskGraph_Subgraph_TotalLimit(t *testing.T) {
	sub := make([]TaskSpec, 30)
	for i := range sub {
		sub[i] = spec(fmt.Sprintf("s%d", i))
	}
	// root 1 + subgraph 30 = 31 > MaxTotalTasks → 失败。
	if _, err := NewTaskGraph([]TaskSpec{{ID: "root", Role: "worker", Goal: "g", Subgraph: sub}}); err == nil {
		t.Fatal("全树任务总数超上限应失败")
	}
	// 恰好 30（root 1 + sub 29）→ 合法。
	sub29 := make([]TaskSpec, 29)
	for i := range sub29 {
		sub29[i] = spec(fmt.Sprintf("s%d", i))
	}
	if _, err := NewTaskGraph([]TaskSpec{{ID: "root", Role: "worker", Goal: "g", Subgraph: sub29}}); err != nil {
		t.Fatalf("总数 30 应合法: %v", err)
	}
}

// TestTaskGraph_Subgraph_InvalidDeps 子图是独立 DAG：依赖缺失或引用外层任务都失败。
func TestTaskGraph_Subgraph_InvalidDeps(t *testing.T) {
	// 子图内依赖不存在的任务。
	if _, err := NewTaskGraph([]TaskSpec{{
		ID: "root", Role: "worker", Goal: "g",
		Subgraph: []TaskSpec{spec("b", "missing")},
	}}); err == nil {
		t.Fatal("子图内依赖不存在应失败")
	}
	// 子图依赖外层任务（跨层引用）也应失败。
	if _, err := NewTaskGraph([]TaskSpec{
		spec("outer"),
		{ID: "root", Role: "worker", Goal: "g", Subgraph: []TaskSpec{spec("inner", "outer")}},
	}); err == nil {
		t.Fatal("子图依赖外层任务应失败")
	}
}

// TestTaskGraph_Subgraph_Cycle 子图内环检测。
func TestTaskGraph_Subgraph_Cycle(t *testing.T) {
	if _, err := NewTaskGraph([]TaskSpec{{
		ID: "root", Role: "worker", Goal: "g",
		Subgraph: []TaskSpec{spec("x", "y"), spec("y", "x")},
	}}); err == nil {
		t.Fatal("子图内环应失败")
	}
}
