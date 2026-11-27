package styles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// setTestThemeDirs overrides ThemeDirs 为 duration 的 测试.
// Tests using 这 must NOT 调用 t.Parallel() since override 是
// shared package-level state.
func setTestThemeDirs(t *testing.T, dirs []string) {
	t.Helper()
	orig := themeDirsOverride
	themeDirsOverride = dirs
	t.Cleanup(func() { themeDirsOverride = orig })
}

func TestLoadTheme_UserFileShadowsBuiltin(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	tf := &ThemeFile{
		Base:    "charmtone-panther",
		Palette: Palette{Primary: "#ff0000"},
	}
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "charmtone-panther.json"), tf))

	s, err := LoadTheme("charmtone-panther")
	require.NoError(t, err)
	require.NotNil(t, s.WorkingGradFromColor)
}

func TestLoadTheme_UserOnlyTheme(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	tf := &ThemeFile{
		Base: "gruvbox-dark",
		Palette: Palette{
			Primary: "#111111",
			BgBase:  "#222222",
		},
	}
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "my-custom.json"), tf))

	s, err := LoadTheme("my-custom")
	require.NoError(t, err)
	require.NotNil(t, s.WorkingGradFromColor)
}

func TestLoadTheme_UserThemeCanInheritUserTheme(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	require.NoError(t, SaveThemeFile(filepath.Join(dir, "parent.json"), &ThemeFile{
		Base:    "gruvbox-dark",
		Palette: Palette{Primary: "#111111"},
	}))
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "child.json"), &ThemeFile{
		Base:    "parent",
		Palette: Palette{Secondary: "#222222"},
	}))

	resolved, err := ExportResolvedPalette("child")
	require.NoError(t, err)
	require.Equal(t, "gruvbox-dark", resolved.Base)
	require.Equal(t, "#111111", resolved.Primary)
	require.Equal(t, "#222222", resolved.Secondary)
}

func TestLoadTheme_RejectsInheritanceCycle(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	require.NoError(t, SaveThemeFile(filepath.Join(dir, "first.json"), &ThemeFile{Base: "second"}))
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "second.json"), &ThemeFile{Base: "first"}))

	_, err := LoadTheme("first")
	require.Error(t, err)
	require.Contains(t, err.Error(), "inheritance cycle")
}

func TestForkedTheme_KeepsLoadingAfterSourceBaseDeleted(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	require.NoError(t, SaveThemeFile(filepath.Join(dir, "parent.json"), &ThemeFile{
		Base:    "gruvbox-dark",
		Palette: Palette{Primary: "#111111"},
	}))

	// Fork 用户 theme way theme dialog does 当 creating
	// new theme: exported Base must stay pinned 到 built-在 root.
	exported, err := ExportResolvedPalette("parent")
	require.NoError(t, err)
	require.Equal(t, "gruvbox-dark", exported.Base)
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "forked.json"), exported))

	// Deleting source 用户 theme must 不 break fork.
	require.NoError(t, os.Remove(filepath.Join(dir, "parent.json")))

	s, err := LoadTheme("forked")
	require.NoError(t, err)
	require.NotNil(t, s.WorkingGradFromColor)
}

func TestLoadTheme_OverriddenBuiltinKeepsThemeOverrides(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "charmtone-panther.json"), &ThemeFile{Base: "charmtone-panther"}))

	loaded, err := LoadTheme("charmtone-panther")
	require.NoError(t, err)
	builtin := CharmtonePantera()
	require.Equal(t, builtin.Editor.PromptBangIconFocused.GetForeground(), loaded.Editor.PromptBangIconFocused.GetForeground())
	require.Equal(t, builtin.Messages.ShellPrompt.GetForeground(), loaded.Messages.ShellPrompt.GetForeground())
}

func TestLoadTheme_BuiltinFallback(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	s, err := LoadTheme("gruvbox-dark")
	require.NoError(t, err)
	require.NotNil(t, s.WorkingGradFromColor)
}

func TestLoadTheme_UnknownStillErrors(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	_, err := LoadTheme("nonexistent-theme-xyz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown theme")
}

