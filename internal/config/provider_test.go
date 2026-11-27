package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func resetProviderState() {
	providerOnce = sync.Once{}
	providerList = nil
	providerErr = nil
	catwalkSyncer = &catwalkSync{}
	hyperSyncer = &hyperSync{}
}

func TestProviders_Integration_AutoUpdateDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	// Use 测试-specific instance 到 avoid global state interference.
	testCatwalkSyncer := &catwalkSync{}
	testHyperSyncer := &hyperSync{}

	originalCatwalSyncer := catwalkSyncer
	originalHyperSyncer := hyperSyncer
	defer func() {
		catwalkSyncer = originalCatwalSyncer
		hyperSyncer = originalHyperSyncer
	}()

	catwalkSyncer = testCatwalkSyncer
	hyperSyncer = testHyperSyncer

	resetProviderState()
	defer resetProviderState()

	cfg := &Config{
		Options: &Options{
			DisableProviderAutoUpdate: true,
		},
	}

	providers, err := Providers(cfg)
	require.NoError(t, err)
	require.NotNil(t, providers)
	require.Greater(t, len(providers), 5, "Expected embedded providers")
}

func TestProviders_Integration_WithMockClients(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	// 创建 fresh syncers 为 这 测试.
	testCatwalkSyncer := &catwalkSync{}
	testHyperSyncer := &hyperSync{}

	// Initialize 用 mock clients.
	mockCatwalkClient := &mockCatwalkClient{
		providers: []catwalk.Provider{
			{Name: "Provider1", ID: "p1"},
			{Name: "Provider2", ID: "p2"},
		},
	}
	mockHyperClient := &mockHyperClient{
		provider: catwalk.Provider{
			Name: "Hyper",
			ID:   "hyper",
			Models: []catwalk.Model{
				{ID: "hyper-1", Name: "Hyper Model"},
			},
		},
	}

	catwalkPath := tmpDir + "/crush/providers.json"
	hyperPath := tmpDir + "/crush/hyper.json"

	testCatwalkSyncer.Init(mockCatwalkClient, catwalkPath, true)
	testHyperSyncer.Init(mockHyperClient, hyperPath, true)

	// Get providers 从 each syncer.
	catwalkProviders, err := testCatwalkSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, catwalkProviders, 2)

	hyperProvider, err := testHyperSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Hyper", hyperProvider.Name)

	// Verify total.
	allProviders := append(catwalkProviders, hyperProvider)
	require.Len(t, allProviders, 3)
}

func TestProviders_Integration_WithCachedData(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	// 创建 缓存 files.
	catwalkPath := tmpDir + "/crush/providers.json"
	hyperPath := tmpDir + "/crush/hyper.json"

	require.NoError(t, os.MkdirAll(tmpDir+"/crush", 0o755))

	// 写入 Catwalk 缓存.
	catwalkProviders := []catwalk.Provider{
		{Name: "Cached1", ID: "c1"},
		{Name: "Cached2", ID: "c2"},
	}
	data, err := json.Marshal(catwalkProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(catwalkPath, data, 0o644))

	// 写入 Hyper 缓存.
	hyperProvider := catwalk.Provider{
		Name: "Cached Hyper",
		ID:   "hyper",
	}
	data, err = json.Marshal(hyperProvider)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hyperPath, data, 0o644))

	// 创建 fresh syncers.
	testCatwalkSyncer := &catwalkSync{}
	testHyperSyncer := &hyperSync{}

	// Mock clients 那 return ErrNotModified.
	mockCatwalkClient := &mockCatwalkClient{
		err: catwalk.ErrNotModified,
	}
	mockHyperClient := &mockHyperClient{
		err: catwalk.ErrNotModified,
	}

	testCatwalkSyncer.Init(mockCatwalkClient, catwalkPath, true)
	testHyperSyncer.Init(mockHyperClient, hyperPath, true)

	// Get providers - 应当 use cached.
	catwalkResult, err := testCatwalkSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, catwalkResult, 2)
	require.Equal(t, "Cached1", catwalkResult[0].Name)

	hyperResult, err := testHyperSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Cached Hyper", hyperResult.Name)
}

