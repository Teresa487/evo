package pinentry

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartIntegrationAndSocketRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("socket wrapper uses a POSIX sh script")
	}

	ctx := context.Background()

	cleanup, err := StartIntegration(ctx, os.Args[0], 0)
	require.NoError(t, err)
	defer func() {
		cleanup()
		DisableIntegration()
	}()
	require.True(t, IntegrationEnabled())

	// socket 路径 是 exported 到 spawned git processes via
	// GitWrapperEnv.
	var socketPath string
	for _, kv := range GitWrapperEnv() {
		if v, ok := strings.CutPrefix(kv, "CRUSH_PINENTRY_SOCK="); ok {
			socketPath = v
		}
	}
	require.NotEmpty(t, socketPath)

	// Answer prompts 从 默认 service, 作为 TUI would.
	prompts := DefaultPrompts()
	sub := prompts.Subscribe(ctx)
	answered := make(chan string, 1)
	go func() {
		ev := <-sub
		require.Equal(t, "GPG is waiting", ev.Payload.Prompt)
		require.True(t, prompts.Respond(ev.Payload.ID, "socket-secret"))
		close(answered)
	}()

	// The git wrapper subcommand fetches the credential over the socket.
	secret, err := AskCredentialOverSocket(ctx, socketPath, PromptRequest{
		Prompt: "GPG is waiting",
		Kind:   KindPassphrase,
	})
	require.NoError(t, err)
	require.Equal(t, "socket-secret", secret)
	<-answered

	// Cancellation round-trips 作为 ErrCancelled.
	go func() {
		ev := <-sub
		require.True(t, prompts.Cancel(ev.Payload.ID))
	}()
	_, err = AskCredentialOverSocket(ctx, socketPath, PromptRequest{
		Prompt: "GPG is waiting",
	})
	require.ErrorIs(t, err, ErrCancelled)

	// 一个 wrapper 那 hangs up (它的 GPG command was cancelled) drops
	// 提示词, 所以 stale dialog 是 dismissed 和 next can open.
	notifs := prompts.SubscribeNotifications(ctx)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(conn).Encode(socketRequest{Request: PromptRequest{ID: "gone"}}))
	require.Equal(t, "gone", (<-sub).Payload.ID)
	require.NoError(t, conn.Close())
	require.Equal(t, "gone", (<-notifs).Payload.RequestID)

	cleanup()
	require.False(t, IntegrationEnabled(), "cleanup disables integration")
	require.NoFileExists(t, socketPath, "socket is removed on cleanup")
}
