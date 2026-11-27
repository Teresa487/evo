// connect.go —— SQLite 连接的生命周期与共享，是消息/session 持久化的底座。
//
// 每个 dataDir 一个 crush.db；同一路径的多个客户端通过连接池共享同一个 DB 连接。
// message、session 等包拿到 db.Querier 后完成读写。
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/dirid"
	"github.com/pressly/goose/v3"
)

var (
	pragmas = map[string]string{
		"foreign_keys":  "ON",
		"journal_mode":  "WAL",
		"page_size":     "4096",
		"temp_store":    "MEMORY",
		"cache_size":    "-8000",
		"synchronous":   "NORMAL",
		"secure_delete": "ON",
		"busy_timeout":  "30000",
	}
	gooseInitOnce sync.Once
	gooseInitErr  error
)

// dsnPathReplacer escapes characters 那 SQLite's URI parser treats
// specially 在 路径 的 "文件:" DSN: "%" starts escape, "?"
// 查询 string 和 "#" fragment. Unescaped, data 目录 such 作为
// "C#app" would open 数据库 文件 named "C" next 到 project.
var dsnPathReplacer = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

//go:embed migrations/*.sql
var FS embed.FS

func init() {
	goose.SetBaseFS(FS)

	if testing.Testing() {
		goose.SetLogger(goose.NopLogger())
	}
}

// connEntry holds shared 数据库 连接, 它的 reference count,
// 和 data-目录 锁 那 gates access 到 这 条目.
// 锁 是 acquired exactly once 当 条目 是 created 和 released
// 当 last reference 是 dropped, which lets same process open
// same data 目录 concurrently while still blocking second
// crush process 从 racing storage.
type connEntry struct {
	db       *sql.DB
	refCount int
	lock     *dataDirLock
}

var (
	pool   = make(map[string]*connEntry)
	poolMu sync.Mutex
)

// ConnectOption configures Connect 调用. Options 是 applied 在
// order; later options override earlier ones 为 same 字段.
type ConnectOption func(*connectOptions)

// connectOptions holds resolved configuration 为 Connect 调用.
type connectOptions struct {
	lockDataDir bool
}

// WithDataDirLock toggles acquisition 的 per-data-目录 锁
// 为 这 Connect 调用. 锁 是 off 通过 默认 所以 local-mode
// invocations do 不 regress today's behavior; 服务端's
// 工作区-bootstrap 路径 opts 在. CRUSH_SKIP_DATADIR_LOCK still
// bypasses acquisition even 当 这 option 是 set.
func WithDataDirLock(enable bool) ConnectOption {
	return func(o *connectOptions) { o.lockDataDir = enable }
}

