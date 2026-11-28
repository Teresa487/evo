// dispatch.go —— 把命令分发到内置实现或系统 shell。

package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/crush/internal/filepathext"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// probeWindow 是 how many bytes we 读取 从 head 的 文件 到 decide
// how 到 派发 it. 128 是 plenty 为 shebang line 和 为 magic-byte
// inspection, while small enough 到 make probe cheap 为 users whose
// hooks invoke many scripts.
const probeWindow = 128

// scriptDispatchHandler returns middleware 那 intercepts exec 的
// 路径-prefixed argv[0] (e.g. ./foo.sh, /opt/bin/工具, C:\foo\bar.exe) 和
// dispatches based 在 文件's contents:
//
//  1. Shebang line (#!...) → exec the named interpreter via os/exec.
//     The interpreter 是 resolved literally first, then via 路径 在
//     basename 作为 permissive fallback (所以 #!/bin/bash works 在
//     Windows boxes where Git 为 Windows puts bash.exe 在 路径).
//  2. Known 二进制 magic (MZ, ELF, Mach-O) 或 NUL byte 在 probe
//     窗口 → pass through 到 next 处理器 (mvdan's 默认 exec).
//  3. Otherwise → treat 文件 作为 shell source 和 运行 it 在-process
//     via nested interp.Runner 那 reuses same 处理器 stack.
//
// Non-路径-prefixed argv[0] 和 empty args 是 passed straight through; 这
// 处理器 是 no-op 为 ordinary commands like `echo` 或 `jq`.
//
// blockFuncs 是 block list. 它 gates interpreter shebang script
// names, 和 是 passed 到 nested runner built 为 shell-source case
// 所以 deny rules apply recursively 到 commands 在-process script invokes.
// coreutils 是 likewise passed 到 nested runner 所以 script's core
// utilities resolve same way 它的 parent's do. Neither can reach inside
// child process: once shebang script 是 handed 到 external
// interpreter, whatever 那 interpreter goes 在 到 运行 是 beyond 这
// 处理器's view.
func scriptDispatchHandler(blockFuncs []BlockFunc, coreutils CoreutilsMode) execMiddleware {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 || !isPathPrefixed(args[0]) {
				return next(ctx, args)
			}

			// Resolve relative paths against interpreter's cwd, 不
			// process cwd — 钩子 commands 是 authored 用 钩子
			// Runner's cwd 在 mind 和 sub-shells can cd before exec.
			scriptPath := filepathext.SmartJoin(interp.HandlerCtx(ctx).Dir, args[0])
			probe, err := probeFile(scriptPath)
			if err != nil {
				return err
			}

			switch {
			case hasShebang(probe):
				return dispatchShebang(ctx, scriptPath, probe, args, blockFuncs)
			case isBinary(probe):
				return next(ctx, args)
			default:
				return runShellSource(ctx, scriptPath, args, blockFuncs, coreutils)
			}
		}
	}
}

// isPathPrefixed reports whether argv[0] 是 文件 reference (作为 opposed
// 到 bare command 到 是 resolved via 路径). 一个 路径 reference starts 用
// `./`, `../`, `/`, 或 — 在 Windows — drive-letter prefix.
//
// Note: mvdan already performs tilde expansion during word expansion, 所以
// `~/script.sh` arrives here 作为 absolute 路径. We still 调用 helper
// 在 raw string 到 stay robust 如果 future change ever bypasses 那
// expansion; cover 那 路径 用 regression 测试.
func isPathPrefixed(arg string) bool {
	switch {
	case strings.HasPrefix(arg, "./"),
		strings.HasPrefix(arg, "../"),
		strings.HasPrefix(arg, "/"):
		return true
	}
	if runtime.GOOS == "windows" {
		// Drive-letter paths: C:\foo 或 C:/foo (length check avoids
		// accidentally matching single letter followed 通过 colon).
		if len(arg) >= 3 && isDriveLetter(arg[0]) && arg[1] == ':' &&
			(arg[2] == '\\' || arg[2] == '/') {
			return true
		}
		// Also treat backslash-prefixed UNC-like paths 作为 路径-prefixed.
		if strings.HasPrefix(arg, "\\") {
			return true
		}
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// probeFile reads first probeWindow bytes 的 target 路径. 它
// deliberately does 不 slurp whole 文件: callers 那 need full
// contents (只 shell-source branch) re-open via os.ReadFile. 这
// keeps memory bounded 当 argv[0] turns out 到 是 large 二进制.
//
// Returns errors surfaced 通过 os.Open/os.Stat directly 所以 callers see
// real reason: ENOENT, EACCES, EISDIR, ELOOP, etc.
func probeFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s: is a directory", path)
	}
	probe := make([]byte, probeWindow)
	n, err := io.ReadFull(f, probe)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return probe[:n], nil
}

