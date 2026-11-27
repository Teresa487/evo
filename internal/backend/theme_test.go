package backend

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// nextHostThemeEvent returns next host theme 事件 published 在
// 工作区 总线, 或 fails after short wait.
func nextHostThemeEvent(t *testing.T, events <-chan pubsub.Event[tea.Msg]) pubsub.Event[proto.HostThemeState] {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if e, ok := ev.Payload.(pubsub.Event[proto.HostThemeState]); ok {
				return e
			}
		case <-timeout:
			require.FailNow(t, "no host theme event published")
		}
	}
}

func TestSetHostTheme(t *testing.T) {
	t.Parallel()
	b, ws, _ := newPromptsTestBackend(t)
	events := ws.Events(t.Context())

	theme := &proto.HostTheme{Name: "first", Palette: map[string]string{"primary": "#6B50FF"}}
	st, err := b.SetHostTheme(ws.ID, theme)
	require.NoError(t, err)
	require.Equal(t, ws.ID, st.WorkspaceID)
	require.True(t, theme.Equal(st.Theme))
	ev := nextHostThemeEvent(t, events)
	require.Equal(t, pubsub.UpdatedEvent, ev.Type)
	require.True(t, theme.Equal(ev.Payload.Theme))

	// 存储 theme 是 copy: caller's later edits do 不 leak 在.
	theme.Palette["primary"] = "#000000"
	got, err := b.GetHostTheme(ws.ID)
	require.NoError(t, err)
	require.Equal(t, "#6B50FF", got.Theme.Palette["primary"])

	// An equal theme 是 no-op ( empty map equals missing one):
	// next 事件 published 是 second theme's.
	same := &proto.HostTheme{Name: "first", Palette: map[string]string{"primary": "#6B50FF"}, Syntax: map[string]string{}}
	_, err = b.SetHostTheme(ws.ID, same)
	require.NoError(t, err)
	_, err = b.SetHostTheme(ws.ID, &proto.HostTheme{Name: "second"})
	require.NoError(t, err)
	require.Equal(t, "second", nextHostThemeEvent(t, events).Payload.Theme.Name)

	st, err = b.SetHostTheme(ws.ID, nil)
	require.NoError(t, err)
	require.Nil(t, st.Theme)
	ev = nextHostThemeEvent(t, events)
	require.Equal(t, pubsub.DeletedEvent, ev.Type)
	require.Nil(t, ev.Payload.Theme)
}

func TestSetHostTheme_Errors(t *testing.T) {
	t.Parallel()
	b, ws, _ := newPromptsTestBackend(t)

	_, err := b.SetHostTheme(ws.ID, &proto.HostTheme{Palette: map[string]string{"primary": "#6b50ffff"}})
	require.ErrorIs(t, err, ErrInvalidTheme)
	require.ErrorContains(t, err, "palette.primary")

	_, err = b.SetHostTheme(ws.ID, &proto.HostTheme{Base: "definitely-not-a-theme-3f9a"})
	require.ErrorIs(t, err, ErrInvalidTheme)

	got, err := b.GetHostTheme(ws.ID)
	require.NoError(t, err)
	require.Nil(t, got.Theme, "a rejected theme is not stored")

	_, err = b.SetHostTheme("missing", nil)
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
	_, err = b.GetHostTheme("missing")
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}
