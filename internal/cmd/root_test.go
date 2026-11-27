package cmd

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

func TestHasTerminal(t *testing.T) {
	t.Parallel()

	const stdin, stdout uintptr = 0, 1
	tests := []struct {
		name      string
		terminals map[uintptr]bool
		want      bool
	}{
		{"both terminals", map[uintptr]bool{stdin: true, stdout: true}, true},
		{"piped stdin", map[uintptr]bool{stdout: true}, true},
		{"redirected stdout", map[uintptr]bool{stdin: true}, true},
		{"no terminal", map[uintptr]bool{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			isTerminal := func(fd uintptr) bool { return tt.terminals[fd] }
			require.Equal(t, tt.want, hasTerminal(isTerminal, stdin, stdout))
		})
	}
}

// TestWarnPermissionMode covers 权限 mode warnings 在 客户端/服务端
// mode, where project 服务端 already has open keeps 它的 mode: -yy must
// 不 claim sysadmin mode it did 不 get, 和 project earlier 客户端
// left 在 sysadmin mode must 不 运行 那 way without word.
func TestWarnPermissionMode(t *testing.T) {
	t.Parallel()

	const (
		normal   = proto.WorkspacePermissionModeNormal
		yolo     = proto.WorkspacePermissionModeYolo
		sysadmin = proto.WorkspacePermissionModeSysadmin
	)
	tests := []struct {
		name              string
		requested, actual proto.WorkspacePermissionMode
		wantKept          bool
		wantSysadmin      bool
	}{
		{"normal", normal, normal, false, false},
		{"sysadmin granted", sysadmin, sysadmin, false, true},
		{"sysadmin left by another client", normal, sysadmin, true, true},
		{"sysadmin asked of a normal project", sysadmin, normal, true, false},
		{"yolo asked of a normal project", yolo, normal, true, false},
		{"server too old to report the mode", sysadmin, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			warnPermissionMode(&out, tt.requested, tt.actual)
			require.Equal(t, tt.wantKept, bytes.Contains(out.Bytes(), []byte("already open on the server")), out.String())
			require.Equal(t, tt.wantSysadmin, bytes.Contains(out.Bytes(), []byte(sysadminModeWarning)), out.String())
		})
	}
}
