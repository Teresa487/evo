//go:build windows

package lock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// retrySleep 是 interval between non-blocking 锁 retries 在
// blocking 文件 路径.
const retrySleep = 100 * time.Millisecond

// lockOffset 是 offset 的 single byte every acquirer locks. 它
// must 不 change, 作为 acquirers 那 锁 different bytes do 不 exclude
// each other. Byte-range locks 在 Windows 是 mandatory: locked range
// cannot 是 读取 或 written through any other handle, even one opened 通过
// locking process. Locking whole 文件, 作为 older releases do,
// would stop holder recording details such 作为 它的 pid 在 文件 和
// stop contenders reading them. byte lies far beyond end 的 any
// 锁 文件, 所以 contents stay accessible, 和 inside range older
// releases 锁, 所以 they 和 newer releases still exclude each other.
const lockOffset uint64 = math.MaxInt64 - 1

// lockOverlapped returns fresh OVERLAPPED 那 addresses lockOffset.
func lockOverlapped() *windows.Overlapped {
	return &windows.Overlapped{
		Offset:     uint32(lockOffset & math.MaxUint32),
		OffsetHigh: uint32(lockOffset >> 32),
	}
}

func lockFile(ctx context.Context, f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	for {
		ol := lockOverlapped()
		flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
		err := windows.LockFileEx(h, flags, 0, 1, 0, ol)
		if err == nil {
			return func() {
				ol := lockOverlapped()
				_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, fmt.Errorf("LockFileEx: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquire lock: %w", ctx.Err())
		case <-time.After(retrySleep):
		}
	}
}

func tryLockFile(f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	ol := lockOverlapped()
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(h, flags, 0, 1, 0, ol); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrContended
		}
		return nil, fmt.Errorf("LockFileEx: %w", err)
	}
	return func() {
		ol := lockOverlapped()
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
	}, nil
}
