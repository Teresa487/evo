// lsp.go —— LSP 诊断/状态的 UI 展示。

package model

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/x/nerdfont"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// lspStatesTTL bounds how long the memoized LSP state may go without a
// re-probe being scheduled; LSP events normally 刷新 it much sooner.
// backstop covers events missed across SSE reconnects 在 客户端/服务端
// mode, 所以 it can 是 order 的 magnitude looser than busy/权限
// TTL (which drives interactive affordances like spinner 和 队列
// pill): few seconds 的 stale LSP counts 是 invisible, few seconds 的
// stale busy state 是 不. Package var 所以 tests can pin it.
var lspStatesTTL = 5 * time.Second

// lspStatesMsg delivers LSP states 和 per-服务端 diagnostic counts fetched
// off-thread.
type lspStatesMsg struct {
	states      map[string]workspace.LSPClientInfo
	diagnostics map[string]lsp.DiagnosticCounts
}

// LSPInfo wraps LSP 客户端 information 用 diagnostic counts 通过 severity.
type LSPInfo struct {
	workspace.LSPClientInfo
	Diagnostics map[protocol.DiagnosticSeverity]int
}

// requestLSPRefresh schedules off-thread refresh 的 memoized LSP
// state. While fetch 是 already 在 flight it 只 marks state dirty;
// applyLSPStates re-dispatches 所以 freshest data still lands.
func (m *UI) requestLSPRefresh() tea.Cmd {
	if m.lspFetchInFlight {
		m.lspRefreshQueued = true
		return nil
	}
	return m.dispatchLSPRefresh()
}

// dispatchLSPRefresh returns command 那 fetches LSP states 和
// per-服务端 diagnostic counts off 更新 goroutine (each synchronous
// HTTP round-trip 在 客户端/服务端 mode), delivering lspStatesMsg. 它
// returns nil while fetch 是 already 在 flight. closure captures 只
// locals (never m) 所以 it 是 safe off-thread.
func (m *UI) dispatchLSPRefresh() tea.Cmd {
	if m.lspFetchInFlight || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	m.lspFetchInFlight = true
	// Stamp check time 在 派发 too 所以 TTL backstop doesn't
	// keep re-requesting while 这 fetch 是 在 flight.
	m.lspCheckedAt = time.Now()
	ws := m.com.Workspace
	return func() tea.Msg {
		states := ws.LSPGetStates()
		diagnostics := make(map[string]lsp.DiagnosticCounts, len(states))
		for name := range states {
			diagnostics[name] = ws.LSPGetDiagnosticCounts(name)
		}
		return lspStatesMsg{states: states, diagnostics: diagnostics}
	}
}

// applyLSPStates stores off-thread LSP fetch 结果 和 re-dispatches
// 当 events arrived while it was 在 flight. Runs 在 更新 goroutine.
func (m *UI) applyLSPStates(msg lspStatesMsg) tea.Cmd {
	m.lspFetchInFlight = false
	m.lspCheckedAt = time.Now()
	m.lspStates = msg.states
	m.lspDiagnostics = msg.diagnostics
	if m.lspRefreshQueued {
		m.lspRefreshQueued = false
		return m.dispatchLSPRefresh()
	}
	return nil
}

// lspErrorCount returns the total diagnostic count across the memoized LSP
// states, shown 在 compact header.
func (m *UI) lspErrorCount() int {
	count := 0
	for _, info := range m.lspStates {
		count += info.DiagnosticCount
	}
	return count
}

