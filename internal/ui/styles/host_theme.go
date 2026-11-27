package styles

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

// SyntaxField describes code highlighting 令牌 whose foreground color
// host theme can set. Name 是 token's key 在 host theme's syntax map;
// Style returns token's style 在 chroma 配置.
type SyntaxField struct {
	Name  string
	Style func(*ansi.Chroma) *ansi.StylePrimitive
}

// SyntaxFields returns ordered list 的 code highlighting tokens host
// theme can color. 这 是 single source 的 truth 为 syntax keys.
// They 是 snake_case names 的 [ansi.Chroma] fields 那 carry
// foreground color; 错误 ( foreground 在 background) 和 Background 是
// left 到 palette.
//
// Every chroma 用户 在 Crush (markdown code blocks, 文件 views, diffs)
// derives 它的 colors 从 Markdown.CodeBlock.Chroma, 所以 token colored here
// 是 colored everywhere.
func SyntaxFields() []SyntaxField {
	return []SyntaxField{
		{"text", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Text }},
		{"comment", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Comment }},
		{"comment_preproc", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.CommentPreproc }},
		{"keyword", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Keyword }},
		{"keyword_reserved", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.KeywordReserved }},
		{"keyword_namespace", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.KeywordNamespace }},
		{"keyword_type", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.KeywordType }},
		{"operator", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Operator }},
		{"punctuation", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Punctuation }},
		{"name", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Name }},
		{"name_builtin", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameBuiltin }},
		{"name_tag", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameTag }},
		{"name_attribute", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameAttribute }},
		{"name_class", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameClass }},
		{"name_constant", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameConstant }},
		{"name_decorator", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameDecorator }},
		{"name_exception", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameException }},
		{"name_function", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameFunction }},
		{"name_other", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.NameOther }},
		{"literal", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.Literal }},
		{"literal_number", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.LiteralNumber }},
		{"literal_date", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.LiteralDate }},
		{"literal_string", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.LiteralString }},
		{"literal_string_escape", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.LiteralStringEscape }},
		{"generic_deleted", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.GenericDeleted }},
		{"generic_emph", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.GenericEmph }},
		{"generic_inserted", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.GenericInserted }},
		{"generic_strong", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.GenericStrong }},
		{"generic_subheading", func(c *ansi.Chroma) *ansi.StylePrimitive { return &c.GenericSubheading }},
	}
}

// validHostHexColor matches "#rgb" 和 "#rrggbb", hex forms lipgloss
// 可以 render.
var validHostHexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// isValidHostColor reports whether s 是 color host theme may use. 它 是
// stricter than [IsValidColor]: "#rgba" 和 "#rrggbbaa" 是 rejected,
// because lipgloss renders them 作为 no color 在 all 和 host has no way 到
// notice.
func isValidHostColor(s string) bool {
	if strings.HasPrefix(s, "#") {
		return validHostHexColor.MatchString(s)
	}
	return IsValidColor(s)
}

// hostSyntaxColor 是 one syntax override 的 host theme.
type hostSyntaxColor struct {
	field SyntaxField
	color string
}

// parseHostTheme converts host theme's palette 和 syntax maps. Unknown
// keys 是 returned 作为 ignored ("palette.<key>" 或 "syntax.<key>"), empty
// values 是 treated 作为 unset, 和 any invalid color fails whole theme.
func parseHostTheme(palette, syntax map[string]string) (Palette, []hostSyntaxColor, []string, error) {
	var (
		p       Palette
		colors  []hostSyntaxColor
		ignored []string
		errs    []string
	)

	paletteFields := map[string]PaletteField{}
	for _, f := range PaletteFields() {
		paletteFields[f.Name] = f
	}
	for _, k := range slices.Sorted(maps.Keys(palette)) {
		f, ok := paletteFields[k]
		if !ok {
			ignored = append(ignored, "palette."+k)
			continue
		}
		v := palette[k]
		if v == "" {
			continue
		}
		if !isValidHostColor(v) {
			errs = append(errs, fmt.Sprintf("palette.%s: invalid color %q", k, v))
			continue
		}
		f.Set(&p, v)
	}

	syntaxFields := map[string]SyntaxField{}
	for _, f := range SyntaxFields() {
		syntaxFields[f.Name] = f
	}
	for _, k := range slices.Sorted(maps.Keys(syntax)) {
		f, ok := syntaxFields[k]
		if !ok {
			ignored = append(ignored, "syntax."+k)
			continue
		}
		v := syntax[k]
		if v == "" {
			continue
		}
		if !isValidHostColor(v) {
			errs = append(errs, fmt.Sprintf("syntax.%s: invalid color %q", k, v))
			continue
		}
		colors = append(colors, hostSyntaxColor{field: f, color: v})
	}

	if len(errs) > 0 {
		return Palette{}, nil, ignored, fmt.Errorf("invalid theme colors: %s", strings.Join(errs, "; "))
	}
	return p, colors, ignored, nil
}

