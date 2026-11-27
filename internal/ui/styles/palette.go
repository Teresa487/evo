package styles

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/tone"
)

// validHexColor matches "#rgb", "#rrggbb", "#rgba", 或 "#rrggbbaa".
var validHexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// validANSIColor matches ANSI color indices 0-255.
var validANSIColor = regexp.MustCompile(`^([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`)

// charmtoneByName maps lowercase charmtone color names 到 their hex values.
// Built once 在 init time 从 charmtone package.
var charmtoneByName map[string]string

func init() {
	charmtoneByName = make(map[string]string, len(tone.Keys()))
	for _, k := range tone.Keys() {
		charmtoneByName[strings.ToLower(k.String())] = k.Hex()
	}
	// Include aliases.
	charmtoneByName["ash"] = tone.Sash.Hex()
	charmtoneByName["charcoal"] = tone.Char.Hex()
}

// PaletteField describes single customizable color token 在 Palette.
// 它 pairs JSON 字段 name 用 getter/setter accessors 所以 那
// validation, serialization checks, 和 UI editors can all iterate
// over same ordered list without duplicating 字段 names.
type PaletteField struct {
	Name string
	Get  func(Palette) string
	Set  func(*Palette, string)
}

// PaletteFields returns ordered list 的 all customizable color
// tokens 在 Palette. 这 是 single source 的 truth 为 字段
// names, ordering, 和 access; Validate, knownThemeFields, 和
// theme editor all derive 从 it.
func PaletteFields() []PaletteField {
	return []PaletteField{
		{"primary", func(p Palette) string { return p.Primary }, func(p *Palette, v string) { p.Primary = v }},
		{"secondary", func(p Palette) string { return p.Secondary }, func(p *Palette, v string) { p.Secondary = v }},
		{"accent", func(p Palette) string { return p.Accent }, func(p *Palette, v string) { p.Accent = v }},
		{"keyword", func(p Palette) string { return p.Keyword }, func(p *Palette, v string) { p.Keyword = v }},
		{"fg_base", func(p Palette) string { return p.FgBase }, func(p *Palette, v string) { p.FgBase = v }},
		{"fg_subtle", func(p Palette) string { return p.FgSubtle }, func(p *Palette, v string) { p.FgSubtle = v }},
		{"fg_more_subtle", func(p Palette) string { return p.FgMoreSubtle }, func(p *Palette, v string) { p.FgMoreSubtle = v }},
		{"fg_most_subtle", func(p Palette) string { return p.FgMostSubtle }, func(p *Palette, v string) { p.FgMostSubtle = v }},
		{"bg_base", func(p Palette) string { return p.BgBase }, func(p *Palette, v string) { p.BgBase = v }},
		{"bg_most_visible", func(p Palette) string { return p.BgMostVisible }, func(p *Palette, v string) { p.BgMostVisible = v }},
		{"bg_less_visible", func(p Palette) string { return p.BgLessVisible }, func(p *Palette, v string) { p.BgLessVisible = v }},
		{"bg_least_visible", func(p Palette) string { return p.BgLeastVisible }, func(p *Palette, v string) { p.BgLeastVisible = v }},
		{"on_primary", func(p Palette) string { return p.OnPrimary }, func(p *Palette, v string) { p.OnPrimary = v }},
		{"separator", func(p Palette) string { return p.Separator }, func(p *Palette, v string) { p.Separator = v }},
		{"destructive", func(p Palette) string { return p.Destructive }, func(p *Palette, v string) { p.Destructive = v }},
		{"destructive_subtle", func(p Palette) string { return p.DestructiveSubtle }, func(p *Palette, v string) { p.DestructiveSubtle = v }},
		{"error", func(p Palette) string { return p.Error }, func(p *Palette, v string) { p.Error = v }},
		{"warning", func(p Palette) string { return p.Warning }, func(p *Palette, v string) { p.Warning = v }},
		{"warning_subtle", func(p Palette) string { return p.WarningSubtle }, func(p *Palette, v string) { p.WarningSubtle = v }},
		{"attention", func(p Palette) string { return p.Attention }, func(p *Palette, v string) { p.Attention = v }},
		{"busy", func(p Palette) string { return p.Busy }, func(p *Palette, v string) { p.Busy = v }},
		{"info", func(p Palette) string { return p.Info }, func(p *Palette, v string) { p.Info = v }},
		{"info_more_subtle", func(p Palette) string { return p.InfoMoreSubtle }, func(p *Palette, v string) { p.InfoMoreSubtle = v }},
		{"info_most_subtle", func(p Palette) string { return p.InfoMostSubtle }, func(p *Palette, v string) { p.InfoMostSubtle = v }},
		{"success", func(p Palette) string { return p.Success }, func(p *Palette, v string) { p.Success = v }},
		{"success_more_subtle", func(p Palette) string { return p.SuccessMoreSubtle }, func(p *Palette, v string) { p.SuccessMoreSubtle = v }},
		{"success_most_subtle", func(p Palette) string { return p.SuccessMostSubtle }, func(p *Palette, v string) { p.SuccessMostSubtle = v }},
		{"diff_insert_fg", func(p Palette) string { return p.DiffInsertFg }, func(p *Palette, v string) { p.DiffInsertFg = v }},
		{"diff_insert_code_bg", func(p Palette) string { return p.DiffInsertCodeBg }, func(p *Palette, v string) { p.DiffInsertCodeBg = v }},
		{"diff_insert_gutter_bg", func(p Palette) string { return p.DiffInsertGutterBg }, func(p *Palette, v string) { p.DiffInsertGutterBg = v }},
		{"diff_delete_fg", func(p Palette) string { return p.DiffDeleteFg }, func(p *Palette, v string) { p.DiffDeleteFg = v }},
		{"diff_delete_code_bg", func(p Palette) string { return p.DiffDeleteCodeBg }, func(p *Palette, v string) { p.DiffDeleteCodeBg = v }},
		{"diff_delete_gutter_bg", func(p Palette) string { return p.DiffDeleteGutterBg }, func(p *Palette, v string) { p.DiffDeleteGutterBg = v }},
		{"yolo", func(p Palette) string { return p.Yolo }, func(p *Palette, v string) { p.Yolo = v }},
		{"plan", func(p Palette) string { return p.Plan }, func(p *Palette, v string) { p.Plan = v }},
		{"plan_more_subtle", func(p Palette) string { return p.PlanMoreSubtle }, func(p *Palette, v string) { p.PlanMoreSubtle = v }},
		{"button", func(p Palette) string { return p.Button }, func(p *Palette, v string) { p.Button = v }},
		{"button_subtle", func(p Palette) string { return p.ButtonSubtle }, func(p *Palette, v string) { p.ButtonSubtle = v }},
		{"button_inactive", func(p Palette) string { return p.ButtonInactive }, func(p *Palette, v string) { p.ButtonInactive = v }},
		{"button_hovered", func(p Palette) string { return p.ButtonHovered }, func(p *Palette, v string) { p.ButtonHovered = v }},
		{"ansi_black", func(p Palette) string { return p.AnsiBlack }, func(p *Palette, v string) { p.AnsiBlack = v }},
		{"ansi_red", func(p Palette) string { return p.AnsiRed }, func(p *Palette, v string) { p.AnsiRed = v }},
		{"ansi_green", func(p Palette) string { return p.AnsiGreen }, func(p *Palette, v string) { p.AnsiGreen = v }},
		{"ansi_yellow", func(p Palette) string { return p.AnsiYellow }, func(p *Palette, v string) { p.AnsiYellow = v }},
		{"ansi_blue", func(p Palette) string { return p.AnsiBlue }, func(p *Palette, v string) { p.AnsiBlue = v }},
		{"ansi_magenta", func(p Palette) string { return p.AnsiMagenta }, func(p *Palette, v string) { p.AnsiMagenta = v }},
		{"ansi_cyan", func(p Palette) string { return p.AnsiCyan }, func(p *Palette, v string) { p.AnsiCyan = v }},
		{"ansi_white", func(p Palette) string { return p.AnsiWhite }, func(p *Palette, v string) { p.AnsiWhite = v }},
		{"ansi_bright_black", func(p Palette) string { return p.AnsiBrightBlack }, func(p *Palette, v string) { p.AnsiBrightBlack = v }},
		{"ansi_bright_red", func(p Palette) string { return p.AnsiBrightRed }, func(p *Palette, v string) { p.AnsiBrightRed = v }},
		{"ansi_bright_green", func(p Palette) string { return p.AnsiBrightGreen }, func(p *Palette, v string) { p.AnsiBrightGreen = v }},
		{"ansi_bright_yellow", func(p Palette) string { return p.AnsiBrightYellow }, func(p *Palette, v string) { p.AnsiBrightYellow = v }},
		{"ansi_bright_blue", func(p Palette) string { return p.AnsiBrightBlue }, func(p *Palette, v string) { p.AnsiBrightBlue = v }},
		{"ansi_bright_magenta", func(p Palette) string { return p.AnsiBrightMagenta }, func(p *Palette, v string) { p.AnsiBrightMagenta = v }},
		{"ansi_bright_cyan", func(p Palette) string { return p.AnsiBrightCyan }, func(p *Palette, v string) { p.AnsiBrightCyan = v }},
		{"ansi_bright_white", func(p Palette) string { return p.AnsiBrightWhite }, func(p *Palette, v string) { p.AnsiBrightWhite = v }},
	}
}