// hasShebang reports whether probe starts 用 `#!` marker. 一个
// one-byte 文件 那 happens 到 是 `#` 是 不 shebang.
func hasShebang(probe []byte) bool {
	return len(probe) >= 2 && probe[0] == '#' && probe[1] == '!'
}

// isBinary heuristically classifies probe 作为 executable 或 otherwise
// non-文本 文件. 一个 NUL byte 在 first probeWindow bytes 是 classic
// Unix-y 文本-vs-二进制 signal; we additionally recognize known magic
// numbers 所以 we can fast-路径 well-formed binaries 那 happen 到 have no
// NUL 在 first 128 bytes (rare 但 possible 为 small binaries).
func isBinary(probe []byte) bool {
	if bytes.IndexByte(probe, 0) >= 0 {
		return true
	}
	magics := [][]byte{
		{'M', 'Z'},               // Windows PE / DOS MZ.
		{0x7F, 'E', 'L', 'F'},    // ELF.
		{0xFE, 0xED, 0xFA, 0xCE}, // Mach-O 32-bit BE.
		{0xFE, 0xED, 0xFA, 0xCF}, // Mach-O 64-bit BE.
		{0xCF, 0xFA, 0xED, 0xFE}, // Mach-O 64-bit LE.
		{0xCE, 0xFA, 0xED, 0xFE}, // Mach-O 32-bit LE.
		{0xCA, 0xFE, 0xBA, 0xBE}, // Mach-O fat binary.
	}
	for _, m := range magics {
		if bytes.HasPrefix(probe, m) {
			return true
		}
	}
	return false
}

// dispatchShebang parses probe's shebang line 和 execs resolved
// interpreter via os/exec, inheriting parent runner's cwd, env, 和
// stdio. Returns interp.ExitStatus 在 non-zero interpreter exit 所以
// parent interpreter sees it 作为 normal non-zero status.
func dispatchShebang(ctx context.Context, scriptPath string, probe []byte, args []string, blockFuncs []BlockFunc) error {
	sb, err := parseShebang(probe)
	if err != nil {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stderr, "crush: %s: %s\n", scriptPath, err)
		return interp.ExitStatus(126)
	}

	interpreter, err := resolveInterpreter(sb.interpreter)
	if err != nil {
		hc := interp.HandlerCtx(ctx)
		fmt.Fprintf(hc.Stderr, "crush: %s: %s\n", scriptPath, err)
		return interp.ExitStatus(127)
	}

	cmdArgs := append([]string{}, sb.args...)
	cmdArgs = append(cmdArgs, scriptPath)
	cmdArgs = append(cmdArgs, args[1:]...)

	// script picks 它的 own interpreter, 所以 那 choice 是 checked too.
	// Otherwise a shebang naming a denied command runs it under a name the
	// caller never typed. 这 是 last point 在 which deny rule can
	// apply 在 all: past here interpreter 是 separate process 和
	// what it runs 是 nobody's business 但 它的 own.
	for _, blockFunc := range blockFuncs {
		if blockFunc(append([]string{interpreter}, cmdArgs...)) {
			return fmt.Errorf(
				"blocked: %s is a dangerous command, named by the shebang in %s",
				normalizeCommand(interpreter), filepath.Base(scriptPath),
			)
		}
	}

	cmd := exec.CommandContext(ctx, interpreter, cmdArgs...)
	hc := interp.HandlerCtx(ctx)
	cmd.Dir = hc.Dir
	cmd.Env = execEnvList(hc.Env)
	cmd.Stdin = hc.Stdin
	cmd.Stdout = hc.Stdout
	cmd.Stderr = hc.Stderr
	isolateProcess(cmd)
	// Cancellation 必须 take out interpreter's whole process group
	// ( script's own children included), 和 Wait must 不 是 held open
	// forever 通过 descendant still sitting 在 stdio pipes.
	killGroupOnCancel(cmd)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			if code < 0 {
				code = 1
			}
			return interp.ExitStatus(uint8(code))
		}
		return err
	}
	return nil
}

