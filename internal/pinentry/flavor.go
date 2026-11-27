package pinentry

import (
	"fmt"
	"path/filepath"
)

// flavor describes pinentry program's 终态 behavior.
type flavor uint8

const (
	// flavorGUI 是 pinentry 那 does 不 use 终态 (GUI flavors
	// like pinentry-mac 或 pinentry-qt): no 终态 handover needed.
	flavorGUI flavor = iota
	// flavorCurses 是 ncurses-based pinentry: it takes over
	// 终态 和 redraws 它的 dialog 当 it receives Ctrl-L.
	flavorCurses
	// flavorTTY 是 line-based pinentry: it takes over 终态, 但
	// Ctrl-L must 不 是 injected because it reads input 作为 plain bytes
	// 和 would treat byte 作为 passphrase content.
	flavorTTY
)

// guiPinentryNames 是 resolved basenames 的 pinentry flavors 那 render
// their own 窗口 和 never touch 终态.
var guiPinentryNames = map[string]struct{}{
	"pinentry-mac":    {},
	"pinentry-qt":     {},
	"pinentry-qt5":    {},
	"pinentry-qt6":    {},
	"pinentry-gnome3": {},
	"pinentry-gtk":    {},
	"pinentry-gtk2":   {},
	"pinentry-fltk":   {},
	"pinentry-efl":    {},
	"pinentry-w32":    {},
}

// resolveFlavor determines 终态 behavior 的 detected pinentry
// process. plain "pinentry" dispatcher 是 usually symlink (Linux
// alternatives) 或 curses 二进制 (Homebrew), 所以 它的 symlink chain 是
// resolved 到 find real implementation.
func resolveFlavor(p Proc) flavor {
	switch p.Name {
	case "pinentry-curses":
		return flavorCurses
	case "pinentry-tty":
		return flavorTTY
	}
	if _, ok := guiPinentryNames[p.Name]; ok {
		return flavorGUI
	}

	if target := resolveExe(p); target != "" {
		// filepath.Base, 不 路径.Base: resolved 路径 may use
		// backslashes 在 Windows.
		base := filepath.Base(target)
		switch base {
		case "pinentry-curses":
			return flavorCurses
		case "pinentry-tty":
			return flavorTTY
		}
		if _, ok := guiPinentryNames[base]; ok {
			return flavorGUI
		}
	}

	// 一个 plain "pinentry" 那 does 不 resolve 到 known flavor 是
	// commonly the curses build itself (e.g. Homebrew).
	return flavorCurses
}

// resolveExe resolves executable 路径 的 process, following
// symlinks. 它 uses full command 路径 当 process 表 provides
// one (e.g. ps 在 macOS) 和 falls back 到 /proc 在 Linux. Windows has
// neither, 所以 flavor falls back 到 name-based defaults there.
func resolveExe(p Proc) string {
	target := p.Path
	if target == "" {
		if !procExeSupported || p.PID <= 0 {
			return ""
		}
		target = fmt.Sprintf("/proc/%d/exe", p.PID)
	}
	if target == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return ""
	}
	return resolved
}
