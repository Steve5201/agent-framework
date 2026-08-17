// 模型注册表管理端点（P3 大模型管理）。
//
// 路由划分：
//   - /v1/admin/models*：管理端点，须携带 X-Admin-Token（LLM_ADMIN_TOKEN），
//     供 gateway 的 adminsvc 代理调用；返回完整模型信息（密钥打码）；
//   - /v1/models：公开只读列表（仅名字/供应商/默认位），供 agent-service
//     会话配置区渲染模型下拉——不含任何密钥与接入地址。
//
// 安全约束：API Key 只存在于本服务（llm-gateway）。管理端点必须校验令牌，
// 否则 8083 端口（compose 暴露到宿主）上的任何人都能读写模型密钥。
// 令牌未配置（LLM_ADMIN_TOKEN 为空）时管理端点禁用（503），公开列表不受影响。
package llmsvc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// modelNameRe 对外模型名白名单。禁止 '/' 与空白：模型名会作为 URL 路径段
// （/v1/admin/models/{name}）与请求体 model 字段，两者都不接受斜杠；
// 带斜杠的厂商模型名（如 deepseek-ai/DeepSeek-V3）可填在上游模型名
// （upstream_model），对外名用其无斜杠别名。
var modelNameRe = regexp.MustCompile(`^[A-Za-z0-9._:+-]{1,64}$`)

// maxModelBodyBytes 管理端点请求体上限（1 MiB）。
const maxModelBodyBytes = 1 << 20

// ModelAdmin 模型注册表管理处理器。
type ModelAdmin struct {
	store    ModelStore
	registry *Registry
	token    string // LLM_ADMIN_TOKEN；空 = 管理端点禁用
	log      *zap.Logger
}

// NewModelAdmin 创建模型管理处理器。
func NewModelAdmin(store ModelStore, registry *Registry, token string, log *zap.Logger) *ModelAdmin {
	return &ModelAdmin{store: store, registry: registry, token: token, log: log}
}

// RegisterAdmin 注册管理端点（全部要求 X-Admin-Token）。
func (a *ModelAdmin) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/models", a.requireToken(a.handleList))
	mux.HandleFunc("POST /v1/admin/models", a.requireToken(a.handleCreate))
	mux.HandleFunc("PUT /v1/admin/models/{name}", a.requireToken(a.handleUpdate))
	mux.HandleFunc("POST /v1/admin/models/{name}/default", a.requireToken(a.handleSetDefault))
	mux.HandleFunc("POST /v1/admin/models/{name}/status", a.requireToken(a.handleSetStatus))
	mux.HandleFunc("DELETE /v1/admin/models/{name}", a.requireToken(a.handleDelete))
}

// RegisterPublic 注册公开只读列表（会话配置区模型下拉，无密钥暴露）。
func (a *ModelAdmin) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", a.handlePublicList)
}

