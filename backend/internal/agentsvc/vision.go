// vision.go —— 图片视觉解析（路线 A·描述中转 + 多模态兼容预留）。
//
// 背景：上传图片后模型默认"看不到"图片内容。接入的模型能力不一，按能力选路：
//
//   - 路线 A（本文件实现）：纯文本模型（如 DeepSeek 链路）→ 用一个支持视觉的
//     OpenAI 兼容模型把图片转成文字描述，注入 [图片] 消息，模型据此"读懂"图片；
//   - 路线 B（预留，未实现）：模型自带多模态 → 经对话 content 的 image_url 类型
//     直传图片，无需描述中转（届时改 schema/消息协议，见 P2-AL 规划）。
//
// 配置（环境变量驱动，代码零硬编码密钥；与 DEEPSEEK_API_KEY 同源哲学）：
//   - VISION_MODEL    视觉模型名（如 qwen-vl-plus / glm-4v / gpt-4o-mini）。
//     未配置 → NoopVision 降级：图片仅渲染、不解析内容；
//   - VISION_BASE_URL OpenAI 兼容端点（缺省 https://api.deepseek.com/v1）；
//   - VISION_API_KEY  视觉模型密钥（缺省复用 DEEPSEEK_API_KEY）。
//
// 以上三者都能用环境变量覆盖，不写死在代码里。
//
// 降级契约：Describe 出错（未启用/网络/模型不支持）不影响图片上传与渲染，
// 调用方只跳过描述注入并记录日志。
package agentsvc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// VisionProvider 把图片内容转为文字描述的统一入口。
// 实现选择：NoopVision（未配置时的占位）/ OpenAIVision（OpenAI 兼容多模态）。
type VisionProvider interface {
	// Describe 返回图片的文字描述；err 表示当前无法描述（调用方降级：
	// 仅渲染图片、不注入描述，不中断上传流程）。
	Describe(ctx context.Context, image []byte, mime string) (string, error)
}

// visionTimeout 单次视觉描述调用超时（上传流程内同步等待，避免长时间阻塞）。
const visionTimeout = 15 * time.Second

// visionPrompt 描述指令：输出简洁、可被下游文本模型引用的图片说明。
const visionPrompt = "请用简洁的中文描述这张图片的内容，重点包含其中的文字、结构、数据与要点，供无法看到图片的模型引用。若图片是截图/图表，请说明其类型与关键信息。"

// NoopVision 占位实现：未配置 VISION_MODEL 时使用（图片仅渲染、不解析内容）。
type NoopVision struct{}

// Describe 实现 VisionProvider：始终返回未启用错误，调用方据此跳过描述注入。
func (NoopVision) Describe(context.Context, []byte, string) (string, error) {
	return "", errVisionNotEnabled
}

// errVisionNotEnabled 视觉能力未启用占位错误。
var errVisionNotEnabled = errors.New("agentsvc: 图片视觉解析未启用（未配置 VISION_MODEL）")

// OpenAIVision 路线 A 实现：调用 OpenAI 兼容多模态端点把图片转为文字描述。
// 支持主备双后端：主模型失败（如免费模型限流 429）时自动切换备用模型重试
// （兜底机制，见 fallback / NewVisionFromEnv）。
type OpenAIVision struct {
	baseURL string // 主端点
	apiKey  string // 主密钥
	model   string // 主模型名
	log     *zap.Logger
	// fallback 备用视觉后端（可选）：主模型 Describe 失败时顺序尝试。
	// 由 VISION_FALLBACK_MODEL 装配；BASE_URL/API_KEY 缺省复用主配置。
	fallback *visionBackend
	// thinking 思考模式开关（主备共用）：nil = 不发送（厂商默认）；
	// disabled = 关闭深度思考（VISION_THINKING 环境变量装配，响应更快）。
	thinking *visionThinking
}

// visionBackend 单个视觉后端（主/备共用此结构发起 HTTP 调用）。
type visionBackend struct {
	baseURL string
	apiKey  string
	model   string
	log     *zap.Logger
}

// Describe 实现 VisionProvider：先主模型，失败则切备用（兜底机制）。
// 备用也为空或同样失败时返回聚合错误；调用方仍按既有降级契约处理
// （跳过描述注入，不影响图片上传与渲染）。
func (v *OpenAIVision) Describe(ctx context.Context, image []byte, mime string) (string, error) {
	desc, err := v.describeOne(ctx, v.baseURL, v.apiKey, v.model, image, mime)
	if err == nil {
		return desc, nil
	}
	if v.fallback == nil {
		return "", err
	}
	v.log.Warn("主视觉模型失败，切换备用模型重试",
		zap.String("primary_model", v.model),
		zap.String("fallback_model", v.fallback.model),
		zap.Error(err))
	desc, ferr := v.describeOne(ctx, v.fallback.baseURL, v.fallback.apiKey, v.fallback.model, image, mime)
	if ferr == nil {
		return desc, nil
	}
	return "", fmt.Errorf("视觉解析失败（主模型 %s: %v；备用模型 %s: %w）",
		v.model, err, v.fallback.model, ferr)
}

