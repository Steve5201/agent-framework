// memory-demo 演示记忆包的全部接口用法（不需要 API Key，纯本地运行）：
//   - 短期记忆：滑动窗口 + protected 保护 system；
//   - 长期记忆：InMemory（内存）与 File（JSON 文件持久化）；
//   - 哨兵错误 ErrNotFound 的判断；
//   - 把长期记忆注入 Agent 会话。
//
// 运行：
//
//	cd framework
//	go run ./examples/memory-demo
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/memory"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

func main() {
	ctx := context.Background()

	// ---- 1. 短期记忆：滑动窗口 + 保护 system ----
	short, err := memory.NewShortTermMemory(3, 1) // 窗口 3 条，保护开头 1 条
	if err != nil {
		fmt.Println("创建短期记忆失败:", err)
		os.Exit(1)
	}
	short.Add(schema.Message{Role: schema.RoleSystem, Content: "你是教学助手"})
	short.Add(schema.Message{Role: schema.RoleUser, Content: "第一问"})
	short.Add(schema.Message{Role: schema.RoleUser, Content: "第二问"})
	short.Add(schema.Message{Role: schema.RoleUser, Content: "第三问"}) // 触发裁剪：丢"第一问"，system 被保护
	fmt.Println("== 短期记忆 ==")
	for _, m := range short.Recent() {
		fmt.Printf("  [%s] %s\n", m.Role, m.Content)
	}
	short.Clear()
	fmt.Printf("  清空后条数: %d\n\n", short.Len())

	// ---- 2. 长期记忆：文件持久化 ----
	path := filepath.Join(os.TempDir(), "agent-memory-demo.json")
	defer os.Remove(path) // 演示结束清理
	long, err := memory.NewFileLongTermMemory(path)
	if err != nil {
		fmt.Println("创建长期记忆失败:", err)
		os.Exit(1)
	}

	id, err := long.Remember(ctx, memory.MemoryEntry{
		Content: "用户偏好 Go 语言，正在学习 Tauri",
		Source:  "conversation",
		Tags:    []string{"preference", "go"},
	})
	if err != nil {
		fmt.Println("写入记忆失败:", err)
		os.Exit(1)
	}
	_, _ = long.Remember(ctx, memory.MemoryEntry{
		Content: "用户在示例大学读大二",
		Source:  "user_profile",
	})

	fmt.Println("== 长期记忆 ==")
	hits, err := long.Recall(ctx, "Go 学习", 5)
	if err != nil {
		fmt.Println("检索失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  检索 \"Go 学习\" 命中 %d 条:\n", len(hits))
	for _, e := range hits {
		fmt.Printf("    - %s (%s)\n", e.Content, e.ID)
	}

	entry, err := long.Get(ctx, id)
	if err != nil {
		fmt.Printf("读取失败: %v\n", err)
	} else {
		fmt.Printf("  按 ID 读取: %s\n", entry.Content)
	}

	// 哨兵错误判断
	if _, err := long.Get(ctx, "不存在的id"); errors.Is(err, memory.ErrNotFound) {
		fmt.Println("  不存在的记忆返回 ErrNotFound（判断正确）")
	}

	_ = long.Forget(ctx, id)
	list, _ := long.List(ctx)
	fmt.Printf("  删除 1 条后剩余: %d 条\n\n", len(list))

	// ---- 3. 注入 Agent 会话 ----
	// 说明：P1 阶段 Session 已支持注入长期记忆；对话中自动写入/读取
	// 的编排在 P3 落地（届时 MemoryConfig.UseLongTerm=true 生效）。
	provider, err := llm.NewOpenAICompatible(llm.Config{
		Name:    "deepseek",
		BaseURL: llm.DeepSeekBaseURL,
		APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		Model:   llm.DeepSeekFlashModel,
	})
	if err != nil {
		fmt.Println("初始化模型失败:", err)
		os.Exit(1)
	}
	reg := tool.NewRegistry()
	_ = reg.Register(tool.CalculatorTool{})

	cfg := schema.AgentConfig{
		Model:        llm.DeepSeekFlashModel,
		SystemPrompt: "你是教学助手。",
		MaxRounds:    8,
		Memory:       schema.MemoryConfig{MaxMessages: 20, UseLongTerm: true},
	}
	s, err := agent.NewSession(cfg, provider, reg,
		agent.WithLongTermMemory(memory.NewInMemoryLongTermMemory()),
	)
	if err != nil {
		fmt.Println("创建会话失败:", err)
		os.Exit(1)
	}
	fmt.Println("== 接入 Agent ==")
	fmt.Println("  会话已注入长期记忆:", s != nil)
}
