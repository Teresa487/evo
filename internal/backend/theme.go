package backend

import (
	"fmt"
	"log/slog"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// hostThemeState returns 工作区's current host theme.
func (w *Workspace) hostThemeState() proto.HostThemeState {
	w.themeMu.Lock()
	defer w.themeMu.Unlock()
	return proto.HostThemeState{WorkspaceID: w.ID, Theme: w.hostTheme}
}

// GetHostTheme returns host theme 的 given 工作区. 一个 nil Theme
// means none 是 set.
func (b *Backend) GetHostTheme(workspaceID string) (proto.HostThemeState, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.HostThemeState{}, err
	}
	return ws.hostThemeState(), nil
}

// SetHostTheme sets theme host application supplies 为 every 客户端
// 的 工作区, 或 clears it 当 t 是 nil, 和 publishes host_theme
// 事件 (updated 或 deleted) 到 all 的 them. Setting theme equal 到
// current one 是 no-op 那 publishes nothing. Invalid colors 和
// unknown base theme fail 用 [ErrInvalidTheme]; unknown keys 是 ignored.
// 它 returns state after 调用.
func (b *Backend) SetHostTheme(workspaceID string, t *proto.HostTheme) (proto.HostThemeState, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.HostThemeState{}, err
	}
	if t != nil {
		// Validation may 读取 用户 theme files 为 base, 所以 it runs
		// before 锁 是 taken.
		ignored, err := styles.ValidateHostTheme(t.Base, t.Palette, t.Syntax)
		if err != nil {
			return proto.HostThemeState{}, fmt.Errorf("%w: %w", ErrInvalidTheme, err)
		}
		if len(ignored) > 0 {
			slog.Debug("Ignoring unknown host theme keys", "workspace", workspaceID, "keys", ignored)
		}
		t = t.Clone()
	}

	ws.themeMu.Lock()
	defer ws.themeMu.Unlock()
	st := proto.HostThemeState{WorkspaceID: ws.ID, Theme: t}
	if ws.hostTheme.Equal(t) {
		st.Theme = ws.hostTheme
		return st, nil
	}
	ws.hostTheme = t
	// 发布 under 锁 所以 concurrent sets reach subscribers 在
	// order they were 存储. Publishing never blocks.
	if ws.App != nil { // Synthetic test workspaces have no App.
		typ := pubsub.UpdatedEvent
		if t == nil {
			typ = pubsub.DeletedEvent
		}
		ws.SendEvent(pubsub.Event[proto.HostThemeState]{Type: typ, Payload: st})
	}
	return st, nil
}
