package pinentry

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxPassphraseAttempts 是 number 的 times bad credential 是
// re-prompted before giving up, matching GPG's own 重试 count.
const maxPassphraseAttempts = 3

// GPGBinaries 是 set 的 gpg executables shell middleware
// intercepts.
var GPGBinaries = map[string]struct{}{
	"gpg":  {},
	"gpg2": {},
}

// ExitError describes non-zero GPG exit where exit code matters 到
// caller (it 是 surfaced 作为 shell command's exit status).
type ExitError struct {
	// Code 是 process exit status.
	Code int
	// Stderr 是 trimmed excerpt 的 captured GPG stderr.
	Stderr string
}

// 错误 implements 错误.
func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("gpg exited with status %d", e.Code)
	}
	return fmt.Sprintf("gpg exited with status %d: %s", e.Code, e.Stderr)
}

// RunOptions carries everything needed 到 execute single GPG command.
type RunOptions struct {
	// Args 是 full argv including gpg executable 作为 argv[0].
	Args []string
	// Env 是 environment 为 gpg process.
	Env []string
	// Dir 是 working 目录, 或 "" 为 current 目录.
	Dir string
	// Stdin 是 command's standard input (may 是 nil). 它 是 spooled
	// 到 temporary 文件 所以 重试 attempts after bad credential can
	// re-读取 it.
	Stdin io.Reader
	// Stdout receives the command's standard output.
	Stdout io.Writer
	// Stderr receives command's standard 错误.
	Stderr io.Writer
}

// Prompter supplies credential 为 PromptRequest.
type Prompter func(ctx context.Context, req PromptRequest) (string, error)

// Runner drives GPG invocations through loopback pinentry. 一个 nil
// Prompter means credentials cannot 是 collected 在-process, 在 which
// case GPG runs 作为 invoked 和 agent's own pinentry asks.
type Runner struct {
	// Prompter collects credential; may 是 nil.
	Prompter Prompter
	// CacheTimeout enables 在-memory credential 缓存 当 > 0.
	CacheTimeout time.Duration
	// GPGPath 是 resolved gpg 二进制; overrides 路径 lookup 当 set.
	GPGPath string

	cache *passphraseCache
}

func (r *Runner) initCache() {
	if r.CacheTimeout > 0 && r.cache == nil {
		r.cache = newPassphraseCache(r.CacheTimeout)
	}
}

// ClearCache zeroes 和 drops any cached credentials. 它 doubles 作为
// integration shutdown 钩子.
func (r *Runner) ClearCache() {
	r.initCache()
	if r.cache != nil {
		r.cache.clear()
	}
}

// 运行 executes GPG command, prompting 为 credential 只 当
// agent does 不 already have it cached, 和 retrying 在 bad one. When
// loopback pinentry 是 unavailable, GPG runs 作为 invoked 所以 agent's
// own pinentry collects the credential.
func (r *Runner) Run(ctx context.Context, opts RunOptions) error {
	if len(opts.Args) == 0 {
		return errors.New("pinentry: empty gpg argv")
	}

	r.initCache()

	bin := r.GPGPath
	if bin == "" {
		var err error
		bin, err = exec.LookPath(opts.Args[0])
		if err != nil {
			return err
		}
	}

	stdin, cleanup, err := spoolStdin(opts.Stdin)
	if err != nil {
		return err
	}
	defer cleanup()

	// Only the final attempt's output reaches the caller: a failed
	// attempt 可以 leave partial output behind ( clearsign 请求头, say)
	// 那 would corrupt real 结果.
	// ponytail: output 是 held 在 memory; spool it 到 temp 文件 如果
	// large decrypts through Crush ever matter.
	aio := &attemptIO{stdin: stdin}
	defer aio.replay(opts.Stdout, opts.Stderr)

	// Probe: 运行 用 loopback 和 empty passphrase. 这 succeeds
	// immediately 当 key 是 unprotected 或 agent already has
	// the credential cached.
	st := newStatus()
	probeErr := r.exec(ctx, bin, opts, aio, "", st)
	if probeErr == nil {
		return nil
	}
	if !st.credentialRequired() {
		if st.unsupported() {
			return aio.plain(ctx, bin, opts)
		}
		return probeErr
	}

	// operation genuinely needs credential agent does 不
	// have. Collect one 和 重试, re-prompting 在 bad 值.
	//
	// probe 错误 是 deliberately 不 carried into first 提示词:
	// it reflects empty-passphrase probe, 不 用户 attempt, 所以
	// 只 genuine retries ( 用户-provided credential GPG rejected)
	// surface 错误.
	var lastErr error
	for attempt := 1; attempt <= maxPassphraseAttempts; attempt++ {
		secret, perr := r.collectCredential(ctx, st, attempt, lastErr, opts.Args)
		if perr != nil {
			if errors.Is(perr, ErrNoPrompter) {
				return aio.plain(ctx, bin, opts)
			}
			return perr
		}
		st = newStatus()
		err = r.exec(ctx, bin, opts, aio, secret, st)
		if err == nil {
			r.storeCredential(st.cacheKey(), secret)
			return nil
		}
		if st.unsupported() && !st.credentialRequired() {
			return aio.plain(ctx, bin, opts)
		}
		if !st.credentialRequired() {
			return err
		}
		// Never 重放 rejected secret 从 缓存: stale security
		// key PIN would burn card's 重试 计数器.
		r.cache.forget(st.cacheKey())
		lastErr = err
	}
	return lastErr
}

