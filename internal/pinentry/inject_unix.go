//go:build darwin || linux || freebsd || netbsd

package pinentry

import (
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// ctrlL 是 byte 那 makes ncurses-based pinentry dialogs redraw
// their UI.
const ctrlL = 0x0C

// injectCtrlLRedraw writes Ctrl-L into controlling 终态 所以
// curses-based pinentry dialog redraws 它的 UI, approximating 用户
// pressing Ctrl-L. Injection 是 best-effort: there 是 no recovery 为
// caller, 所以 failures 是 只 logged; visible hint printed 在
// 终态 remains 作为 fallback. See [InjectCtrlL].
func injectCtrlLRedraw() {
	if err := InjectCtrlL(); err != nil {
		slog.Debug("Failed to inject Ctrl-L for pinentry redraw", "error", err)
	}
}

// InjectCtrlL writes Ctrl-L byte into controlling 终态's input
// 队列 via TIOCSTI, causing curses-based pinentry dialog 到 redraw.
// 这 是 needed because 终态 handover can erase dialog frame
// 那 pinentry drew while Crush still owned screen, 和 pinentry
// does 不 redraw 在 SIGWINCH.
//
// 它 returns 错误 当 TIOCSTI 是 unavailable 或 disabled (some Linux
// kernels boot 用 dev.tty.legacy_tiocsti=0); callers should treat 那
// 作为 best-effort failure 和 keep any fallback hint visible.
func InjectCtrlL() error {
	// O_RDWR 是 required: macOS denies TIOCSTI 在 写入-只 tty handles.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/tty: %w", err)
	}
	defer tty.Close() //nolint:errcheck

	// TIOCSTI takes 指针 到 byte; IoctlSetPointerInt passes
	// 指针 到 int whose first byte kernel reads (all supported
	// platforms 是 little-endian), 所以 byte 是 queued 作为 如果 typed 在
	// 终态.
	if err := unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCSTI, ctrlL); err != nil {
		return fmt.Errorf("TIOCSTI ioctl failed: %w", err)
	}
	return nil
}
