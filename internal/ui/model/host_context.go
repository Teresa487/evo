package model

import (
	"context"
	"time"

	"github.com/charmbracelet/crush/internal/workspace"
)

// hostOpenFiles returns function 那 lists files host application
// has open, 用 文件 用户 是 在 first, 为 文件 completions 到
// list first. 它 asks 工作区 each time it 是 called, 和 returns none
// 当 那 fails 或 是 slow.
func hostOpenFiles(ws workspace.Workspace) func() []string {
	return func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		hc, err := ws.HostContext(ctx)
		if err != nil {
			return nil
		}
		if hc.ActiveFile == "" {
			return hc.OpenFiles
		}
		return append([]string{hc.ActiveFile}, hc.OpenFiles...)
	}
}
