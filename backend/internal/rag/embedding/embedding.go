// Package embedding 向量化 Provider 抽象（P3-A2）。
//
// DeepSeek 官方不提供 embedding API（官方 issue 已确认无计划），因此向量模型
// 必须走独立供应商。本包定义 Provider 接口（插拔扩展点），默认实现为
// OpenAI 兼容协议（硅基流动 SiliconFlow BGE-M3）；本地模型等其它实现只需
// 实现同一接口即可整体替换，不影响 RAG 框架外部程序。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Provider 向量化接口：将一批文本转换为等长向量列表。
type Provider interface {
	// EmbedBatch 批量向量化文本，返回与输入等长的向量列表（顺序一致）。
	// 空输入返回空切片（不发请求）。
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	// Dim 向量维度（须与迁移建表 vector(n) 一致）。
	Dim() int
	// Name Provider 名（日志/可观测）。
	Name() string
}

// ErrNotConfigured embedding 未配置/不可用（RAG 功能降级提示，不阻塞服务启动）。
// rag-service 未配置 embedding 供应商时使用 UnavailableProvider，
// 相关 RPC（Search/UpsertDocument）返回该错误，由 gRPC 层映射为明确提示。
var ErrNotConfigured = fmt.Errorf("embedding: 未配置向量模型供应商（可设置 SILICONFLOW_API_KEY 走云端，或部署本地 Ollama）")

// UnavailableProvider 未配置 embedding 时的降级 Provider：
// 服务照常启动，但调用向量化的接口返回 ErrNotConfigured。
type UnavailableProvider struct{}

// NewUnavailable 构造降级 Provider（配置缺失时替代真实 Provider）。
func NewUnavailable() Provider { return UnavailableProvider{} }

func (UnavailableProvider) Name() string { return "unavailable" }
func (UnavailableProvider) Dim() int     { return 0 }
func (UnavailableProvider) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, ErrNotConfigured
}

// Config OpenAI 兼容 embedding 端点配置（对应 config.RAGConfig）。
type Config struct {
	BaseURL    string        // 如 https://api.siliconflow.cn/v1
	APIKey     string        // 密钥（SILICONFLOW_API_KEY / RAG_EMBEDDING_API_KEY）
	Model      string        // 如 BAAI/bge-m3
	Dim        int           // 向量维度（校验用，0 = 不校验）
	Timeout    time.Duration // 单次请求超时
	MaxRetries int           // 可重试错误（429/5xx）的最大重试次数
	Log        *zap.Logger
}

// NewOpenAICompatible 构造 OpenAI 兼容 embedding Provider。
// 默认供应商：本地 Ollama（无需 APIKey）或硅基流动（需 key）。
func NewOpenAICompatible(cfg Config) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("embedding: BaseURL 为空")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embedding: Model 为空")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	return &openAICompatible{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

type openAICompatible struct {
	cfg    Config
	client *http.Client
}

func (p *openAICompatible) Name() string { return "openai-compatible:" + p.cfg.Model }
func (p *openAICompatible) Dim() int     { return p.cfg.Dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// EmbedBatch 批量向量化。重试策略：429/5xx/超时指数退避（500ms 起步 ×2，上限 5s）。
func (p *openAICompatible) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: p.cfg.Model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embedding: 序列化请求失败: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(500) * time.Millisecond << (attempt - 1)
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		vecs, retryable, err := p.attempt(ctx, body)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !retryable {
			break
		}
		p.cfg.Log.Warn("embedding 请求重试",
			zap.Int("attempt", attempt+1), zap.Error(err))
	}
	return nil, fmt.Errorf("embedding: 调用 %s 失败: %w", p.cfg.Model, lastErr)
}

func (p *openAICompatible) attempt(ctx context.Context, body []byte) ([][]float32, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	// 本地 Ollama 等供应商无需鉴权：仅当配置了 APIKey 时才携带 Authorization。
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// 网络/超时错误可重试。
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("上游 %s", httpErr(resp.StatusCode, raw))
	default:
		return nil, false, fmt.Errorf("上游 %s", httpErr(resp.StatusCode, raw))
	}

	var parsed embedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false, fmt.Errorf("解析响应失败: %w", err)
	}
	if len(parsed.Data) != len(body2texts(body)) && len(parsed.Data) == 0 {
		return nil, false, fmt.Errorf("响应缺少向量数据（data=%d）", len(parsed.Data))
	}
	if len(parsed.Data) == 0 {
		return nil, false, fmt.Errorf("响应为空（data=[]）")
	}

	// 按 index 排序，保证与输入顺序一致。
	sorted := make([][]float32, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(sorted) {
			return nil, false, fmt.Errorf("响应 index 越界: %d", d.Index)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		if p.cfg.Dim > 0 && len(vec) != p.cfg.Dim {
			return nil, false, fmt.Errorf("向量维度 %d 与配置 %d 不一致", len(vec), p.cfg.Dim)
		}
		sorted[d.Index] = vec
	}
	for i, v := range sorted {
		if v == nil {
			return nil, false, fmt.Errorf("响应缺少 index=%d 的向量", i)
		}
	}
	return sorted, false, nil
}

// body2texts 从请求体还原文本数（校验用；请求体是我们自己序列化的）。
func body2texts(body []byte) []string {
	var req embedRequest
	_ = json.Unmarshal(body, &req)
	return req.Input
}

// httpErr 构造含状态码与上游错误体的错误描述。
func httpErr(status int, raw []byte) string {
	msg := string(raw)
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return fmt.Sprintf("HTTP %d: %s", status, msg)
}
