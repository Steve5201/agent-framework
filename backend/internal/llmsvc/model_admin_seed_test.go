package llmsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// fakeModelStore 内存版 ModelStore，用于 SeedAdminModels 单测。
// 语义对齐 postgresModelStore：同名冲突 ErrModelExists、表空首个模型强制默认、
// SetDefault 转移默认位且目标不存在时报 ErrModelNotFound。
type fakeModelStore struct {
	models   map[string]ModelSpec
	defaultN string // 当前默认模型名（空 = 无默认）
}

func newFakeModelStore() *fakeModelStore {
	return &fakeModelStore{models: make(map[string]ModelSpec)}
}

func (f *fakeModelStore) ListModels(ctx context.Context) ([]ModelSpec, error) {
	out := make([]ModelSpec, 0, len(f.models))
	for _, sp := range f.models {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeModelStore) CreateModel(ctx context.Context, spec ModelSpec) error {
	if _, ok := f.models[spec.Name]; ok {
		return ErrModelExists
	}
	// 表空 → 强制首个模型为默认。
	if len(f.models) == 0 {
		spec.IsDefault = true
		f.defaultN = spec.Name
	} else if spec.IsDefault {
		f.defaultN = spec.Name
	}
	f.models[spec.Name] = spec
	return nil
}

func (f *fakeModelStore) UpdateModel(ctx context.Context, name string, spec ModelSpec) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	spec.Name = name
	f.models[name] = spec
	return nil
}

func (f *fakeModelStore) DeleteModel(ctx context.Context, name string) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	delete(f.models, name)
	return nil
}

func (f *fakeModelStore) SetDefault(ctx context.Context, name string) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	f.defaultN = name
	return nil
}

func (f *fakeModelStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	sp := f.models[name]
	sp.Enabled = enabled
	f.models[name] = sp
	return nil
}

func noopLogger() *zap.Logger {
	return zap.NewNop()
}

// 校验 SeedAdminModels 的契约字段名与 modelInput 完全一致（防漂移）。
func TestSeedAdminModels_PayloadContract(t *testing.T) {
	jsonStr := `[{
		"name":"glm-5.2",
		"provider_name":"school-gateway",
		"base_url":"https://gateway.example.com/v1",
		"api_key":"sk-test",
		"upstream_model":"glm-5.2",
		"timeout_sec":120,
		"max_retries":2,
		"prompt_price_per_1m":0,
		"completion_price_per_1m":0,
		"is_default":true,
		"enabled":true,
		"no_thinking":true
	}]`
	store := newFakeModelStore()
	if err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger()); err != nil {
		t.Fatalf("SeedAdminModels() error = %v", err)
	}
	sp, ok := store.models["glm-5.2"]
	if !ok {
		t.Fatal("expected glm-5.2 to be created")
	}
	if sp.BaseURL != "https://gateway.example.com/v1" {
		t.Errorf("BaseURL = %q, want https://gateway.example.com/v1", sp.BaseURL)
	}
	if sp.UpstreamModel != "glm-5.2" {
		t.Errorf("UpstreamModel = %q, want glm-5.2", sp.UpstreamModel)
	}
	if !sp.Enabled {
		t.Error("seeded model should be enabled")
	}
	if !sp.NoThinking {
		t.Error("seeded model should carry no_thinking=true")
	}
	if store.defaultN != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2", store.defaultN)
	}
}

// 空串 / 空数组 → 无操作不报错。
func TestSeedAdminModels_Empty(t *testing.T) {
	store := newFakeModelStore()
	if err := SeedAdminModels(context.Background(), store, "", noopLogger()); err != nil {
		t.Fatalf("empty string: error = %v", err)
	}
	if err := SeedAdminModels(context.Background(), store, "[]", noopLogger()); err != nil {
		t.Fatalf("empty array: error = %v", err)
	}
	if len(store.models) != 0 {
		t.Errorf("models = %d, want 0", len(store.models))
	}
}