// requireToken 管理端点令牌中间件：令牌未配置 → 503；不匹配 → 401。
func (a *ModelAdmin) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			writeError(w, "", apperr.New(apperr.CodeUnavailable,
				"模型管理端点未启用：请为 llm-gateway 与 gateway 设置 LLM_ADMIN_TOKEN"))
			return
		}
		got := r.Header.Get("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			writeError(w, "", apperr.New(apperr.CodeUnauthenticated, "管理令牌无效"))
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// 请求/响应模型
// ---------------------------------------------------------------------------

// modelInput 创建/更新模型的接入参数（对外 JSON 契约）。
type modelInput struct {
	Name                 string  `json:"name"`                    // 对外模型名（创建必填；更新由路径决定）
	ProviderName         string  `json:"provider_name"`           // 供应商展示名
	BaseURL              string  `json:"base_url"`                // 上游 OpenAI 兼容端点
	APIKey               string  `json:"api_key"`                 // 上游密钥；本地模型留空。更新时空 = 保留原密钥
	UpstreamModel        string  `json:"upstream_model"`          // 实际发给上游的模型名；空 = 使用 name
	TimeoutSec           int     `json:"timeout_sec"`             // 非流式超时（秒）；0 = 60
	MaxRetries           int     `json:"max_retries"`             // 可重试错误最大重试次数；0 = 不重试
	PromptPricePer1M     float64 `json:"prompt_price_per_1m"`     // 输入单价（美元/百万 token）
	CompletionPricePer1M float64 `json:"completion_price_per_1m"` // 输出单价（美元/百万 token）
	IsDefault            bool    `json:"is_default"`              // 创建时是否设为默认
	// NoThinking 上游是否不支持 thinking/reasoning_effort 参数（litellm
	// custom_openai / Ollama 等标准 OpenAI 端点）。true = 转发前剥离。
	NoThinking bool `json:"no_thinking"`
	// MaxTokens 请求级 max_tokens（completion 输出上限）。0 = 不设置（交
	// 由上游服务端默认）；大文档/长工具参数模型（如 DeepSeek）建议显式
	// 设置（如 16384），否则默认输出上限会截断生成内容。
	MaxTokens int `json:"max_tokens"`
}

// validate 创建语义校验；返回业务错误（带用户可读信息）。
func (in *modelInput) validate(creating bool) error {
	if in.Name == "" && creating {
		return apperr.New(apperr.CodeInvalidArgument, "模型名不能为空")
	}
	if in.Name != "" && !modelNameRe.MatchString(in.Name) {
		return apperr.New(apperr.CodeInvalidArgument,
			"模型名只能包含字母/数字/._:+-（1~64 位），不能含斜杠或空白")
	}
	if in.BaseURL == "" {
		return apperr.New(apperr.CodeInvalidArgument, "上游端点 BaseURL 不能为空")
	}
	if in.TimeoutSec < 0 || in.TimeoutSec > 600 {
		return apperr.New(apperr.CodeInvalidArgument, "timeout_sec 需在 0~600 之间（0 = 默认 60）")
	}
	if in.MaxRetries < 0 || in.MaxRetries > 10 {
		return apperr.New(apperr.CodeInvalidArgument, "max_retries 需在 0~10 之间")
	}
	if in.PromptPricePer1M < 0 || in.CompletionPricePer1M < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "模型价格不能为负数")
	}
	// max_tokens 输出上限因模型/厂商而异（如 DeepSeek-V4 支持 384K），
	// 不做全局硬上限：仅拦截负数（协议非法），0 = 不设置；超出模型实际
	// 上限时由上游返回明确错误，避免注册表误伤可用的更大值。
	if in.MaxTokens < 0 {
		return apperr.New(apperr.CodeInvalidArgument,
			"max_tokens 不能为负数（0 = 不设置，使用上游默认）")
	}
	return nil
}

// spec 将输入转为 ModelSpec（Name 由调用方传入；新建模型默认启用）。
func (in *modelInput) spec(name string) ModelSpec {
	return ModelSpec{
		Name:                 name,
		ProviderName:         in.ProviderName,
		BaseURL:              in.BaseURL,
		APIKey:               in.APIKey,
		UpstreamModel:        in.UpstreamModel,
		TimeoutSec:           in.TimeoutSec,
		MaxRetries:           in.MaxRetries,
		PromptPricePer1M:     in.PromptPricePer1M,
		CompletionPricePer1M: in.CompletionPricePer1M,
		NoThinking:           in.NoThinking,
		MaxTokens:            in.MaxTokens,
		Enabled:              true, // 新建模型默认启用；启停走 /{name}/status
	}
}

// SeedAdminModels 从 ADMIN_MODELS（JSON 数组）批量播种模型注册表，幂等。
//
// 用途：多模型一次性接入（如学校本地网关、多个厂商），免去管理端逐条添加。
// 契约与 modelInput 一致：name / provider_name / base_url / api_key /
// upstream_model / timeout_sec / max_retries / prompt_price_per_1m /
// completion_price_per_1m / is_default / enabled。已存在的同名模型跳过；
// 全部播种完成后，is_default=true 的条目统一 SetDefault（转移默认位）。
// jsonStr 为空或解析后无条目时直接返回 nil。
func SeedAdminModels(ctx context.Context, store ModelStore, jsonStr string, log *zap.Logger) error {
	if strings.TrimSpace(jsonStr) == "" {
		return nil
	}
	var in []modelInput
	if err := json.Unmarshal([]byte(jsonStr), &in); err != nil {
		return fmt.Errorf("ADMIN_MODELS 解析失败: %w", err)
	}
	if len(in) == 0 {
		return nil
	}
	// 先全量校验，再统一创建：任一条不合法则整批不生效，
	// 避免"播种一半"的中间状态（启动配置错误应整体暴露）。
	specs := make([]ModelSpec, len(in))
	for i := range in {
		m := &in[i]
		if err := m.validate(true); err != nil {
			return fmt.Errorf("ADMIN_MODELS 第 %d 条校验失败: %w", i+1, err)
		}
		specs[i] = m.spec(m.Name)
	}
	for i, spec := range specs {
		switch err := store.CreateModel(ctx, spec); {
		case err == nil:
			log.Info("已播种管理模型", zap.String("model", spec.Name))
		case errors.Is(err, ErrModelExists):
			log.Info("管理模型已存在，跳过", zap.String("model", in[i].Name))
		default:
			return err
		}
	}
	// 默认位：表空时首个模型已被 CreateModel 强制为默认；此处再按
	// is_default=true 的条目统一转移（覆盖"表已存在默认"的升级场景）。
	for i := range in {
		m := &in[i]
		if !m.IsDefault {
			continue
		}
		if err := store.SetDefault(ctx, m.Name); err != nil && !errors.Is(err, ErrModelNotFound) {
			return err
		}
		log.Info("已将管理模型设为默认", zap.String("model", m.Name))
	}
	return nil
}