func TestProviders_Integration_CatwalkFailsHyperSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	testCatwalkSyncer := &catwalkSync{}
	testHyperSyncer := &hyperSync{}

	// Catwalk fails, Hyper succeeds.
	mockCatwalkClient := &mockCatwalkClient{
		err: catwalk.ErrNotModified, // Will use embedded.
	}
	mockHyperClient := &mockHyperClient{
		provider: catwalk.Provider{
			Name: "Hyper",
			ID:   "hyper",
			Models: []catwalk.Model{
				{ID: "hyper-1", Name: "Hyper Model"},
			},
		},
	}

	catwalkPath := tmpDir + "/crush/providers.json"
	hyperPath := tmpDir + "/crush/hyper.json"

	testCatwalkSyncer.Init(mockCatwalkClient, catwalkPath, true)
	testHyperSyncer.Init(mockHyperClient, hyperPath, true)

	catwalkResult, err := testCatwalkSyncer.Get(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, catwalkResult) // Should have embedded.

	hyperResult, err := testHyperSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Hyper", hyperResult.Name)
}

func TestProviders_Integration_BothFail(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	testCatwalkSyncer := &catwalkSync{}
	testHyperSyncer := &hyperSync{}

	// Both fail.
	mockCatwalkClient := &mockCatwalkClient{
		err: catwalk.ErrNotModified,
	}
	mockHyperClient := &mockHyperClient{
		provider: catwalk.Provider{}, // Empty provider.
	}

	catwalkPath := tmpDir + "/crush/providers.json"
	hyperPath := tmpDir + "/crush/hyper.json"

	testCatwalkSyncer.Init(mockCatwalkClient, catwalkPath, true)
	testHyperSyncer.Init(mockHyperClient, hyperPath, true)

	catwalkResult, err := testCatwalkSyncer.Get(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, catwalkResult) // Should fall back to embedded.

	hyperResult, err := testHyperSyncer.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Charm Hyper", hyperResult.Name) // Falls back to embedded when no models.
}

func TestCache_StoreAndGet(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/test.json"

	cache := newCache[[]catwalk.Provider](cachePath)

	providers := []catwalk.Provider{
		{Name: "Provider1", ID: "p1"},
		{Name: "Provider2", ID: "p2"},
	}

	// 存储.
	err := cache.Store(providers)
	require.NoError(t, err)

	// Get.
	result, etag, err := cache.Get()
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, "Provider1", result[0].Name)
	require.NotEmpty(t, etag)
}

func TestCache_GetNonExistent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/nonexistent.json"

	cache := newCache[[]catwalk.Provider](cachePath)

	_, _, err := cache.Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read provider cache file")
}

func TestCache_GetInvalidJSON(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/invalid.json"

	require.NoError(t, os.WriteFile(cachePath, []byte("invalid json"), 0o644))

	cache := newCache[[]catwalk.Provider](cachePath)

	_, _, err := cache.Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to unmarshal provider data from cache")
}

func TestCachePathFor(t *testing.T) {
	tests := []struct {
		name            string
		crushGlobalData string
		xdgDataHome     string
		expected        string
	}{
		{
			name:            "with CRUSH_GLOBAL_DATA",
			crushGlobalData: "/scratch/data",
			expected:        "/scratch/data/providers.json",
		},
		{
			name:            "CRUSH_GLOBAL_DATA takes priority over XDG_DATA_HOME",
			crushGlobalData: "/scratch/data",
			xdgDataHome:     "/custom/data",
			expected:        "/scratch/data/providers.json",
		},
		{
			name:        "with XDG_DATA_HOME",
			xdgDataHome: "/custom/data",
			expected:    "/custom/data/crush/providers.json",
		},
		{
			name:     "without either",
			expected: "", // Will use platform-specific default.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CRUSH_GLOBAL_DATA", tt.crushGlobalData)
			t.Setenv("XDG_DATA_HOME", tt.xdgDataHome)

			result := cachePathFor("providers")
			if tt.expected != "" {
				require.Equal(t, tt.expected, filepath.ToSlash(result))
			} else {
				require.Contains(t, result, "crush")
				require.Contains(t, result, "providers.json")
			}
		})
	}
}

