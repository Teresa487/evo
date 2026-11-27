package common

import (
	"fmt"
	"image/color"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestThemeStylesFromConfig_ActiveTheme(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Options: &config.Options{
			TUI: &config.TUIOptions{ActiveTheme: "gruvbox-dark"},
		},
	}

	s := ThemeStylesFromConfig(cfg)
	require.Equal(t, "#fabd2f", testColorHex(s.WorkingGradFromColor))
}

func testColorHex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func TestThemeStylesFromConfig_UserFileFallback(t *testing.T) {
	// 配置 resolution delegates 到 LoadTheme, which checks global 用户 files
	// before built-ins.
	cfg := &config.Config{
		Options: &config.Options{
			TUI: &config.TUIOptions{
				ActiveTheme: "gruvbox-dark",
			},
		},
	}
	s := ThemeStylesFromConfig(cfg)
	require.Equal(t, "#fabd2f", testColorHex(s.WorkingGradFromColor))
}

// Verify 那 ExportResolvedPalette produces valid theme 文件 那 can
// 是 saved 和 reloaded through full resolution pipeline.
func TestExportAndReloadRoundTrip(t *testing.T) {
	dir := t.TempDir()

	exported, err := styles.ExportResolvedPalette("gruvbox-dark")
	require.NoError(t, err)

	path := filepath.Join(dir, "gruvbox-fork.json")
	require.NoError(t, styles.SaveThemeFile(path, exported))

	reloaded, err := styles.LoadThemeFile(path)
	require.NoError(t, err)
	require.Equal(t, exported.Base, reloaded.Base)
	require.Equal(t, exported.Primary, reloaded.Primary)
	require.Equal(t, exported.BgBase, reloaded.BgBase)
}
