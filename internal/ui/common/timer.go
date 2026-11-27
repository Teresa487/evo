package common

import (
	"fmt"
	"sync"
	"time"
)

// ElapsedSince 是 how long timer started 在 started shows 在 end. Both
// ends snap 到 wall-clock second 所以 every running timer changes 在
// same frame, 和 finished timer never reads less than it last showed.
func ElapsedSince(started, end time.Time) time.Duration {
	return max(0, end.Truncate(time.Second).Sub(started.Truncate(time.Second)))
}

// turnClock tracks 当 each 会话's current turn started. Turns 在
// different sessions 运行 concurrently, 所以 one clock per 会话: turn
// finishing 在 one 会话 must 不 reset elapsed time shown 在
// another.
var turnClock = struct {
	mu      sync.Mutex
	current string
	started map[string]time.Time
}{started: map[string]time.Time{}}

// StartTurn starts sessionID's turn clock 从 now, replacing any earlier
// start.
func StartTurn(sessionID string) {
	if sessionID == "" {
		return
	}
	turnClock.mu.Lock()
	defer turnClock.mu.Unlock()
	turnClock.started[sessionID] = time.Now()
	turnClock.current = sessionID
}

// StopTurn stops tracking sessionID's turn.
func StopTurn(sessionID string) {
	turnClock.mu.Lock()
	defer turnClock.mu.Unlock()
	delete(turnClock.started, sessionID)
	if turnClock.current == sessionID {
		turnClock.current = ""
	}
}

// CurrentElapsed returns formatted elapsed time 为 most recently
// started turn still running, 或 empty string 当 no turn 是 running.
// Render paths 那 show live timer 但 do 不 know which 会话 they
// belong 到, such 作为 在-flight 工具 calls, use 这 instead 的 Elapsed.
func CurrentElapsed() string {
	turnClock.mu.Lock()
	sessionID := turnClock.current
	turnClock.mu.Unlock()
	if sessionID == "" {
		return ""
	}
	return Elapsed(sessionID)
}

// Elapsed returns formatted elapsed time 为 sessionID's turn, 或
// empty string 当 那 会话 是 不 timing one.
func Elapsed(sessionID string) string {
	turnClock.mu.Lock()
	startTime, timing := turnClock.started[sessionID]
	turnClock.mu.Unlock()
	if !timing {
		return ""
	}

	return FormatDuration(ElapsedSince(startTime, time.Now()))
}

// FormatDuration formats duration 作为 human friendly string.
func FormatDuration(elapsed time.Duration) string {
	totalSeconds := int(elapsed.Seconds())
	minutes := int(elapsed.Minutes())
	hours := int(elapsed.Hours())

	switch {
	case hours >= 1:
		return fmt.Sprintf("%dh %dm", hours, minutes%60)
	case minutes >= 1:
		return fmt.Sprintf("%dm %ds", minutes, totalSeconds%60)
	default:
		return fmt.Sprintf("%ds", totalSeconds)
	}
}
