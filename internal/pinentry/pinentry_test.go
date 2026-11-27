package pinentry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestParsePS(t *testing.T) {
	t.Parallel()

	out := []byte(`    1 /sbin/launchd
  338 /usr/libexec/logd
 1337 /opt/homebrew/bin/pinentry-curses
 1442 gpg
 2499 gpg-agent
 3000 /usr/bin/Google Chrome Helper
bad line here
`)

	procs := parsePS(out)
	require.Len(t, procs, 6)
	require.Equal(t, Proc{PID: 1, Name: "launchd", Path: "/sbin/launchd"}, procs[0])
	require.Equal(t, Proc{PID: 1337, Name: "pinentry-curses", Path: "/opt/homebrew/bin/pinentry-curses"}, procs[2])
	// Bare names (Linux ps style) carry no 路径.
	require.Equal(t, Proc{PID: 1442, Name: "gpg"}, procs[3])
	// gpg-agent must 不 是 confused 用 gpg.
	require.Equal(t, Proc{PID: 2499, Name: "gpg-agent"}, procs[4])
	// Commands 用 spaces keep full remainder 作为 their name.
	require.Equal(t, Proc{PID: 3000, Name: "Google Chrome Helper", Path: "/usr/bin/Google Chrome Helper"}, procs[5])
}

func TestClassify(t *testing.T) {
	t.Parallel()

	handover, ctrlL, gpg := classify([]Proc{{PID: 1, Name: "pinentry-curses"}, {PID: 2, Name: "gpg"}})
	require.True(t, handover)
	require.True(t, ctrlL)
	require.True(t, gpg)

	handover, ctrlL, gpg = classify([]Proc{{PID: 1, Name: "pinentry-tty"}})
	require.True(t, handover)
	require.False(t, ctrlL, "pinentry-tty reads raw bytes; Ctrl-L would corrupt the passphrase")
	require.False(t, gpg)

	handover, ctrlL, _ = classify([]Proc{{PID: 1, Name: "pinentry"}})
	require.True(t, handover)
	require.True(t, ctrlL, "an unresolvable plain pinentry defaults to the curses behavior")

	// GUI pinentry flavors never need 终态.
	handover, ctrlL, gpg = classify([]Proc{{PID: 1, Name: "pinentry-mac"}, {PID: 2, Name: "pinentry-qt"}})
	require.False(t, handover)
	require.False(t, ctrlL)
	require.False(t, gpg)

	// gpg-agent 是 不 gpg.
	handover, ctrlL, gpg = classify([]Proc{{PID: 1, Name: "gpg-agent"}})
	require.False(t, handover)
	require.False(t, ctrlL)
	require.False(t, gpg)
}

func TestResolveFlavor(t *testing.T) {
	t.Parallel()

	t.Run("direct names", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, flavorCurses, resolveFlavor(Proc{Name: "pinentry-curses"}))
		require.Equal(t, flavorTTY, resolveFlavor(Proc{Name: "pinentry-tty"}))
		require.Equal(t, flavorGUI, resolveFlavor(Proc{Name: "pinentry-mac"}))
	})

	t.Run("symlink to curses", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "pinentry-curses")
		require.NoError(t, os.WriteFile(real, []byte(""), 0o755))
		link := filepath.Join(dir, "pinentry")
		require.NoError(t, os.Symlink(real, link))
		require.Equal(t, flavorCurses, resolveFlavor(Proc{PID: 1, Name: "pinentry", Path: link}))
	})

	t.Run("alternatives chain to gui", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "pinentry-qt")
		require.NoError(t, os.WriteFile(real, []byte(""), 0o755))
		alt := filepath.Join(dir, "alt-pinentry")
		require.NoError(t, os.Symlink(real, alt))
		link := filepath.Join(dir, "pinentry")
		require.NoError(t, os.Symlink(alt, link))
		require.Equal(t, flavorGUI, resolveFlavor(Proc{PID: 1, Name: "pinentry", Path: link}))
	})

	t.Run("real binary defaults to curses", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "pinentry")
		require.NoError(t, os.WriteFile(real, []byte(""), 0o755))
		require.Equal(t, flavorCurses, resolveFlavor(Proc{PID: 1, Name: "pinentry", Path: real}))
	})

	t.Run("unresolvable defaults to curses", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, flavorCurses, resolveFlavor(Proc{PID: 1, Name: "pinentry"}))
	})
}

