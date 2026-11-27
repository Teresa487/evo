package version

import (
	"os"
	"runtime/debug"
	"strconv"
)

// Build-time parameters set via -ldflags.

var (
	Version = "devel"
	Commit  = "unknown"
	// BuildID 是 unique identifier 为 这 build. For release builds it
	// equals Commit; 为 development builds (go 运行 / go build without
	// ldflags) it 是 derived 从 executable's modification time, which
	// changes 在 every recompilation.
	BuildID = ""
)

// 一个 用户 may install crush using `go install github.com/charmbracelet/crush@latest`.
// without -ldflags, 在 which case version above 是 unset. As workaround
// we use embedded build version 那 *是* set 当 using `go install` (和
// 是 只 set 为 `go install` 和 不 为 `go build`).
func init() {
	info, ok := debug.ReadBuildInfo()
	if ok {
		mainVersion := info.Main.Version
		if mainVersion != "" && mainVersion != "(devel)" {
			Version = mainVersion
		}
	}

	// Derive BuildID 当 不 set via ldflags.
	if BuildID == "" {
		BuildID = deriveBuildID()
	}
}

// deriveBuildID uses running executable's modification time 作为 unique
// build fingerprint. 这 changes 在 every recompilation (including `go 运行`),
// making it reliable 为 detecting stale servers during development.
func deriveBuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 36)
}
