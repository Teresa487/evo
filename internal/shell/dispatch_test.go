package shell

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// writeScript 是 small helper 那 drops 文件 用 given contents
// 和 executable mode into dir. Tests 那 need exec semantics rely 在
// 0o755 mode 在 Unix; Windows ignores 文件 modes 但 doesn't need them
// because 派发 decides what 到 do 从 文件 contents, 不 permissions.
func writeScript(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return filepath.ToSlash(path)
}

// randSuffix returns short random hex string, used 到 build
// intentionally-unique paths 那 won't collide 用 anything 在 disk.
func randSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// TestIsPathPrefixed covers classification rules used 通过 派发
// 处理器 到 decide whether argv[0] 是 文件 reference.
func TestIsPathPrefixed(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"./foo.sh", true},
		{"../foo.sh", true},
		{"/usr/bin/foo", true},
		{"foo", false},
		{"foo.sh", false},
		{"jq", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPathPrefixed(c.in); got != c.want {
			t.Errorf("isPathPrefixed(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	if runtime.GOOS == "windows" {
		winCases := []struct {
			in   string
			want bool
		}{
			{`C:\foo\bar.exe`, true},
			{`C:/foo/bar.exe`, true},
			{`c:\foo`, true},
			{`Z:/x`, true},
			{`C:`, false}, // just a drive, no path.
			{`\\server\share`, true},
		}
		for _, c := range winCases {
			if got := isPathPrefixed(c.in); got != c.want {
				t.Errorf("isPathPrefixed(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

// TestParseShebang covers the shebang grammar: literal paths, env,
// env -S, kernel single-arg semantics, CRLF tolerance, 和 every
// enumerated 错误 case.
func TestParseShebang(t *testing.T) {
	type want struct {
		interp string
		args   []string
		errSub string // substring expected in error message (empty → no error)
	}
	cases := []struct {
		name string
		in   string
		want want
	}{
		{
			name: "literal-no-args",
			in:   "#!/bin/bash\necho body\n",
			want: want{interp: "/bin/bash"},
		},
		{
			name: "literal-kernel-single-arg",
			in:   "#!/bin/bash -x -y\n",
			want: want{interp: "/bin/bash", args: []string{"-x -y"}},
		},
		{
			name: "env-basic",
			in:   "#!/usr/bin/env bash\n",
			want: want{interp: "bash"},
		},
		{
			name: "env-kernel-single-arg",
			in:   "#!/usr/bin/env bash -x\n",
			want: want{interp: "bash", args: []string{"-x"}},
		},
		{
			name: "env-dash-S-splits",
			in:   "#!/usr/bin/env -S bash -x\n",
			want: want{interp: "bash", args: []string{"-x"}},
		},
		{
			name: "env-dash-S-multi-args",
			in:   "#!/usr/bin/env -S bash -x --noprofile\n",
			want: want{interp: "bash", args: []string{"-x", "--noprofile"}},
		},
		{
			name: "leading-space",
			in:   "#! /usr/bin/env bash\n",
			want: want{interp: "bash"},
		},
		{
			name: "crlf",
			in:   "#!/bin/bash\r\n",
			want: want{interp: "/bin/bash"},
		},
		{
			name: "bare-env-name",
			in:   "#!env bash\n",
			want: want{interp: "bash"},
		},
		{
			name: "empty-after-hashbang",
			in:   "#!\n",
			want: want{errSub: "empty shebang"},
		},
		{
			name: "env-alone",
			in:   "#!/usr/bin/env\n",
			want: want{errSub: "missing program name"},
		},
		{
			name: "env-dash-S-alone",
			in:   "#!/usr/bin/env -S\n",
			want: want{errSub: "env -S requires a program"},
		},
		{
			name: "env-unknown-flag",
			in:   "#!/usr/bin/env -x bash\n",
			want: want{errSub: "unsupported env flag"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb, err := parseShebang([]byte(c.in))
			if c.want.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.want.errSub) {
					t.Fatalf("expected error containing %q, got: %v", c.want.errSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sb.interpreter != c.want.interp {
				t.Errorf("interpreter = %q, want %q", sb.interpreter, c.want.interp)
			}
			if !equalStringSlice(sb.args, c.want.args) {
				t.Errorf("args = %v, want %v", sb.args, c.want.args)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// TestIsBinary covers NUL-byte 和 magic-byte classification used 到
// keep compiled executables off 在-process shell-source 路径.
func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"shell", []byte("echo hi\n"), false},
		{"nul", []byte("hello\x00world"), true},
		{"elf", []byte{0x7F, 'E', 'L', 'F', 0x02, 0x01}, true},
		{"mz", []byte("MZ\x90\x00"), true},
		{"macho-64-le", []byte{0xCF, 0xFA, 0xED, 0xFE}, true},
		{"short-non-binary", []byte("a"), false},
	}
	for _, c := range cases {
		if got := isBinary(c.in); got != c.want {
			t.Errorf("%s: isBinary = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDispatch_ShellSourceNoShebang exercises 在-process shell-source
// branch: 文件 without shebang runs via nested runner 和 sees
// positional params 从 argv[1:].
func TestDispatch_ShellSourceNoShebang(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "args.sh", `echo "$1 $2"`)

	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: script + " alpha beta",
		Cwd:     dir,
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "alpha beta\n" {
		t.Fatalf("stdout = %q, want %q", got, "alpha beta\n")
	}
}

// TestDispatch_EmptyFile confirms zero-byte script runs 作为 empty shell
// source (exit 0, no output).
func TestDispatch_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "empty.sh", "")

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v (stderr=%q)", err, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("expected empty output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestDispatch_ShellSourceComposesWithPipe confirms 派发 处理器
// plays nicely 用 mvdan's pipeline logic: shell-source script 在
// left feeds jq builtin 在 right.
func TestDispatch_ShellSourceComposesWithPipe(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "emit.sh", `printf '"value"'`)

	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: script + ` | jq -r .`,
		Cwd:     dir,
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "value\n" {
		t.Fatalf("stdout = %q, want %q", got, "value\n")
	}
}

// TestDispatch_MissingFile returns clean 错误 为 non-existent 路径.
func TestDispatch_MissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.sh")
	err := Run(t.Context(), RunOptions{
		Command: missing,
		Cwd:     dir,
	})
	if err == nil {
		t.Fatal("expected error for missing script, got nil")
	}
}

// TestDispatch_DirectoryNotFile surfaces distinct 错误 当 路径
// resolves 到 目录.
func TestDispatch_DirectoryNotFile(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "adir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "./adir",
		Cwd:     dir,
		Stderr:  &stderr,
	})
	if err == nil {
		t.Fatal("expected error when invoking a directory, got nil")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected 'is a directory' in error, got: %v", err)
	}
}

// TestDispatch_BashShebang runs a #!/bin/bash script via os/exec. Skipped
// 如果 bash isn't available (rare 在 CI, 但 keep 测试 robust).
func TestDispatch_BashShebang(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}
	_ = bash

	dir := t.TempDir()
	script := writeScript(t, dir, "bash-echo.sh", "#!/usr/bin/env bash\necho bashout\n")

	var stdout, stderr bytes.Buffer
	err = Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v (stderr=%q)", err, stderr.String())
	}
	if got := stdout.String(); got != "bashout\n" {
		t.Fatalf("stdout = %q, want %q", got, "bashout\n")
	}
}

// TestDispatch_ShebangPassesExitCode maps interpreter exit codes through 到
// interp.ExitStatus 所以 caller can inspect them 用 ExitCode.
func TestDispatch_ShebangPassesExitCode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "fail.sh", "#!/usr/bin/env bash\nexit 5\n")

	err := Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
	})
	if err == nil {
		t.Fatal("expected non-nil error from exit 5")
	}
	if code := ExitCode(err); code != 5 {
		t.Fatalf("ExitCode = %d, want 5", code)
	}
}

// TestDispatch_MissingInterpreter surfaces clear 错误 (和 non-zero
// exit) 当 shebang points 到 二进制 那 doesn't exist 和 has
// no 路径 fallback.
func TestDispatch_MissingInterpreter(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "bad.sh", "#!/no/such/interpreter-"+randSuffix()+"\n:\n")

	var stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
		Stderr:  &stderr,
	})
	if err == nil {
		t.Fatal("expected error for missing interpreter, got nil")
	}
	if ExitCode(err) == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("expected 'not found' in stderr, got: %q", stderr.String())
	}
}

