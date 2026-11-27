package styles

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLoadTheme_Builtin(t *testing.T) {
	_, err := LoadTheme("charmtone-panther")
	if err != nil {
		t.Fatalf("LoadTheme(charmtone-panther): %v", err)
	}
}

func TestLoadTheme_DeprecatedAlias(t *testing.T) {
	_, err := LoadTheme("charmtone")
	if err != nil {
		t.Fatalf("LoadTheme(charmtone) via deprecated alias: %v", err)
	}
	newStyles, err := LoadTheme("charmtone-panther")
	if err != nil {
		t.Fatalf("LoadTheme(charmtone-panther): %v", err)
	}
	alias, err := LoadTheme("charmtone")
	if err != nil {
		t.Fatalf("LoadTheme(charmtone): %v", err)
	}
	if *alias.Markdown.Document.Color != *newStyles.Markdown.Document.Color {
		t.Error("deprecated alias should resolve to the same styles")
	}
}

func TestLoadTheme_CaseInsensitive(t *testing.T) {
	_, err := LoadTheme("Gruvbox-Dark")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	_, err = LoadTheme("Keanu")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	_, err = LoadTheme("KITT")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
}

func TestLoadTheme_Empty(t *testing.T) {
	s, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme empty: %v", err)
	}
	if s.WorkingGradFromColor == nil {
		t.Error("expected non-nil WorkingGradFromColor in default theme")
	}
}

func TestLoadTheme_Unknown(t *testing.T) {
	_, err := LoadTheme("nonexistent-theme")
	if err == nil {
		t.Fatal("expected error for unknown theme")
	}
}

func TestBuiltinThemeNames(t *testing.T) {
	names := BuiltinThemeNames()
	if len(names) < 2 {
		t.Fatal("expected at least two builtin themes")
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("names not sorted: %q before %q", names[i-1], names[i])
		}
	}
}

func TestAllBuiltinThemes_DoNotPanic(t *testing.T) {
	for _, name := range BuiltinThemeNames() {
		t.Run(name, func(t *testing.T) {
			_, err := LoadTheme(name)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
		})
	}
}

func TestCloneDoesNotAlias(t *testing.T) {
	s := CharmtonePantera()
	clone := s.Clone()

	origColor := s.Markdown.Document.Color
	if origColor == nil {
		t.Fatal("expected non-nil Document.Color in default styles")
	}

	newColor := "#ff0000"
	clone.Markdown.Document.Color = &newColor

	if s.Markdown.Document.Color == clone.Markdown.Document.Color {
		t.Error("Clone() aliased Markdown.Document.Color pointer")
	}
	if *s.Markdown.Document.Color == "#ff0000" {
		t.Error("modifying clone mutated original")
	}
}