func TestListAllThemes_IncludesBuiltins(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	infos := ListAllThemes()
	require.NotEmpty(t, infos)

	foundCharmtone := false
	for _, info := range infos {
		if info.Name == "charmtone-panther" {
			foundCharmtone = true
			require.Equal(t, ThemeSourceBuiltin, info.Source)
			require.False(t, info.Overridden)
		}
	}
	require.True(t, foundCharmtone, "expected charmtone-panther in theme list")
}

func TestListAllThemes_ShowsOverridden(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	tf := &ThemeFile{Base: "gruvbox-dark", Palette: Palette{Primary: "#ff0000"}}
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "gruvbox-dark.json"), tf))

	infos := ListAllThemes()
	for _, info := range infos {
		if info.Name == "gruvbox-dark" {
			require.Equal(t, ThemeSourceBuiltin, info.Source)
			require.True(t, info.Overridden)
			return
		}
	}
	t.Fatal("gruvbox-dark not found in theme list")
}

func TestRevertOverriddenTheme_RestoresBuiltin(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	// Override built-在 用 custom primary color.
	path := filepath.Join(dir, "gruvbox-dark.json")
	tf := &ThemeFile{Base: "gruvbox-dark", Palette: Palette{Primary: "#ff0000"}}
	require.NoError(t, SaveThemeFile(path, tf))

	// built-在 now reports 作为 overridden.
	requireOverridden(t, "gruvbox-dark", true)

	// Reverting removes shadowing 文件; built-在 resolves clean 和
	// 是 no longer reported 作为 overridden.
	require.NoError(t, os.Remove(path))
	requireOverridden(t, "gruvbox-dark", false)
	_, err := LoadTheme("gruvbox-dark")
	require.NoError(t, err)
}

func requireOverridden(t *testing.T, name string, want bool) {
	t.Helper()
	for _, info := range ListAllThemes() {
		if info.Name == name {
			require.Equal(t, want, info.Overridden)
			return
		}
	}
	t.Fatalf("theme %q not found", name)
}

func TestListAllThemes_IncludesUserOnlyThemes(t *testing.T) {
	userDir := t.TempDir()
	setTestThemeDirs(t, []string{userDir})

	tf := &ThemeFile{Base: "charmtone-panther"}
	require.NoError(t, SaveThemeFile(filepath.Join(userDir, "my-neon.json"), tf))

	infos := ListAllThemes()
	found := false
	for _, info := range infos {
		if info.Name == "my-neon" {
			found = true
			require.Equal(t, ThemeSourceUser, info.Source)
			require.False(t, info.Overridden)
		}
	}
	require.True(t, found, "expected my-neon in theme list")
}

func TestListAllThemes_Sorted(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	infos := ListAllThemes()
	for i := 1; i < len(infos); i++ {
		require.LessOrEqual(t, infos[i-1].Name, infos[i].Name,
			"themes not sorted: %q before %q", infos[i-1].Name, infos[i].Name)
	}
}

func TestLoadPaletteTheme_OverridesOnlyForPureBuiltin(t *testing.T) {
	// An untouched base theme keeps built-在's style overrides
	// (e.g. the hardcoded Charmtone syntax colors).
	pure, err := LoadPaletteTheme("charmtone-panther", Palette{})
	require.NoError(t, err)
	builtin := CharmtonePantera()
	require.Equal(t, builtin.Markdown.CodeBlock.Chroma.CommentPreproc.Color,
		pure.Markdown.CodeBlock.Chroma.CommentPreproc.Color)

	// 一个 customized theme must 不 inherit those overrides; otherwise
	// light themes would keep dark-tuned syntax 和 markdown colors.
	light := Palette{BgBase: "#ffffff", FgBase: "#000000"}
	custom, err := LoadPaletteTheme("charmtone-panther", light)
	require.NoError(t, err)
	require.NotEqual(t, builtin.Markdown.CodeBlock.Chroma.CommentPreproc.Color,
		custom.Markdown.CodeBlock.Chroma.CommentPreproc.Color)
}

