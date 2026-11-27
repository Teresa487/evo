package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newGatedTitleAgent returns agent whose small (标题) 模型 blocks 它的
// first 流 until release 是 called. 清理 releases gate 和 drains
// titles before 测试 数据库 closes.
func newGatedTitleAgent(t *testing.T) (*sessionAgent, fakeEnv, *gatedStreamModel, func()) {
	t.Helper()
	env := testEnv(t)
	small := &gatedStreamModel{
		text:    "Gated Title",
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	sa := testSessionAgent(env, fastModel{}, small, "system").(*sessionAgent)
	var once sync.Once
	release := func() { once.Do(func() { close(small.gate) }) }
	t.Cleanup(func() {
		release()
		sa.CancelAll()
	})
	return sa, env, small, release
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestTitle_CancelAllWaitsForInFlightTitle(t *testing.T) {
	t.Parallel()
	sa, env, small, release := newGatedTitleAgent(t)

	sess, err := env.sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)

	go sa.GenerateTitle(t.Context(), sess.ID, "hello there")
	waitClosed(t, small.entered, "title stream")

	// No 运行 是 活跃, 所以 只 标题 can hold CancelAll.
	done := make(chan struct{})
	go func() {
		sa.CancelAll()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("CancelAll returned while a title was still generating")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	waitClosed(t, done, "CancelAll")

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "Gated Title", got.Title)
}

func TestTitle_RunDoesNotWaitForTitle(t *testing.T) {
	t.Parallel()
	sa, env, small, release := newGatedTitleAgent(t)

	sess, err := env.sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)

	// 标题 流 stays gated until after 运行 returns, 所以 运行
	// returning 在 all proves it does 不 wait 为 标题.
	runDone := make(chan struct{})
	var runErr error
	go func() {
		defer close(runDone)
		_, runErr = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello there"})
	}()
	waitClosed(t, runDone, "Run")
	require.NoError(t, runErr)
	waitClosed(t, small.entered, "title stream")

	release()
	sa.CancelAll()

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "Gated Title", got.Title)
}

func TestTitle_StartDuringDrainDoesNotPanic(t *testing.T) {
	t.Parallel()
	sa, env, small, release := newGatedTitleAgent(t)

	sess, err := env.sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)

	go sa.GenerateTitle(t.Context(), sess.ID, "first")
	waitClosed(t, small.entered, "title stream")

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(sa.CancelAll)
	}
	for range 8 {
		wg.Go(func() { sa.GenerateTitle(t.Context(), sess.ID, "more") })
	}
	time.Sleep(50 * time.Millisecond)
	release()
	wg.Wait()

	// Every 标题 has ended, 所以 fresh drain returns 在 once.
	require.True(t, sa.titles.wait(0))
}
