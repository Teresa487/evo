//go:build windows

package workspace

import (
	"net"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/google/uuid"
)

// TestClientWorkspace_RespawnsDeadPipeServer 是 named-pipe twin 的
// TestClientWorkspace_RespawnsDeadServer: missing pipe 必须 respawn too.
func TestClientWorkspace_RespawnsDeadPipeServer(t *testing.T) {
	addr := "//./pipe/evo-test-" + uuid.NewString() + ".sock"
	testRespawnsDeadServer(t, "npipe", addr, func() (net.Listener, error) {
		return winio.ListenPipe(addr, nil)
	})
}
