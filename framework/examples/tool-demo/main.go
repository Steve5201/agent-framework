// tool-demo 演示 Function Calling 完整闭环：
// LLM 根据用户需求，自主决定调用 calculator 工具并给出答案。
//
// 前置条件：设置环境变量 DEEPSEEK_API_KEY（使用「智能体测试」Key）。
//
// 运行：
//
//	cd framework
//	go run ./examples/tool-demo
//
// 预期：LLM 会先发出工具调用指令 → 框架执行计算 → 回填结果 →
// LLM 基于真实计算结果作答（而不是自己心算）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

func main() {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Println("错误：请先设置环境变量 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	provider, err := llm.NewOpenAICompatible(llm.Config{
		Name:    "deepseek",
		BaseURL: llm.DeepSeekBaseURL,
		APIKey:  apiKey,
		Model:   llm.DeepSeekFlashModel,
	})
	if err != nil {
		fmt.Println("初始化模型失败:", err)
		os.Exit(1)
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		fmt.Println("注册计算器失败:", err)
		os.Exit(1)
	}

	cfg := schema.AgentConfig{
		Model: llm.DeepSeekFlashModel,
		SystemPrompt: "你是计算助手。凡是数学计算，必须调用 calculator 工具得到精确结果，" +
			"严禁自己心算或估算。",
		MaxRounds: 8,
		Memory:    schema.MemoryConfig{MaxMessages: 20},
	}

	s, err := agent.NewSession(cfg, provider, reg)
	if err != nil {
		fmt.Println("创建会话失败:", err)
		os.Exit(1)
	}

	res, err := s.Run(context.Background(), "请分别计算 12*13 和 8/4 的结果，并告诉我答案")
	if err != nil {
		fmt.Println("运行失败:", err)
		os.Exit(1)
	}

	fmt.Println("AI:", res.Content)
	fmt.Printf("统计：%d 轮消息循环，调用工具 %d 次\n", res.Rounds, res.ToolCalls)
}
