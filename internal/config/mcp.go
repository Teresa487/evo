package config

import "fmt"

// SetMCPServerDisabledConfig persists disabled flag 的 single MCP
// 服务端 在 given scope's 配置 文件. 它 does 不 touch running
// 客户端; callers coordinate runtime state separately. 它 fails 当
// 服务端 是 不 configured, 所以 typo can never 创建 empty
// 配置 条目 carrying 只 disabled flag.
func (s *ConfigStore) SetMCPServerDisabledConfig(scope Scope, name string, disabled bool) error {
	if _, ok := s.Config().MCP[name]; !ok {
		return fmt.Errorf("mcp %q is not configured", name)
	}
	return s.update(scope, func(c *Config) map[string]any {
		m := c.MCP[name]
		m.Disabled = disabled
		c.MCP[name] = m
		return map[string]any{"mcp." + name + ".disabled": disabled}
	})
}
