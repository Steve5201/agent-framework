package orchestrate

// ProgressType 编排运行进度事件类型。
//
// 进度事件是编排器的"可观测性"出口：宿主（agent-service）把这些事件
// 实时映射为 SSE 下发，前端据此渲染节点状态流（running → completed /
// failed / skipped），无需等整个编排结束。
type ProgressType string

// 进度事件类型全集。
const (
	// ProgressTaskStarted 子任务开始执行（status=running）。
	ProgressTaskStarted ProgressType = "task_started"
	// ProgressTaskFinished 子任务执行结束（status=completed/failed/skipped）。
	ProgressTaskFinished ProgressType = "task_finished"
	// ProgressRunCompleted 整个编排成功结束（所有任务终态已定）。
	ProgressRunCompleted ProgressType = "run_completed"
	// ProgressRunFailed 整个编排失败（如 FailAbort 提前中止、取消等）。
	ProgressRunFailed ProgressType = "run_failed"
)

// ProgressEvent 一次编排进度事件（宿主映射为 SSE task_status 事件）。
type ProgressEvent struct {
	Type   ProgressType `json:"type"`
	TaskID string       `json:"task_id,omitempty"` // task_started/task_finished 时非空
	Status TaskStatus   `json:"status,omitempty"`  // task_finished 时为任务终态
	Result *TaskResult  `json:"result,omitempty"`  // task_finished 时携带（含 Usage/耗时）
	Error  string       `json:"error,omitempty"`   // run_failed 时为失败原因
}

// ProgressFunc 进度回调。nil = 不回调。回调应尽量轻量（勿阻塞调度）。
type ProgressFunc func(ProgressEvent)
