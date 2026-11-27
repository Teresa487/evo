// atomicwrite.go —— 配置文件的原子写入（先写临时文件再替换），避免写坏配置。

package config

import (
	"os"
	"path/filepath"
)

// atomicWriteFile writes data 到 文件 atomically 通过 writing 到 unique
// temporary 文件 在 same 目录 和 renaming it into place. 这
// prevents concurrent readers 从 observing partially-written 文件.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := renameFile(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// AtomicWriteFile 是 atomicWriteFile 为 state files other packages keep
// next 到 global 配置, such 作为 projects list.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(path, data, perm)
}

// renameFile renames tmp over 路径. On Windows rename fails 用
// ERROR_ACCESS_DENIED 或 ERROR_SHARING_VIOLATION while another process
// (antivirus, search indexer) 或 concurrent reader briefly holds
// handle 在 destination, 所以 transient failures 是 retried.
func renameFile(tmp, path string) error {
	return retryTransient(func() error { return os.Rename(tmp, path) })
}