func newTestService() *Service {
	s := NewService()
	s.reopenWindow = 300 * time.Millisecond
	s.touchGrace = 800 * time.Millisecond
	s.touchMax = 3 * time.Minute
	return s
}

func tickAll(s *Service, start time.Time, procs []Proc, interval time.Duration, n int) time.Time {
	now := start
	for range n {
		s.tick(now, procs)
		now = now.Add(interval)
	}
	return now
}

func TestWatcherPassphraseFlow(t *testing.T) {
	t.Parallel()

	s := newTestService()
	now := time.Now()
	pin := []Proc{{PID: 10, Name: "pinentry-curses"}, {PID: 20, Name: "gpg"}}

	// Dialog appears: 终态 handover requested, dialog redraws 在
	// Ctrl-L.
	s.tick(now, pin)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last)

	// Dialog closes 和 gpg finishes immediately: keep handover 为
	// reopen 窗口, then release.
	now = now.Add(100 * time.Millisecond)
	s.tick(now, nil)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last, "reopen window should keep the handover")

	tickAll(s, now, nil, 100*time.Millisecond, 4)
	require.Equal(t, Event{}, s.last)
	require.Equal(t, stateIdle, s.state)
}

func TestWatcherDialogReopenDebounce(t *testing.T) {
	t.Parallel()

	s := newTestService()
	now := time.Now()
	pin := []Proc{{PID: 10, Name: "pinentry"}}

	s.tick(now, pin)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last)

	// dialog disappears 和 new one (passphrase 重试) appears
	// within reopen 窗口: no 事件 flapping.
	now = now.Add(100 * time.Millisecond)
	s.tick(now, nil)
	now = now.Add(100 * time.Millisecond)
	s.tick(now, pin)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last, "no release while a follow-up dialog opens")
	require.Equal(t, stateDialog, s.state)
}

func TestWatcherGUIPinentryIgnored(t *testing.T) {
	t.Parallel()

	// 一个 "pinentry" dispatcher resolving 到 GUI flavor must 不 trigger
	// 终态 handover.
	dir := t.TempDir()
	real := filepath.Join(dir, "pinentry-mac")
	require.NoError(t, os.WriteFile(real, []byte(""), 0o755))
	link := filepath.Join(dir, "pinentry")
	require.NoError(t, os.Symlink(real, link))

	s := newTestService()
	s.tick(time.Now(), []Proc{{PID: 10, Name: "pinentry", Path: link}})
	require.Equal(t, Event{}, s.last)
	require.Equal(t, stateIdle, s.state)
}

func TestWatcherTouchAfterPassphrase(t *testing.T) {
	t.Parallel()

	s := newTestService()
	now := time.Now()
	pinAndGPG := []Proc{{PID: 10, Name: "pinentry-curses"}, {PID: 20, Name: "gpg"}}
	gpgOnly := []Proc{{PID: 20, Name: "gpg"}}

	// Passphrase dialog 用 gpg waiting behind it.
	s.tick(now, pinAndGPG)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last)

	// Dialog closes, gpg 保持 running: handover released after
	// reopen 窗口, no touch hint yet.
	now = now.Add(100 * time.Millisecond)
	s.tick(now, gpgOnly)
	now = tickAll(s, now, gpgOnly, 100*time.Millisecond, 4)
	require.Equal(t, Event{}, s.last, "handover released, touch hint still within grace")

	// After grace period gpg 是 still waiting: touch hint.
	tickAll(s, now, gpgOnly, 100*time.Millisecond, 10)
	require.Equal(t, Event{TouchPending: true}, s.last)

	// Touch happened, gpg exited: hint cleared.
	s.tick(now, nil)
	require.Equal(t, Event{}, s.last)
	require.Equal(t, stateIdle, s.state)
}