// collectCredential returns credential 为 current attempt,
// preferring optional 缓存 在 first attempt.
func (r *Runner) collectCredential(ctx context.Context, st *status, attempt int, lastErr error, args []string) (string, error) {
	if attempt == 1 {
		if key := st.cacheKey(); key != "" {
			if secret, ok := r.cache.get(key); ok {
				return secret, nil
			}
		}
	}
	req := PromptRequest{
		Key:        st.key(),
		Kind:       classifyKind(st),
		RetryCount: attempt - 1,
	}
	// Security keys 报告 no key ID 在 status output, 所以 fall back
	// 到 key caller asked 到 sign 用.
	if req.Key.KeyID == "" && req.Key.Fingerprint == "" {
		req.Key.KeyID = localUser(args)
	}
	req.Prompt = promptMessage(req.Kind)
	if lastErr != nil {
		req.Error = lastErr.Error()
	}
	if r.Prompter == nil {
		return "", ErrNoPrompter
	}
	return r.Prompter(ctx, req)
}

func (r *Runner) storeCredential(key, secret string) {
	r.initCache()
	if r.cache != nil {
		r.cache.set(key, secret)
	}
}

// exec runs single gpg attempt through 回环 pinentry. An empty
// passphrase 是 probe ( operation succeeds 如果 credential 是
// cached 或 key 是 unprotected). secret 是 written over
// dedicated pipe (fd 3) 作为 `--passphrase-fd 3`.
func (r *Runner) exec(ctx context.Context, bin string, opts RunOptions, aio *attemptIO, passphrase string, st *status) error {
	// passphrase pipe 是 always wired up, even 为 empty probe:
	// gpg 是 told 到 读取 fd 3 和 would fail 用 bad-descriptor
	// 错误 如果 it were missing. 写入 是 best-effort 在 goroutine
	// 所以 gpg 那 exits early (或 never reads fd 3) cannot block.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	go func() {
		_, _ = io.WriteString(pw, passphrase+"\n")
		_ = pw.Close()
	}()

	// loopback flags go first: gpg stops parsing options 在
	// first non-option argument, 所以 flags after 文件 operand would 是
	// taken 作为 more 文件 names.
	args := []string{"--batch", "--pinentry-mode", "loopback", "--passphrase-fd", passphraseFD()}
	files := []*os.File{pr}

	// Status lines name key being unlocked. Callers like git ask 为
	// them 在 stderr, which st already records; 为 everyone else, ask
	// gpg 为 them 在 private pipe 所以 caller's stderr stays clean.
	if !hasStatusFD(opts.Args) {
		sr, sw, err := os.Pipe()
		if err != nil {
			return err
		}
		defer sr.Close()
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(statusWriter{st: st}, sr)
			close(done)
		}()
		defer func() {
			_ = sw.Close()
			<-done
		}()
		args = append(args, "--status-fd", "4")
		files = append(files, sw)
	}

	args = append(args, opts.Args[1:]...)
	return aio.run(ctx, bin, args, opts, st, files)
}

// hasStatusFD reports whether a gpg argv already routes status lines
// somewhere; gpg honors 只 one status destination.
func hasStatusFD(args []string) bool {
	for _, a := range args {
		if a == "--status-fd" || a == "--status-file" ||
			strings.HasPrefix(a, "--status-fd=") || strings.HasPrefix(a, "--status-file=") {
			return true
		}
	}
	return false
}