// Palette 是 JSON-serializable theme palette. Each 字段 maps 到
// quickStyleOpts color, 存储 作为 hex string ("#rrggbb"). Empty
// strings mean "inherit 从 base" 当 merging.
type Palette struct {
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	Accent    string `json:"accent,omitempty"`
	Keyword   string `json:"keyword,omitempty"`

	FgBase       string `json:"fg_base,omitempty"`
	FgSubtle     string `json:"fg_subtle,omitempty"`
	FgMoreSubtle string `json:"fg_more_subtle,omitempty"`
	FgMostSubtle string `json:"fg_most_subtle,omitempty"`

	BgBase         string `json:"bg_base,omitempty"`
	BgMostVisible  string `json:"bg_most_visible,omitempty"`
	BgLessVisible  string `json:"bg_less_visible,omitempty"`
	BgLeastVisible string `json:"bg_least_visible,omitempty"`

	OnPrimary string `json:"on_primary,omitempty"`
	Separator string `json:"separator,omitempty"`

	Destructive       string `json:"destructive,omitempty"`
	DestructiveSubtle string `json:"destructive_subtle,omitempty"`
	Error             string `json:"error,omitempty"`
	Warning           string `json:"warning,omitempty"`
	WarningSubtle     string `json:"warning_subtle,omitempty"`
	Attention         string `json:"attention,omitempty"`
	Busy              string `json:"busy,omitempty"`
	Info              string `json:"info,omitempty"`
	InfoMoreSubtle    string `json:"info_more_subtle,omitempty"`
	InfoMostSubtle    string `json:"info_most_subtle,omitempty"`
	Success           string `json:"success,omitempty"`
	SuccessMoreSubtle string `json:"success_more_subtle,omitempty"`
	SuccessMostSubtle string `json:"success_most_subtle,omitempty"`

	DiffInsertFg       string `json:"diff_insert_fg,omitempty"`
	DiffInsertCodeBg   string `json:"diff_insert_code_bg,omitempty"`
	DiffInsertGutterBg string `json:"diff_insert_gutter_bg,omitempty"`
	DiffDeleteFg       string `json:"diff_delete_fg,omitempty"`
	DiffDeleteCodeBg   string `json:"diff_delete_code_bg,omitempty"`
	DiffDeleteGutterBg string `json:"diff_delete_gutter_bg,omitempty"`

	Yolo           string `json:"yolo,omitempty"`
	Plan           string `json:"plan,omitempty"`
	PlanMoreSubtle string `json:"plan_more_subtle,omitempty"`

	Button         string `json:"button,omitempty"`
	ButtonSubtle   string `json:"button_subtle,omitempty"`
	ButtonInactive string `json:"button_inactive,omitempty"`
	ButtonHovered  string `json:"button_hovered,omitempty"`

	AnsiBlack         string `json:"ansi_black,omitempty"`
	AnsiRed           string `json:"ansi_red,omitempty"`
	AnsiGreen         string `json:"ansi_green,omitempty"`
	AnsiYellow        string `json:"ansi_yellow,omitempty"`
	AnsiBlue          string `json:"ansi_blue,omitempty"`
	AnsiMagenta       string `json:"ansi_magenta,omitempty"`
	AnsiCyan          string `json:"ansi_cyan,omitempty"`
	AnsiWhite         string `json:"ansi_white,omitempty"`
	AnsiBrightBlack   string `json:"ansi_bright_black,omitempty"`
	AnsiBrightRed     string `json:"ansi_bright_red,omitempty"`
	AnsiBrightGreen   string `json:"ansi_bright_green,omitempty"`
	AnsiBrightYellow  string `json:"ansi_bright_yellow,omitempty"`
	AnsiBrightBlue    string `json:"ansi_bright_blue,omitempty"`
	AnsiBrightMagenta string `json:"ansi_bright_magenta,omitempty"`
	AnsiBrightCyan    string `json:"ansi_bright_cyan,omitempty"`
	AnsiBrightWhite   string `json:"ansi_bright_white,omitempty"`
}

