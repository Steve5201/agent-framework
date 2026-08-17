package adminsvc

import (
	"net/http"
)

// PlaceholderModule 占位模块：仅声明模块元信息（前端渲染"规划中"状态），
// 不注册任何业务路由。后续实现时替换为真实模块（实现 Module 的 Register），
// 或在现有模块文件里补齐路由——占位 → 实现只增不改，不影响其它模块。
type PlaceholderModule struct {
	key         string
	name        string
	description string
}

// Placeholder 创建占位模块。
func Placeholder(key, name, description string) Module {
	return PlaceholderModule{key: key, name: name, description: description}
}

func (m PlaceholderModule) Key() string         { return m.key }
func (m PlaceholderModule) Name() string        { return m.name }
func (m PlaceholderModule) Description() string { return m.description }
func (m PlaceholderModule) Implemented() bool   { return false }

// Register 占位模块无路由。
func (m PlaceholderModule) Register(_ *http.ServeMux, _ *Service) {}
