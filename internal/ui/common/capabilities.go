package common

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xstrings "github.com/charmbracelet/x/exp/strings"

	"github.com/charmbracelet/crush/internal/ui/notification"
)

// Capabilities define different 终态 capabilities supported.
type Capabilities struct {
	// Profile 是 终态 color profile used 到 determine how colors 是
	// rendered.
	Profile colorprofile.Profile
	// Columns 是 number 的 character columns 在 终态.
	Columns int
	// Rows 是 number 的 character rows 在 终态.
	Rows int
	// PixelX 是 width 的 终态 在 pixels.
	PixelX int
	// PixelY 是 height 的 终态 在 pixels.
	PixelY int
	// KittyGraphics indicates whether 终态 supports Kitty graphics
	// protocol.
	KittyGraphics bool
	// SixelGraphics indicates whether 终态 supports Sixel graphics.
	SixelGraphics bool
	// Env 是 终态 environment variables.
	Env uv.Environ
	// TerminalVersion 是 终态 version string.
	TerminalVersion string
	// ReportFocusEvents indicates whether 终态 supports focus events.
	ReportFocusEvents bool
	// OSC99Notifications indicates whether 终态 supports OSC 99 notifications.
	OSC99Notifications bool
}

// 更新 updates capabilities based 在 given 消息.
func (c *Capabilities) Update(msg any) {
	switch m := msg.(type) {
	case tea.EnvMsg:
		c.Env = uv.Environ(m)
	case tea.ColorProfileMsg:
		c.Profile = m.Profile
	case tea.WindowSizeMsg:
		c.Columns = m.Width
		c.Rows = m.Height
	case uv.PixelSizeEvent:
		c.PixelX = m.Width
		c.PixelY = m.Height
	case uv.KittyGraphicsEvent:
		c.KittyGraphics = true
	case uv.PrimaryDeviceAttributesEvent:
		if slices.Contains(m, 4) {
			c.SixelGraphics = true
		}
	case tea.TerminalVersionMsg:
		c.TerminalVersion = m.Name
	case tea.ModeReportMsg:
		switch m.Mode {
		case ansi.ModeFocusEvent:
			c.ReportFocusEvents = modeSupported(m.Value)
		}
	case uv.UnknownOscEvent:
		if notification.DetectOSC99Support(string(m)) {
			c.OSC99Notifications = true
		}
	}
}

// QueryCmd returns [tea.Cmd] 那 queries 终态 为 different
// capabilities.
func QueryCmd(env uv.Environ) tea.Cmd {
	var sb strings.Builder
	sb.WriteString(ansi.RequestPrimaryDeviceAttributes)
	sb.WriteString(ansi.QueryModifyOtherKeys)
	sb.WriteString(ansi.RequestModeFocusEvent)
	sb.WriteString(notification.OSC99QuerySequence())

	// Queries 那 should 只 是 sent 到 "smart" normal terminals.
	shouldQueryFor := shouldQueryCapabilities(env)
	if shouldQueryFor {
		sb.WriteString(ansi.RequestNameVersion)
		sb.WriteString(ansi.WindowOp(14)) // Window size in pixels
		kittyReq := ansi.KittyGraphics([]byte("AAAA"), "i=31", "s=1", "v=1", "a=q", "t=d", "f=24")
		if _, isTmux := env.LookupEnv("TMUX"); isTmux {
			kittyReq = ansi.TmuxPassthrough(kittyReq)
		}
		sb.WriteString(kittyReq)
	}

	return tea.Raw(sb.String())
}

// SupportsTrueColor returns true 如果 终态 supports true color.
func (c Capabilities) SupportsTrueColor() bool {
	return c.Profile == colorprofile.TrueColor
}

// SupportsKittyGraphics returns true 如果 终态 supports Kitty graphics.
func (c Capabilities) SupportsKittyGraphics() bool {
	return c.KittyGraphics
}

// SupportsSixelGraphics returns true 如果 终态 supports Sixel graphics.
func (c Capabilities) SupportsSixelGraphics() bool {
	return c.SixelGraphics
}

// CellSize returns size 的 single 终态 cell 在 pixels.
func (c Capabilities) CellSize() (width, height int) {
	if c.Columns == 0 || c.Rows == 0 {
		return 0, 0
	}
	return c.PixelX / c.Columns, c.PixelY / c.Rows
}

func modeSupported(v ansi.ModeSetting) bool {
	return v.IsSet() || v.IsReset()
}

// kittyTerminals defines terminals supporting querying capabilities.
var kittyTerminals = []string{"alacritty", "ghostty", "kitty", "rio", "wezterm"}

func shouldQueryCapabilities(env uv.Environ) bool {
	const osVendorTypeApple = "Apple"
	termType := env.Getenv("TERM")
	termProg, okTermProg := env.LookupEnv("TERM_PROGRAM")
	_, okSSHTTY := env.LookupEnv("SSH_TTY")
	if okTermProg && strings.Contains(termProg, osVendorTypeApple) {
		return false
	}
	return (!okTermProg && !okSSHTTY) ||
		(!strings.Contains(termProg, osVendorTypeApple) && !okSSHTTY) ||
		// Terminals 那 do support XTVERSION.
		xstrings.ContainsAnyOf(termType, kittyTerminals...)
}