// PaletteFromOpts extracts Palette 从 quickStyleOpts, converting
// each color.Color 到 它的 "#rrggbb" hex representation. Diff tokens
// left unset stay empty 所以 overrides can flow through merge chain;
// derivation happens once, after all overrides 是 applied (see
// deriveDiffColors).
func PaletteFromOpts(o quickStyleOpts) Palette {
	return Palette{
		Primary:   colorToHex(o.primary),
		Secondary: colorToHex(o.secondary),
		Accent:    colorToHex(o.accent),
		Keyword:   colorToHex(o.keyword),

		FgBase:       colorToHex(o.fgBase),
		FgSubtle:     colorToHex(o.fgSubtle),
		FgMoreSubtle: colorToHex(o.fgMoreSubtle),
		FgMostSubtle: colorToHex(o.fgMostSubtle),

		BgBase:         colorToHex(o.bgBase),
		BgMostVisible:  colorToHex(o.bgMostVisible),
		BgLessVisible:  colorToHex(o.bgLessVisible),
		BgLeastVisible: colorToHex(o.bgLeastVisible),

		OnPrimary: colorToHex(o.onPrimary),
		Separator: colorToHex(o.separator),

		Destructive:       colorToHex(o.destructive),
		DestructiveSubtle: colorToHex(o.destructiveSubtle),
		Error:             colorToHex(o.error),
		Warning:           colorToHex(o.warning),
		WarningSubtle:     colorToHex(o.warningSubtle),
		Attention:         colorToHex(o.attention),
		Busy:              colorToHex(o.busy),
		Info:              colorToHex(o.info),
		InfoMoreSubtle:    colorToHex(o.infoMoreSubtle),
		InfoMostSubtle:    colorToHex(o.infoMostSubtle),
		Success:           colorToHex(o.success),
		SuccessMoreSubtle: colorToHex(o.successMoreSubtle),
		SuccessMostSubtle: colorToHex(o.successMostSubtle),

		DiffInsertFg:       colorToHex(o.diffInsertFg),
		DiffInsertCodeBg:   colorToHex(o.diffInsertCodeBg),
		DiffInsertGutterBg: colorToHex(o.diffInsertGutterBg),
		DiffDeleteFg:       colorToHex(o.diffDeleteFg),
		DiffDeleteCodeBg:   colorToHex(o.diffDeleteCodeBg),
		DiffDeleteGutterBg: colorToHex(o.diffDeleteGutterBg),

		Yolo:           colorToHex(o.yolo),
		Plan:           colorToHex(o.plan),
		PlanMoreSubtle: colorToHex(o.planMoreSubtle),

		Button:         colorToHex(o.button),
		ButtonSubtle:   colorToHex(o.buttonSubtle),
		ButtonInactive: colorToHex(o.buttonInactive),
		ButtonHovered:  colorToHex(o.buttonHovered),

		AnsiBlack:         colorToHex(o.ansiBlack),
		AnsiRed:           colorToHex(o.ansiRed),
		AnsiGreen:         colorToHex(o.ansiGreen),
		AnsiYellow:        colorToHex(o.ansiYellow),
		AnsiBlue:          colorToHex(o.ansiBlue),
		AnsiMagenta:       colorToHex(o.ansiMagenta),
		AnsiCyan:          colorToHex(o.ansiCyan),
		AnsiWhite:         colorToHex(o.ansiWhite),
		AnsiBrightBlack:   colorToHex(o.ansiBrightBlack),
		AnsiBrightRed:     colorToHex(o.ansiBrightRed),
		AnsiBrightGreen:   colorToHex(o.ansiBrightGreen),
		AnsiBrightYellow:  colorToHex(o.ansiBrightYellow),
		AnsiBrightBlue:    colorToHex(o.ansiBrightBlue),
		AnsiBrightMagenta: colorToHex(o.ansiBrightMagenta),
		AnsiBrightCyan:    colorToHex(o.ansiBrightCyan),
		AnsiBrightWhite:   colorToHex(o.ansiBrightWhite),
	}
}

