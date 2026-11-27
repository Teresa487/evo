package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/hyper"
)

// hyperCreditsModel wraps Hyper language 模型 所以 那 every 请求
// also refreshes remaining hypercredit balance 从 /v1/credits
// endpoint. Hyper completions no longer carry balance, 所以 it has 到
// 是 fetched separately. Refreshes 运行 在 background 所以 they never
// add latency 到 turn, 和 只 one runs 在 time: refreshes requested
// while one 是 在 flight 是 coalesced into single follow-up fetch, 所以
// balance still reflects every 请求 那 finished meanwhile.
type hyperCreditsModel struct {
	fantasy.LanguageModel
	apiKey func() string
	fetch  func(context.Context, string) (*int, error)

	mu       sync.Mutex
	inflight bool
	pending  bool
}

// newHyperCreditsModel wraps m 所以 every 请求 refreshes
// hypercredit balance once it finishes. apiKey resolves the Hyper API key
// 在 fetch time. 一个 nil 模型 或 resolver returns m unchanged.
func newHyperCreditsModel(m fantasy.LanguageModel, apiKey func() string) fantasy.LanguageModel {
	if m == nil || apiKey == nil {
		return m
	}
	return &hyperCreditsModel{
		LanguageModel: m,
		apiKey:        apiKey,
		fetch:         hyper.FetchCredits,
	}
}

// Generate implements [fantasy.LanguageModel].
func (m *hyperCreditsModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	resp, err := m.LanguageModel.Generate(ctx, call)
	if err == nil {
		m.refreshBalance()
	}
	return resp, err
}

// 流 implements [fantasy.LanguageModel]. 流 是 consumed after
// 流 returns, 所以 refresh has 到 wait 为 iteration 到 end:
// balance changes 只 once 请求 has been served.
func (m *hyperCreditsModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer m.refreshBalance()
		stream(yield)
	}, nil
}

// refreshBalance starts background fetch 的 hypercredit balance, 或
// marks one pending 当 fetch 是 already running.
func (m *hyperCreditsModel) refreshBalance() {
	m.mu.Lock()
	if m.inflight {
		m.pending = true
		m.mu.Unlock()
		return
	}
	m.inflight = true
	m.mu.Unlock()

	go m.fetchBalance()
}

// fetchBalance fetches balance 在 detached 上下文 所以 cancelled
// 或 finished 运行 still lands 它的 final refresh. Fetch failures 是
// logged 和 otherwise ignored: stale balance beats no balance.
func (m *hyperCreditsModel) fetchBalance() {
	if apiKey := m.apiKey(); apiKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := m.fetch(ctx, apiKey); err != nil {
			slog.Warn("Failed to fetch Hyper credits", "error", err)
		}
		cancel()
	}

	m.mu.Lock()
	m.inflight = false
	pending := m.pending
	m.pending = false
	m.mu.Unlock()

	if pending {
		m.refreshBalance()
	}
}