// describeOne 向单个 OpenAI 兼容多模态端点发起一次描述请求。
// 响应兼容"深度思考"类模型：content 为空时回退取 reasoning_content
// （如智谱 GLM-4.1V-Thinking-Flash，答案常落在 reasoning_content）。
func (v *OpenAIVision) describeOne(ctx context.Context, baseURL, apiKey, model string, image []byte, mime string) (string, error) {
	if model == "" || baseURL == "" {
		return "", errVisionNotEnabled
	}
	// OpenAI 多模态协议：content 为 part 数组（text + image_url）。
	payload := visionChatRequest{
		Model: model,
		Messages: []visionMessage{{
			Role: "user",
			Content: []visionPart{
				{Type: "text", Text: visionPrompt},
				{
					Type:     "image_url",
					ImageURL: visionImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)},
				},
			},
		}},
		MaxTokens: 1024,
		Thinking:  v.thinking,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("编码视觉请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("构造视觉请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用视觉模型失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return "", fmt.Errorf("视觉模型返回 %d: %s", resp.StatusCode, e.Error.Message)
	}
	var out visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("解析视觉模型响应失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("视觉模型未返回内容")
	}
	desc := strings.TrimSpace(out.Choices[0].Message.Content)
	if desc == "" {
		// 深度思考类模型（GLM-4.1V-Thinking 系列）：答案在 reasoning_content。
		desc = strings.TrimSpace(out.Choices[0].Message.ReasoningContent)
	}
	if desc == "" {
		return "", errors.New("视觉模型返回空描述")
	}
	if v.log != nil {
		v.log.Debug("vision describe ok", zap.String("model", model), zap.Int("image_bytes", len(image)))
	}
	return desc, nil
}

// NewVisionFromEnv 按环境变量装配视觉解析实现（未配置 VISION_MODEL 时返回
// NoopVision 占位，保持"图片仅渲染不解析"的既有行为，向后兼容）。
// 支持备用模型兜底：VISION_FALLBACK_MODEL 可选，其 BASE_URL/API_KEY 缺省
// 复用主配置（典型场景：智谱两个免费视觉模型互为兜底，抵御限流 429）。
func NewVisionFromEnv(log *zap.Logger) VisionProvider {
	model := strings.TrimSpace(os.Getenv("VISION_MODEL"))
	if model == "" {
		return NoopVision{}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("VISION_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}
	apiKey := strings.TrimSpace(os.Getenv("VISION_API_KEY"))
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}

	var fallback *visionBackend
	if fbModel := strings.TrimSpace(os.Getenv("VISION_FALLBACK_MODEL")); fbModel != "" {
		fbBase := strings.TrimRight(strings.TrimSpace(os.Getenv("VISION_FALLBACK_BASE_URL")), "/")
		if fbBase == "" {
			fbBase = baseURL
		}
		fbKey := strings.TrimSpace(os.Getenv("VISION_FALLBACK_API_KEY"))
		if fbKey == "" {
			fbKey = apiKey
		}
		fallback = &visionBackend{baseURL: fbBase, apiKey: fbKey, model: fbModel, log: log}
	}
	return &OpenAIVision{baseURL: baseURL, apiKey: apiKey, model: model, log: log, fallback: fallback, thinking: thinkingFromEnv()}
}

// thinkingFromEnv 读取 VISION_THINKING 环境变量 → 思考模式开关（主备模型共用）。
//   - disabled：关闭深度思考（响应更快；图片描述任务推荐，智谱视觉模型实测支持）
//   - enabled：显式开启
//   - 其它/空：返回 nil，不发送 thinking 参数（厂商默认行为，兼容未知端点）
func thinkingFromEnv() *visionThinking {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VISION_THINKING"))) {
	case "disabled":
		return &visionThinking{Type: "disabled"}
	case "enabled":
		return &visionThinking{Type: "enabled"}
	}
	return nil
}

// ---------------------------------------------------------------------------
// OpenAI 多模态协议（仅视觉通道使用，与主链路 llm 包的纯文本协议隔离）
// ---------------------------------------------------------------------------

type visionChatRequest struct {
	Model     string          `json:"model"`
	Messages  []visionMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	// Thinking 思考模式开关（三态，omitempty）：nil = 不发送（厂商默认）；
	// disabled = 关闭深度思考（响应更快，图片描述任务建议）；enabled = 开启。
	// 由 VISION_THINKING 环境变量装配（智谱视觉模型实测均支持）。
	Thinking *visionThinking `json:"thinking,omitempty"`
}

type visionThinking struct {
	Type string `json:"type"`
}

type visionMessage struct {
	Role    string       `json:"role"`
	Content []visionPart `json:"content"`
}

type visionPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

// visionChatResponse OpenAI 兼容多模态响应。
// ReasoningContent 兼容"深度思考"类模型（GLM-4.1V-Thinking 系列）：
// 答案可能不在 content 而在 reasoning_content。
type visionChatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
}
