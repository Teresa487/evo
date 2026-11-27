//go:build windows

package shell

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveInterpreter_PermissiveFallback_Windows 是 Windows-native
// counterpart 到 POSIX permissive-fallback 测试. 它 proves one
// behavior 那 makes `#!/bin/bash` hooks work 在 stock Windows box
// 用 Git 为 Windows installed: 当 literal interpreter 路径 does
// 不 exist, we fall back 到 路径-lookup 在 basename 和 那
// lookup accepts any executable extension Windows honors (here, `.bat`).
//
// We plant bash.bat 在 tempdir rather than .exe because producing
// .exe would require toolchain step; LookPath 在 Windows resolves
// PATHEXT extensions, 所以 .bat 是 just 作为 valid 为 lookup codepath.
func TestResolveInterpreter_PermissiveFallback_Windows(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "bash.bat")
	contents := "@echo off\r\nexit /b 0\r\n"
	if err := os.WriteFile(fake, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fake bash.bat: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".BAT;.CMD;.EXE")

	// Literal 路径 must 是 absent 所以 stat fails 用 ENOENT.
	missing := filepath.Join(dir, "definitely-not-here-"+randSuffix(), "bash")
	resolved, err := resolveInterpreter(missing)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got: %v", err)
	}
	if resolved != fake {
		t.Fatalf("resolved = %q, want %q", resolved, fake)
	}
}