// ToQuickStyleOpts converts Palette back 到 quickStyleOpts. Non-empty
// fields 是 parsed 作为 hex colors; empty fields fall back 到
// provided base palette.
func (p Palette) ToQuickStyleOpts(base quickStyleOpts) quickStyleOpts {
	return quickStyleOpts{
		primary:   resolveColor(p.Primary, base.primary),
		secondary: resolveColor(p.Secondary, base.secondary),
		accent:    resolveColor(p.Accent, base.accent),
		keyword:   resolveColor(p.Keyword, base.keyword),

		fgBase:       resolveColor(p.FgBase, base.fgBase),
		fgSubtle:     resolveColor(p.FgSubtle, base.fgSubtle),
		fgMoreSubtle: resolveColor(p.FgMoreSubtle, base.fgMoreSubtle),
		fgMostSubtle: resolveColor(p.FgMostSubtle, base.fgMostSubtle),

		bgBase:         resolveColor(p.BgBase, base.bgBase),
		bgMostVisible:  resolveColor(p.BgMostVisible, base.bgMostVisible),
		bgLessVisible:  resolveColor(p.BgLessVisible, base.bgLessVisible),
		bgLeastVisible: resolveColor(p.BgLeastVisible, base.bgLeastVisible),

		onPrimary: resolveColor(p.OnPrimary, base.onPrimary),
		separator: resolveColor(p.Separator, base.separator),

		destructive:       resolveColor(p.Destructive, base.destructive),
		destructiveSubtle: resolveColor(p.DestructiveSubtle, base.destructiveSubtle),
		error:             resolveColor(p.Error, base.error),
		warning:           resolveColor(p.Warning, base.warning),
		warningSubtle:     resolveColor(p.WarningSubtle, base.warningSubtle),
		attention:         resolveColor(p.Attention, base.attention),
		busy:              resolveColor(p.Busy, base.busy),
		info:              resolveColor(p.Info, base.info),
		infoMoreSubtle:    resolveColor(p.InfoMoreSubtle, base.infoMoreSubtle),
		infoMostSubtle:    resolveColor(p.InfoMostSubtle, base.infoMostSubtle),
		success:           resolveColor(p.Success, base.success),
		successMoreSubtle: resolveColor(p.SuccessMoreSubtle, base.successMoreSubtle),
		successMostSubtle: resolveColor(p.SuccessMostSubtle, base.successMostSubtle),

		diffInsertFg:       resolveColor(p.DiffInsertFg, base.diffInsertFg),
		diffInsertCodeBg:   resolveColor(p.DiffInsertCodeBg, base.diffInsertCodeBg),
		diffInsertGutterBg: resolveColor(p.DiffInsertGutterBg, base.diffInsertGutterBg),
		diffDeleteFg:       resolveColor(p.DiffDeleteFg, base.diffDeleteFg),
		diffDeleteCodeBg:   resolveColor(p.DiffDeleteCodeBg, base.diffDeleteCodeBg),
		diffDeleteGutterBg: resolveColor(p.DiffDeleteGutterBg, base.diffDeleteGutterBg),

		yolo:           resolveColor(p.Yolo, base.yolo),
		plan:           resolveColor(p.Plan, base.plan),
		planMoreSubtle: resolveColor(p.PlanMoreSubtle, base.planMoreSubtle),

		button:         resolveColor(p.Button, base.button),
		buttonSubtle:   resolveColor(p.ButtonSubtle, base.buttonSubtle),
		buttonInactive: resolveColor(p.ButtonInactive, base.buttonInactive),
		buttonHovered:  resolveColor(p.ButtonHovered, base.buttonHovered),

		ansiBlack:   resolveColor(p.AnsiBlack, base.ansiBlack),
		ansiRed:     resolveColor(p.AnsiRed, base.ansiRed),
		ansiGreen:   resolveColor(p.AnsiGreen, base.ansiGreen),
		ansiYellow:  resolveColor(p.AnsiYellow, base.ansiYellow),
		ansiBlue:    resolveColor(p.AnsiBlue, base.ansiBlue),
		ansiMagenta: resolveColor(p.AnsiMagenta, base.ansiMagenta),
		ansiCyan:    resolveColor(p.AnsiCyan, base.ansiCyan),
		ansiWhite:   resolveColor(p.AnsiWhite, base.ansiWhite),

		ansiBrightBlack:   resolveColor(p.AnsiBrightBlack, base.ansiBrightBlack),
		ansiBrightRed:     resolveColor(p.AnsiBrightRed, base.ansiBrightRed),
		ansiBrightGreen:   resolveColor(p.AnsiBrightGreen, base.ansiBrightGreen),
		ansiBrightYellow:  resolveColor(p.AnsiBrightYellow, base.ansiBrightYellow),
		ansiBrightBlue:    resolveColor(p.AnsiBrightBlue, base.ansiBrightBlue),
		ansiBrightMagenta: resolveColor(p.AnsiBrightMagenta, base.ansiBrightMagenta),
		ansiBrightCyan:    resolveColor(p.AnsiBrightCyan, base.ansiBrightCyan),
		ansiBrightWhite:   resolveColor(p.AnsiBrightWhite, base.ansiBrightWhite),
	}
}

