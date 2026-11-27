// background.go —— 后台 shell 任务的管理与输出读取。

package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxFinishedJobs bounds how many finished jobs keep their output
	// readable. oldest 是 dropped first.
	MaxFinishedJobs = 250
	// CompletedJobRetention 是 how long finished job's output stays
	// readable.
	CompletedJobRetention = 8 * time.Hour
)

// BackgroundShell represents shell running 在 background.
type BackgroundShell struct {
	ID          string
	Command     string
	Description string
	Shell       *Shell
	WorkingDir  string
	owner       any // Who started the shell, for KillOwned
	ctx         context.Context
	cancel      context.CancelFunc
	stdout      *cappedBuffer
	stderr      *cappedBuffer
	done        chan struct{}
	exitErr     error
	completedAt atomic.Int64 // Unix timestamp when job completed (0 if still running)
}

// BackgroundShellManager manages 后台 shell instances.
type BackgroundShellManager struct {
	mu     sync.Mutex
	shells map[string]*BackgroundShell
}

var (
	backgroundManager     *BackgroundShellManager
	backgroundManagerOnce sync.Once
	idCounter             atomic.Uint64
)

// newBackgroundShellManager creates a new BackgroundShellManager instance.
func newBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{shells: map[string]*BackgroundShell{}}
}

// GetBackgroundShellManager returns singleton 后台 shell manager.
func GetBackgroundShellManager() *BackgroundShellManager {
	backgroundManagerOnce.Do(func() {
		backgroundManager = newBackgroundShellManager()
	})
	return backgroundManager
}

// Start creates 和 starts new background shell 用 given command,
// unless maxJobs jobs 是 already running. shell 是 tagged 用 owner,
// comparable 值 identifying who started it, 所以 那
// [BackgroundShellManager.KillOwned] can stop it 用 那 owner's other
// shells.
func (m *BackgroundShellManager) Start(ctx context.Context, owner any, workingDir string, blockFuncs []BlockFunc, coreutils CoreutilsMode, command, description string, maxJobs int) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if running := m.prune(); running >= maxJobs {
		return nil, fmt.Errorf("maximum number of running background jobs (%d) reached. Please terminate or wait for some jobs to complete", maxJobs)
	}

	id := fmt.Sprintf("%03X", idCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		BlockFuncs: blockFuncs,
		Coreutils:  coreutils,
	})

	shellCtx, cancel := context.WithCancel(ctx)

	bgShell := &BackgroundShell{
		ID:          id,
		Command:     command,
		Description: description,
		WorkingDir:  workingDir,
		Shell:       shell,
		owner:       owner,
		ctx:         shellCtx,
		cancel:      cancel,
		stdout:      newCappedBuffer(maxKeptOutput),
		stderr:      newCappedBuffer(maxKeptOutput),
		done:        make(chan struct{}),
	}

	m.shells[id] = bgShell

	go func() {
		defer close(bgShell.done)

		err := shell.ExecStream(shellCtx, command, bgShell.stdout, bgShell.stderr)

		bgShell.exitErr = err
		bgShell.completedAt.Store(time.Now().Unix())
	}()

	return bgShell, nil
}

// prune drops finished jobs 那 是 past retention 或 beyond
// MaxFinishedJobs, 和 reports how many jobs 是 still running. Callers
// 必须 hold m.mu.
func (m *BackgroundShellManager) prune() (running int) {
	cutoff := time.Now().Add(-CompletedJobRetention).Unix()
	var finished []*BackgroundShell
	for id, shell := range m.shells {
		switch at := shell.completedAt.Load(); {
		case at == 0:
			running++
		case at < cutoff:
			delete(m.shells, id)
		default:
			finished = append(finished, shell)
		}
	}

	if over := len(finished) - MaxFinishedJobs; over > 0 {
		slices.SortFunc(finished, func(a, b *BackgroundShell) int {
			return cmp.Compare(a.completedAt.Load(), b.completedAt.Load())
		})
		for _, shell := range finished[:over] {
			delete(m.shells, shell.ID)
		}
	}
	return running
}

