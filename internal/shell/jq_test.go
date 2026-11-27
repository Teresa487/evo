package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestJQ_CtxCancel verifies 那 handleJQ polls ctx during iteration 和
// returns ctx.Err() (不 interp.ExitStatus) 当 上下文 是
// cancelled. 这 是 what lets 钩子 timeouts interrupt long-running jq
// filters rather than waiting 为 iterator 到 terminate naturally.
func TestJQ_CtxCancel(t *testing.T) {
	t.Parallel()

	// `range(N)` generates large 流 的 values. With slurped input
	// filter produces all N values 在 序号; ctx cancellation
	// between values 应当 short-circuit loop.
	const filter = "range(10000000)"
	stdin := strings.NewReader("null\n")

	ctx, cancel := context.WithCancel(t.Context())
	// 取消 almost immediately 所以 we catch next iteration check.
	cancel()

	err := handleJQ(ctx, []string{"jq", filter}, stdin, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected ctx cancel error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestJQ_CtxCancel_DuringFilter verifies cancellation mid-流: ctx 是
// cancelled after jq has started producing output, 和 loop must
// observe 取消 在 next iteration rather than running 到
// completion.
func TestJQ_CtxCancel_DuringFilter(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	// 100M values; without ctx polling 这 would take many seconds 到
	// fully emit. With ctx polling the loop exits shortly after the
	// 期限.
	stdin := strings.NewReader("null\n")
	var stdout, stderr bytes.Buffer

	start := time.Now()
	err := handleJQ(ctx, []string{"jq", "-c", "range(100000000)"}, stdin, &stdout, &stderr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	// Allow generous slack 为 slow CI; important invariant 是 那 we
	// don't 运行 all 100M iterations (which would take orders 的 magnitude
	// longer than 1s).
	if elapsed > time.Second {
		t.Fatalf("handleJQ took %v after 50ms timeout; ctx polling is not tight enough", elapsed)
	}
}

// slowReader serves bytes 在 small chunks 用 fixed delay between
// 读取 calls. 它 never blocks indefinitely — each 读取 returns after
// chunkDelay — 所以 cancellation must 是 observed via ctxReader's ctx
// check, 不 通过 underlying reader itself. That isolates
// behavior we want 到 测试: wrapper polling ctx between chunks.
type slowReader struct {
	remaining  []byte
	chunk      int
	chunkDelay time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.remaining) == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.chunkDelay)
	n := min(len(p), min(s.chunk, len(s.remaining)))
	copy(p, s.remaining[:n])
	s.remaining = s.remaining[n:]
	return n, nil
}

// TestJQ_CtxCancel_MidReadAll verifies 那 ctx cancellation observed
// *during* io.ReadAll — after several chunks have already been consumed
// — short-circuits 读取 via ctxReader, rather than draining
// whole source. 这 是 guarantee 钩子 runner relies 在 当
// it feeds a large bytes.Reader payload.
//
// reader serves bytes 在 512-byte chunks 用 5ms gap between
// reads. ctx 是 cancelled after ~50ms, 所以 several chunks have already
// been 读取 当 ctxReader first observes cancellation. 测试
// asserts 那 () we got 上下文.Canceled 错误 和 (b) 调用
// returned well before the reader would have been fully drained.
func TestJQ_CtxCancel_MidReadAll(t *testing.T) {
	t.Parallel()

	const (
		size       = 64 * 1024 * 1024 // 64 MiB
		chunk      = 512
		chunkDelay = 5 * time.Millisecond
	)
	// At 512 bytes / 5ms, draining 64 MiB would take ~11 minutes. Any
	// return within second proves 取消 was observed mid-流, 不
	// after EOF.
	reader := &slowReader{
		remaining:  bytes.Repeat([]byte("a"), size),
		chunk:      chunk,
		chunkDelay: chunkDelay,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// 取消 after enough time 那 several 读取 calls have completed
	// 和 io.ReadAll 是 actively consuming source.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := handleJQ(ctx, []string{"jq", "-R", "."}, reader, io.Discard, io.Discard)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Generous slack 为 slow CI; invariant 是 orders-的-magnitude
	// faster than draining the full source.
	if elapsed > time.Second {
		t.Fatalf("mid-ReadAll cancel took %v; ctxReader is not polling between chunks", elapsed)
	}
	// Sanity check: we should have been cancelled mid-流, 不
	// before any reads happened. If remaining == size, 取消 fired 所以
	// early nothing was consumed — 那's fast-fail 路径, 不
	// mid-读取 guarantee we want 到 verify.
	consumed := size - len(reader.remaining)
	if consumed == 0 {
		t.Fatal("reader was never read from; test did not exercise mid-ReadAll cancel")
	}
	if consumed >= size {
		t.Fatal("reader was fully drained; cancel was not observed mid-read")
	}
}

// failOnReadReader fails 测试 如果 读取 是 ever called. 它 proves 那
// code 路径 short-circuited before touching input, without relying 在
// wall-clock timing.
type failOnReadReader struct {
	t *testing.T
}

func (r failOnReadReader) Read(p []byte) (int, error) {
	r.t.Error("Read called; outer guard did not short-circuit before io.ReadAll")
	return 0, io.EOF
}

// TestJQ_CtxCancel_PreCancel verifies fast-fail 路径: ctx already
// cancelled before handleJQ 是 called returns 上下文.Canceled
// immediately via the outer-loop guard, never entering io.ReadAll.
// Complements TestJQ_CtxCancel_MidReadAll.
//
// invariant — 不 wall-clock timing — 是 what proves guard fired:
// input reader fails 测试 如果 it 是 ever 读取 从. 一个 previous
// version asserted a 100ms ceiling, which measured scheduler latency rather
// than guard 和 flaked 在 contended Windows CI runners under -race.
func TestJQ_CtxCancel_PreCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := handleJQ(ctx, []string{"jq", "-R", "."},
		failOnReadReader{t: t},
		io.Discard, io.Discard)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestJQ_Success confirms ctx-aware refactor did 不 regress
// success 路径.
func TestJQ_Success(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	err := handleJQ(
		t.Context(),
		[]string{"jq", "-c", ".a"},
		strings.NewReader(`{"a":1}`),
		&stdout, io.Discard,
	)
	if err != nil {
		t.Fatalf("handleJQ returned error: %v", err)
	}
	if got := stdout.String(); got != "1\n" {
		t.Fatalf("stdout = %q, want %q", got, "1\n")
	}
}
