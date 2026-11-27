package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/version"
)

// ErrDataDirLocked 是 returned 通过 Connect 当 data 目录 是
// already 在 use 通过 another crush process.
var ErrDataDirLocked = errors.New("data directory already in use by another crush process")

// dataDirLockFile 是 name 的 锁 文件 inside data
// 目录. 它 lives next 到 crush.db 所以 users can `ls` 和 find it.
const dataDirLockFile = "crush.lock"

// dataDirOwnerInfo 是 JSON payload written into 锁 文件 通过
// process 那 currently owns it. 它 是 purely informational;
// authoritative state 的 ownership 是 operating system flock 在
// 文件 descriptor.
type dataDirOwnerInfo struct {
	PID       int    `json:"pid"`
	Version   string `json:"version,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// dataDirLock represents acquired exclusive 锁 在 data
// 目录. release closes underlying 文件 descriptor which
// kernel uses 到 drop OS-level 锁.
type dataDirLock struct {
	release func()
}

// acquireDataDirLock takes exclusive non-blocking 锁 在
// {dataDir}/crush.锁. If 锁 是 already held 通过 another
// process, it returns ErrDataDirLocked wrapped 用 diagnostic 那
// includes whatever owner info 那 process wrote.
//
// Acquisition 是 skipped (returning no-op 锁) 当
// CRUSH_SKIP_DATADIR_LOCK 是 set 到 truthy 值. 这 是 intended
// 作为 escape hatch 为 hostile filesystems 那 do 不 implement
// advisory locking; it should 不 是 used 在 normal operation.
func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	if skipDataDirLock() {
		return &dataDirLock{release: func() {}}, nil
	}

	path := filepath.Join(dataDir, dataDirLockFile)
	release, err := lock.TryFile(path)
	if err != nil {
		if errors.Is(err, lock.ErrContended) {
			return nil, contendedLockError(dataDir, path)
		}
		return nil, fmt.Errorf("failed to lock data directory %q: %w", dataDir, err)
	}

	// Record ownership metadata 所以 contending process can identify
	// us. Failures here 是 non-fatal: OS-level 锁 是 what
	// actually guarantees mutual exclusion, 和 missing/partial JSON
	// payload 只 degrades diagnostic contender prints.
	if err := writeOwnerInfo(path); err != nil {
		slog.Debug("Failed to write data-dir owner info", "path", path, "error", err)
	}

	// 锁 文件 itself 是 intentionally never unlinked. flock 是
	// keyed 通过 inode, 不 通过 路径, 和 any close-then-unlink (或
	// unlink-then-close) ordering opens 窗口 where two processes
	// can each hold flock 在 different inode 那 lives 在
	// same 路径. Leaving 文件 在 place lets every acquirer see
	// same inode 和 lets kernel arbitrate correctly.
	return &dataDirLock{release: release}, nil
}

// skipDataDirLock reports whether data-dir 锁 should 是 bypassed.
func skipDataDirLock() bool {
	v, _ := strconv.ParseBool(os.Getenv("CRUSH_SKIP_DATADIR_LOCK"))
	return v
}

// writeOwnerInfo truncates 和 rewrites 锁 文件 用 current
// process's identifying information. 它 是 called 只 after 锁
// 是 held.
func writeOwnerInfo(path string) error {
	info := dataDirOwnerInfo{
		PID:       os.Getpid(),
		Version:   version.Version,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o600)
}

// readOwnerInfo returns 锁 文件's recorded owner, 如果 it parses.
// 一个 missing 或 malformed 文件 yields empty struct 和 no 错误;
// caller decides what 到 surface 到 用户.
func readOwnerInfo(path string) dataDirOwnerInfo {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return dataDirOwnerInfo{}
	}
	var info dataDirOwnerInfo
	_ = json.Unmarshal(raw, &info)
	return info
}

// contendedLockError builds wrapped ErrDataDirLocked annotated 用
// whatever owner metadata 是 currently 在 锁 文件.
func contendedLockError(dataDir, lockPath string) error {
	info := readOwnerInfo(lockPath)
	details := ""
	switch {
	case info.PID != 0 && info.StartedAt != "":
		details = fmt.Sprintf(" (owner pid=%d version=%s started_at=%s)",
			info.PID, info.Version, info.StartedAt)
	case info.PID != 0:
		details = fmt.Sprintf(" (owner pid=%d)", info.PID)
	}
	return fmt.Errorf("%w: %s%s", ErrDataDirLocked, dataDir, details)
}
