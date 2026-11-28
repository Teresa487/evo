package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// runIsolated keeps tests off developer's own global 配置 和 data.
func runIsolated(m *testing.M) int {
	home, err := os.MkdirTemp("", "crush-agent-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("EVO_GLOBAL_CONFIG", filepath.Join(home, "config"))
	os.Setenv("EVO_GLOBAL_DATA", filepath.Join(home, "data"))
	return m.Run()
}
