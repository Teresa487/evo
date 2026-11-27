package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/stretchr/testify/require"
)

func TestConnect_SharesConnectionForSameDataDir(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()

	conn1, err := Connect(context.Background(), dataDir)
	require.NoError(t, err)

	conn2, err := Connect(context.Background(), dataDir)
	require.NoError(t, err)

	require.Same(t, conn1, conn2, "should return the same *sql.DB for the same data dir")

	// Releasing once should 不 close 连接.
	require.NoError(t, Release(dataDir))
	require.NoError(t, conn1.PingContext(context.Background()), "connection should still be usable after partial release")

	// Releasing again 应当 close it.
	require.NoError(t, Release(dataDir))
	require.Error(t, conn1.PingContext(context.Background()), "connection should be closed after final release")
}

func TestConnect_SeparateConnectionsForDifferentDataDirs(t *testing.T) {
	t.Cleanup(ResetPool)

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	conn1, err := Connect(context.Background(), dir1)
	require.NoError(t, err)

	conn2, err := Connect(context.Background(), dir2)
	require.NoError(t, err)

	require.NotSame(t, conn1, conn2, "different data dirs should get different connections")

	require.NoError(t, Release(dir1))
	require.NoError(t, Release(dir2))
}

// TestConnect_PathWithURIMetacharacters guards 那 "#", "?" 和 "%" 在
// data 目录 是 escaped 在 SQLite DSN, 不 parsed 作为 URI syntax.
func TestConnect_PathWithURIMetacharacters(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := filepath.Join(t.TempDir(), "C#app", "100%41")
	_, err := Connect(context.Background(), dataDir)
	require.NoError(t, err)
	require.NoError(t, Release(dataDir))
	require.FileExists(t, filepath.Join(dataDir, "crush.db"))
}

func TestRelease_NoopForUnknownDataDir(t *testing.T) {
	t.Cleanup(ResetPool)

	require.NoError(t, Release("/nonexistent/path"), "releasing unknown data dir should not error")
}

// TestConnect_FailsWhenDataDirLocked simulates second crush process 通过
// taking data-dir 锁 directly via OS primitive 在 separate
// 文件 descriptor 和 then asserting 那 Connect surfaces clean
// ErrDataDirLocked instead 的 opening 数据库 under contention.
func TestConnect_FailsWhenDataDirLocked(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.Error(t, err, "Connect must refuse to open a locked data dir")
	require.ErrorIs(t, err, ErrDataDirLocked)
}

// TestConnect_SucceedsAfterContenderReleases ensures 锁 是 purely
// advisory 和 那 clean release lets next Connect proceed.
func TestConnect_SucceedsAfterContenderReleases(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.ErrorIs(t, err, ErrDataDirLocked)

	release()

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err, "Connect should succeed once the contender releases the lock")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_LockReleasedOnFinalRelease confirms 那 closing last
// reference 到 pool 条目 also drops OS 锁, 所以 subsequent
// processes 可以 take data dir.
func TestConnect_LockReleasedOnFinalRelease(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)
	require.NoError(t, conn.PingContext(context.Background()))

	// Holding 在-process 条目 must keep OS 锁 held 所以
	// "second process" (simulated 通过 fresh 锁.TryFile 调用) 是
	// rejected.
	_, lockErr := lock.TryFile(lockPath)
	require.Error(t, lockErr)
	require.True(t, errors.Is(lockErr, lock.ErrContended), "expected contended lock while pool entry is live")

	require.NoError(t, Release(dataDir))

	// After final release 锁 是 free again.
	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected lock to be released after final Release")
	release()
}

// TestConnect_SharedPoolDoesNotReacquireLock makes sure 那 subsequent
// 在-process Connect calls reuse existing OS 锁 through refcount,
// 不 通过 re-acquiring it. simplest observable signal 的 correctness
// 是 那 second Connect does 不 错误 和 锁 是 still held
// after a single Release.
func TestConnect_SharedPoolDoesNotReacquireLock(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	_, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)

	// Drop one reference; 锁 must still 是 held.
	require.NoError(t, Release(dataDir))
	_, lockErr := lock.TryFile(lockPath)
	require.ErrorIs(t, lockErr, lock.ErrContended)

	require.NoError(t, Release(dataDir))
}

// TestConnect_SharesEntryAcrossDataDirSpellings guards 那 symlinked
// spelling 的 open data dir reuses 它的 pool 条目 instead 的 contending
// 为 它的 锁.
func TestConnect_SharesEntryAcrossDataDirSpellings(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dataDir, alias); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	conn1, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err)
	conn2, err := Connect(context.Background(), alias, WithDataDirLock(true))
	require.NoError(t, err)
	require.Same(t, conn1, conn2)

	require.NoError(t, Release(dataDir))
	require.NoError(t, Release(alias))
	require.Error(t, conn1.PingContext(context.Background()))
}

// TestConnect_SkipLockEnvBypassesAcquisition exercises the escape
// hatch used 通过 users 在 filesystems where flock 是 unreliable.
func TestConnect_SkipLockEnvBypassesAcquisition(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err)
	t.Cleanup(release)

	t.Setenv("CRUSH_SKIP_DATADIR_LOCK", "1")

	conn, err := Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.NoError(t, err, "skip-lock env should bypass contention")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_DefaultIgnoresContendedLock confirms 那 without
// WithDataDirLock(true) 锁 文件 是 irrelevant: contender can
// hold 锁.TryFile 和 Connect still succeeds. 这 pins
// local-mode 默认 到 它的 pre-锁 behavior.
func TestConnect_DefaultIgnoresContendedLock(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	conn, err := Connect(context.Background(), dataDir)
	require.NoError(t, err, "default Connect must not take the lock and must succeed under contention")
	require.NoError(t, conn.PingContext(context.Background()))
	require.NoError(t, Release(dataDir))
}

// TestConnect_ServerPathFailsWhenDataDirLocked 是 服务端's
// 工作区-bootstrap analogue 的 TestConnect_FailsWhenDataDirLocked:
// passing WithDataDirLock(true) must surface ErrDataDirLocked 当
// contender already holds 锁.
func TestConnect_ServerPathFailsWhenDataDirLocked(t *testing.T) {
	t.Cleanup(ResetPool)

	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, dataDirLockFile)

	release, err := lock.TryFile(lockPath)
	require.NoError(t, err, "expected to take the data-dir lock for the first time")
	t.Cleanup(release)

	_, err = Connect(context.Background(), dataDir, WithDataDirLock(true))
	require.Error(t, err, "server-path Connect must refuse to open a locked data dir")
	require.ErrorIs(t, err, ErrDataDirLocked)
}