// TestProviders_KeepsCatalogWhenCachingFails covers case 那 used 到
// sign Hyper users out: 提供方 list was fetched successfully 但 could
// 不 是 written 到 在-disk 缓存, 和 Providers discarded it. Hyper's
// endpoint 和 models live 在 catalog rather than 在 用户's 配置,
// 所以 losing it there removed 提供方 entirely 和 invalidated
// 用户's saved 模型.
func TestProviders_KeepsCatalogWhenCachingFails(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	// 一个 文件 where 目录 needs 到 是, 所以 every 缓存 写入 fails.
	blocked := filepath.Join(tmpDir, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("block"), 0o644))
	unwritable := filepath.Join(blocked, "subdir", "cache.json")

	resetProviderState()
	defer resetProviderState()

	// Prime both syncers 用 mock clients 所以 Providers reuses memoized
	// outcome instead 的 reaching network.
	catwalkSyncer.Init(&mockCatwalkClient{
		providers: []catwalk.Provider{{Name: "Provider1", ID: "p1"}},
	}, unwritable, true)
	hyperSyncer.Init(&mockHyperClient{
		provider: catwalk.Provider{
			Name:   "Hyper",
			ID:     "hyper",
			Models: []catwalk.Model{{ID: "hyper-1", Name: "Hyper Model"}},
		},
	}, unwritable, true)

	catwalkProviders, catwalkErr := catwalkSyncer.Get(t.Context())
	require.Error(t, catwalkErr, "cache write should fail")
	require.NotEmpty(t, catwalkProviders, "syncer still returns a usable catalog")

	hyperProvider, hyperErr := hyperSyncer.Get(t.Context())
	require.Error(t, hyperErr, "cache write should fail")
	require.Equal(t, "Hyper", hyperProvider.Name)

	providers, err := Providers(&Config{Options: &Options{}})

	// failure 是 reported, 但 作为 warning alongside usable catalog.
	require.Error(t, err)
	require.Len(t, providers, 2)
	require.Equal(t, catwalk.InferenceProvider("hyper"), providers[0].ID, "Hyper stays at the front")
	require.Equal(t, catwalk.InferenceProvider("p1"), providers[1].ID)
}

// TestProviders_FallsBackToEmbeddedHyper checks 那 Hyper 是 still 在
// catalog 当 it could 不 是 fetched 在 all, using copy bundled 用
// 这 release.
func TestProviders_FallsBackToEmbeddedHyper(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	resetProviderState()
	defer resetProviderState()

	catwalkSyncer.Init(&mockCatwalkClient{
		providers: []catwalk.Provider{{Name: "Provider1", ID: "p1"}},
	}, filepath.Join(tmpDir, "providers.json"), true)
	hyperSyncer.Init(&mockHyperClient{
		err: errors.New("network error"),
	}, filepath.Join(tmpDir, "hyper.json"), true)

	_, _ = catwalkSyncer.Get(t.Context())
	_, _ = hyperSyncer.Get(t.Context())

	providers, err := Providers(&Config{Options: &Options{}})
	require.NoError(t, err)
	require.Len(t, providers, 2)
	require.Equal(t, catwalk.InferenceProvider("hyper"), providers[0].ID)
	require.NotEmpty(t, providers[0].Models, "the embedded Hyper provider carries models")
}

// TestProviders_HonorsDisableDefaultProviders makes sure the embedded Hyper
// fallback does 不 smuggle 默认 提供方 back 在.
func TestProviders_HonorsDisableDefaultProviders(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	resetProviderState()
	defer resetProviderState()

	providers, err := Providers(&Config{
		Options: &Options{DisableDefaultProviders: true},
	})
	require.NoError(t, err)
	require.Empty(t, providers)
}

// TestCacheStore_ReplacesFileInsteadOfRewritingIt guards property 那
// several Crush instances depend 在: 提供方 缓存 是 swapped into place
// 作为 finished 文件, never truncated 和 refilled underneath reader 那 是
// already reading it. 一个 reader 那 loses 那 race cannot parse catalog
// 和 silently falls back 到 bundled copy.
func TestCacheStore_ReplacesFileInsteadOfRewritingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	c := newCache[[]catwalk.Provider](path)

	require.NoError(t, c.Store([]catwalk.Provider{{ID: "first", Name: "First"}}))
	before, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, c.Store([]catwalk.Provider{{ID: "second", Name: "Second"}}))
	after, err := os.Stat(path)
	require.NoError(t, err)

	// os.Stat 在 Windows resolves 文件 identity lazily 通过 reopening 路径,
	// 所以 both stats describe whichever 文件 路径 points 在 通过 time
	// they 是 compared 和 SameFile cannot observe replacement.
	// 写入 路径 是 shared, 所以 asserting 这 在 other platforms covers
	// it. checks below still 运行 everywhere.
	if runtime.GOOS != "windows" {
		require.False(t, os.SameFile(before, after),
			"the cache should be replaced by a rename, not rewritten in place")
	}

	// new contents 是 完成 和 no temporary files 是 left behind.
	got, _, err := c.Get()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, catwalk.InferenceProvider("second"), got[0].ID)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the cache file should remain")
	require.Equal(t, "providers.json", entries[0].Name())
}
