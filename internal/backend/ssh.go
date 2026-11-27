package backend

import (
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/sshaskpass"
)

// AnswerSSH resolves pending integrated SSH 提示词 用 用户's
// secret. returned bool reports whether 这 调用 resolved
// pending 提示词 (true) 或 found it already resolved 通过 previous
// caller (false).
func (b *Backend) AnswerSSH(workspaceID string, req proto.SSHAnswer) (bool, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return false, err
	}
	return sshaskpass.DefaultPrompts().Respond(req.RequestID, req.Secret), nil
}

// CancelSSH dismisses pending integrated SSH 提示词. Returns true
// 如果 提示词 was cancelled, false 如果 none was pending.
func (b *Backend) CancelSSH(workspaceID string, req proto.SSHCancel) (bool, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return false, err
	}
	return sshaskpass.DefaultPrompts().Cancel(req.RequestID), nil
}
