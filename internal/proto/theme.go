// theme.go —— 协议里的主题类型（宿主主题同步）。

package proto

import "maps"

// HostTheme 是 theme supplied 通过 host application 那 runs Crush 在
// one 的 它的 panes ( editor, 为 example). 它 是 written 在 Crush's own
// vocabulary, 所以 any host can send one: Palette keys 是 theme-文件 keys
// (see styles.PaletteFields) 和 Syntax keys name code highlighting tokens
// (see styles.SyntaxFields). Colors host leaves out come 从 Base.
// Unknown keys 是 ignored, 所以 host can target newer Crush versions.
//
// 一个 host theme 是 never persisted: it lives 在 服务端's memory 为 作为
// long 作为 工作区 does, 或 until host clears it.
type HostTheme struct {
	// Name 是 host's name 为 theme. 它 是 informational 只.
	Name string `json:"name,omitempty"`
	// Source names host application. 它 是 informational 只.
	Source string `json:"source,omitempty"`
	// Base 是 Crush built-在 或 用户 theme 那 supplies every color
	// host leaves out. Empty means built-在 charmtone-panther.
	Base string `json:"base,omitempty"`
	// Palette maps theme-文件 keys (such 作为 "primary" 或 "bg_base") 到
	// colors: "#rgb", "#rrggbb", ANSI index (0-255) 或 CharmTone name.
	Palette map[string]string `json:"palette,omitempty"`
	// Syntax maps code highlighting token keys (such 作为 "keyword" 或
	// "literal_string") 到 foreground colors, 在 same formats.
	Syntax map[string]string `json:"syntax,omitempty"`
	// Transparent, 当 set, overrides Crush's own options.tui.transparent
	// while host theme 是 applied. Nil keeps Crush's setting.
	Transparent *bool `json:"transparent,omitempty"`
}

// Equal reports whether t 和 o describe same theme. Two nil themes
// 是 equal, 和 nil map equals empty one.
func (t *HostTheme) Equal(o *HostTheme) bool {
	if t == nil || o == nil {
		return t == o
	}
	if (t.Transparent == nil) != (o.Transparent == nil) ||
		(t.Transparent != nil && *t.Transparent != *o.Transparent) {
		return false
	}
	return t.Name == o.Name &&
		t.Source == o.Source &&
		t.Base == o.Base &&
		maps.Equal(t.Palette, o.Palette) &&
		maps.Equal(t.Syntax, o.Syntax)
}

// Clone returns deep copy 的 t, 或 nil 当 t 是 nil.
func (t *HostTheme) Clone() *HostTheme {
	if t == nil {
		return nil
	}
	c := *t
	c.Palette = maps.Clone(t.Palette)
	c.Syntax = maps.Clone(t.Syntax)
	if t.Transparent != nil {
		c.Transparent = new(*t.Transparent)
	}
	return &c
}

// HostThemeRequest 是 body 的 POST /v1/workspaces/{id}/theme. 一个 nil
// Theme clears 主机 theme.
type HostThemeRequest struct {
	Theme *HostTheme `json:"theme"`
}

// HostThemeState 是 工作区's host theme. 它 是 响应 的
// theme endpoints 和 payload 的 host_theme events ( updated 事件
// 当 theme 是 set, deleted one 当 it 是 cleared). 一个 nil Theme
// means no host theme 是 set.
type HostThemeState struct {
	WorkspaceID string     `json:"workspace_id"`
	Theme       *HostTheme `json:"theme"`
}
