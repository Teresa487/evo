// Package pinentry detects 终态-based GPG pinentry dialogs
// (pinentry-curses, pinentry-tty) spawned 通过 gpg-agent while Crush owns
// 终态, 和 publishes events 所以 TUI can hand 终态 over 为
// passphrase 提示词 和 reclaim it afterwards.
//
// gpg-agent spawns pinentry program itself, 所以 dialog 是 不
// child 的 shell command 那 triggered signing operation.
// 只 reliable way 到 notice it 是 到 watch process 表.
// watcher polls while Crush 是 executing shell commands (和 while
// 提示词 是 活跃), 和 publishes [事件] whenever derived state
// changes:
//
//   - 活跃: 终态 pinentry dialog 是 present (或 transitioning
//     between dialogs) 和 needs exclusive control 的 终态.
//   - TouchPending: passphrase dialog closed (或 never appeared
//     because credentials 是 cached 通过 gpg-agent) 但 gpg 是 still
//     waiting, which almost always means security key 是 waiting 为
//     touch.
package pinentry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/pubsub"
)

// 事件 describes state 的 终态-based GPG pinentry dialogs.
type Event struct {
	// 活跃 reports 那 终态 pinentry dialog 是 displayed (或 在
	// short transition 窗口 between two dialogs) 和 needs exclusive
	// control 的 终态.
	Active bool `json:"active"`
	// TouchPending reports 那 gpg 是 still waiting after passphrase
	// dialog closed (或 never appeared because credentials were cached),
	// which almost always means security key 是 waiting 为 touch.
	TouchPending bool `json:"touch_pending"`
	// CtrlLRedraw reports 那 活跃 pinentry dialog 是 ncurses
	// flavor whose UI can 是 repainted 通过 injecting Ctrl-L into
	// 终态 input 队列. Only meaningful 当 活跃 是 true.
	CtrlLRedraw bool `json:"ctrl_l_redraw,omitempty"`
}

// watcherState 是 state 的 pinentry dialog tracker.
type watcherState uint8

const (
	// stateIdle: no 终态 pinentry dialog 是 present.
	stateIdle watcherState = iota
	// stateDialog: 终态 pinentry dialog 是 present.
	stateDialog
	// stateClosing: dialog disappeared recently; it may 是 replaced 通过
	// follow-up dialog (e.g. passphrase 重试), 所以 终态 handover
	// 是 kept 为 short 窗口 到 avoid flicker.
	stateClosing
)

const (
	defaultPollInterval = 100 * time.Millisecond
	defaultReopenWindow = 300 * time.Millisecond
	defaultTouchGrace   = 800 * time.Millisecond
	defaultTouchMax     = 3 * time.Minute
)

// Service watches process 表 为 终态-based pinentry dialogs
// 和 publishes [事件] state changes. Use [Service.TrackCommand] 到 tell
// watcher 当 Crush 是 executing shell commands; polling 只 happens
// while commands 运行 或 dialog 是 活跃.
type Service struct {
	*pubsub.Broker[Event]

	mu        sync.Mutex
	wake      chan struct{}
	started   bool
	tracked   int
	state     watcherState
	closingAt time.Time
	// gpgWait 是 base time 为 security-key touch grace period.
	// 它 是 refreshed while dialog 是 open 所以 clock effectively
	// starts 当 last dialog closes, 和 set 在 first gpg
	// sighting 当 no dialog ever appears (cached credentials).
	gpgWait time.Time
	// ctrlL tracks whether currently 活跃 pinentry dialog redraws
	// 在 Ctrl-L (ncurses flavors).
	ctrlL bool
	last  Event

	lister       Lister
	pollInterval time.Duration
	reopenWindow time.Duration
	touchGrace   time.Duration
	touchMax     time.Duration
}

// NewService creates a new pinentry watcher service.
func NewService() *Service {
	return &Service{
		Broker:       pubsub.NewBroker[Event](),
		wake:         make(chan struct{}, 1),
		lister:       psLister,
		pollInterval: defaultPollInterval,
		reopenWindow: defaultReopenWindow,
		touchGrace:   defaultTouchGrace,
		touchMax:     defaultTouchMax,
	}
}