// Connect opens SQLite 数据库 连接 为 given data
// 目录 和 runs migrations. If 连接 到 same 数据库
// 文件 already exists, existing 连接 是 returned 用 它的
// reference count incremented. Callers must pair each Connect 用
// [Release] 当 they no longer need 连接.
func Connect(ctx context.Context, dataDir string, opts ...ConnectOption) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data.dir is not set")
	}

	var cfg connectOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	dbPath := filepath.Join(dataDir, "crush.db")

	// Resolve 到 absolute 路径 所以 那 different relative paths 到
	// same 文件 share single 连接.
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}

	poolMu.Lock()
	defer poolMu.Unlock()

	if _, entry, ok := lookupLocked(absPath); ok {
		entry.refCount++
		return entry.db, nil
	}

	// Take per-data-目录 锁 before opening 数据库 所以
	// we fail fast 和 用 clear 错误 rather than racing another
	// crush process 在 same SQLite 文件. 锁 是 released 当
	// matching Release 调用 drops refcount 到 zero. Ensuring
	// data 目录 exists 是 required because 锁 文件
	// lives inside it. Locking 是 opt-在 via WithDataDirLock 所以 那
	// local-mode invocations do 不 refuse second crush against
	// same data dir until 客户端/服务端 becomes 默认.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory %q: %w", dataDir, err)
	}
	var lock *dataDirLock
	if cfg.lockDataDir && !skipDataDirLock() {
		lock, err = acquireDataDirLock(dataDir)
		if err != nil {
			return nil, err
		}
	}

	conn, err := openDB(dbPath)
	if err != nil {
		if lock != nil {
			lock.release()
		}
		return nil, err
	}

	// Serialize all access through single 连接. SQLite
	// serializes writes 在 文件 level anyway, 和 allowing multiple
	// pool connections 到 interleave writes/checkpoints (especially
	// 在并发子代理下）曾导致 WAL/头部失步
	// resulting 在 SQLITE_NOTADB (26) 在 next open.
	conn.SetMaxOpenConns(1)

	releaseLock := func() {
		if lock != nil {
			lock.release()
		}
	}

	if err = conn.PingContext(ctx); err != nil {
		conn.Close()
		releaseLock()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := initGoose(); err != nil {
		conn.Close()
		releaseLock()
		slog.Error("Failed to initialize goose", "error", err)
		return nil, fmt.Errorf("failed to initialize goose: %w", err)
	}

	if err := goose.Up(conn, "migrations"); err != nil {
		conn.Close()
		releaseLock()
		slog.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	pool[absPath] = &connEntry{db: conn, refCount: 1, lock: lock}
	return conn, nil
}

// Release decrements reference count 为 数据库 在 given
// data 目录. When count reaches zero underlying 连接
// 是 closed 和 removed 从 pool.
func Release(dataDir string) error {
	dbPath := filepath.Join(dataDir, "crush.db")
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}

	poolMu.Lock()
	defer poolMu.Unlock()

	key, entry, ok := lookupLocked(absPath)
	if !ok {
		return nil
	}

	entry.refCount--
	if entry.refCount > 0 {
		return nil
	}

	delete(pool, key)
	closeErr := entry.db.Close()
	if entry.lock != nil {
		entry.lock.release()
	}
	return closeErr
}

// lookupLocked returns pool 条目 为 数据库 在 absPath 和
// key it 是 存储 under. On miss it falls back 到 条目 在 same
// 目录: filepath.Abs leaves some spellings 的 one 目录 apart (
// symlinked parent, another case 在 case-insensitive filesystem,
// Windows junction 或 UNC 路径), 和 second 条目 would take
// data-dir 锁 again 和 fail against 这 very process. Both identities
// 是 读取 afresh, 所以 条目 matches 只 目录 那 是 在 它的
// 路径 now: one whose 目录 was removed cannot match unrelated
// 目录 那 reuses 它的 inode, though it does match 目录 later
// recreated 在 它的 路径, 作为 exact key hit would. poolMu must 是 held.
func lookupLocked(absPath string) (string, *connEntry, bool) {
	if entry, ok := pool[absPath]; ok {
		return absPath, entry, true
	}
	dir, ok := dirid.Of(filepath.Dir(absPath))
	if !ok {
		return "", nil, false
	}
	for key, entry := range pool {
		other, ok := dirid.Of(filepath.Dir(key))
		if ok && dir.Same(other) {
			return key, entry, true
		}
	}
	return "", nil, false
}

// ResetPool closes all pooled connections 和 clears pool. 这 是
// intended 为 use 在 tests 到 ensure clean state between 测试 cases.
func ResetPool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	for path, entry := range pool {
		entry.db.Close()
		if entry.lock != nil {
			entry.lock.release()
		}
		delete(pool, path)
	}
}

// ConnectReadOnly opens 读取-只 SQLite 数据库 连接 without running
// migrations. Used 为 aggregating stats across multiple project databases.
func ConnectReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return db, nil
}

func initGoose() error {
	gooseInitOnce.Do(func() {
		goose.SetBaseFS(FS)
		gooseInitErr = goose.SetDialect("sqlite3")
	})

	return gooseInitErr
}
