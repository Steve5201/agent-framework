package orchestrate

import (
	"fmt"
	"sort"
	"strings"
)

// TaskGraph 任务依赖图：TaskSpec 集合 + ID 索引。
//
// 构建时做静态校验（ID 唯一 / 依赖存在 / 无环），保证调度阶段不需要再
// 处理非法图。调度由"已完成集合 + 入度 0"驱动（见 Executor）。
type TaskGraph struct {
	Tasks []TaskSpec
	byID  map[string]int
}

// 递归编排上限（P4-J 阶段3）：
const (
	// MaxSubgraphDepth 子图最大嵌套深度。root 层深度 0，一层 subgraph 深度 1，
	// 依此类推；超过上限的任务图构建失败。防模型无限套娃导致失控。
	MaxSubgraphDepth = 3
	// MaxTotalTasks 一次编排全树（含所有嵌套子图）任务总数上限。防过度拆分
	// 导致单次编排 LLM 调用数/成本爆炸。
	MaxTotalTasks = 30
)

// NewTaskGraph 构建任务图并做静态校验：
//   - 全树（含嵌套 subgraph）递归校验：任务字段合法、ID 唯一、深度与总数上限；
//   - 本层 DAG 校验：依赖存在、无环；
//   - 嵌套 subgraph 是独立 DAG：内部依赖只能引用子图内任务，递归构建校验。
func NewTaskGraph(tasks []TaskSpec) (*TaskGraph, error) {
	counter := 0
	if err := validateTree(tasks, 0, &counter); err != nil {
		return nil, err
	}
	return buildGraph(tasks)
}

// validateTree 递归校验任务树（字段/ID 唯一/深度/总数上限）。
func validateTree(tasks []TaskSpec, depth int, counter *int) error {
	if depth > MaxSubgraphDepth {
		return fmt.Errorf("orchestrate: 子图嵌套深度超过上限 %d", MaxSubgraphDepth)
	}
	ids := make(map[string]bool, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		if err := t.Validate(); err != nil {
			return err
		}
		if ids[t.ID] {
			return fmt.Errorf("orchestrate: 任务 ID %q 重复", t.ID)
		}
		ids[t.ID] = true
		*counter++
		if *counter > MaxTotalTasks {
			return fmt.Errorf("orchestrate: 任务总数超过上限 %d（含子图）", MaxTotalTasks)
		}
		if len(t.Subgraph) > 0 {
			if err := validateTree(t.Subgraph, depth+1, counter); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildGraph 构建单层 DAG（ID 索引 + 依赖存在 + 无环），嵌套 subgraph 递归构建。
func buildGraph(tasks []TaskSpec) (*TaskGraph, error) {
	g := &TaskGraph{byID: make(map[string]int, len(tasks))}
	for i := range tasks {
		g.byID[tasks[i].ID] = i
		g.Tasks = append(g.Tasks, tasks[i])
	}
	for i := range g.Tasks {
		for _, dep := range g.Tasks[i].Deps {
			if _, ok := g.byID[dep]; !ok {
				return nil, fmt.Errorf("orchestrate: 任务 %s 依赖不存在的任务 %q", g.Tasks[i].ID, dep)
			}
		}
		if len(g.Tasks[i].Subgraph) > 0 {
			if _, err := buildGraph(g.Tasks[i].Subgraph); err != nil {
				return nil, err
			}
		}
	}
	if err := g.checkAcyclic(); err != nil {
		return nil, err
	}
	return g, nil
}

// Len 任务总数。
func (g *TaskGraph) Len() int { return len(g.Tasks) }

// Task 按 ID 取任务；不存在返回错误。
func (g *TaskGraph) Task(id string) (TaskSpec, error) {
	i, ok := g.byID[id]
	if !ok {
		return TaskSpec{}, fmt.Errorf("orchestrate: 任务 %q 不存在", id)
	}
	return g.Tasks[i], nil
}

// ReadyTasks 返回当前可执行的任务 ID（全部依赖已完成，且自身未完成）。
// done 集合由执行器维护（ID → 结果）。返回结果按任务定义顺序，调度可预期。
func (g *TaskGraph) ReadyTasks(done map[string]bool) []string {
	var out []string
	for _, t := range g.Tasks {
		if done[t.ID] {
			continue
		}
		ready := true
		for _, dep := range t.Deps {
			if !done[dep] {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, t.ID)
		}
	}
	return out
}

// checkAcyclic 拓扑排序检测环：有环则任务无法全部推进，返回错误。
func (g *TaskGraph) checkAcyclic() error {
	indeg := make(map[string]int, len(g.Tasks))
	adj := make(map[string][]string, len(g.Tasks))
	for _, t := range g.Tasks {
		indeg[t.ID] = len(t.Deps)
		for _, dep := range t.Deps {
			adj[dep] = append(adj[dep], t.ID)
		}
	}
	queue := make([]string, 0, len(g.Tasks))
	for id, d := range indeg {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue) // 稳定遍历顺序
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		nexts := adj[id]
		sort.Strings(nexts)
		for _, next := range nexts {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(g.Tasks) {
		// 找出环内节点（入度仍未清零的）便于排查
		var inCycle []string
		for id, d := range indeg {
			if d > 0 {
				inCycle = append(inCycle, id)
			}
		}
		sort.Strings(inCycle)
		return fmt.Errorf("orchestrate: 任务依赖图存在环（涉及 %s）",
			strings.Join(inCycle, ", "))
	}
	return nil
}
