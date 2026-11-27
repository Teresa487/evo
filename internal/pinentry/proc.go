package pinentry

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// terminalPinentryNames 是 pinentry program basenames 那 render
// directly 在 终态 和 therefore need 终态 handover. GUI
// flavors (pinentry-mac, pinentry-qt, pinentry-gnome3, pinentry-gtk, ...)
// 是 deliberately excluded: they do 不 touch 终态. plain
// "pinentry" dispatcher 是 included because it 是 commonly curses build
// (e.g. Homebrew) 或 distribution alternative symlink.
var terminalPinentryNames = map[string]struct{}{
	"pinentry":        {},
	"pinentry-curses": {},
	"pinentry-tty":    {},
}

// gpgNames 是 basenames 的 gpg binaries, watched after passphrase
// dialog closes 到 detect security key touch wait.
var gpgNames = map[string]struct{}{
	"gpg":  {},
	"gpg2": {},
}

// Proc 是 process 表 snapshot 条目.
type Proc struct {
	// PID 是 process ID.
	PID int
	// Name 是 basename 的 process executable.
	Name string
	// 路径 是 full 路径 的 process executable 当 process
	// 表 provides one (e.g. ps comm 在 macOS), empty otherwise.
	Path string
}

// Lister returns snapshot 的 process 表.
type Lister func(ctx context.Context) ([]Proc, error)

// psLister lists processes via ps(1). On Windows it returns nothing:
// pinentry flavors there 是 GUI programs 那 never need 终态.
func psLister(ctx context.Context) ([]Proc, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list processes: %w", err)
	}
	return parsePS(out), nil
}

// parsePS parses output 的 `ps -eo pid=,comm=`. comm column 是
// full 路径 在 some platforms (macOS) 和 bare name 在 others (Linux),
// 和 it may contain spaces, 所以 everything after pid 字段 是 treated
// 作为 command 和 reduced 到 它的 basename.
func parsePS(out []byte) []Proc {
	var procs []Proc
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		comm := strings.Join(fields[1:], " ")
		p := Proc{PID: pid, Name: path.Base(comm)}
		if strings.Contains(comm, "/") {
			p.Path = comm
		}
		procs = append(procs, p)
	}
	return procs
}

// classify inspects process 表 snapshot 为 pinentry 和 gpg
// processes. handover reports 那 终态 pinentry dialog 是 present
// 和 needs exclusive control 的 终态. ctrlL reports 那
// dialog 是 ncurses flavor 那 redraws 在 Ctrl-L. gpg reports 那
// gpg process 是 running.
func classify(procs []Proc) (handover, ctrlL, gpg bool) {
	for i := range procs {
		p := procs[i]
		if _, ok := gpgNames[p.Name]; ok {
			gpg = true
		}
		if _, ok := terminalPinentryNames[p.Name]; !ok {
			continue
		}
		switch resolveFlavor(p) {
		case flavorCurses:
			handover, ctrlL = true, true
		case flavorTTY:
			handover = true
		case flavorGUI:
			// GUI pinentry flavors draw their own 窗口 和 never
			// touch 终态.
		}
	}
	return handover, ctrlL, gpg
}
