// landing.go —— 没有选中 session 时的欢迎页。

package model

import (
	"image"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/ultraviolet/layout"
)

// selectedLargeModel returns currently selected large language 模型 作为
// memoized 通过 off-thread busy/agent probe (see workspace_cache.go), 或
// nil 当 agent isn't ready. 它 must never probe 工作区: it 是
// called 在 every frame 和 AgentIsReady/AgentModel 是 synchronous HTTP
// round-trips 在 客户端/服务端 mode.
func (m *UI) selectedLargeModel() *workspace.AgentModel {
	if m.agentReady {
		model := m.agentModel
		return &model
	}
	return nil
}

// landingView 渲染 落地页 view showing current working
// 目录, 模型 information, 和 LSP/MCP status 在 two-column layout.
func (m *UI) landingView() string {
	t := m.com.Styles
	width := m.layout.main.Dx()
	cwd := common.PrettyPathInline(t, m.com.Workspace.WorkingDir(), m.gitBranchLabel(), width)

	parts := []string{
		cwd,
	}

	parts = append(parts, "", m.modelInfo(width))
	infoSection := lipgloss.JoinVertical(lipgloss.Left, parts...)

	var remainingHeightArea image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(infoSection)+1),
		layout.Fill(1),
	).Split(m.layout.main).Assign(new(image.Rectangle), &remainingHeightArea)

	// Channels 是 experimental 和 opt-在, 所以 their column 只 appears
	// once 服务端 是 opted 在; otherwise other columns keep their
	// full width.
	channels := m.channelStatusItems()
	columns := 3
	if len(channels) > 0 {
		columns = 4
	}
	mcpLspSectionWidth := min(30, (width-(columns-1))/columns)
	sectionHeight := max(1, remainingHeightArea.Dy())

	lspSection := m.lspInfo(mcpLspSectionWidth, sectionHeight, false)
	mcpSection := m.mcpInfo(mcpLspSectionWidth, sectionHeight, false)
	skillsSection := m.skillsInfo(mcpLspSectionWidth, sectionHeight, false)
	sections := []string{lspSection, " ", mcpSection, " ", skillsSection}
	if len(channels) > 0 {
		sections = append(sections, " ", m.channelsInfo(channels, mcpLspSectionWidth, sectionHeight, false))
	}

	content := lipgloss.JoinHorizontal(lipgloss.Left, sections...)

	return lipgloss.NewStyle().
		Width(width).
		Height(m.layout.main.Dy() - 1).
		PaddingTop(1).
		Render(
			lipgloss.JoinVertical(lipgloss.Left, infoSection, "", content),
		)
}
