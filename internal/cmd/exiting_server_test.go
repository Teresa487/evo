package cmd

import (
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/stretchr/testify/require"
)

// TestAwaitExitingServerGone_WaitsForRelease guards clients waiting 在
// exiting tcp 服务端 那 will never 是 ready, then spawning replacement
// while it still holds address, 所以 replacement cannot bind.
func TestAwaitExitingServerGone_WaitsForRelease(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := server.NewServer(nil, "tcp", ln.Addr().String())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	hostURL := &url.URL{Scheme: "tcp", Host: ln.Addr().String()}
	require.NoError(t, srv.Backend().ShutdownWorkspaces(t.Context()))

	start := time.Now()
	require.ErrorIs(t, waitForServerReady(t.Context(), hostURL, nil), client.ErrServerShuttingDown)
	require.Less(t, time.Since(start), 5*time.Second, "waited on an exiting server")

	done := make(chan error, 1)
	go func() { done <- awaitExitingServerGone(t.Context(), hostURL) }()
	select {
	case err := <-done:
		t.Fatalf("stopped waiting while the exiting server held the address: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, srv.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("kept waiting after the exiting server released the address")
	}
}