// modelView 完整视图（密钥打码，对外契约）。
func modelView(sp ModelSpec) map[string]any {
	return map[string]any{
		"name":                    sp.Name,
		"provider_name":           sp.ProviderName,
		"base_url":                sp.BaseURL,
		"api_key":                 maskKey(sp.APIKey),
		"has_api_key":             sp.APIKey != "" && sp.APIKey != localPlaceholderKey,
		"upstream_model":          sp.UpstreamModel,
		"timeout_sec":             sp.TimeoutSec,
		"max_retries":             sp.MaxRetries,
		"prompt_price_per_1m":     sp.PromptPricePer1M,
		"completion_price_per_1m": sp.CompletionPricePer1M,
		"is_default":              sp.IsDefault,
		"enabled":                 sp.Enabled,
		"no_thinking":             sp.NoThinking,
		"max_tokens":              sp.MaxTokens,
		"created_at":              sp.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":              sp.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// maskKey 密钥打码：保留末 4 位，其余以 **** 遮盖；空/占位密钥返回空串。
func maskKey(k string) string {
	if k == "" || k == localPlaceholderKey {
		return ""
	}
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// ---------------------------------------------------------------------------
// 处理函数
// ---------------------------------------------------------------------------

// handleList GET /v1/admin/models → 全量列表（密钥打码）。
func (a *ModelAdmin) handleList(w http.ResponseWriter, r *http.Request) {
	specs, err := a.store.ListModels(r.Context())
	if err != nil {
		a.log.Error("查询模型列表失败", zap.Error(err))
		writeError(w, "", apperr.New(apperr.CodeInternal, "查询模型列表失败"))
		return
	}
	out := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		out = append(out, modelView(sp))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// handleCreate POST /v1/admin/models。
func (a *ModelAdmin) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if err := decodeModelJSON(r, &in); err != nil {
		writeError(w, "", err)
		return
	}
	if err := in.validate(true); err != nil {
		writeError(w, "", err)
		return
	}
	sp := in.spec(in.Name)
	sp.IsDefault = in.IsDefault
	if err := a.store.CreateModel(r.Context(), sp); err != nil {
		writeError(w, "", mapModelStoreError(err))
		return
	}
	a.reload(r)
	// 回读创建后的权威数据：首个模型会被强制设为默认，返回真实默认位。
	if created, err := a.findOne(r.Context(), in.Name); err == nil {
		writeJSON(w, http.StatusCreated, map[string]any{"model": modelView(created)})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"model": modelView(sp)})
}

// handleUpdate PUT /v1/admin/models/{name}。
// APIKey 留空 = 保留原密钥（前端只回传打码值，不可直接覆盖明文）。
func (a *ModelAdmin) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in modelInput
	if err := decodeModelJSON(r, &in); err != nil {
		writeError(w, "", err)
		return
	}
	if err := in.validate(false); err != nil {
		writeError(w, "", err)
		return
	}
	// 读取现有条目：更新时 api_key 空 = 保留原值；同时禁止修改默认位（走 /default）。
	existing, err := a.store.ListModels(r.Context())
	if err != nil {
		a.log.Error("查询模型失败", zap.String("model", name), zap.Error(err))
		writeError(w, "", apperr.New(apperr.CodeInternal, "查询模型失败"))
		return
	}
	var cur *ModelSpec
	for i := range existing {
		if existing[i].Name == name {
			cur = &existing[i]
			break
		}
	}
	if cur == nil {
		writeError(w, "", mapModelStoreError(ErrModelNotFound))
		return
	}
	sp := in.spec(name)
	if in.APIKey == "" {
		sp.APIKey = cur.APIKey // 未提供密钥 → 保留原密钥
	}
	sp.IsDefault = cur.IsDefault // 默认位只能经 /{name}/default 修改
	if err := a.store.UpdateModel(r.Context(), name, sp); err != nil {
		writeError(w, "", mapModelStoreError(err))
		return
	}
	a.reload(r)
	writeJSON(w, http.StatusOK, map[string]any{"model": modelView(sp)})
}

// handleSetDefault POST /v1/admin/models/{name}/default。
func (a *ModelAdmin) handleSetDefault(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.store.SetDefault(r.Context(), name); err != nil {
		writeError(w, "", mapModelStoreError(err))
		return
	}
	a.reload(r)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetStatus POST /v1/admin/models/{name}/status。
// 启用/禁用模型：禁用的模型不参与路由、不出现在公开列表，但保留配置。
// 默认模型不可禁用（默认位 = 始终可用），可先转移默认位再禁用。
func (a *ModelAdmin) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeModelJSON(r, &body); err != nil {
		writeError(w, "", err)
		return
	}
	if err := a.store.SetEnabled(r.Context(), name, body.Enabled); err != nil {
		writeError(w, "", mapModelStoreError(err))
		return
	}
	a.reload(r)
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete DELETE /v1/admin/models/{name}。
// 默认模型受保护不可删除（ErrDefaultModel），可先将另一模型设为默认再删。
func (a *ModelAdmin) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.store.DeleteModel(r.Context(), name); err != nil {
		writeError(w, "", mapModelStoreError(err))
		return
	}
	a.reload(r)
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicList GET /v1/models（公开）：仅名字/供应商/默认位 + 启用过滤。
// 会话配置区下拉只展示可用的（启用）模型。
func (a *ModelAdmin) handlePublicList(w http.ResponseWriter, r *http.Request) {
	specs, err := a.store.ListModels(r.Context())
	if err != nil {
		a.log.Error("查询模型列表失败（公开）", zap.Error(err))
		writeError(w, "", apperr.New(apperr.CodeInternal, "查询模型列表失败"))
		return
	}
	out := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		if !sp.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"name":          sp.Name,
			"provider_name": sp.ProviderName,
			"is_default":    sp.IsDefault,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// findOne 按名回读单条模型（ListModels 过滤；管理端点写库后回读权威数据用）。
func (a *ModelAdmin) findOne(ctx context.Context, name string) (ModelSpec, error) {
	specs, err := a.store.ListModels(ctx)
	if err != nil {
		return ModelSpec{}, err
	}
	for _, sp := range specs {
		if sp.Name == name {
			return sp, nil
		}
	}
	return ModelSpec{}, ErrModelNotFound
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// reload 写库成功后重建运行期注册表（失败仅记日志，下次请求仍用旧镜像）。
// 用独立超时 context：请求可能已结束，刷新不能被请求生命周期拖累。
func (a *ModelAdmin) reload(r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	specs, err := a.store.ListModels(ctx)
	if err != nil {
		a.log.Error("刷新模型注册表失败", zap.Error(err))
		return
	}
	a.registry.Reload(specs, a.log)
}

// decodeModelJSON 解析管理端点 JSON 请求体（含大小限制与非法 JSON 错误）。
func decodeModelJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxModelBodyBytes))
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "读取请求体失败")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "请求体不是合法的 JSON")
	}
	return nil
}

// writeJSON 输出统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// mapModelStoreError 存储错误 → 业务错误。
func mapModelStoreError(err error) error {
	switch err {
	case ErrModelExists:
		return apperr.New(apperr.CodeAlreadyExists, "同名模型已存在")
	case ErrModelNotFound:
		return apperr.New(apperr.CodeNotFound, "模型不存在")
	case ErrDefaultModel:
		return apperr.New(apperr.CodeFailedPrecondition,
			"默认模型受保护：不可删除或禁用，请先将另一模型设为默认")
	case ErrClearDefault:
		return apperr.New(apperr.CodeFailedPrecondition, "不能取消当前默认模型，请先将另一模型设为默认")
	default:
		return apperr.Wrap(apperr.CodeInternal, "模型存储操作失败", err)
	}
}