func TestWatcherTouchWithCachedCredentials(t *testing.T) {
	t.Parallel()

	s := newTestService()
	now := time.Now()
	gpgOnly := []Proc{{PID: 20, Name: "gpg2"}}

	// gpg starts waiting without ever showing a dialog (cached
	// passphrase). No hint before the grace period.
	now = tickAll(s, now, gpgOnly, 100*time.Millisecond, 7)
	require.Equal(t, Event{}, s.last)

	// After grace period 用 gpg still waiting: touch hint.
	tickAll(s, now, gpgOnly, 100*time.Millisecond, 2)
	require.Equal(t, Event{TouchPending: true}, s.last)
}

func TestWatcherTouchExpiry(t *testing.T) {
	t.Parallel()

	s := newTestService()
	s.touchGrace = 100 * time.Millisecond
	s.touchMax = 500 * time.Millisecond
	now := time.Now()
	gpgOnly := []Proc{{PID: 20, Name: "gpg"}}

	now = tickAll(s, now, gpgOnly, 100*time.Millisecond, 2)
	require.Equal(t, Event{TouchPending: true}, s.last)

	// Past maximum wait, assume gpg 是 stuck 在 something else 和
	// drop the hint.
	tickAll(s, now, gpgOnly, 100*time.Millisecond, 5)
	require.Equal(t, Event{}, s.last)
}

func TestWatcherDialogReappearsDuringTouchWait(t *testing.T) {
	t.Parallel()

	s := newTestService()
	s.touchGrace = 100 * time.Millisecond
	now := time.Now()
	gpgOnly := []Proc{{PID: 20, Name: "gpg"}}
	pinAndGPG := []Proc{{PID: 10, Name: "pinentry-tty"}, {PID: 20, Name: "gpg"}}

	// Touch wait detected without a dialog.
	now = tickAll(s, now, gpgOnly, 100*time.Millisecond, 2)
	require.Equal(t, Event{TouchPending: true}, s.last)

	// 一个 dialog appears after all (e.g. confirm dialog): hand over
	// 终态 和 clear touch hint. pinentry-tty does 不 support
	// Ctrl-L redraw.
	s.tick(now, pinAndGPG)
	require.Equal(t, Event{Active: true}, s.last)
}

func TestWatcherReleasePrecedesTouchHint(t *testing.T) {
	t.Parallel()

	s := newTestService()
	s.reopenWindow = 100 * time.Millisecond
	s.touchGrace = 100 * time.Millisecond
	now := time.Now()
	pinAndGPG := []Proc{{PID: 10, Name: "pinentry-curses"}, {PID: 20, Name: "gpg"}}
	gpgOnly := []Proc{{PID: 20, Name: "gpg"}}

	s.tick(now, pinAndGPG)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last)

	// dialog disappears; closing 窗口 starts 在 这 tick.
	now = now.Add(20 * time.Millisecond)
	s.tick(now, gpgOnly)
	require.Equal(t, Event{Active: true, CtrlLRedraw: true}, s.last)

	// 一个 slow poll makes reopen 窗口 和 touch grace expire 在
	// same tick. handover release must still 是 published 在 它的
	// own before touch hint, 或 订阅者 waiting 为 release
	// never sees it.
	now = now.Add(300 * time.Millisecond)
	s.tick(now, gpgOnly)
	require.Equal(t, Event{}, s.last, "release is published even when the grace expires in the same tick")

	s.tick(now.Add(20*time.Millisecond), gpgOnly)
	require.Equal(t, Event{TouchPending: true}, s.last)
}

func TestTrackCommand(t *testing.T) {
	t.Parallel()

	s := newTestService()
	done := s.TrackCommand()
	require.Equal(t, 1, s.tracked)
	done()
	require.Equal(t, 0, s.tracked)
	// returned function 是 idempotent.
	done()
	require.Equal(t, 0, s.tracked)
}

