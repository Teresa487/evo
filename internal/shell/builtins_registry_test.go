package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestRegisterBuiltin_DispatchedThroughRun verifies 那 处理器 registered
// via RegisterBuiltin 是 dispatched through stateless 运行 路径, receiving
// full args slice 和 I/O streams 从 interpreter 上下文.
func TestRegisterBuiltin_DispatchedThroughRun(t *testing.T) {
	RegisterBuiltin("evotest-echo", func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		io.WriteString(stdout, "builtin:"+args[1])
		return nil
	})
	t.Cleanup(func() { delete(builtins, "evotest-echo") })

	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "evotest-echo hello",
		Cwd:     t.TempDir(),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "builtin:hello" {
		t.Fatalf("stdout = %q, want %q", got, "builtin:hello")
	}
}

// TestRegisterBuiltin_OverridesPATHBinary verifies 那 registered builtin
// takes precedence over executable 的 same name found 在 路径. 这 是
// stated design intent 的 builtinHandler sitting ahead 的 exec layer.
func TestRegisterBuiltin_OverridesPATHBinary(t *testing.T) {
	dir := t.TempDir()
	// Put fake "evotest-shadowed" 二进制 在 路径 那 would print
	// "从-路径" 如果 it ran.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "evotest-shadowed")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho from-path\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	RegisterBuiltin("evotest-shadowed", func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		io.WriteString(stdout, "from-builtin")
		return nil
	})
	t.Cleanup(func() { delete(builtins, "evotest-shadowed") })

	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "evotest-shadowed",
		Cwd:     dir,
		Env:     os.Environ(),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "from-builtin" {
		t.Fatalf("builtin did not shadow PATH binary: stdout = %q, want %q", got, "from-builtin")
	}
}

// TestBuiltinHandler_UnknownFallsThrough verifies 那 unregistered command
// name 是 passed 到 underlying exec layer rather than being swallowed.
func TestBuiltinHandler_UnknownFallsThrough(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "echo fallthrough",
		Cwd:     t.TempDir(),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "fallthrough\n" {
		t.Fatalf("stdout = %q, want %q", got, "fallthrough\n")
	}
}

// TestRegisterBuiltin_JQRegistered verifies 那 jq 是 registered through
// same registry 作为 every other builtin (no special-cased 派发 路径).
func TestRegisterBuiltin_JQRegistered(t *testing.T) {
	if _, ok := builtins["jq"]; !ok {
		t.Fatal("jq is not registered in the builtins map")
	}
}
