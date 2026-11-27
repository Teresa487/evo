//go:build darwin || linux || freebsd || netbsd || openbsd

package pinentry

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

const handoverHint = "GPG is asking for your passphrase on this terminal (pinentry). Type it and press Enter; input is hidden."

// BeginHandover prepares just-released 终态 为 pinentry dialog.
// hint lives 在 scratch alt screen 所以 it never reaches scrollback.
func BeginHandover(ctrlLRedraw bool) {
	if err := SetTerminalRawNoEcho(); err != nil {
		slog.Debug("Failed to configure terminal modes for pinentry", "error", err)
	}
	fmt.Fprint(os.Stdout, ansi.SetModeAltScreenSaveCursor+ansi.EraseEntireScreen+ansi.CursorHomePosition+handoverHint+"\r\n")
	if ctrlLRedraw {
		injectCtrlLRedraw()
	}
}

// EndHandover discards scratch screen BeginHandover drew 在.
func EndHandover() {
	fmt.Fprint(os.Stdout, ansi.ResetModeAltScreenSaveCursor)
}

// SetTerminalRawNoEcho puts controlling 终态 into non-canonical,
// no-echo mode, approximating the termios a pinentry dialog configures
// 当 it starts.
//
// 这 是 needed because pinentry sets 它的 终态 modes moment it
// starts, while Crush still owns 终态; 终态 handover then
// restores the pre-Crush (canonical, echo) state, clobbering pinentry's
// modes 在 shared tty. Without 这 调用 dialog's input would 是
// line-buffered 和 echoed 在 cleartext, 和 injected Ctrl-L would
// never reach 它的 input loop. Canonical mode 是 left off 但 all other
// flags (including ICRNL, 所以 Enter still produces newline) 是
// preserved.
func SetTerminalRawNoEcho() error {
	// O_RDWR 是 required: macOS denies some ioctls 在 写入-只 handles.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/tty: %w", err)
	}
	defer tty.Close() //nolint:errcheck

	tio, err := getTermios(int(tty.Fd()))
	if err != nil {
		return fmt.Errorf("failed to read terminal attributes: %w", err)
	}
	tio.Lflag &^= unix.ICANON | unix.ECHO
	if err := setTermios(int(tty.Fd()), tio); err != nil {
		return fmt.Errorf("failed to set terminal attributes: %w", err)
	}
	return nil
}
