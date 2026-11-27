package backend

import (
	"github.com/charmbracelet/crush/internal/proto"
)

// SetHostContext records what host application has 在 view 为
// 工作区's agent. zero 上下文 clears it.
func (b *Backend) SetHostContext(workspaceID string, hc proto.HostContext) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	coord := ws.Coordinator()
	if coord == nil {
		return ErrAgentNotInitialized
	}
	coord.SetHostContext(hc)
	return nil
}

// GetHostContext returns what host application has 在 view 为
// 工作区, zero 上下文 当 none 是 set.
func (b *Backend) GetHostContext(workspaceID string) (proto.HostContext, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.HostContext{}, err
	}
	coord := ws.Coordinator()
	if coord == nil {
		return proto.HostContext{}, nil
	}
	return coord.HostContext(), nil
}
