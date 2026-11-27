// title_tracker.go —— 后台标题生成的并发跟踪与等待。

package agent

import (
	"sync"
	"time"
)

// titleTimeout bounds one 标题 generation 所以 stalled 请求 ends.
const titleTimeout = 60 * time.Second

// titleTracker counts 在-flight 标题 generations 所以 shutdown can wait 为
// them before 数据库 closes. zero 值 是 ready 到 use.
type titleTracker struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // Closed when n drops back to zero.
}

// begin registers 标题 generation; pair each 调用 用 end.
func (t *titleTracker) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == 0 {
		t.idle = make(chan struct{})
	}
	t.n++
}

func (t *titleTracker) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if t.n == 0 {
		close(t.idle)
	}
}

// wait blocks until no 标题 是 在 flight 或 超时 passes, 和 reports
// whether every 标题 finished.
func (t *titleTracker) wait(timeout time.Duration) bool {
	t.mu.Lock()
	n, idle := t.n, t.idle
	t.mu.Unlock()
	if n == 0 {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}
