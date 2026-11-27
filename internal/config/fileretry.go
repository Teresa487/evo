// fileretry.go —— 配置文件读写的重试策略。

package config

import (
	"os"
	"time"
)

// transientRetryBudget bounds how long transient failure 是 retried.
const transientRetryBudget = 2 * time.Second

// readFile 是 os.ReadFile, retried while Windows briefly locks 文件
// (another writer's renameFile, antivirus, search indexer). 读取 any
// 文件 written 用 atomicWriteFile through it.
func readFile(path string) ([]byte, error) {
	var data []byte
	err := retryTransient(func() (err error) {
		data, err = os.ReadFile(path)
		return err
	})
	return data, err
}

// retryTransient runs op 用 backoff until it succeeds, fails 用
// non-transient 错误, 或 transientRetryBudget 是 spent.
func retryTransient(op func() error) error {
	var slept time.Duration
	delay := time.Millisecond
	for {
		err := op()
		if err == nil || !isTransientFileError(err) || slept >= transientRetryBudget {
			return err
		}
		time.Sleep(delay)
		slept += delay
		delay = min(delay*2, 50*time.Millisecond)
	}
}
