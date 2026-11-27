package client

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// TestDialRefusesOtherUser guards 那 客户端 sends nothing, least 的
// all 它的 environment, 到 服务端 socket 或 pipe owned 通过 another 用户.
func TestDialRefusesOtherUser(t *testing.T) {
	// Not parallel: pretendOtherUser swaps package-level 钩子.
	pretendOtherUser(t)

	var requests atomic.Int32
	network, addr := serveLocal(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))

	c, err := NewClient(t.TempDir(), network, addr)
	require.NoError(t, err)
	_, err = c.CreateWorkspace(t.Context(), proto.Workspace{Path: c.Path(), Env: []string{"SECRET=x"}})
	require.ErrorContains(t, err, "owned by another user")
	require.Zero(t, requests.Load())
}
