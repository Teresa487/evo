package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// infoMsgFrom runs command 和 returns InfoMsg it carries.
func infoMsgFrom(t *testing.T, m *UI, target permission.PermissionMode) util.InfoMsg {
	t.Helper()
	cmd := m.toggleModeAndReport(target)
	require.NotNil(t, cmd, "a mode toggle has to announce itself")
	info, ok := cmd().(util.InfoMsg)
	require.True(t, ok, "a mode toggle reports through an InfoMsg")
	return info
}

// TestPermissionModeBannersMatchTheCycle pins thing 那 made sysadmin
// look wrong: reached 从 command palette it announced itself 作为
// plain warning, while very same mode reached 从 Shift+Tab cycle
// showed badge. One mode has 到 look like one mode however it 是 reached.
func TestPermissionModeBannersMatchTheCycle(t *testing.T) {
	pinTTLs(t)

	for _, tc := range []struct {
		name string
		mode permission.PermissionMode
		want util.InfoType
		msg  string
	}{
		{"sysadmin", permission.PermissionModeSysadmin, util.InfoTypeSysadmin, sysadminModeBannerMsg},
		{"yolo", permission.PermissionModeYolo, util.InfoTypeYolo, yoloModeBannerMsg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := &countingWorkspace{ready: true, permMode: permission.PermissionModeNormal}
			info := infoMsgFrom(t, newBusyUI(ws), tc.mode)

			require.Equal(t, tc.want, info.Type, "the palette must raise the same banner as the cycle")
			require.Equal(t, tc.msg, info.Msg, "both roads into a mode say the same thing")
		})
	}
}

// TestLeavingAPermissionModeIsOrdinaryNews keeps badge 为 state
// worth flagging. Dropping back 到 normal 是 不 warning 和 must 不
// borrow banner 的 mode being left.
func TestLeavingAPermissionModeIsOrdinaryNews(t *testing.T) {
	pinTTLs(t)

	for _, mode := range []permission.PermissionMode{
		permission.PermissionModeSysadmin,
		permission.PermissionModeYolo,
	} {
		ws := &countingWorkspace{ready: true, permMode: mode}
		info := infoMsgFrom(t, newBusyUI(ws), mode)

		require.Equal(t, util.InfoTypeInfo, info.Type, "leaving %v is not a banner", mode)
		require.Contains(t, info.Msg, "normal", "the message names where the toggle landed")
	}
}

// TestSysadminBannerSaysWhatItCosts guards the wording. The badge alone
// names mode; 消息 是 只 place 那 says what entering it
// actually gives away.
func TestSysadminBannerSaysWhatItCosts(t *testing.T) {
	t.Parallel()
	require.Contains(t, sysadminModeBannerMsg, "unblocked")
}
