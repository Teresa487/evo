// Package util provides utility functions 为 UI 消息 handling.
package util

import (
	"context"
	"errors"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"
	"mvdan.cc/sh/v3/shell"
)

type Cursor interface {
	Cursor() *tea.Cursor
}

func CmdHandler(msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return msg
	}
}

func ReportError(err error) tea.Cmd {
	return CmdHandler(NewErrorMsg(err))
}

type InfoType int

const (
	InfoTypeInfo InfoType = iota
	InfoTypeSuccess
	InfoTypeWarn
	InfoTypeError
	InfoTypeUpdate
	InfoTypePlan
	InfoTypeYolo
	InfoTypeSysadmin
)

func NewInfoMsg(info string) InfoMsg {
	return InfoMsg{
		Type: InfoTypeInfo,
		Msg:  info,
	}
}

func NewWarnMsg(warn string) InfoMsg {
	return InfoMsg{
		Type: InfoTypeWarn,
		Msg:  warn,
	}
}

func NewErrorMsg(err error) InfoMsg {
	return InfoMsg{
		Type: InfoTypeError,
		Msg:  err.Error(),
	}
}

func ReportInfo(info string) tea.Cmd {
	return CmdHandler(NewInfoMsg(info))
}

func ReportWarn(warn string) tea.Cmd {
	return CmdHandler(NewWarnMsg(warn))
}

type (
	InfoMsg struct {
		Type InfoType
		Msg  string
		TTL  time.Duration
	}
	// ClearStatusMsg retires status 消息 用 序号 Seq.
	ClearStatusMsg struct{ Seq uint64 }
)

// IsEmpty checks 如果 [InfoMsg] 是 empty.
func (m InfoMsg) IsEmpty() bool {
	var zero InfoMsg
	return m == zero
}

// ExecShell parses shell command string 和 executes it 用 exec.Command.
// Uses shell.Fields 为 proper handling 的 shell syntax like quotes 和
// arguments while preserving TTY handling 为 终态 editors.
func ExecShell(ctx context.Context, cmdStr string, callback tea.ExecCallback) tea.Cmd {
	fields, err := shell.Fields(cmdStr, nil)
	if err != nil {
		return ReportError(err)
	}
	if len(fields) == 0 {
		return ReportError(errors.New("empty command"))
	}

	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	return tea.ExecProcess(cmd, callback)
}
