// cli 终端交互式聊天示例：真实调用 DeepSeek。
//
// 前置条件：设置环境变量 DEEPSEEK_API_KEY（使用「智能体测试」Key）。
//
// 运行：
//
//	cd framework
//	go run ./examples/cli
//
// 说明：本示例演示 framework 的核心能力——流式输出 + 工具调用
// （已注册 calculator，你可以输入"帮我算 12*13"试试）。
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

func main() {
	// 安全红线：API Key 只从环境变量读取，禁止硬编码
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Println("错误：请先设置环境变量 DEEPSEEK_API_KEY")
		fmt.Println("PowerShell: $env:DEEPSEEK_API_KEY='你的Key'")
		os.Exit(1)
	}

	// 1. 模型 Provider（DeepSeek，OpenAI 兼容协议）
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

	// 2. 工具注册表（框架内置两个示例工具）
	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		fmt.Println("注册计算器失败:", err)
		os.Exit(1)
	}
	if err := reg.Register(tool.EchoTool{}); err != nil {
		fmt.Println("注册回声失败:", err)
		os.Exit(1)
	}

	// 3. Agent 配置（人格 + 能力 + 记忆策略）
	cfg := schema.AgentConfig{
		Model:        llm.DeepSeekFlashModel, // 默认模型（高速低成本）
		SystemPrompt: "你是智能助手，回答要清晰简洁，用中文。涉及数学计算时务必使用 calculator 工具，不要心算。",
		MaxRounds:    8,
		Memory:       schema.MemoryConfig{MaxMessages: 20},
	}

	// 4. 创建会话
	s, err := agent.NewSession(cfg, provider, reg)
	if err != nil {
		fmt.Println("创建会话失败:", err)
		os.Exit(1)
	}

	// 5. 交互循环
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("=== Agent 终端聊天（输入 exit 退出）===")
	for {
		fmt.Print("你> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			break
		}

		// 流式输出：逐 token 打印（打字机效果）
		fmt.Print("AI> ")
		res, err := s.RunStream(context.Background(), input, func(part string) {
			fmt.Print(part)
		})
		fmt.Println()
		if err != nil {
			fmt.Println("[错误]", err)
			continue
		}
		fmt.Printf("\n(共 %d 轮，调用工具 %d 次)\n", res.Rounds, res.ToolCalls)
	}
}