// lspInfo renders LSP status section showing 活跃 LSP clients 和 their
// diagnostic counts. 它 renders 从 memoized state 只: 这 runs 在
// every frame, 和 工作区 probes behind it 是 synchronous HTTP
// round-trips 在 客户端/服务端 mode. LSP events (plus TTL backstop)
// keep the memoized state fresh off-thread; see requestLSPRefresh.
func (m *UI) lspInfo(width, maxItems int, isSection bool) string {
	t := m.com.Styles

	states := slices.SortedFunc(maps.Values(m.lspStates), func(a, b workspace.LSPClientInfo) int {
		return strings.Compare(a.Name, b.Name)
	})

	var lsps []LSPInfo
	for _, state := range states {
		counts := m.lspDiagnostics[state.Name]
		lsps = append(lsps, LSPInfo{LSPClientInfo: state, Diagnostics: map[protocol.DiagnosticSeverity]int{
			protocol.SeverityError:       counts.Error,
			protocol.SeverityWarning:     counts.Warning,
			protocol.SeverityHint:        counts.Hint,
			protocol.SeverityInformation: counts.Information,
		}})
	}

	title := t.Resource.Heading.Render("LSPs")
	if isSection {
		title = common.Section(t, title, width)
	}
	list := t.Resource.AdditionalText.Render("None")
	if len(lsps) > 0 {
		list = lspList(t, lsps, width, maxItems)
	}

	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// lspDiagnostics formats diagnostic counts 用 appropriate icons 和 colors.
func lspDiagnostics(t *styles.Styles, diagnostics map[protocol.DiagnosticSeverity]int) string {
	var errs []string
	if diagnostics[protocol.SeverityError] > 0 {
		errs = append(errs, t.LSP.ErrorDiagnostic.Render(fmt.Sprintf("%s%d", nerdfont.Glyph(styles.LSPErrorIcon, "E"), diagnostics[protocol.SeverityError])))
	}
	if diagnostics[protocol.SeverityWarning] > 0 {
		errs = append(errs, t.LSP.WarningDiagnostic.Render(fmt.Sprintf("%s%d", nerdfont.Glyph(styles.LSPWarningIcon, "W"), diagnostics[protocol.SeverityWarning])))
	}
	if diagnostics[protocol.SeverityHint] > 0 {
		errs = append(errs, t.LSP.HintDiagnostic.Render(fmt.Sprintf("%s%d", nerdfont.Glyph(styles.LSPHintIcon, "H"), diagnostics[protocol.SeverityHint])))
	}
	if diagnostics[protocol.SeverityInformation] > 0 {
		errs = append(errs, t.LSP.InfoDiagnostic.Render(fmt.Sprintf("%s%d", nerdfont.Glyph(styles.LSPInfoIcon, "I"), diagnostics[protocol.SeverityInformation])))
	}
	return strings.Join(errs, " ")
}

// lspList renders list 的 LSP clients 用 their status 和 diagnostics,
// truncating 到 maxItems 如果 needed.
func lspList(t *styles.Styles, lsps []LSPInfo, width, maxItems int) string {
	var renderedLsps []string
	for _, l := range lsps {
		var icon string
		title := t.Resource.Name.Render(l.Name)
		var description string
		var diagnostics string
		switch l.State {
		case lsp.StateUnstarted:
			icon = t.Resource.OfflineIcon.String()
			description = t.Resource.StatusText.Render("unstarted")
		case lsp.StateStopped:
			icon = t.Resource.OfflineIcon.String()
			description = t.Resource.StatusText.Render("stopped")
		case lsp.StateStarting:
			icon = t.Resource.BusyIcon.String()
			description = t.Resource.StatusText.Render("starting...")
		case lsp.StateReady:
			icon = t.Resource.OnlineIcon.String()
			diagnostics = lspDiagnostics(t, l.Diagnostics)
		case lsp.StateError:
			icon = t.Resource.ErrorIcon.String()
			description = t.Resource.StatusText.Render("error")
			if l.Error != nil {
				description = t.Resource.StatusText.Render(fmt.Sprintf("error: %s", l.Error.Error()))
			}
		case lsp.StateDisabled:
			icon = t.Resource.DisabledIcon.String()
			description = t.Resource.StatusText.Render("disabled")
		default:
			continue
		}
		renderedLsps = append(renderedLsps, common.Status(t, common.StatusOpts{
			Icon:         icon,
			Title:        title,
			Description:  description,
			ExtraContent: diagnostics,
		}, width))
	}

	return lipgloss.JoinVertical(lipgloss.Left, truncateRows(renderedLsps, maxItems, t.Resource.AdditionalText)...)
}