func TestServicePublishesOnStart(t *testing.T) {
	t.Parallel()

	s := newTestService()
	s.pollInterval = 5 * time.Millisecond
	var calls int
	s.lister = func(context.Context) ([]Proc, error) {
		calls++
		if calls > 2 {
			return []Proc{{PID: 10, Name: "pinentry-curses"}}, nil
		}
		return nil, nil
	}

	ctx := t.Context()
	s.Start(ctx)

	sub := s.Subscribe(ctx)
	done := s.TrackCommand()
	defer done()

	select {
	case ev := <-sub:
		require.Equal(t, pubsub.UpdatedEvent, ev.Type)
		require.Equal(t, Event{Active: true, CtrlLRedraw: true}, ev.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pinentry event")
	}
}

func TestServiceSleepsWithoutTrackedCommands(t *testing.T) {
	t.Parallel()

	s := newTestService()
	s.pollInterval = time.Millisecond
	s.lister = func(context.Context) ([]Proc, error) {
		return []Proc{{PID: 10, Name: "pinentry-curses"}}, nil
	}

	ctx := t.Context()
	s.Start(ctx)

	sub := s.Subscribe(ctx)
	select {
	case ev := <-sub:
		t.Fatalf("unexpected event without tracked commands: %+v", ev.Payload)
	case <-time.After(50 * time.Millisecond):
	}

	// Once command runs, watcher wakes up 和 notices dialog.
	done := s.TrackCommand()
	defer done()
	select {
	case ev := <-sub:
		require.Equal(t, Event{Active: true, CtrlLRedraw: true}, ev.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pinentry event")
	}
}

// buildFakeProc builds given helper source 从 testdata under
// given 二进制 name 所以 process 表 shows it 作为 那 name (e.g.
// "pinentry-curses", "gpg").
func buildFakeProc(t *testing.T, source, name string) string {
	t.Helper()

	goBin, err := exec.LookPath("go")
	require.NoError(t, err)

	dst := filepath.Join(t.TempDir(), name)
	out, err := exec.CommandContext(t.Context(), goBin, "build", "-o", dst, "testdata/"+source).CombinedOutput()
	require.NoError(t, err, "go build: %s", out)
	return dst
}

func TestServiceDetectsRealProcess(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("process name emulation relies on ps(1)")
	}

	fakePinentry := buildFakeProc(t, "fakeproc.go", "pinentry-curses")
	fakeGPG := buildFakeProc(t, "fakeproc.go", "gpg")

	s := newTestService()
	s.lister = psLister
	s.pollInterval = 20 * time.Millisecond
	s.reopenWindow = 50 * time.Millisecond
	s.touchGrace = 100 * time.Millisecond

	ctx := t.Context()
	s.Start(ctx)
	sub := s.Subscribe(ctx)

	expectEvent := func(want Event) {
		t.Helper()
		for {
			select {
			case ev := <-sub:
				if ev.Payload == want {
					return
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out waiting for event %+v", want)
			}
		}
	}

	done := s.TrackCommand()
	defer done()

	// 一个 "pinentry-curses" process appears while gpg runs: handover.
	gpgCmd := exec.CommandContext(ctx, fakeGPG, "30")
	require.NoError(t, gpgCmd.Start())
	defer func() { _ = gpgCmd.Process.Kill() }()
	pinCmd := exec.CommandContext(ctx, fakePinentry, "30")
	require.NoError(t, pinCmd.Start())

	expectEvent(Event{Active: true, CtrlLRedraw: true})

	// dialog closes 但 gpg keeps waiting: handover released, then
	// the security key touch hint appears.
	require.NoError(t, pinCmd.Process.Kill())
	_ = pinCmd.Wait()
	expectEvent(Event{})
	expectEvent(Event{TouchPending: true})

	// gpg exits: hint cleared.
	require.NoError(t, gpgCmd.Process.Kill())
	_ = gpgCmd.Wait()
	expectEvent(Event{})
}
