package config

import (
	"cmp"
	"context"
	"log/slog"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/discover"
)

// discoveryProbeTimeout bounds inline probe 的 提供方 那 has no
// cached models yet.
const discoveryProbeTimeout = 3 * time.Second

// discoveryJob 是 one custom 提供方 whose models come 从 它的 /models
// 端点.
type discoveryJob struct {
	id           string
	cfg          discover.Config
	providerType string
}

// discoveredModels 是 在-disk 缓存 条目 为 one 提供方. BaseURL 是
// unresolved 配置 值, 所以 pointing 提供方 elsewhere invalidates
// 它的 条目.
type discoveredModels struct {
	BaseURL string          `json:"base_url"`
	Models  []catwalk.Model `json:"models"`
}

// discoveryJobs returns custom providers 那 need 模型 discovery:
// enabled, 用 base URL, 和 either opted 在 through discover_models 或
// lacking an explicit models list.
func discoveryJobs(c *Config, known map[string]bool) []discoveryJob {
	var jobs []discoveryJob
	for id, pc := range c.Providers.Seq2() {
		if known[id] || pc.Disable || pc.BaseURL == "" {
			continue
		}
		wanted := len(pc.Models) == 0
		if pc.AutoDiscoverModels != nil {
			wanted = *pc.AutoDiscoverModels
		}
		if !wanted {
			continue
		}
		jobs = append(jobs, discoveryJob{
			id: id,
			cfg: discover.Config{
				ID:           cmp.Or(pc.ID, id),
				BaseURL:      pc.BaseURL,
				APIKey:       pc.APIKey,
				ExtraHeaders: pc.ExtraHeaders,
			},
			providerType: string(cmp.Or(pc.Type, catwalk.TypeCompletions)),
		})
	}
	return jobs
}

// resolveDiscoveredModels returns discovered models 为 each job. Jobs
// 用 缓存 条目 use it without touching network 和 是 recorded
// 所以 RefreshDiscoveredModels can 更新 them later; rest 是 probed
// inline, bounded 通过 discoveryProbeTimeout 和 ctx.
func (s *ConfigStore) resolveDiscoveredModels(ctx context.Context, jobs []discoveryJob, resolver VariableResolver) map[string][]catwalk.Model {
	found := make(map[string][]catwalk.Model, len(jobs))
	var cached, misses []discoveryJob
	for _, job := range jobs {
		if models, ok := s.cachedModels(job); ok {
			found[job.id] = models
			cached = append(cached, job)
			continue
		}
		misses = append(misses, job)
	}
	s.cachedDiscovery = cached

	probeCtx, cancel := context.WithTimeout(ctx, discoveryProbeTimeout)
	defer cancel()
	for id, models := range s.probeAndCache(probeCtx, misses, resolver) {
		found[id] = models
	}
	return found
}

// RefreshDiscoveredModels re-probes every 提供方 whose models came 从
// discovery 缓存, 和 reloads 配置 当 any 的 them changed. 它
// reports whether reload happened. Meant 到 运行 在 background after
// startup.
func (s *ConfigStore) RefreshDiscoveredModels(ctx context.Context) (bool, error) {
	s.writeMu.RLock()
	jobs := s.cachedDiscovery
	resolver := s.resolver
	s.writeMu.RUnlock()

	if len(jobs) == 0 {
		return false, nil
	}

	before := make(map[string][]catwalk.Model, len(jobs))
	for _, job := range jobs {
		before[job.id], _ = s.cachedModels(job)
	}
	changed := false
	for id, fresh := range s.probeAndCache(ctx, jobs, resolver) {
		changed = changed || !reflect.DeepEqual(before[id], fresh)
	}
	if !changed {
		return false, nil
	}
	return true, s.ReloadFromDisk(ctx)
}

// probeAndCache discovers models 为 jobs concurrently 和 caches every
// non-empty 结果. Failures 是 logged 和 left out 的 returned map.
func (s *ConfigStore) probeAndCache(ctx context.Context, jobs []discoveryJob, resolver VariableResolver) map[string][]catwalk.Model {
	found := make(map[string][]catwalk.Model, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Go(func() {
			models, err := discover.DiscoverModels(ctx, job.cfg, resolver)
			if err != nil {
				slog.Warn("Model discovery failed", "provider", job.id, "error", err)
				return
			}
			if len(models) == 0 {
				return
			}
			if enricher := discover.GetEnricher(job.providerType); enricher != nil {
				models, _ = enricher.EnrichModels(ctx, job.cfg, resolver, models)
			}
			slog.Info("Discovered models for provider", "provider", job.id, "count", len(models))
			s.cacheModels(job, models)
			mu.Lock()
			found[job.id] = models
			mu.Unlock()
		})
	}
	wg.Wait()
	return found
}

// discoveryCachePath 是 empty 当 存储 has no data 目录, which
// disables 缓存.
func (s *ConfigStore) discoveryCachePath(providerID string) string {
	if s.globalDataPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.globalDataPath), "discovered_models", url.PathEscape(providerID)+".json")
}

func (s *ConfigStore) cachedModels(job discoveryJob) ([]catwalk.Model, bool) {
	path := s.discoveryCachePath(job.id)
	if path == "" {
		return nil, false
	}
	entry, _, err := newCache[discoveredModels](path).Get()
	if err != nil || entry.BaseURL != job.cfg.BaseURL || len(entry.Models) == 0 {
		return nil, false
	}
	return entry.Models, true
}

func (s *ConfigStore) cacheModels(job discoveryJob, models []catwalk.Model) {
	path := s.discoveryCachePath(job.id)
	if path == "" {
		return
	}
	entry := discoveredModels{BaseURL: job.cfg.BaseURL, Models: models}
	if err := newCache[discoveredModels](path).Store(entry); err != nil {
		slog.Warn("Failed to cache discovered models", "provider", job.id, "error", err)
	}
}

// mergeModels appends discovered models whose IDs 配置 does 不
// already list; models 从 配置 win.
func mergeModels(configured, discovered []catwalk.Model) []catwalk.Model {
	listed := make(map[string]bool, len(configured))
	for _, m := range configured {
		listed[m.ID] = true
	}
	merged := slices.Clone(configured)
	for _, m := range discovered {
		if !listed[m.ID] {
			merged = append(merged, m)
		}
	}
	return merged
}
