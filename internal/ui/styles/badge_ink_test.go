package styles

import (
	"fmt"
	"image/color"
	"testing"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

// badge 是 style whose 文本 [badgeInk] picks: 文本 sits 在 fill 和
// 是 background 角色 ink unless 那 是 hard 到 读取.
type badge struct {
	name  string
	style lipgloss.Style
	fill  color.Color
	ink   color.Color
}

func rgbHex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func badgesOf(s Styles, o quickStyleOpts) []badge {
	return []badge{
		{"Status.SuccessIndicator", s.Status.SuccessIndicator, o.success, o.bgLessVisible},
		{"Status.InfoIndicator", s.Status.InfoIndicator, o.success, o.bgLessVisible},
		{"Status.UpdateIndicator", s.Status.UpdateIndicator, o.success, o.bgLessVisible},
		{"Status.WarnIndicator", s.Status.WarnIndicator, o.warning, o.bgMostVisible},
		{"Status.ErrorIndicator", s.Status.ErrorIndicator, o.destructive, o.bgBase},
		{"Status.SuccessMessage", s.Status.SuccessMessage, o.successMostSubtle, o.bgLessVisible},
		{"Status.InfoMessage", s.Status.InfoMessage, o.successMostSubtle, o.bgLessVisible},
		{"Status.UpdateMessage", s.Status.UpdateMessage, o.successMostSubtle, o.bgLessVisible},
		{"Status.WarnMessage", s.Status.WarnMessage, o.warningSubtle, o.bgMostVisible},
		{"Status.ModeBadgeYolo", s.Status.ModeBadgeYolo, o.busy, o.bgBase},
		{"Status.ModeBannerYolo", s.Status.ModeBannerYolo, o.yolo, o.bgBase},
		{"Editor.PromptYoloIconFocused", s.Editor.PromptYoloIconFocused, o.busy, o.bgBase},
		{"Button.Hovered", s.Button.Hovered, o.buttonHovered, o.onPrimary},
		{"Attachments.Image", s.Attachments.Image, o.success, o.bgLessVisible},
		{"Attachments.Text", s.Attachments.Text, o.success, o.bgLessVisible},
		{"Attachments.Skill", s.Attachments.Skill, o.success, o.bgLessVisible},
		{"Tool.AgenticFetchPromptTag", s.Tool.AgenticFetchPromptTag, o.success, o.separator},
	}
}

// badgeInkExceptions pins inks [badgeInk] swaps 在 built-在 themes
// whose fills sit below [minBadgeInkContrast] against their 后台
// 角色. Rosé Pine's mid-tone pine fills take 文本 color; dawn's light
// gold fills take deep 文本 color instead.
var badgeInkExceptions = map[string]map[string]string{
	"rose-pine": {
		"Status.SuccessIndicator":    "#e0def4",
		"Status.InfoIndicator":       "#e0def4",
		"Status.UpdateIndicator":     "#e0def4",
		"Status.SuccessMessage":      "#e0def4",
		"Status.InfoMessage":         "#e0def4",
		"Status.UpdateMessage":       "#e0def4",
		"Attachments.Image":          "#e0def4",
		"Attachments.Text":           "#e0def4",
		"Attachments.Skill":          "#e0def4",
		"Tool.AgenticFetchPromptTag": "#e0def4",
	},
	"rose-pine-moon": {
		"Status.SuccessMessage": "#e0def4",
		"Status.InfoMessage":    "#e0def4",
		"Status.UpdateMessage":  "#e0def4",
	},
	"rose-pine-dawn": {
		"Status.WarnIndicator":         "#575279",
		"Status.WarnMessage":           "#575279",
		"Status.SuccessMessage":        "#575279",
		"Status.InfoMessage":           "#575279",
		"Status.UpdateMessage":         "#575279",
		"Status.ModeBadgeYolo":         "#575279",
		"Status.ModeBannerYolo":        "#575279",
		"Editor.PromptYoloIconFocused": "#575279",
	},
}

// TestBadgeInk_BuiltinsKeepTheirInk pins built-在 themes' look: each
// keeps background 角色 it always wrote 在 它的 badges, except where
// [badgeInk] swaps it per [badgeInkExceptions].
func TestBadgeInk_BuiltinsKeepTheirInk(t *testing.T) {
	setTestThemeDirs(t, []string{t.TempDir()})
	for name, opts := range builtinThemes {
		o := opts()
		s, err := LoadTheme(name)
		require.NoError(t, err)
		for _, b := range badgesOf(s, o) {
			want := rgbHex(b.ink)
			if ex, ok := badgeInkExceptions[name]; ok {
				if v, ok := ex[b.name]; ok {
					want = v
				}
			}
			require.Equal(t, want, rgbHex(b.style.GetForeground()), "%s: %s", name, b.name)
		}
	}
}

// TestBadgeInk_LightTheme checks badges stay legible 在 light theme,
// where background roles they 写入 通过 默认 是 light too.
func TestBadgeInk_LightTheme(t *testing.T) {
	t.Parallel()
	light := map[string]string{
		"fg_base":             "#3A3943",
		"fg_subtle":           "#4D4C57",
		"bg_base":             "#F7F6FB",
		"bg_least_visible":    "#ECEBF0",
		"bg_less_visible":     "#D6D3DC",
		"bg_most_visible":     "#BFBCC8",
		"separator":           "#D6D3DC",
		"destructive":         "#AB2454",
		"warning":             "#BF976F",
		"warning_subtle":      "#4D4C57",
		"busy":                "#BF976F",
		"yolo":                "#4D4C57",
		"success":             "#00A475",
		"success_most_subtle": "#00A475",
		"on_primary":          "#FFFAF1",
		"button_hovered":      "#A2A0AD",
	}
	s, err := HostThemeStyles("", light, nil)
	require.NoError(t, err)

	p, _, _, err := parseHostTheme(light, nil)
	require.NoError(t, err)
	base, err := builtinThemeOpts("charmtone-panther")
	require.NoError(t, err)
	o := p.ToQuickStyleOpts(base)
	for _, b := range badgesOf(s, o) {
		got := wcagContrast(b.style.GetForeground(), b.fill)
		require.GreaterOrEqual(t, got, minBadgeInkContrast, "%s: %s on %s", b.name,
			rgbHex(b.style.GetForeground()), rgbHex(b.fill))
	}
	// light background 角色 那 reads well stays.
	require.Equal(t, "#f7f6fb", rgbHex(s.Status.ErrorIndicator.GetForeground()))
}

// primaryBadgesOf lists styles [legibleBadges] picks 文本 的, 作为
// 文本 和 fill colors.
func primaryBadgesOf(s Styles) map[string][2]color.Color {
	pair := func(st lipgloss.Style) [2]color.Color {
		return [2]color.Color{st.GetForeground(), st.GetBackground()}
	}
	h1 := func(b ansi.StyleBlock) [2]color.Color {
		return [2]color.Color{lipgloss.Color(*b.Color), lipgloss.Color(*b.BackgroundColor)}
	}
	return map[string][2]color.Color{
		"Status.ModeBadgePlan":             pair(s.Status.ModeBadgePlan),
		"Status.ModeBannerPlanBadge":       pair(s.Status.ModeBannerPlanBadge),
		"Status.ModeBannerPlan":            pair(s.Status.ModeBannerPlan),
		"Editor.PromptQuestionIconFocused": pair(s.Editor.PromptQuestionIconFocused),
		"FilePicker.Selected":              pair(s.FilePicker.Selected),
		"Markdown.H1":                      h1(s.Markdown.H1),
		"PlanMarkdown.H1":                  h1(s.PlanMarkdown.H1),
	}
}

// TestLegibleBadges_LightTheme checks the primary-filled badges, the plan
// banner 和 H1 heading stay legible 在 light host 或 用户 theme.
func TestLegibleBadges_LightTheme(t *testing.T) {
	light := map[string]string{
		"primary":          "#6B50FF",
		"on_primary":       "#FFFAF1",
		"fg_base":          "#3A3943",
		"bg_base":          "#F7F6FB",
		"warning_subtle":   "#4D4C57",
		"plan_more_subtle": "#6B50FF",
	}
	setTestThemeDirs(t, []string{t.TempDir()})
	host, err := HostThemeStyles("", light, nil)
	require.NoError(t, err)

	var p Palette
	for _, f := range PaletteFields() {
		if v, ok := light[f.Name]; ok {
			f.Set(&p, v)
		}
	}
	user, err := LoadPaletteTheme("", p)
	require.NoError(t, err)

	for theme, s := range map[string]Styles{"host": host, "user": user} {
		for name, c := range primaryBadgesOf(s) {
			got := wcagContrast(c[0], c[1])
			require.GreaterOrEqual(t, got, minBadgeInkContrast, "%s theme: %s: %s on %s", theme, name,
				rgbHex(c[0]), rgbHex(c[1]))
		}
	}
}

// TestLegibleBadges_BuiltinsKeepTheirLook checks built-在, loaded 作为
// theme 或 mirrored 通过 host, keeps ink it always wrote 在 these
// styles, even where it reads poorly (gruvbox-dark's H1).
func TestLegibleBadges_BuiltinsKeepTheirLook(t *testing.T) {
	setTestThemeDirs(t, []string{t.TempDir()})
	for name := range builtinThemes {
		want, err := LoadTheme(name)
		require.NoError(t, err)
		host, err := HostThemeStyles(name, nil, nil)
		require.NoError(t, err)
		got := primaryBadgesOf(host)
		for style, c := range primaryBadgesOf(want) {
			require.Equal(t, rgbHex(c[0]), rgbHex(got[style][0]), "%s: %s", name, style)
		}
	}
	// 默认 theme's plan banner keeps fgBase although it 是 below
	// 3:1 there.
	require.Equal(t, rgbHex(charmtoneOpts().fgBase), rgbHex(CharmtonePantera().Status.ModeBannerPlan.GetForeground()))
}

func TestBadgeInk(t *testing.T) {
	t.Parallel()
	dark, light := lipgloss.Color("#201F26"), lipgloss.Color("#F7F6FB")
	bright, deep := lipgloss.Color("#00FFB2"), lipgloss.Color("#AB2454")

	require.Equal(t, dark, badgeInk(bright, dark, light), "legible ink stays")
	require.Equal(t, dark, badgeInk(bright, light, dark), "illegible ink is swapped")
	require.Equal(t, light, badgeInk(deep, light, dark))
	// An alternative 那 reads no better 是 不 taken.
	fill, ink := lipgloss.Color("#777777"), lipgloss.Color("#808080")
	require.Equal(t, ink, badgeInk(fill, ink, fill))
	require.Nil(t, badgeInk(bright, nil, dark))
	require.Equal(t, dark, badgeInk(nil, dark, light))
}
