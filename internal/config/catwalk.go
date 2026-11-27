// catwalk.go —— 与 catwalk 模型目录对接：provider 列表、模型能力、默认值。

package config

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/catwalk/v3/pkg/embedded"
)

type catwalkClient interface {
	GetProviders(context.Context, string) ([]catwalk.Provider, error)
}

var _ syncer[[]catwalk.Provider] = (*catwalkSync)(nil)

type catwalkSync struct {
	once       sync.Once
	result     []catwalk.Provider
	err        error
	cache      cache[[]catwalk.Provider]
	client     catwalkClient
	autoupdate bool
	init       atomic.Bool
	updated    atomic.Bool
}

func (s *catwalkSync) Init(client catwalkClient, path string, autoupdate bool) {
	s.client = client
	s.cache = newCache[[]catwalk.Provider](path)
	s.autoupdate = autoupdate
	s.init.Store(true)
}

func (s *catwalkSync) Get(ctx context.Context) ([]catwalk.Provider, error) {
	if !s.init.Load() {
		panic("called Get before Init")
	}

	// 结果 和 错误 是 memoized together 所以 那 every caller
	// sees same outcome, 不 just one 那 won once.
	s.once.Do(func() {
		if !s.autoupdate {
			cached, _, cachedErr := s.cache.Get()
			if len(cached) > 0 && cachedErr == nil {
				slog.Info("Using cached Catwalk providers (auto-update disabled)")
				s.result = cached
				return
			}
			slog.Info("Using embedded Catwalk providers")
			s.result = embedded.GetAll()
			return
		}

		cached, etag, cachedErr := s.cache.Get()
		if len(cached) == 0 || cachedErr != nil {
			// 如果 cached 文件 是 empty, 默认 到 embedded providers
			cached = embedded.GetAll()
		}

		slog.Info("Fetching providers from Catwalk")
		result, err := s.client.GetProviders(ctx, etag)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("Catwalk providers not updated in time")
			s.result = cached
			return
		}
		if errors.Is(err, catwalk.ErrNotModified) {
			slog.Info("Catwalk providers not modified")
			s.result = cached
			return
		}
		if err != nil {
			// Fall back 到 cached (which defaults 到 embedded 如果 empty).
			// Being offline 是 routine 和 fallback 是 sound, 所以 这
			// 是 logged rather than reported 到 caller.
			slog.Warn("Could not fetch providers from Catwalk", "error", err)
			s.result = cached
			return
		}
		if len(result) == 0 {
			s.result = cached
			s.err = errors.New("empty providers list from catwalk")
			return
		}

		// catalog 是 usable 从 here 在. 一个 缓存 写入 failure 只
		// costs next 运行 refresh, 所以 it 是 reported alongside valid
		// 结果 rather than 在 place 的 one.
		s.result = result
		s.err = s.cache.Store(result)
		s.updated.Store(true)
	})
	return s.result, s.err
}

// Updated reports whether last Get replaced catalog 用 fresh
// data 从 Catwalk rather than falling back 到 缓存, embedded
// copy, 或 304 Not Modified 响应. 它 是 false before Get runs 和
// stays false 为 whole process 当 auto-更新 是 disabled, which
// makes it signal 为 refreshing catalogs 那 shadow Catwalk's,
// such 作为 ChatGPT 模型 catalog.
func (s *catwalkSync) Updated() bool {
	return s.updated.Load()
}
