// Package 锁 provides cross-process advisory 文件 locking.
//
// 文件 acquires exclusive 锁 在 文件 在 路径, blocking until
// 上下文 是 cancelled (或 它的 期限 elapses). TryFile does
// same 但 returns ErrContended immediately 如果 锁 是 already
// held. In both cases returned release function drops 锁 和
// closes underlying 文件 descriptor.
//
// 锁 是 released automatically 通过 kernel 在 process
// termination (including crash), 所以 no stale-锁 recovery 是 needed.
//
// 锁 文件 在 路径 是 created 如果 it does 不 exist. 它 是 never
// unlinked — flock 是 keyed 通过 inode, 不 路径, 和 unlinking could
// 创建 窗口 where two processes 锁 different inodes 在
// same 路径.
//
// 这 是 canonical 文件-locking helper 为 Crush. Callers should
// prefer it over rolling their own platform-specific code.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ErrContended 是 returned 通过 TryFile 当 锁 是 already held 通过
// another process.
var ErrContended = errors.New("file lock is held by another process")

// 文件 acquires exclusive advisory 锁 在 文件 在 路径, blocking
// until 锁 是 acquired 或 ctx 是 cancelled. 它 returns release
// function 那 drops 锁 和 closes underlying 文件 descriptor.
//
// Pass 上下文 用 期限 (e.g. 上下文.WithTimeout) 到 bound
// wait. Pass 上下文.Background() 到 block indefinitely.
func File(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %q: %w", path, err)
	}

	release, err := lockFile(ctx, f)
	if err != nil {
		f.Close()
		return nil, err
	}

	return func() {
		release()
		f.Close()
	}, nil
}

// TryFile 是 like 文件 但 returns ErrContended immediately 如果 锁
// 是 already held 通过 another process. Use 这 当 you want 到 fail
// fast rather than wait.
func TryFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %q: %w", path, err)
	}

	release, err := tryLockFile(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	return func() {
		release()
		f.Close()
	}, nil
}
