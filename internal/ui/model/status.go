package model

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// DefaultStatusTTL 是 默认 time-到-live 为 status messages.
const DefaultStatusTTL = 5 * time.Second

// badgeLeftInset 是 number 的 cells between status bar's left edge
// 和 mode badge.
const badgeLeftInset = 1

// Status 是 status bar 和 help 模型.
type Status struct {
	com      *common.Common
	hideHelp bool
	help     help.Model
	helpKm   help.KeyMap
	msg      util.InfoMsg

	// msgSeq identifies 消息 在 screen; expiry timers carry it.
	msgSeq uint64

	// inputMode 和 permMode drive mode badge shown before help
	// hints.
	inputMode uiInputMode
	permMode  permission.PermissionMode
}

// NewStatus creates new status bar 和 help 模型.
func NewStatus(com *common.Common, km help.KeyMap) *Status {
	s := new(Status)
	s.com = com
	s.help = help.New()
	s.help.Styles = com.Styles.Help
	s.helpKm = km
	return s
}

// Show puts msg 在 screen 和 returns command 那 retires it once 它的
// TTL, 或 [DefaultStatusTTL] 如果 unset, has passed.
func (s *Status) Show(msg util.InfoMsg) tea.Cmd {
	ttl := msg.TTL
	if ttl <= 0 {
		ttl = DefaultStatusTTL
	}
	s.msg = msg
	s.msgSeq++
	seq := s.msgSeq
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return util.ClearStatusMsg{Seq: seq}
	})
}

// ClearInfoMsg clears 消息 在 screen 如果 seq still identifies it.
func (s *Status) ClearInfoMsg(seq uint64) {
	if seq == s.msgSeq {
		s.msg = util.InfoMsg{}
	}
}

// SetMode sets input mode 和 权限 mode used 为 mode badge.
func (s *Status) SetMode(mode uiInputMode, permMode permission.PermissionMode) {
	s.inputMode = mode
	s.permMode = permMode
}

// modeBadge renders badge 为 current mode, 或 empty string 在
// 默认 coding mode.
func (s *Status) modeBadge() string {
	t := s.com.Styles
	// Mirror editor 提示词 precedence: planning wins over YOLO, which
	// can 是 carried into plan mode.
	if s.inputMode == uiInputModePlan {
		return t.Status.ModeBadgePlan.String()
	}
	switch s.permMode {
	case permission.PermissionModeSysadmin:
		return t.Status.ModeBadgeSysadmin.String()
	case permission.PermissionModeYolo:
		return t.Status.ModeBadgeYolo.String()
	}
	return ""
}

// SetWidth sets width 的 status bar 和 help view.
func (s *Status) SetWidth(width int) {
	helpStyle := s.com.Styles.Status.Help
	horizontalPadding := helpStyle.GetPaddingLeft() + helpStyle.GetPaddingRight()
	s.help.SetWidth(width - horizontalPadding)
}

// ShowingAll returns whether full help view 是 shown.
func (s *Status) ShowingAll() bool {
	return s.help.ShowAll
}

// ToggleHelp toggles the full help view.
func (s *Status) ToggleHelp() {
	s.help.ShowAll = !s.help.ShowAll
}

// SetHideHelp sets whether app 是 在 onboarding flow.
func (s *Status) SetHideHelp(hideHelp bool) {
	s.hideHelp = hideHelp
}

// Draw draws the status bar onto the screen.
func (s *Status) Draw(scr uv.Screen, area uv.Rectangle) {
	if !s.hideHelp {
		helpStyle := s.com.Styles.Status.Help
		helpWidth := area.Dx() - helpStyle.GetPaddingLeft() - helpStyle.GetPaddingRight()
		badge := s.modeBadge()
		if badge != "" {
			// Shrink hints 所以 badge does 不 push them past
			// status area.
			helpWidth -= lipgloss.Width(badge) + 1 + badgeLeftInset
		}
		s.help.SetWidth(max(0, helpWidth))
		helpView := helpStyle.Render(s.help.View(s.helpKm))
		if badge != "" {
			// Indent rows after first 所以 expanded help lines up
			// 用 hints 在 badge 行.
			indent := strings.Repeat(" ", badgeLeftInset+lipgloss.Width(badge)+1)
			helpView = strings.ReplaceAll(helpView, "\n", "\n"+indent)
			helpView = strings.Repeat(" ", badgeLeftInset) + badge + " " + helpView
		}
		uv.NewStyledString(helpView).Draw(scr, area)
	}

	// Render notifications
	if s.msg.IsEmpty() {
		return
	}

	var indStyle lipgloss.Style
	var msgStyle lipgloss.Style
	// Mode banners show same badge 那 sits next 到 help hints, 所以
	// they honor same left inset 到 keep indicator 从 jumping 当
	// banner appears 或 expires.
	indInset := 0
	switch s.msg.Type {
	case util.InfoTypePlan:
		indStyle = s.com.Styles.Status.ModeBannerPlanBadge
		msgStyle = s.com.Styles.Status.ModeBannerPlan
		indInset = badgeLeftInset
	case util.InfoTypeYolo:
		indStyle = s.com.Styles.Status.ModeBannerYoloBadge
		msgStyle = s.com.Styles.Status.ModeBannerYolo
		indInset = badgeLeftInset
	case util.InfoTypeSysadmin:
		indStyle = s.com.Styles.Status.ModeBannerSysadminBadge
		msgStyle = s.com.Styles.Status.ModeBannerSysadmin
		indInset = badgeLeftInset
	case util.InfoTypeError:
		indStyle = s.com.Styles.Status.ErrorIndicator
		msgStyle = s.com.Styles.Status.ErrorMessage
	case util.InfoTypeWarn:
		indStyle = s.com.Styles.Status.WarnIndicator
		msgStyle = s.com.Styles.Status.WarnMessage
	case util.InfoTypeUpdate:
		indStyle = s.com.Styles.Status.UpdateIndicator
		msgStyle = s.com.Styles.Status.UpdateMessage
	case util.InfoTypeInfo:
		indStyle = s.com.Styles.Status.InfoIndicator
		msgStyle = s.com.Styles.Status.InfoMessage
	case util.InfoTypeSuccess:
		indStyle = s.com.Styles.Status.SuccessIndicator
		msgStyle = s.com.Styles.Status.SuccessMessage
	}

	ind := indStyle.String()
	indWidth := lipgloss.Width(ind)
	msgPad := msgStyle.GetPaddingLeft() + msgStyle.GetPaddingRight()
	avail := max(0, area.Dx()-indWidth-msgPad-indInset)
	msg := strings.Join(strings.Split(s.msg.Msg, "\n"), " ")
	msg = ansi.Truncate(msg, avail, "…")
	if w := lipgloss.Width(msg); w < avail {
		msg += strings.Repeat(" ", avail-w)
	}
	info := msgStyle.Render(msg)

	// Draw info 消息 over help view
	uv.NewStyledString(strings.Repeat(" ", indInset)+ind+info).Draw(scr, area)
}
