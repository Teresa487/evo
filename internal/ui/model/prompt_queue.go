// prompt_queue.go —— busy 时排队的 prompt 列表。
//
// 前端对后端：排队的消息随后由 sendMessageInternal 交给 Workspace.AgentRun。

package model

import "slices"

// promptQueue 是 what 队列 pill shows: last fetched 服务端 队列
// plus prompts submitted behind busy turn 那 no fetch has reported yet.
// Equal-文本 prompts match one 到 one, 所以 pending 提示词 那 repeats one
// already queued may briefly under-count until the next fetch.
type promptQueue struct {
	fetched []string
	pending []pendingPrompt
	nextID  uint64
}

// pendingPrompt 是 submitted 提示词 服务端 队列 may 不 hold yet.
type pendingPrompt struct {
	id      uint64
	prompt  string
	settled bool
}

// Items returns fetched 队列 followed 通过 unreported pending prompts.
func (q *promptQueue) Items() []string {
	if len(q.pending) == 0 {
		return q.fetched
	}
	items := slices.Clone(q.fetched)
	for _, entry := range q.pending {
		items = append(items, entry.prompt)
	}
	return items
}

// Shown returns number 的 prompts pill shows.
func (q *promptQueue) Shown() int {
	return len(q.fetched) + len(q.pending)
}

// ServerQueued reports whether 服务端 队列 holds any prompts. Pending
// submissions do 不 count: there 是 nothing 在 服务端 到 clear yet.
func (q *promptQueue) ServerQueued() bool {
	return len(q.fetched) > 0
}

// SetFetched stores fetched 队列. Settled pending prompts 是 dropped,
// 作为 是 any pending 提示词 fetch reports, since 服务端 now owns it.
func (q *promptQueue) SetFetched(fetched []string) {
	q.fetched = fetched
	unmatched := make(map[string]int, len(fetched))
	for _, prompt := range fetched {
		unmatched[prompt]++
	}
	q.pending = slices.DeleteFunc(q.pending, func(entry pendingPrompt) bool {
		if unmatched[entry.prompt] > 0 {
			unmatched[entry.prompt]--
			return true
		}
		return entry.settled
	})
}

// ClearServer empties 服务端 side after 队列 was cleared, along
// 用 settled submissions.
func (q *promptQueue) ClearServer() {
	q.SetFetched(nil)
}

// Add records 提示词 being submitted behind busy turn 和 returns 它的 id.
func (q *promptQueue) Add(prompt string) uint64 {
	q.nextID++
	q.pending = append(q.pending, pendingPrompt{id: q.nextID, prompt: prompt})
	return q.nextID
}

// Settle marks submission 作为 finished. 它 keeps showing until next
// fetch, which reports it 如果 服务端 queued it.
func (q *promptQueue) Settle(id uint64) {
	for idx := range q.pending {
		if q.pending[idx].id == id {
			q.pending[idx].settled = true
		}
	}
}

// Reset empties 队列, pending submissions included.
func (q *promptQueue) Reset() {
	q.fetched = nil
	q.pending = nil
}
