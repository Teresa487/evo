package dialog

import (
	"image"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestPermissions(t *testing.T) *Permissions {
	t.Helper()
	return newTestPermissionsWithDanger(t, "")
}

// newTestPermissionsWithDanger builds dialog 为 请求 flagged 用
// given danger reason. An empty reason means ordinary 请求.
func newTestPermissionsWithDanger(t *testing.T, danger string) *Permissions {
	t.Helper()
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:         "perm-test",
		ToolCallID: "tool-call-test",
		ToolName:   "bash",
		Danger:     danger,
	}
	return NewPermissions(com, perm)
}

// TestPermissions_ActionKeysResolve verifies 那 action keys produce
// correct 权限 响应.
func TestPermissions_ActionKeysResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    tea.KeyPressMsg
		action PermissionAction
	}{
		{keyMsg('a'), PermissionAllow},
		{keyMsg('A'), PermissionAllow},
		{keyMsg('d'), PermissionDeny},
		{keyMsg('D'), PermissionDeny},
		{keyMsg('s'), PermissionAllowForSession},
		{keyMsg('S'), PermissionAllowForSession},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		action := p.HandleMsg(tc.key)
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q should produce ActionPermissionResponse", tc.key.Text)
		require.Equal(t, tc.action, resp.Action)
	}
}

// TestPermissions_NavigationCyclesOptions verifies 那 tab 和 arrow keys
// cycle through three 权限 options.
func TestPermissions_NavigationCyclesOptions(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	require.Equal(t, 0, p.selectedOption)

	// Tab cycles forward.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 2, p.selectedOption)

	// Wrap around.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 0, p.selectedOption)

	// Left cycles backward.
	p.HandleMsg(keyMsg('h'))
	require.Equal(t, 2, p.selectedOption)
}

// TestPermissions_EnterConfirmsSelection verifies 那 enter confirms
// currently selected option.
func TestPermissions_EnterConfirmsSelection(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 1 // Allow for session.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionAllowForSession, resp.Action)
}

// TestPermissions_DefaultSelectionAvoidsApprovingDangerousRequests verifies
// 那 ordinary 请求 starts 在 Allow while dangerous one starts 在
// Deny, 所以 reflexive enter refuses instead 的 approving.
func TestPermissions_DefaultSelectionAvoidsApprovingDangerousRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		danger string
		action PermissionAction
	}{
		{"ordinary request", "", PermissionAllow},
		{"dangerous request", "sudo", PermissionDeny},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newTestPermissionsWithDanger(t, tc.danger)
			require.Equal(t, optionIndex(tc.action), p.selectedOption)

			// selection 是 只 meaningful 如果 confirming it lands 在
			// the matching action.
			action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
			resp, ok := action.(ActionPermissionResponse)
			require.True(t, ok)
			require.Equal(t, tc.action, resp.Action)
		})
	}
}

// TestPermissions_CtrlYDoesNotRespond verifies 那 ctrl+y, global
// yolo-mode toggle, does 不 resolve dialog. Dialogs see keys before
// global 处理器, 所以 accepting it here would silently approve whatever
// 是 在 screen.
func TestPermissions_CtrlYDoesNotRespond(t *testing.T) {
	t.Parallel()

	for _, danger := range []string{"", "sudo"} {
		p := newTestPermissionsWithDanger(t, danger)
		action := p.HandleMsg(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
		require.Nil(t, action, "ctrl+y should not resolve the permission dialog")
	}
}

// TestPermissions_DangerousButtonsLookDifferent verifies 那 approving
// buttons 是 styled distinctly 在 dangerous 请求, 所以 提示词 does
// 不 look identical 到 routine one.
func TestPermissions_DangerousButtonsLookDifferent(t *testing.T) {
	t.Parallel()

	ordinary := newTestPermissionsWithDanger(t, "")
	dangerous := newTestPermissionsWithDanger(t, "sudo")

	// Compare same selection 所以 只 danger styling differs.
	ordinary.selectedOption = optionIndex(PermissionAllow)
	dangerous.selectedOption = optionIndex(PermissionAllow)

	require.NotEqual(t, ordinary.renderButtonGroup("  "), dangerous.renderButtonGroup("  "))
}

// TestPermissions_EscapeDenies verifies 那 escape denies 请求.
func TestPermissions_EscapeDenies(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
}

// TestPermissions_TallerViewportClampsOffset verifies 那 growing
// dialog's height while scrolled 到 bottom does 不 leave viewport
// scrolled past end 用 blank rows under content.
func TestPermissions_TallerViewportClampsOffset(t *testing.T) {
	t.Parallel()
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i)
	}
	p := NewPermissions(com, permission.PermissionRequest{
		ID:          "perm-test",
		ToolName:    "custom_tool",
		Description: strings.Join(lines, "\n"),
	})

	p.Draw(uv.NewScreenBuffer(250, 25), image.Rect(0, 0, 250, 25))
	p.viewport.GotoBottom()
	p.Draw(uv.NewScreenBuffer(250, 60), image.Rect(0, 0, 250, 60))

	maxOffset := max(0, p.viewport.TotalLineCount()-p.viewport.Height())
	require.LessOrEqual(t, p.viewport.YOffset(), maxOffset)
}

// TestPermissions_BashSessionButtonNamesProgram verifies 那 会话
// button names program 当 它的 grant covers every 调用 的 it, 和 keeps
// plain label 当 grant 是 keyed 到 exact command.
func TestPermissions_BashSessionButtonNamesProgram(t *testing.T) {
	t.Parallel()
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}

	buttons := func(program string) string {
		p := NewPermissions(com, permission.PermissionRequest{
			ID:       "perm-test",
			ToolName: tools.BashToolName,
			Params: tools.BashPermissionsParams{
				Command:        "git commit -m x",
				SessionProgram: program,
			},
		})
		return ansi.Strip(p.renderButtonGroup("  "))
	}

	require.Contains(t, buttons("git"), "Allow git for Session")
	require.Contains(t, buttons(""), "Allow for Session")
	require.NotContains(t, buttons(""), "git")
}