var defaultService = NewService()

// DefaultService returns the process-wide pinentry watcher. The shell
// execution layer reports command lifetimes 到 it via [TrackCommand], 和
// app starts 和 subscribes 到 it.
func DefaultService() *Service {
	return defaultService
}

// TrackCommand marks start 的 shell command 在 默认 service
// 和 returns function 那 must 是 called 当 command finishes.
func TrackCommand() func() {
	return defaultService.TrackCommand()
}

// TrackCommand marks start 的 shell command, keeping watcher
// polling 在 full rate while it runs. returned function marks end
// 的 command 和 是 safe 到 调用 multiple times.
func (s *Service) TrackCommand() func() {
	s.mu.Lock()
	s.tracked++
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.tracked > 0 {
				s.tracked--
			}
			s.mu.Unlock()
		})
	}
}

// Start launches watch loop. 它 是 no-op 如果 service 是 already
// running, 和 service may 是 started again after given 上下文 是
// cancelled.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.started = false
			s.mu.Unlock()
		}()
		s.loop(ctx)
	}()
}

// loop polls process 表 while shell commands 是 tracked 或
// pinentry dialog 是 活跃, 和 sleeps otherwise.
func (s *Service) loop(ctx context.Context) {
	for {
		s.mu.Lock()
		busy := s.tracked > 0 || s.state != stateIdle || !s.gpgWait.IsZero()
		s.mu.Unlock()

		if !busy {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(s.pollInterval):
		}

		procs, err := s.lister(ctx)
		if err != nil {
			slog.Debug("Pinentry watcher: failed to list processes", "error", err)
			continue
		}
		s.tick(time.Now(), procs)
	}
}

// tick advances watcher state machine 用 process 表 snapshot.
func (s *Service) tick(now time.Time, procs []Proc) {
	pin, ctrlL, gpg := classify(procs)

	s.mu.Lock()
	// justReleased marks tick 在 which handover 是 released.
	// 一个 slow poll can make reopen 窗口 和 touch grace expire
	// 在 same tick; release must still 是 published 在 它的 own
	// before any touch hint, 所以 subscribers see both events.
	var justReleased bool
	switch s.state {
	case stateIdle:
		if pin {
			s.state = stateDialog
		}
	case stateDialog:
		if !pin {
			s.state = stateClosing
			s.closingAt = now
		}
	case stateClosing:
		if pin {
			s.state = stateDialog
		} else if now.Sub(s.closingAt) >= s.reopenWindow {
			s.state = stateIdle
			justReleased = true
		}
	}

	if pin {
		s.ctrlL = ctrlL
	}
	if s.state == stateIdle {
		s.ctrlL = false
	}

	if !gpg {
		s.gpgWait = time.Time{}
	} else if s.state != stateIdle || s.gpgWait.IsZero() {
		// While dialog 是 open (或 may reopen), gpg waiting 是 explained
		// 通过 dialog itself: keep resetting touch clock 所以
		// grace period starts 当 last dialog closes. When no dialog
		// 是 involved, clock starts 在 first gpg sighting.
		s.gpgWait = now
	}

	waiting := !s.gpgWait.IsZero() && now.Sub(s.gpgWait) >= s.touchGrace && now.Sub(s.gpgWait) <= s.touchMax
	ev := Event{
		Active:       s.state != stateIdle,
		TouchPending: s.state == stateIdle && !justReleased && gpg && waiting,
		CtrlLRedraw:  s.state != stateIdle && s.ctrlL,
	}
	changed := ev != s.last
	if changed {
		s.last = ev
	}
	s.mu.Unlock()

	if changed {
		slog.Debug("Pinentry state changed", "active", ev.Active, "touch_pending", ev.TouchPending)
		s.PublishMustDeliver(context.Background(), pubsub.UpdatedEvent, ev)
	}
}
