// 模型注册表（P3 大模型管理）：对外模型名 → 上游接入参数 + 运行期 provider。
//
// 职责：
//   - 持有注册表条目的内存镜像（并发读，写时整体替换）；
//   - 由 ModelStore（PostgreSQL models 表）驱动：管理端点每次写库后
//     Reload 重建 provider，会话请求按 model 名解析到具体上游；
//   - 本地模型（APIKey 为空）构造上游客户端时使用占位密钥——
//     framework llm.NewOpenAICompatible 强制要求非空密钥（防"忘注入密钥"），
//     而 Ollama 等本地端点忽略 Authorization 头，占位不产生实际鉴权。
package llmsvc

import (
	"fmt"
	"sync"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
)

// ModelSpec 模型注册表条目（models 表的一行）。
type ModelSpec struct {
	Name                 string  // 对外模型名（客户端/会话配置使用的名字，主键）
	ProviderName         string  // 供应商展示名（如 deepseek / openai / ollama-local）
	BaseURL              string  // 上游 OpenAI 兼容端点（如 https://api.deepseek.com 或 http://ollama:11434/v1）
	APIKey               string  // 上游密钥；本地模型可为空
	UpstreamModel        string  // 实际发给上游的模型名；空 = 使用 Name
	TimeoutSec           int     // 非流式请求超时（秒）；0 = 60
	MaxRetries           int     // 可重试错误最大重试次数；0 = 不重试
	PromptPricePer1M     float64 // 输入单价（美元/百万 token）
	CompletionPricePer1M float64 // 输出单价（美元/百万 token）
	IsDefault            bool    // 是否默认（入站未指定 model 时使用）
	Enabled              bool    // 是否启用：false = 不参与路由/公开列表，保留配置可再启用
	// NoThinking 上游是否不支持 thinking/reasoning_effort 参数（litellm
	// custom_openai / Ollama 等标准 OpenAI 端点）。true = llm-gateway 转发前
	// 剥离这两个参数，否则上游 400 UnsupportedParamsError。
	NoThinking bool
	// MaxTokens 请求级 max_tokens（completion 输出上限）。0 = 不设置，交由
	// 上游服务端默认值。必须显式配置的原因：DeepSeek 等官方端点未传
	// max_tokens 时走服务端默认（实测 completion 被卡 8192 截断），
	// 大文档/长工具参数轮次必须显式放宽。
	MaxTokens int
	CreatedAt time.Time // 创建时间
	UpdatedAt time.Time // 更新时间
}

// localPlaceholderKey 本地模型（无密钥）构造上游客户端时的占位密钥。
// 本地端点（Ollama 等）忽略 Authorization 头；占位仅满足构造器非空校验。
const localPlaceholderKey = "local-model-no-key"

// ModelEntry 注册表条目：规格 + 运行期 provider。
type ModelEntry struct {
	Spec     ModelSpec
	Provider llm.Provider
	// err 记录该条目的上游客户端构造错误（Reload 时保留，请求侧给出明确提示）。
	err error
}

// Err 返回条目是否可用的构造错误；nil = 可用。
func (e *ModelEntry) Err() error { return e.err }

// Registry 模型注册表运行期镜像：按模型名索引 + 默认模型指针。
// 写少读多，故用 RWMutex + 整体替换，读路径零锁竞争。
type Registry struct {
	mu          sync.RWMutex
	entries     map[string]*ModelEntry // key = 对外模型名
	defaultName string                 // 当前默认模型名（无默认 = 空串）
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*ModelEntry)}
}

// Reload 用 specs 重建全部条目与 provider。单个条目构造失败不阻断其它
// 模型（该条目保留 err，请求侧返回明确错误，便于管理端排查）。
// 默认模型：is_default=true 且构造成功的条目；若其构造失败则回退到
// 任意一个可用条目（保证"未指定 model"的请求不会因默认坏掉而整体不可用）。
func (r *Registry) Reload(specs []ModelSpec, log *zap.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]*ModelEntry, len(specs))
	defaultName := ""
	for _, sp := range specs {
		if sp.Name == "" || !sp.Enabled {
			// 空名忽略；禁用模型不进入注册表（不参与路由与公开列表）。
			continue
		}
		entry := &ModelEntry{Spec: sp}
		p, err := buildProvider(sp)
		if err != nil {
			entry.err = err
			if log != nil {
				log.Error("模型上游客户端构造失败",
					zap.String("model", sp.Name), zap.Error(err))
			}
		} else {
			entry.Provider = p
		}
		next[sp.Name] = entry
		if sp.IsDefault {
			defaultName = sp.Name // 覆盖：默认名以最后一个声明默认的为准
		}
	}

	// 默认条目构造失败时，回退到第一个可用条目。
	if d, ok := next[defaultName]; !ok || d.err != nil {
		for _, e := range next {
			if e.err == nil {
				defaultName = e.Spec.Name
				break
			}
		}
	}

	r.entries = next
	r.defaultName = defaultName
}

// Get 按对外模型名取条目；不存在返回 nil。
func (r *Registry) Get(name string) *ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name]
}

// Default 返回默认模型条目；无默认返回 nil。
func (r *Registry) Default() *ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[r.defaultName]
}

// DefaultName 返回当前默认模型名（无默认 = 空串）。
func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}

// Names 返回全部模型名（保持传入 specs 的顺序）；公开列表接口用。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for name := range r.entries {
		out = append(out, name)
	}
	return out
}

// Len 返回注册表条目数（含构造失败的占位条目）。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// buildProvider 按规格构造上游 OpenAI 兼容客户端。
func buildProvider(sp ModelSpec) (llm.Provider, error) {
	if sp.BaseURL == "" {
		return nil, fmt.Errorf("模型 %q 缺少 BaseURL", sp.Name)
	}
	apiKey := sp.APIKey
	if apiKey == "" {
		apiKey = localPlaceholderKey // 本地模型：满足构造器非空校验
	}
	upstream := sp.UpstreamModel
	if upstream == "" {
		upstream = sp.Name
	}
	timeout := time.Duration(sp.TimeoutSec) * time.Second
	if sp.TimeoutSec <= 0 {
		timeout = 60 * time.Second
	}
	// MaxTokens：>0 才设置（覆盖上游服务端默认输出上限）；0 = 不发该字段。
	var maxTokens *int
	if sp.MaxTokens > 0 {
		mt := sp.MaxTokens
		maxTokens = &mt
	}
	return llm.NewOpenAICompatible(llm.Config{
		Name:       sp.Name,
		BaseURL:    sp.BaseURL,
		APIKey:     apiKey,
		Model:      upstream,
		Timeout:    timeout,
		MaxRetries: sp.MaxRetries,
		MaxTokens:  maxTokens,
	})
}