func TestLoadPaletteTheme_AnsiColors(t *testing.T) {
	// ANSI 16-color remapping 是 customizable 所以 light themes can keep
	// raw 终态 output legible.
	custom := Palette{BgBase: "#ffffff", AnsiWhite: "#000000", AnsiBrightWhite: "#1a1a1a"}
	s, err := LoadPaletteTheme("charmtone-panther", custom)
	require.NoError(t, err)
	require.Equal(t, "#000000", *hex(s.ANSI[7]))
	require.Equal(t, "#1a1a1a", *hex(s.ANSI[15]))
}

func TestExportResolvedPalette_Builtin(t *testing.T) {
	t.Parallel()
	tf, err := ExportResolvedPalette("charmtone-panther")
	require.NoError(t, err)
	require.Equal(t, "charmtone-panther", tf.Base)
	require.NotEmpty(t, tf.Primary)
	require.NotEmpty(t, tf.BgBase)
	require.NotEmpty(t, tf.FgBase)
	require.NotEmpty(t, tf.Success)
	require.NoError(t, tf.Validate())
}

func TestExportResolvedPalette_GruvboxDark(t *testing.T) {
	t.Parallel()
	tf, err := ExportResolvedPalette("gruvbox-dark")
	require.NoError(t, err)
	require.Equal(t, "gruvbox-dark", tf.Base)
	require.Equal(t, "#fabd2f", tf.Primary)
	require.Equal(t, "#282828", tf.BgBase)
}

func TestExportResolvedPalette_UserTheme(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	userTf := &ThemeFile{
		Base:    "charmtone-panther",
		Palette: Palette{Primary: "#ff0000"},
	}
	require.NoError(t, SaveThemeFile(filepath.Join(dir, "custom.json"), userTf))

	exported, err := ExportResolvedPalette("custom")
	require.NoError(t, err)
	require.Equal(t, "charmtone-panther", exported.Base)
	require.Equal(t, "#ff0000", exported.Primary)
	require.NotEmpty(t, exported.BgBase)
	require.NotEmpty(t, exported.FgBase)
}

func TestExportResolvedPalette_Unknown(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	_, err := ExportResolvedPalette("does-not-exist")
	require.Error(t, err)
}

func TestThemeSource_String(t *testing.T) {
	t.Parallel()
	require.Equal(t, "builtin", ThemeSourceBuiltin.String())
	require.Equal(t, "user", ThemeSourceUser.String())
}

func TestIsBuiltinTheme(t *testing.T) {
	t.Parallel()
	require.True(t, IsBuiltinTheme("charmtone-panther"))
	require.True(t, IsBuiltinTheme("charmtone")) // deprecated alias
	require.True(t, IsBuiltinTheme("Gruvbox-Dark"))
	require.True(t, IsBuiltinTheme("rose-pine"))
	require.True(t, IsBuiltinTheme("rose-pine-moon"))
	require.True(t, IsBuiltinTheme("rose-pine-dawn"))
	require.True(t, IsBuiltinTheme("Rose-Pine-Moon")) // case-insensitive
	require.False(t, IsBuiltinTheme("my-custom"))
}

func TestValidateThemeName_Valid(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	for _, name := range []string{"neon", "my-theme", "my_theme", "theme2", "a1-b2_c3"} {
		require.NoError(t, ValidateThemeName(name), "expected %q to be valid", name)
	}
}

func TestValidateThemeName_Empty(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	err := ValidateThemeName("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestValidateThemeName_UnsafeCharacters(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	// 路径 traversal 和 separators must 是 rejected 所以 theme name can
	// never escape themes 目录.
	for _, name := range []string{"../evil", "a/b", "a\\b", "my theme", "Theme!", "-lead", "trail-", "a--b"} {
		require.Error(t, ValidateThemeName(name), "expected %q to be rejected", name)
	}
}

func TestValidateThemeName_RejectsBuiltin(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	err := ValidateThemeName("charmtone-panther")
	require.Error(t, err)
	require.Contains(t, err.Error(), "built-in")
}

func TestValidateThemeName_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	setTestThemeDirs(t, []string{dir})

	require.NoError(t, SaveThemeFile(filepath.Join(dir, "taken.json"), &ThemeFile{Base: "charmtone-panther"}))

	err := ValidateThemeName("taken")
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}