// 幂等：重复播种不报错、不覆盖已有条目。
func TestSeedAdminModels_Idempotent(t *testing.T) {
	jsonStr := `[{"name":"qwen3.6-27b","base_url":"https://gateway.example.com/v1"}]`
	store := newFakeModelStore()
	if err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger()); err != nil {
		t.Fatalf("first seed: error = %v", err)
	}
	if err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger()); err != nil {
		t.Fatalf("second seed: error = %v", err)
	}
	if len(store.models) != 1 {
		t.Errorf("models = %d, want 1 (idempotent)", len(store.models))
	}
}

// 多模型播种：无显式默认时首个为默认；显式默认覆盖。
func TestSeedAdminModels_MultiAndDefaultTransfer(t *testing.T) {
	jsonStr := `[
		{"name":"minimax-m2.7","base_url":"https://gateway.example.com/v1"},
		{"name":"glm-5.2","base_url":"https://gateway.example.com/v1","is_default":true}
	]`
	store := newFakeModelStore()
	if err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger()); err != nil {
		t.Fatalf("SeedAdminModels() error = %v", err)
	}
	if len(store.models) != 2 {
		t.Fatalf("models = %d, want 2", len(store.models))
	}
	if store.defaultN != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2 (explicit is_default wins)", store.defaultN)
	}
}

// 表已存在默认（如旧部署的 deepseek）时，播种仍能按 is_default 转移默认位。
func TestSeedAdminModels_ExistingDefaultTransfer(t *testing.T) {
	store := newFakeModelStore()
	if err := store.CreateModel(context.Background(), ModelSpec{Name: "deepseek", BaseURL: "https://api.deepseek.com", Enabled: true}); err != nil {
		t.Fatalf("preseed deepseek: %v", err)
	}
	jsonStr := `[{"name":"glm-5.2","base_url":"https://gateway.example.com/v1","is_default":true}]`
	if err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger()); err != nil {
		t.Fatalf("SeedAdminModels() error = %v", err)
	}
	if _, ok := store.models["deepseek"]; !ok {
		t.Error("existing deepseek should remain")
	}
	if store.defaultN != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2 (default transferred)", store.defaultN)
	}
}

// 非法 JSON → 返回解析错误。
func TestSeedAdminModels_InvalidJSON(t *testing.T) {
	store := newFakeModelStore()
	err := SeedAdminModels(context.Background(), store, `[{not-json`, noopLogger())
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "解析失败") {
		t.Errorf("error = %v, want mention 解析失败", err)
	}
}

// isUniqueViolation：23505（pgconn.PgError 文本格式）识别为冲突，其它错误不识别。
func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"pg 23505", errors.New(`ERROR: duplicate key value violates unique constraint "models_pkey" (SQLSTATE 23505)`), true},
		{"pg 23505 wrapped", fmt.Errorf("insert: %w", errors.New(`duplicate key ... (SQLSTATE 23505)`)), true},
		{"other pg error", errors.New(`ERROR: relation "models" does not exist (SQLSTATE 42P01)`), false},
		{"app error", ErrModelNotFound, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isUniqueViolation(c.err); got != c.want {
			t.Errorf("%s: isUniqueViolation() = %v, want %v", c.name, got, c.want)
		}
	}
}

// 单条校验失败 → 整批失败并指出条目标号（不静默吞掉）。
func TestSeedAdminModels_ValidateFails(t *testing.T) {
	store := newFakeModelStore()
	jsonStr := `[
		{"name":"good-model","base_url":"https://gateway.example.com/v1"},
		{"name":"bad/name","base_url":"https://gateway.example.com/v1"}
	]`
	err := SeedAdminModels(context.Background(), store, jsonStr, noopLogger())
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "第 2 条") {
		t.Errorf("error = %v, want mention 第 2 条", err)
	}
	if len(store.models) != 0 {
		t.Errorf("models = %d, want 0 (transactional: none partially created)", len(store.models))
	}
}