// resolveInterpreter tries literal shebang 路径 first, then falls back
// 到 路径-lookup 在 它的 basename — 但 只 当 literal 路径 是
// genuinely missing. 一个 文件 那 exists 但 fails stat 为 another reason
// (EACCES, ELOOP, etc.) surfaces real 错误: silently resolving
// different 二进制 off 路径 在 那 case would hide real problem 和
// produce surprising behavior 为 用户.
//
// permissive fallback 是 what makes #!/bin/bash portable 到 Windows
// boxes where Git 为 Windows puts bash.exe 在 路径 但 there 是 no
// /bin/bash 在 disk.
func resolveInterpreter(path string) (string, error) {
	_, statErr := os.Stat(path)
	if statErr == nil {
		return path, nil
	}
	if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}

	base := filepath.Base(path)
	if base == "" || base == path && !strings.ContainsAny(path, `/\`) {
		// Already bare name — just do 路径 lookup.
		resolved, err := exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("interpreter %q not found in PATH", path)
		}
		return resolved, nil
	}
	resolved, err := exec.LookPath(base)
	if err != nil {
		return "", fmt.Errorf("interpreter %q not found and %q not in PATH", path, base)
	}
	slog.Debug("Shebang interpreter not found; falling back to PATH",
		"requested", path, "resolved", resolved)
	return resolved, nil
}

// shebang captures parsed `#!` line. interpreter 是 program 到
// invoke; args 是 list 的 extra arguments 到 pass before script
// 路径. kernel's single-arg semantics (为 literal paths 和 为 env
// without `-S`) 是 encoded 通过 returning single-element args slice
// containing the un-tokenized remainder.
type shebang struct {
	interpreter string
	args        []string
}

// parseShebang extracts interpreter invocation 从 probe. 它 tolerates
// CRLF line endings 和 single leading space between `#!` 和 路径.
// env special-cases: `/usr/bin/env NAME [args...]` unwraps 到 NAME 用
// kernel single-arg semantics; `-S` enables tokenized argument splitting.
func parseShebang(probe []byte) (*shebang, error) {
	if !hasShebang(probe) {
		return nil, errors.New("not a shebang")
	}
	line := probe[2:]
	// Take up 到 first newline.
	if idx := bytes.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	// Strip trailing CR (CRLF-authored scripts).
	line = bytes.TrimRight(line, "\r")
	// Strip leading whitespace ("#! /usr/bin/env bash" 是 legal).
	line = bytes.TrimLeft(line, " \t")
	if len(line) == 0 {
		return nil, errors.New("empty shebang")
	}

	var pathStr, rest string
	if idx := bytes.IndexAny(line, " \t"); idx >= 0 {
		pathStr = string(line[:idx])
		rest = strings.TrimLeft(string(line[idx+1:]), " \t")
	} else {
		pathStr = string(line)
	}

	if isEnvShebang(pathStr) {
		return parseEnvShebang(rest)
	}

	// Literal-路径 shebang: kernel semantics pass remainder 作为
	// single argv[1], 不 tokenized.
	sb := &shebang{interpreter: pathStr}
	if rest != "" {
		sb.args = []string{rest}
	}
	return sb, nil
}

// isEnvShebang reports whether shebang 路径 targets `env`. We 受理
// both common absolute paths 和 bare `env` 所以 那 unusual setups
// (NixOS, BSDs) still work.
func isEnvShebang(p string) bool {
	if p == "/usr/bin/env" || p == "/bin/env" {
		return true
	}
	return filepath.Base(p) == "env"
}

// parseEnvShebang handles `/usr/bin/env` rewriting. Without `-S`, the
// remainder after program name 是 single argv[1] (kernel
// single-arg semantics via env, even though real env would fail 到 find
// program named "bash -x"). With `-S`, remainder 是 tokenized 在
// whitespace. Any other `env` flag 是 rejected — forwarding unknown flags
// 到 /usr/bin/env 在 disk 是 subtle portability footgun we don't want.
func parseEnvShebang(rest string) (*shebang, error) {
	if rest == "" {
		return nil, errors.New("env: missing program name")
	}

	useSplit := false
	if strings.HasPrefix(rest, "-") {
		var flag, after string
		if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
			flag = rest[:idx]
			after = strings.TrimLeft(rest[idx+1:], " \t")
		} else {
			flag = rest
			after = ""
		}
		if flag != "-S" {
			return nil, fmt.Errorf("unsupported env flag: %s", flag)
		}
		useSplit = true
		rest = after
		if rest == "" {
			return nil, errors.New("env -S requires a program")
		}
	}

	if rest == "" {
		return nil, errors.New("env: missing program name")
	}

	var prog, remainder string
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		prog = rest[:idx]
		remainder = strings.TrimLeft(rest[idx+1:], " \t")
	} else {
		prog = rest
	}

	sb := &shebang{interpreter: prog}
	if remainder != "" {
		if useSplit {
			sb.args = strings.Fields(remainder)
		} else {
			sb.args = []string{remainder}
		}
	}
	return sb, nil
}

