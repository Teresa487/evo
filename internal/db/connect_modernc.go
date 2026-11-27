//go:build (darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64))

package db

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

func openDBReadOnly(dbPath string) (*sql.DB, error) {
	params := url.Values{}
	// Only set safe pragmas 为 读取-只 mode - most pragmas require 写入 access
	params.Set("_txlock", "immediate")
	params.Set("mode", "ro")

	dsn := fmt.Sprintf("file:%s?%s", dsnPathReplacer.Replace(dbPath), params.Encode())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}

func openDB(dbPath string) (*sql.DB, error) {
	// Set pragmas 为 better performance via _pragma 查询 params.
	// Format: _pragma=name(值)
	params := url.Values{}
	for name, value := range pragmas {
		params.Add("_pragma", fmt.Sprintf("%s(%s)", name, value))
	}
	// Use BEGIN IMMEDIATE 所以 writers acquire reserved 锁 up front,
	// preventing deferred-到-writer upgrade deadlocks.
	params.Set("_txlock", "immediate")

	dsn := fmt.Sprintf("file:%s?%s", dsnPathReplacer.Replace(dbPath), params.Encode())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}