// relativeLuminance 和 contrastRatio implement WCAG 2.x formulas 所以 we
// can assert 那 inline code stays legible 在 built-在 themes.
func relativeLuminance(hexColor string) float64 {
	h := strings.TrimPrefix(hexColor, "#")
	if len(h) != 6 {
		return 0
	}
	channel := func(offset int) float64 {
		v, _ := strconv.ParseInt(h[offset:offset+2], 16, 0)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := channel(0), channel(2), channel(4)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func contrastRatio(fg, bg string) float64 {
	lf, lb := relativeLuminance(fg), relativeLuminance(bg)
	hi, lo := math.Max(lf, lb), math.Min(lf, lb)
	return (hi + 0.05) / (lo + 0.05)
}

func TestGruvboxDark_InlineCodeContrast(t *testing.T) {
	// Regression 测试 为 reported low-contrast inline code 在 Gruvbox
	// Dark (bright red 在 code background was 只 ~2.6:1).
	const minAA = 4.5
	s, err := LoadTheme("gruvbox-dark")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	code := s.Markdown.Code
	if code.Color == nil || code.BackgroundColor == nil {
		t.Fatal("inline code style is missing fg/bg colors")
	}
	if ratio := contrastRatio(*code.Color, *code.BackgroundColor); ratio < minAA {
		t.Errorf("gruvbox-dark inline code contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}
}

func TestKeanu_BrightOnBrightContrast(t *testing.T) {
	// Keanu 是 monochrome green: fgBase 和 primary 是 both neon
	// #00ff41, 所以 shared "fgBase 在 primary" pairings would render
	// invisible 文本. overrides must swap those foregrounds 到
	// dark ink.
	const minAA = 4.5
	s, err := LoadTheme("keanu")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	fg := *hex(s.FilePicker.Selected.GetForeground())
	if ratio := contrastRatio(fg, "#00ff41"); ratio < minAA {
		t.Errorf("keanu selected-file contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}

	if s.Markdown.H1.Color == nil || s.Markdown.H1.BackgroundColor == nil {
		t.Fatal("H1 style is missing fg/bg colors")
	}
	if ratio := contrastRatio(*s.Markdown.H1.Color, *s.Markdown.H1.BackgroundColor); ratio < minAA {
		t.Errorf("keanu H1 contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}

	code := s.Markdown.Code
	if code.Color == nil || code.BackgroundColor == nil {
		t.Fatal("inline code style is missing fg/bg colors")
	}
	if ratio := contrastRatio(*code.Color, *code.BackgroundColor); ratio < minAA {
		t.Errorf("keanu inline code contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}

	// inline code chip must stand out 从 消息 background,
	// like it does inside the thinking block.
	if bg := *hex(s.Background); *code.BackgroundColor == bg {
		t.Errorf("inline code background %s matches the message background; the chip is invisible", bg)
	}
}

func TestKeanu_ModeBadges(t *testing.T) {
	// Mode badges 和 banners pair foreground 用 bright mode
	// background; 在 monochrome green theme 默认 plan pairing
	// collapses into green-在-green. banner badge 是 copied 从
	// badge inside quickStyle before overrides 运行, 所以 check both.
	const minAA = 4.5
	s, err := LoadTheme("keanu")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	badges := map[string]lipgloss.Style{
		"ModeBadgePlan":       s.Status.ModeBadgePlan,
		"ModeBannerPlanBadge": s.Status.ModeBannerPlanBadge,
		"ModeBannerPlan":      s.Status.ModeBannerPlan,
		"ModeBadgeYolo":       s.Status.ModeBadgeYolo,
		"ModeBannerYoloBadge": s.Status.ModeBannerYoloBadge,
		"ModeBannerYolo":      s.Status.ModeBannerYolo,
	}
	for name, style := range badges {
		fg := *hex(style.GetForeground())
		bg := *hex(style.GetBackground())
		if ratio := contrastRatio(fg, bg); ratio < minAA {
			t.Errorf("keanu %s contrast %.2f is below WCAG AA (%.1f)", name, ratio, minAA)
		}
	}
}

func TestKeanu_BodyTextContrast(t *testing.T) {
	// main 文本 color must stay legible 在 pure black background.
	const minAA = 4.5
	s, err := LoadTheme("keanu")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fg := *hex(s.Messages.NoContent.GetForeground())
	bg := *hex(s.Background)
	if ratio := contrastRatio(fg, bg); ratio < minAA {
		t.Errorf("keanu body text contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}
}

func TestSysadminBanner_VisibleOnEveryTheme(t *testing.T) {
	// banner body sits 在 subtler destructive token, which 只
	// Charmtone names. Every other theme, 和 overridden Charmtone, used
	// 到 end up 用 nil fill 和 bgBase-colored 文本: empty line. 一个
	// fallback 应当 keep banner visible wherever it's rendered.
	const minContrast = 3.0
	themes := map[string]func() (Styles, error){}
	for _, name := range BuiltinThemeNames() {
		themes[name] = func() (Styles, error) { return LoadTheme(name) }
	}
	themes["overridden-charmtone"] = func() (Styles, error) {
		return LoadPaletteTheme("charmtone-panther", Palette{Primary: "#ff0000"})
	}

	for name, load := range themes {
		t.Run(name, func(t *testing.T) {
			s, err := load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			style := s.Status.ModeBannerSysadmin
			fg, bg := style.GetForeground(), style.GetBackground()
			if fg == nil {
				t.Fatal("sysadmin banner has no foreground")
			}
			if bg == nil {
				t.Fatal("sysadmin banner has no background fill")
			}
			if ratio := contrastRatio(*hex(fg), *hex(bg)); ratio < minContrast {
				t.Errorf("sysadmin banner contrast %.2f is below %.1f", ratio, minContrast)
			}
		})
	}
}

func TestKitt_BadgeContrast(t *testing.T) {
	// KITT fills badges 用 scanner red primary. Bold badges keep
	// white 文本 在 bold-文本 floor; selection rows 和 H1
	// use dark ink 和 must clear WCAG AA. banner badges 是
	// copied inside quickStyle before overrides 运行, 所以 check both.
	const minAA = 4.5
	const minBold = 3.0
	s, err := LoadTheme("kitt")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	boldBadges := map[string]lipgloss.Style{
		"PromptQuestionIconFocused": s.Editor.PromptQuestionIconFocused,
		"ModeBadgePlan":             s.Status.ModeBadgePlan,
		"ModeBannerPlanBadge":       s.Status.ModeBannerPlanBadge,
	}
	for name, style := range boldBadges {
		if !style.GetBold() {
			t.Errorf("kitt %s must be bold to use the bold-text contrast floor", name)
		}
		fg := *hex(style.GetForeground())
		bg := *hex(style.GetBackground())
		if ratio := contrastRatio(fg, bg); ratio < minBold {
			t.Errorf("kitt %s contrast %.2f is below the bold-text floor (%.1f)", name, ratio, minBold)
		}
	}

	badges := map[string]lipgloss.Style{
		"FilePickerSelected":  s.FilePicker.Selected,
		"ModeBadgeYolo":       s.Status.ModeBadgeYolo,
		"ModeBannerYoloBadge": s.Status.ModeBannerYoloBadge,
		"CompletionsFocused":  s.Completions.Focused,
		"DialogSelectedItem":  s.Dialog.SelectedItem,
	}
	for name, style := range badges {
		fg := *hex(style.GetForeground())
		bg := *hex(style.GetBackground())
		if ratio := contrastRatio(fg, bg); ratio < minAA {
			t.Errorf("kitt %s contrast %.2f is below WCAG AA (%.1f)", name, ratio, minAA)
		}
	}

	for name, h1 := range map[string]*string{
		"Markdown.H1":     s.Markdown.H1.Color,
		"PlanMarkdown.H1": s.PlanMarkdown.H1.Color,
	} {
		if h1 == nil || s.Markdown.H1.BackgroundColor == nil {
			t.Fatalf("%s style is missing fg/bg colors", name)
		}
		if ratio := contrastRatio(*h1, *s.Markdown.H1.BackgroundColor); ratio < minAA {
			t.Errorf("kitt %s contrast %.2f is below WCAG AA (%.1f)", name, ratio, minAA)
		}
	}
}

func TestKitt_TextContrast(t *testing.T) {
	// Body 文本 和 inline code must stay legible 在 near-black
	// dashboard 后台.
	const minAA = 4.5
	s, err := LoadTheme("kitt")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fg := *hex(s.Messages.NoContent.GetForeground())
	bg := *hex(s.Background)
	if ratio := contrastRatio(fg, bg); ratio < minAA {
		t.Errorf("kitt body text contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}

	code := s.Markdown.Code
	if code.Color == nil || code.BackgroundColor == nil {
		t.Fatal("inline code style is missing fg/bg colors")
	}
	if ratio := contrastRatio(*code.Color, *code.BackgroundColor); ratio < minAA {
		t.Errorf("kitt inline code contrast %.2f is below WCAG AA (%.1f)", ratio, minAA)
	}
}

func TestKitt_ModeColorsAreDistinct(t *testing.T) {
	// Plan, YOLO 和 Sysadmin each get their own fill 所以 活跃 mode
	// 是 obvious 在 glance: green, amber 和 red respectively.
	const minBold = 3.0
	s, err := LoadTheme("kitt")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	modeBadges := map[string]lipgloss.Style{
		"plan":     s.Status.ModeBadgePlan,
		"yolo":     s.Status.ModeBadgeYolo,
		"sysadmin": s.Status.ModeBadgeSysadmin,
	}
	promptIcons := map[string]lipgloss.Style{
		"plan":     s.Editor.PromptPlanIconFocused,
		"yolo":     s.Editor.PromptYoloIconFocused,
		"sysadmin": s.Editor.PromptSysadminIconFocused,
	}
	for _, group := range []map[string]lipgloss.Style{modeBadges, promptIcons} {
		seenFills := map[string]string{}
		for mode, style := range group {
			fill := *hex(style.GetBackground())
			if other, ok := seenFills[fill]; ok {
				t.Errorf("kitt %s and %s modes share the fill %s", mode, other, fill)
			}
			seenFills[fill] = mode

			fg := *hex(style.GetForeground())
			if ratio := contrastRatio(fg, fill); ratio < minBold {
				t.Errorf("kitt %s mode contrast %.2f is below the bold-text floor (%.1f)", mode, ratio, minBold)
			}
		}
	}

	banners := map[string]lipgloss.Style{
		"plan":     s.Status.ModeBannerPlan,
		"yolo":     s.Status.ModeBannerYolo,
		"sysadmin": s.Status.ModeBannerSysadmin,
	}
	const minAA = 4.5
	for mode, style := range banners {
		if _, ok := style.GetBackground().(lipgloss.NoColor); ok {
			t.Errorf("kitt %s mode banner has no fill", mode)
			continue
		}
		fg := *hex(style.GetForeground())
		bg := *hex(style.GetBackground())
		if ratio := contrastRatio(fg, bg); ratio < minAA {
			t.Errorf("kitt %s mode banner contrast %.2f is below WCAG AA (%.1f)", mode, ratio, minAA)
		}
	}
}