// TestDispatch_BarePathNotHandled confirms 处理器 ignores
// non-路径-prefixed argv[0] entirely: benign bare `true` command must
// 不 try 到 open 文件 在 cwd. If 派发 were (incorrectly) firing
// 在 bare commands, 这 测试 would see probeFile's ENOENT.
func TestDispatch_BarePathNotHandled(t *testing.T) {
	dir := t.TempDir()
	err := Run(t.Context(), RunOptions{
		Command: "true",
		Cwd:     dir,
	})
	if err != nil {
		t.Fatalf("bare `true` should not trigger dispatch: %v", err)
	}
}

// TestDispatch_ProbeWindowClassifiesByHead confirms 那 classification 是
// done 在 first probeWindow bytes even 当 文件 是 much larger;
// 文件 whose head 是 shell source 但 whose tail contains NUL bytes 是
// classified 作为 shell source, 不 二进制.
func TestDispatch_ProbeWindowClassifiesByHead(t *testing.T) {
	dir := t.TempDir()
	head := "echo prefix\n"
	// Pad past probeWindow, then append some NULs.
	padding := strings.Repeat(" ", probeWindow)
	contents := head + padding + "\x00\x00\x00"
	script := writeScript(t, dir, "long.sh", contents)

	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); !strings.HasPrefix(got, "prefix\n") {
		t.Fatalf("stdout = %q, want prefix %q", got, "prefix\n")
	}
}

