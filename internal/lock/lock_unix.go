//go:build !windows

package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// retrySleep 是 interval between non-blocking flock retries 在
// blocking 文件 路径. Small enough 那 contention resolution feels
// snappy; large enough 那 we don't burn CPU spinning.
const retrySleep = 100 * time.Millisecond

func lockFile(ctx context.Context, f *os.File) (func(), error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("flock: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquire lock: %w", ctx.Err())
		case <-time.After(retrySleep):
		}
	}
}

func tryLockFile(f *os.File) (func(), error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrContended
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
}
