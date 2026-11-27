// nerdfont.go —— Nerd Font 图标支持。

package model

import (
	"log/slog"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/x/nerdfont"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// nerdFontsMsg reports whether 终态 should render Nerd Font glyphs.
type nerdFontsMsg bool

// detectNerdFonts resolves Nerd Font support off render 路径, since
// probing may shell out 到 fc-list 或 scan font directories.
func detectNerdFonts(tui *config.TUIOptions) tea.Cmd {
	return func() tea.Msg {
		env, _ := os.LookupEnv(nerdfont.EnvVar)
		result := resolveNerdFonts(tui, env, nerdfont.Detect)
		slog.Debug("Nerd font support", "supported", result.Supported, "reason", result.Reason)
		return nerdFontsMsg(result.Supported)
	}
}

// resolveNerdFonts applies the precedence NERDFONT env var, then the
// tui.nerd_fonts option, then detection.
func resolveNerdFonts(tui *config.TUIOptions, env string, detect func() nerdfont.Result) nerdfont.Result {
	envSet := env != "" && !strings.EqualFold(strings.TrimSpace(env), "auto")
	if !envSet && tui != nil && tui.NerdFonts != nil {
		return nerdfont.Result{Supported: *tui.NerdFonts, Reason: "tui.nerd_fonts option"}
	}
	return detect()
}

// gitBranchLabel 是 polled branch, prefixed 用 branch glyph 当
// Nerd Fonts 是 available.
func (m *UI) gitBranchLabel() string {
	if m.gitBranch == "" || !m.nerdFonts {
		return m.gitBranch
	}
	return styles.GitBranchIcon + " " + m.gitBranch
}