// attemptIO 是 stdio shared 通过 gpg attempts 的 one [Runner.运行]:
// spooled stdin every attempt re-reads ( caller's reader 是
// consumed once), 和 output 的 latest attempt.
type attemptIO struct {
	stdin          *os.File
	stdout, stderr bytes.Buffer
}

// 运行 executes one gpg attempt 用 given arguments (argv[0]
// excluded), replacing previous attempt's output. Extra files 是
// passed 到 gpg starting 在 fd 3.
func (a *attemptIO) run(ctx context.Context, bin string, args []string, opts RunOptions, st *status, files []*os.File) error {
	if _, err := a.stdin.Seek(0, io.SeekStart); err != nil {
		return err
	}
	a.stdout.Reset()
	a.stderr.Reset()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = opts.Env
	cmd.Dir = opts.Dir
	cmd.Stdin = a.stdin
	cmd.Stdout = &a.stdout
	cmd.Stderr = statusWriter{inner: &a.stderr, st: st}
	cmd.ExtraFiles = files
	prepareProcess(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}
	stopKill := watchCancel(ctx, cmd)
	err := cmd.Wait()
	stopKill()
	if err == nil {
		return nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return &ExitError{Code: ee.ExitCode(), Stderr: st.excerpt()}
	}
	return err
}

// plain runs gpg exactly 作为 caller invoked it, 为 当 loopback
// pinentry 是 unavailable: agent's own pinentry collects
// credential.
func (a *attemptIO) plain(ctx context.Context, bin string, opts RunOptions) error {
	return a.run(ctx, bin, opts.Args[1:], opts, newStatus(), nil)
}

// 重放 writes final attempt's output 到 caller.
func (a *attemptIO) replay(stdout, stderr io.Writer) {
	if stdout != nil {
		_, _ = stdout.Write(a.stdout.Bytes())
	}
	if stderr != nil {
		_, _ = stderr.Write(a.stderr.Bytes())
	}
}

// status accumulates stderr 的 single gpg invocation 所以
// calling flow can classify outcome. 它 captures up 到
// statusCaptureLimit bytes, which 是 ample 为 status/diagnostic lines
// (二进制 payloads go 到 stdout 在 probe 和 在 success).
type status struct {
	mu  sync.Mutex
	raw []byte
	cap int
}

const statusCaptureLimit = 8192

func newStatus() *status { return &status{} }

// statusWriter tees stderr through 到 caller while recording it 为
// classification.
type statusWriter struct {
	inner io.Writer
	st    *status
}

func (w statusWriter) Write(p []byte) (int, error) {
	w.st.record(p)
	if w.inner != nil {
		return w.inner.Write(p)
	}
	return len(p), nil
}

func (s *status) record(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cap < statusCaptureLimit {
		room := statusCaptureLimit - s.cap
		if len(p) > room {
			p = p[:room]
		}
		s.raw = append(s.raw, p...)
		s.cap += len(p)
	}
}

func (s *status) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.raw))
}

func (s *status) excerpt() string { return s.body() }

// cacheKey returns stable identity 为 key being unlocked, 或 ""
// 当 GPG reported none. fingerprint comes first: GPG 2.1+ reports
// KEY_CONSIDERED 但 omits USERID_HINT 当 passphrase comes 从
// 文件 descriptor.
func (s *status) cacheKey() string {
	k := s.key()
	return cmp.Or(k.Fingerprint, k.KeyID, k.Holder)
}

// key parses key details GPG reported 为 credential. 它 reads
// status lines GPG emits alongside credential 请求:
// USERID_HINT (holder), NEED_PASSPHRASE (key ID), KEY_CONSIDERED
// (fingerprint), 和 CARDCTRL (smartcard serial number).
func (s *status) key() Key {
	var k Key
	for _, line := range strings.Split(s.body(), "\n") {
		fs := strings.Fields(line)
		if len(fs) < 3 {
			continue
		}
		switch fs[1] {
		case "USERID_HINT":
			if k.KeyID == "" {
				k.KeyID = fs[2]
			}
			if len(fs) > 3 {
				k.Holder = strings.Join(fs[3:], " ")
			}
		case "NEED_PASSPHRASE":
			if k.KeyID == "" {
				k.KeyID = fs[2]
			}
		case "ENC_TO":
			if k.KeyID == "" {
				k.KeyID = fs[2]
			}
		case "KEY_CONSIDERED", "DECRYPTION_KEY":
			if k.Fingerprint == "" || fs[1] == "KEY_CONSIDERED" {
				k.Fingerprint = fs[2]
			}
		case "CARDCTRL":
			// CARDCTRL <what> [<serialno>]: serial 只 rides
			// along 为 values 那 concern specific card.
			if len(fs) > 3 {
				k.CardNumber = fs[3]
			}
		}
	}
	return k
}

