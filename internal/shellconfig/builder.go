package shellconfig

import (
	"context"
	"encoding/json"
	"fmt"
)

// ConfigBuilder accumulates 配置 state 作为 配置 builtins execute during
// evorc load. Builtins mutate nested map directly, 在 execution order,
// 所以 imperative operations (append, set, remove, reset) resolve exactly 作为
// script intends. builder 是 存储 在 shell 上下文 所以 builtins can
// reach it; it 是 absent during normal bash 工具 execution, which makes
// 配置 builtins no-ops there.
//
// At end 的 script builder marshals 到 single JSON object, which
// 配置 loader merges 用 any other 配置 files.
type ConfigBuilder struct {
	root map[string]any
}

// newConfigBuilder returns an empty builder.
func newConfigBuilder() *ConfigBuilder {
	return &ConfigBuilder{root: make(map[string]any)}
}

// section returns top-level object 存储 在 key, creating it 如果 absent.
// Panics 如果 existing 值 是 non-nil 和 不 map: 那 invariant
// violation indicates bug 在 builtin 处理器, 不 用户 错误.
func (b *ConfigBuilder) section(key string) map[string]any {
	m, ok := b.root[key].(map[string]any)
	if !ok {
		if b.root[key] != nil {
			panic(fmt.Sprintf("shellconfig: section(%q) overwrites non-map value %T", key, b.root[key]))
		}
		m = make(map[string]any)
		b.root[key] = m
	}
	return m
}

// empty reports whether builder holds no 配置.
func (b *ConfigBuilder) empty() bool {
	return len(b.root) == 0
}

// setEnv records a variable the script exported under the top-level env
// object, which loader expands like any other template 字段.
func (b *ConfigBuilder) setEnv(name, value string) {
	b.section("env")[name] = templateEscaper.Replace(value)
}

// JSON marshals accumulated 配置 到 single JSON object.
func (b *ConfigBuilder) JSON() ([]byte, error) {
	return json.Marshal(b.root)
}

// childMap returns/creates the nested object parent[key].
// Panics 如果 existing 值 是 non-nil 和 不 map, same invariant 作为
// section().
func childMap(parent map[string]any, key string) map[string]any {
	m, ok := parent[key].(map[string]any)
	if !ok {
		if parent[key] != nil {
			panic(fmt.Sprintf("shellconfig: childMap(%q) overwrites non-map value %T", key, parent[key]))
		}
		m = make(map[string]any)
		parent[key] = m
	}
	return m
}

type configBuilderCtxKey struct{}

// configBuilderFromCtx returns ConfigBuilder 在 上下文, 或 nil 如果
// none 是 present (normal bash 工具 execution).
func configBuilderFromCtx(ctx context.Context) *ConfigBuilder {
	v, _ := ctx.Value(configBuilderCtxKey{}).(*ConfigBuilder)
	return v
}

// withConfigBuilder returns 上下文 carrying given ConfigBuilder.
func withConfigBuilder(ctx context.Context, b *ConfigBuilder) context.Context {
	return context.WithValue(ctx, configBuilderCtxKey{}, b)
}
