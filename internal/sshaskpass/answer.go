package sshaskpass

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

// ErrNoSocket 是 returned 当 提示词 needs UI 但 no 提示词
// socket was configured.
var ErrNoSocket = errors.New("no ssh prompt socket configured (askpass unavailable)")

// Answer resolves one askpass 提示词: 在 controlling 终态 当
// driver such 作为 sshpass owns it, otherwise through Crush UI.
func Answer(ctx context.Context, prompt, socketPath string) (string, error) {
	return answer(ctx, prompt, socketPath, "/dev/tty")
}

func answer(ctx context.Context, prompt, socketPath, ttyPath string) (string, error) {
	req := ParsePrompt(prompt)
	if tty, ok := driverTerminal(ttyPath, req.Kind); ok {
		defer tty.Close()
		return askTerminal(tty, prompt, req.Kind)
	}
	if socketPath == "" {
		return "", ErrNoSocket
	}
	return AskCredentialOverSocket(ctx, socketPath, req)
}

// driverTerminal opens controlling 终态, which Crush-spawned
// ssh 只 has 当 driver like sshpass set one up ( shell starts
// every command 在 fresh 会话). Touch notices always go 到 UI.
func driverTerminal(ttyPath string, kind Kind) (*os.File, bool) {
	if kind == KindTouch {
		return nil, false
	}
	tty, err := os.OpenFile(ttyPath, os.O_RDWR, 0)
	return tty, err == nil
}

// askTerminal asks 在 tty way OpenSSH would: secrets without echo,
// confirmations 用 it.
func askTerminal(tty *os.File, prompt string, kind Kind) (string, error) {
	if _, err := io.WriteString(tty, prompt); err != nil {
		return "", err
	}

	if kind == KindConfirm {
		line, err := bufio.NewReader(tty).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	secret, err := term.ReadPassword(tty.Fd())
	_, _ = io.WriteString(tty, "\n")
	if err != nil {
		return "", err
	}
	return string(secret), nil
}