// localUser returns key named 通过 -u/--local-用户 在 gpg argv
// (including bundled short flags such 作为 -bsau KEYID), 或 "".
func localUser(args []string) string {
	for i, arg := range args {
		switch {
		case arg == "--local-user" || arg == "--default-key":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "--local-user="):
			return strings.TrimPrefix(arg, "--local-user=")
		case strings.HasPrefix(arg, "--default-key="):
			return strings.TrimPrefix(arg, "--default-key=")
		case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.HasSuffix(arg, "u"):
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// credentialRequired reports whether gpg rejected attempt because 的
// missing 或 bad credential (所以 we should 提示词 和 重试).
func (s *status) credentialRequired() bool {
	low := strings.ToLower(s.body())
	for _, m := range []string{
		"[gnupg:] need_passphrase",
		"[gnupg:] missing_passphrase",
		"[gnupg:] bad_passphrase",
		"bad passphrase",
		"bad pin",
		"invalid pin",
		"no passphrase given",
		"no passphrase was given",
	} {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// unsupported reports whether agent 是 unable 到 participate 在
// loopback pinentry (所以 we must fall back 到 external pinentry).
func (s *status) unsupported() bool {
	low := strings.ToLower(s.body())
	for _, m := range []string{
		"inappropriate ioctl",
		"no pinentry",
		"not allowed",
		"unknown option",
		"not supported",
	} {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// classifyKind guesses whether credential 是 security key PIN 从
// gpg output; otherwise it 是 passphrase.
func classifyKind(st *status) Kind {
	lower := strings.ToLower(st.body())
	if strings.Contains(lower, "pin") && !strings.Contains(lower, "passphrase") {
		return KindPIN
	}
	return KindPassphrase
}

// ShouldIntercept reports whether external command should 是 routed
// through integrated pinentry runner: gpg 二进制 performing
// credential-relevant operation without 它的 own pinentry management.
func ShouldIntercept(args []string) bool {
	if !IntegrationEnabled() {
		return false
	}
	if len(args) == 0 {
		return false
	}
	base := filepath.Base(args[0])
	if _, ok := GPGBinaries[base]; !ok {
		return false
	}
	// Never touch invocations 那 manage their own credential/pinentry.
	for _, a := range args[1:] {
		switch {
		case strings.HasPrefix(a, "--passphrase") || a == "--passphrase-fd" || a == "--passphrase-file":
			return false
		case a == "--pinentry-mode" || strings.HasPrefix(a, "--pinentry-mode="):
			return false
		}
	}
	return hasCredentialOperation(args)
}

// hasCredentialOperation reports whether args 请求 sign, decrypt,
// 或 symmetric-encrypt operation ( ones 那 can need credential).
func hasCredentialOperation(args []string) bool {
	credentialLong := map[string]bool{
		"--sign": true, "--clearsign": true, "--clear-sign": true,
		"--detach-sign": true, "--decrypt": true, "--symmetric": true,
	}
	credentialShort := map[rune]bool{
		's': true, 'b': true, 'c': true, 'd': true,
	}
	for _, a := range args[1:] {
		switch {
		case strings.HasPrefix(a, "--"):
			if credentialLong[a] {
				return true
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, ch := range a[1:] {
				if credentialShort[ch] {
					return true
				}
			}
		}
	}
	return false
}

// spoolStdin copies command's stdin into temporary 文件 所以 it can
// 是 re-读取 across 重试 attempts. Returns 文件 (positioned 在 0)
// 和 清理 function.
func spoolStdin(r io.Reader) (*os.File, func(), error) {
	f, err := os.CreateTemp("", "crush-gpg-stdin-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
	}
	if r != nil {
		if _, err := io.Copy(f, r); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return f, cleanup, nil
}

// passphraseFD 是 文件 descriptor used 为 passphrase pipe;
// ExtraFiles[0] 是 assigned fd 3 在 child.
func passphraseFD() string { return "3" }