// take removes 和 returns shell 用 given ID.
func (m *BackgroundShellManager) take(id string) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	if !ok {
		return nil, fmt.Errorf("background shell not found: %s", id)
	}
	delete(m.shells, id)
	return shell, nil
}

// Get retrieves background shell 通过 ID.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	return shell, ok
}

// Remove stops tracking 后台 shell without terminating it.
func (m *BackgroundShellManager) Remove(id string) error {
	_, err := m.take(id)
	return err
}

// killWaitTimeout bounds how long Kill waits 为 cancelled shell 到
// 报告 done before giving up 和 returning.
const killWaitTimeout = 5 * time.Second

// Kill terminates background shell 通过 ID. 它 never blocks indefinitely:
// stubborn descendant 可以 keep shell's pipes open past cancellation,
// 所以 it stops waiting once ctx 是 done 或 killWaitTimeout elapses. In 那
// case kill signal has been sent 但 process may still 是 exiting.
func (m *BackgroundShellManager) Kill(ctx context.Context, id string) error {
	shell, err := m.take(id)
	if err != nil {
		return err
	}
	shell.cancel()
	timer := time.NewTimer(killWaitTimeout)
	defer timer.Stop()
	select {
	case <-shell.done:
		return nil
	case <-ctx.Done():
		return errors.New("kill signal sent, process may still be exiting")
	case <-timer.C:
		return errors.New("kill signal sent, process may still be exiting")
	}
}

// BackgroundShellInfo contains information about 后台 shell.
type BackgroundShellInfo struct {
	ID          string
	Command     string
	Description string
}

// List returns all 后台 shell IDs.
func (m *BackgroundShellManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Keys(m.shells))
}

// KillAll terminates all background shells. provided 上下文 bounds how
// long function waits 为 each shell 到 exit.
func (m *BackgroundShellManager) KillAll(ctx context.Context) {
	m.mu.Lock()
	shells := m.shells
	m.shells = map[string]*BackgroundShell{}
	m.mu.Unlock()

	killShells(ctx, shells)
}

// KillOwned terminates background shells started 在 behalf 的 owner,
// leaving every other owner's shells running. provided 上下文 bounds
// how long function waits 为 each shell 到 exit.
func (m *BackgroundShellManager) KillOwned(ctx context.Context, owner any) {
	m.mu.Lock()
	shells := map[string]*BackgroundShell{}
	for id, shell := range m.shells {
		if shell.owner == owner {
			shells[id] = shell
			delete(m.shells, id)
		}
	}
	m.mu.Unlock()

	killShells(ctx, shells)
}

// killShells cancels given shells 和 waits 为 each 到 exit, 或 为
// ctx 到 是 done.
func killShells(ctx context.Context, shells map[string]*BackgroundShell) {
	var wg sync.WaitGroup
	for _, shell := range shells {
		wg.Go(func() {
			shell.cancel()
			select {
			case <-shell.done:
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
}

// GetOutput returns current output 的 background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// OutputSeq returns 计数器 那 increments whenever stdout 或 stderr
// receives bytes, allowing pollers 到 skip unchanged output without copying.
func (bs *BackgroundShell) OutputSeq() uint64 {
	return bs.stdout.seq.Load() + bs.stderr.seq.Load()
}

// GetOutputTail returns 在 most last maxBytes 的 stdout 和 stderr.
func (bs *BackgroundShell) GetOutputTail(maxBytes int) (stdout, stderr string) {
	return bs.stdout.Tail(maxBytes), bs.stderr.Tail(maxBytes)
}

// IsDone checks 如果 background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Done returns 渠道 那 是 closed 当 background shell exits.
func (bs *BackgroundShell) Done() <-chan struct{} {
	return bs.done
}

// Wait blocks until 后台 shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}

func (bs *BackgroundShell) WaitContext(ctx context.Context) bool {
	select {
	case <-bs.done:
		return true
	case <-ctx.Done():
		return false
	}
}