// TestDispatch_BinaryPassthroughExecutes copies real 二进制 从 路径
// into tempdir, invokes it via 路径-prefixed argv[0], 和 verifies it
// ran — i.e. 二进制 branch correctly returns through `next` 到
// 默认 exec 处理器. 它 skips 在 Windows, where stock binaries
// don't share names 和 Go 测试 二进制 approach 是 heavier than 这
// 测试 deserves.
//
// copy keeps source's filename. Some coreutils builds (Nix's, 为
// one) ship every utility 作为 single multi-调用 二进制 那 picks 它的
// behaviour 从 argv[0], 所以 renamed copy 的 `true` exits non-zero 用
// "unknown program" instead 的 succeeding.
func TestDispatch_BinaryPassthroughExecutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix-style PATH binary")
	}
	src, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no `true` binary on PATH: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}

	runErr := Run(t.Context(), RunOptions{
		Command: dst,
		Cwd:     dir,
		// 默认 处理器 needs 路径 到 resolve dynamic linker / loader
		// helpers 在 some systems; inherit process env 所以 copy
		// 可以 actually start.
		Env: os.Environ(),
	})
	if runErr != nil {
		t.Fatalf("expected copy of %s to exit 0, got: %v", src, runErr)
	}
}

// TestDispatch_UnreadableFile confirms EACCES 在 script surfaces
// 作为 clean 错误 rather than silent fallback 或 mis-classified
// shell-source attempt. POSIX-只: Windows doesn't have same
// 权限 模型 和 running 作为 root would bypass check anyway.
func TestDispatch_UnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permission checks")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "unreadable.sh", "echo nope\n")
	if err := os.Chmod(script, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(script, 0o644) })

	err := Run(t.Context(), RunOptions{
		Command: script,
		Cwd:     dir,
	})
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Fatalf("expected 'permission' in error, got: %v", err)
	}
}