// hostThemeBase resolves theme host theme builds 在 和 returns 它的
// colors 和 它的 root built-在. An empty name 是 built-在
// charmtone-panther, resolved without touching disk ( 用户 文件 那
// overrides charmtone-panther 是 不 consulted). Any other name resolves
// way Crush resolves theme names everywhere: 用户 theme 文件 wins over
// built-在 的 same name.
func hostThemeBase(name string) (quickStyleOpts, string, error) {
	if name == "" {
		const root = "charmtone-panther"
		opts, err := builtinThemeOpts(root)
		return opts, root, err
	}
	base, root, err := resolveThemePalette(name, map[string]bool{})
	if err != nil {
		return quickStyleOpts{}, "", err
	}
	rootOpts, err := builtinThemeOpts(root)
	if err != nil {
		return quickStyleOpts{}, "", err
	}
	return base.ToQuickStyleOpts(rootOpts), root, nil
}

// ValidateHostTheme checks theme supplied 通过 host application (see
// [HostThemeStyles]). 它 returns keys it ignored because it does 不
// know them, 所以 host written 为 newer Crush keeps working, 和 错误
// naming every invalid color, 或 saying base theme cannot 是 resolved.
func ValidateHostTheme(base string, palette, syntax map[string]string) ([]string, error) {
	_, _, ignored, err := parseHostTheme(palette, syntax)
	if err != nil {
		return ignored, err
	}
	if base != "" {
		if _, _, err := resolveThemePalette(base, map[string]bool{}); err != nil {
			return ignored, fmt.Errorf("base theme: %w", err)
		}
	}
	return ignored, nil
}

// HostThemeStyles builds Styles 从 theme supplied 通过 host application
// 那 runs Crush 在 one 的 它的 panes. palette maps theme-文件 keys (see
// [PaletteFields]) 到 colors 和 syntax maps code highlighting keys (see
// [SyntaxFields]) 到 foreground colors; unknown keys 是 ignored 和 colors
// left out come 从 base (empty means charmtone-panther, see
// [hostThemeBase]). Colors 是 "#rgb", "#rrggbb", ANSI index (0-255) 或
// a CharmTone name.
//
// base theme's hardcoded overrides 是 kept 只 当 resulting
// palette 是 exactly base built-在's, 所以 host 那 mirrors built-在
// gets 那 built-在's look rather than approximation 的 it. Any other
// palette gets legible badge 文本 instead (see [legibleBadges]).
func HostThemeStyles(base string, palette, syntax map[string]string) (Styles, error) {
	p, colors, _, err := parseHostTheme(palette, syntax)
	if err != nil {
		return Styles{}, err
	}
	baseOpts, root, err := hostThemeBase(base)
	if err != nil {
		return Styles{}, fmt.Errorf("base theme: %w", err)
	}

	opts := p.ToQuickStyleOpts(baseOpts)
	s := quickStyle(opts)
	if !isBuiltinPalette(opts, root) {
		s = legibleBadges(s, opts)
	} else if override, ok := builtinThemeOverrides[root]; ok {
		s = override(s)
	}
	if chroma := s.Markdown.CodeBlock.Chroma; chroma != nil {
		// PlanMarkdown shares 这 配置, 所以 它的 code blocks follow too.
		for _, c := range colors {
			c.field.Style(chroma).Color = hex(lipgloss.Color(ColorString(c.color)))
		}
	}
	return s, nil
}

// isBuiltinPalette reports whether opts resolves 到 exactly colors 的
// named built-在 theme.
func isBuiltinPalette(opts quickStyleOpts, name string) bool {
	ref, err := builtinThemeOpts(name)
	if err != nil {
		return false
	}
	ref.deriveDiffColors()
	opts.deriveDiffColors()
	return PaletteFromOpts(opts) == PaletteFromOpts(ref)
}