// runShellSource parses 路径's contents 作为 POSIX shell 和 runs it
// 在-process via nested interp.Runner. 它 reuses parent runner's cwd,
// env, 和 stdio, 和 rebuilds Crush 处理器 stack 所以 builtins 和
// 派发 处理器 itself remain available 到 anything script invokes.
// coreutils mode 是 inherited 所以 script resolves core utilities
// same way 它的 parent does. Positional parameters ($1, $2, …) come 从
// args[1:].
//
// 这 是 只 branch 那 reads full 文件; probeFile keeps 它的
// 读取 到 probeWindow bytes 所以 二进制/shebang paths never touch more
// than 128 bytes 的 I/O.
func runShellSource(ctx context.Context, path string, args []string, blockFuncs []BlockFunc, coreutils CoreutilsMode) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	file, err := syntax.NewParser().Parse(bytes.NewReader(data), path)
	if err != nil {
		return fmt.Errorf("could not parse %s: %w", path, err)
	}

	hc := interp.HandlerCtx(ctx)

	opts := []interp.RunnerOption{
		interp.StdIO(hc.Stdin, hc.Stdout, hc.Stderr),
		interp.Interactive(false),
		interp.Env(hc.Env),
		interp.Dir(hc.Dir),
		execHandlerOption(blockFuncs, coreutils),
	}
	if len(args) > 1 {
		// Params 用 leading "--" avoids any 的 args[1:] being
		// misinterpreted 作为 set-options (e.g. 用户 passing "-e" 作为
		// positional arg 到 their script).
		params := append([]string{"--"}, args[1:]...)
		opts = append(opts, interp.Params(params...))
	}

	runner, err := interp.New(opts...)
	if err != nil {
		return fmt.Errorf("could not build runner for %s: %w", path, err)
	}
	return runner.Run(ctx, file)
}

// execEnvList converts expand.Environ 到 []string form 那
// os/exec.Cmd.Env expects. Only exported string variables 是 included,
// matching what real shell would pass 到 child process.
func execEnvList(env expand.Environ) []string {
	var out []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			out = append(out, name+"="+vr.Str)
		}
		return true
	})
	return out
}
