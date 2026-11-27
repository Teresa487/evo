//go:build !((darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64)))

package db

import (
	"database/sql"
	"fmt"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

func openDBReadOnly(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_txlock=immediate", dsnPathReplacer.Replace(dbPath))
	db, err := driver.Open(dsn, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}

func openDB(dbPath string) (*sql.DB, error) {
	// Use BEGIN IMMEDIATE 所以 writers acquire reserved 锁 up front,
	// preventing deferred-到-writer upgrade deadlocks. "文件:" prefix
	// 是 required 为 ncruces driver 到 parse 查询 parameters.
	dsn := fmt.Sprintf("file:%s?_txlock=immediate", dsnPathReplacer.Replace(dbPath))
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		// Set pragmas 为 better performance via _pragma 查询 params.
		// Format: PRAGMA name = 值;
		for name, value := range pragmas {
			if err := c.Exec(fmt.Sprintf("PRAGMA %s = %s;", name, value)); err != nil {
				return fmt.Errorf("failed to set pragma %q: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}