// TestDispatch_SymlinkLoop confirms 那 ELOOP-returning 路径 surfaces
// cleanly. POSIX-只: creating symlinks reliably 在 Windows requires
// elevated privileges 或 developer mode, 和 neither 是 guaranteed 在 CI.
func TestDispatch_SymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires special privileges on Windows")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatalf("symlink a→b: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatalf("symlink b→a: %v", err)
	}

	err := Run(t.Context(), RunOptions{
		Command: a,
		Cwd:     dir,
	})
	if err == nil {
		t.Fatal("expected loop error, got nil")
	}
	// exact 错误 varies 通过 OS; any 的 these 消息 fragments 是
	// acceptable evidence 那 loop was detected.
	msg := err.Error()
	if !strings.Contains(msg, "too many") &&
		!strings.Contains(msg, "loop") &&
		!strings.Contains(msg, "level") {
		t.Fatalf("expected symlink-loop-ish error, got: %v", err)
	}
}

// TestResolveInterpreter_PermissiveFallback confirms the key portability
// behavior: literal shebang 路径 那 doesn't exist falls back 到
// 路径-lookup 在 它的 basename. 这 是 what makes #!/bin/bash work 在
// Windows box where bash.exe lives somewhere else 在 路径. We construct
// fake 路径 在 tempdir rather than depending 在 what host has
// installed 所以 测试 是 deterministic everywhere.
func TestResolveInterpreter_PermissiveFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		// exec.LookPath 在 Windows requires recognized extension
		// (.exe/.bat/.cmd). Producing one 的 those without compiler
		// 运行 是 more ceremony than 这 smoke 测试 deserves;
		// logic under 测试 是 exercised 通过 Unix 运行.
		t.Skip("Windows PATH lookup requires an extension-matched binary")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "bash")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake bash: %v", err)
	}
	t.Setenv("PATH", dir)

	// Basename must match fake we planted 在 路径; 目录
	// prefix must 不 exist 所以 literal stat fails.
	missingDir := filepath.Join(dir, "definitely-not-here-"+randSuffix())
	resolved, err := resolveInterpreter(filepath.Join(missingDir, "bash"))
	if err != nil {
		t.Fatalf("expected fallback to succeed, got: %v", err)
	}
	if resolved != fake {
		t.Fatalf("resolved = %q, want %q", resolved, fake)
	}
}

// TestResolveInterpreter_NonENOENTErrorsSurface guards against silently
// falling back 到 路径 当 stat fails 为 reason other than 文件
// being missing. With 目录 在 shebang 路径, os.Stat succeeds
// (no fallback needed), 但 用 EACCES'd 文件 it fails 用 non-
// ENOENT 错误 那 must 是 surfaced — otherwise we'd silently resolve
// different 二进制 off 路径 和 hide real problem.
func TestResolveInterpreter_NonENOENTErrorsSurface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses dir mode permission checks")
	}
	dir := t.TempDir()
	// Put a candidate interpreter inside an unreadable/untraversable dir.
	inner := filepath.Join(dir, "private")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	interp := filepath.Join(inner, "bash")
	if err := os.WriteFile(interp, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write interpreter: %v", err)
	}
	// Drop search 权限 在 inner 所以 os.Stat(interp) returns EACCES.
	if err := os.Chmod(inner, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o755) })

	_, err := resolveInterpreter(interp)
	if err == nil {
		t.Fatal("expected error for unreadable interpreter, got nil")
	}
	// Must NOT have silently fallen back — returned 路径 shouldn't
	// 是 valid resolution; either way, 错误 has 到 surface.
	if !strings.Contains(err.Error(), "permission") {
		t.Fatalf("expected permission-denied error to surface, got: %v", err)
	}
}