// IsValidColor reports whether s 是 valid color 值: hex code
// (#rgb, #rrggbb, #rgba, #rrggbbaa), ANSI index (0-255), 或 named
// charmtone color (case-insensitive).
func IsValidColor(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "#") {
		return validHexColor.MatchString(s)
	}
	if validANSIColor.MatchString(s) {
		return true
	}
	_, ok := charmtoneByName[strings.ToLower(s)]
	return ok
}

// ParseColor normalizes supported color 为 storage 和 display. 它 accepts
// hex codes, ANSI indices (0-255), 和 named Charmtone colors. Colors matching
// Charmtone 条目 use 它的 canonical name; other valid values 是 returned
// unchanged. Unrecognized input returns an empty string.
func ParseColor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "#") {
		if validHexColor.MatchString(s) {
			if name := tone.NameFromHex(s); name != "" {
				return name
			}
			return s
		}
		return ""
	}
	if validANSIColor.MatchString(s) {
		return s
	}
	if hex, ok := charmtoneByName[strings.ToLower(s)]; ok {
		if name := tone.NameFromHex(hex); name != "" {
			return name
		}
		return hex
	}
	return ""
}

// Validate checks 那 all non-empty color strings 在 palette 是
// valid color values (hex, ANSI 0-255, 或 charmtone name). Returns
// 错误 listing all invalid fields.
func (p Palette) Validate() error {
	var errs []string
	for _, f := range PaletteFields() {
		if v := f.Get(p); v != "" && !IsValidColor(v) {
			errs = append(errs, fmt.Sprintf("%s: invalid color %q", f.Name, v))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid palette colors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// IsEmpty reports whether no palette colors 是 set, meaning theme
// inherits everything 从 它的 base.
func (p Palette) IsEmpty() bool {
	for _, f := range PaletteFields() {
		if f.Get(p) != "" {
			return false
		}
	}
	return true
}

// ThemePalette returns Palette 为 built-在 theme 通过 name.
// Returns 错误 如果 theme 是 不 recognized.
func ThemePalette(name string) (Palette, error) {
	optsFn, ok := builtinThemes[normalizeThemeName(name)]
	if !ok {
		return Palette{}, fmt.Errorf("unknown theme %q; available themes: %s", name, strings.Join(BuiltinThemeNames(), ", "))
	}
	opts := optsFn()
	opts.deriveDiffColors()
	return PaletteFromOpts(opts), nil
}

// MergePalette applies palette overrides 在 top 的 built-在 base theme
// 和 returns fully resolved palette. Empty baseName uses tone.
// MergePalette applies palette overrides 在 top 的 built-在 或 用户 theme
// 和 returns fully resolved palette. Empty baseName uses tone.
func MergePalette(baseName string, palette Palette) (Palette, error) {
	base, root, err := resolveThemePalette(baseName, map[string]bool{})
	if err != nil {
		return Palette{}, err
	}
	if err := palette.Validate(); err != nil {
		return Palette{}, err
	}
	rootOpts, err := builtinThemeOpts(root)
	if err != nil {
		return Palette{}, err
	}
	opts := palette.ToQuickStyleOpts(base.ToQuickStyleOpts(rootOpts))
	opts.deriveDiffColors()
	return PaletteFromOpts(opts), nil
}

// baseThemeOpts returns quickStyleOpts 的 named built-在 theme.
// An empty name yields 默认 Charmtone palette.
// resolveThemePalette returns fully resolved palette 和 root built-在 为
// theme. 用户 themes may inherit 从 other 用户 themes; cycles 是 rejected.
func resolveThemePalette(name string, visiting map[string]bool) (Palette, string, error) {
	if name == "" {
		name = "charmtone-panther"
	}
	key := normalizeThemeName(name)
	if visiting[key] {
		return Palette{}, "", fmt.Errorf("theme inheritance cycle at %q", key)
	}
	visiting[key] = true
	defer delete(visiting, key)

	if path, err := FindThemeFile(key); err == nil {
		tf, err := LoadThemeFile(path)
		if err != nil {
			return Palette{}, "", err
		}
		baseName := tf.Base
		if baseName == "" {
			baseName = "charmtone-panther"
		}
		var base Palette
		var root string
		if strings.EqualFold(baseName, key) && IsBuiltinTheme(key) {
			opts, err := builtinThemeOpts(key)
			if err != nil {
				return Palette{}, "", err
			}
			base = PaletteFromOpts(opts)
			root = key
		} else {
			base, root, err = resolveThemePalette(baseName, visiting)
			if err != nil {
				return Palette{}, "", err
			}
		}
		if err := tf.Validate(); err != nil {
			return Palette{}, "", err
		}
		rootOpts, err := builtinThemeOpts(root)
		if err != nil {
			return Palette{}, "", err
		}
		return PaletteFromOpts(tf.ToQuickStyleOpts(base.ToQuickStyleOpts(rootOpts))), root, nil
	}

	optsFn, ok := builtinThemes[key]
	if !ok {
		return Palette{}, "", fmt.Errorf("unknown theme %q; available themes: %s", name, strings.Join(BuiltinThemeNames(), ", "))
	}
	return PaletteFromOpts(optsFn()), key, nil
}

// builtinThemeOpts returns quickStyleOpts 的 built-在 theme.
func builtinThemeOpts(name string) (quickStyleOpts, error) {
	if name == "" {
		name = "charmtone-panther"
	}
	optsFn, ok := builtinThemes[normalizeThemeName(name)]
	if !ok {
		return quickStyleOpts{}, fmt.Errorf("unknown theme %q; available themes: %s", name, strings.Join(BuiltinThemeNames(), ", "))
	}
	return optsFn(), nil
}

// LoadPaletteTheme builds Styles 通过 applying palette overrides 在 top 的
// built-在 base theme. Empty baseName uses 默认 Charmtone theme.
// LoadPaletteTheme builds Styles 通过 applying palette overrides 在 top 的
// built-在 或 用户 theme. Empty baseName uses 默认 Charmtone theme.
func LoadPaletteTheme(baseName string, palette Palette) (Styles, error) {
	base, root, err := resolveThemePalette(baseName, map[string]bool{})
	if err != nil {
		return Styles{}, err
	}
	if err := palette.Validate(); err != nil {
		return Styles{}, err
	}
	rootOpts, err := builtinThemeOpts(root)
	if err != nil {
		return Styles{}, err
	}
	opts := palette.ToQuickStyleOpts(base.ToQuickStyleOpts(rootOpts))
	s := quickStyle(opts)
	// Root theme overrides hardcode built-在 colors (e.g. Charmtone
	// syntax palette) 那 只 make sense 为 untouched built-在.
	// Only apply them 当 theme 是 unmodified, 所以 用户 themes (e.g.
	// light themes) get fully 令牌-driven styles.
	pure := palette.IsEmpty() && base == PaletteFromOpts(rootOpts)
	if !pure {
		s = legibleBadges(s, opts)
	} else if override, ok := builtinThemeOverrides[root]; ok {
		s = override(s)
	}
	return s, nil
}

// colorToHex returns canonical display string 为 color. If color
// matches Charmtone palette 条目, 它的 name 是 returned; otherwise
// "#rrggbb" hex 值 是 returned. Nil colors return empty string.
func colorToHex(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	hex := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	if name := tone.NameFromHex(hex); name != "" {
		return name
	}
	return hex
}

// ColorString returns lipgloss-compatible color string 为 given
// input. Charmtone names 是 converted 到 hex; hex 和 ANSI values 是
// returned 作为-是. Returns empty string 为 unrecognized input.
func ColorString(s string) string {
	resolved := ParseColor(s)
	if resolved == "" {
		return ""
	}
	if hex, ok := charmtoneByName[strings.ToLower(resolved)]; ok {
		return hex
	}
	return resolved
}

// resolveColor parses a color string into a color.Color, falling back
// 到 provided 默认 当 string 是 empty. Supports hex codes,
// ANSI indices, 和 charmtone names.
func resolveColor(s string, fallback color.Color) color.Color {
	if s == "" {
		return fallback
	}
	cs := ColorString(s)
	if cs == "" {
		return fallback
	}
	return lipgloss.Color(cs)
}
