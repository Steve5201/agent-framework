package tool

import (
	"encoding/json"
	"fmt"

	"github.com/Steve5201/agent-framework/schema"
)

// jsonSchema 工具参数的 JSON Schema 结构（只取校验所需的子集）。
// LLM 传来的参数必须满足这个 Schema，否则拒绝执行。
type jsonSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]propertyDef `json:"properties"`
	Required   []string               `json:"required"`
}

// propertyDef 单个参数的声明。
type propertyDef struct {
	Type string `json:"type"`
}

// ValidateArgs 按 ToolSchema 校验调用参数。
//
// 校验规则：
//  1. 参数必须是合法 JSON 对象（LLM 偶尔会生成残缺 JSON）；
//  2. 必填参数（ToolSchema.Required 与 Schema 内声明的 required）
//     必须全部存在；
//  3. 已提供的参数，类型必须与 properties 中声明的一致。
//
// 宽松策略：Schema 中未声明的参数不做类型检查（LLM 可能多传字段，
// 过于严格会导致调用失败率上升）。
func ValidateArgs(ts schema.ToolSchema, args json.RawMessage) error {
	// 空参数处理：无必填项则允许空参数。
	if len(args) == 0 {
		if len(ts.Required) > 0 {
			return fmt.Errorf("tool: 工具 %q 缺少必填参数 %v", ts.Name, ts.Required)
		}
		return nil
	}

	// 1. 必须是 JSON 对象
	var argMap map[string]any
	if err := json.Unmarshal(args, &argMap); err != nil {
		return fmt.Errorf("tool: 工具 %q 参数不是合法 JSON: %w", ts.Name, err)
	}

	// 2. 解析工具声明的 Schema，拿到属性类型与必填列表
	propTypes := map[string]string{}
	var schemaRequired []string
	if len(ts.Parameters) > 0 {
		var s jsonSchema
		if err := json.Unmarshal(ts.Parameters, &s); err != nil {
			return fmt.Errorf("tool: 工具 %q 的参数 Schema 非法: %w", ts.Name, err)
		}
		for name, def := range s.Properties {
			propTypes[name] = def.Type
		}
		schemaRequired = s.Required
	}

	// 3. 必填校验（合并两处声明，避免遗漏）
	required := append([]string{}, ts.Required...)
	required = append(required, schemaRequired...)
	for _, name := range required {
		if _, ok := argMap[name]; !ok {
			return fmt.Errorf("tool: 工具 %q 缺少必填参数 %q", ts.Name, name)
		}
	}

	// 4. 类型校验
	for name, val := range argMap {
		wantType, ok := propTypes[name]
		if !ok {
			continue // 未声明的参数，宽松放行
		}
		if !jsonValueMatchesType(val, wantType) {
			return fmt.Errorf("tool: 工具 %q 参数 %q 类型应为 %s", ts.Name, name, wantType)
		}
	}
	return nil
}

// jsonValueMatchesType 判断 JSON 值是否匹配 JSON Schema 类型。
// 注意：Go 中 JSON 数字一律反序列化为 float64，因此 integer 需额外
// 判断是否为整数。
func jsonValueMatchesType(v any, want string) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	default:
		return true // 未知类型不做校验，交给工具自身
	}
}
