package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func resetProviderState() {
	providerList = nil
	hyperSyncer = &hyperSync{}
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
		name          string
		evoGlobalData string
		xdgDataHome   string
		expected      string
	}{
		{
			name:          "with EVO_GLOBAL_DATA",
			evoGlobalData: "/scratch/data",
			expected:      "/scratch/data/providers.json",
		},
		{
			name:          "EVO_GLOBAL_DATA takes priority over XDG_DATA_HOME",
			evoGlobalData: "/scratch/data",
			xdgDataHome:   "/custom/data",
			expected:      "/scratch/data/providers.json",
		},
		{
			name:        "with XDG_DATA_HOME",
			xdgDataHome: "/custom/data",
			expected:    "/custom/data/evo/providers.json",
		},
		{
			name:     "without either",
			expected: "", // Will use platform-specific default.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EVO_GLOBAL_DATA", tt.evoGlobalData)
			t.Setenv("XDG_DATA_HOME", tt.xdgDataHome)

			result := cachePathFor("providers")
			if tt.expected != "" {
				require.Equal(t, tt.expected, filepath.ToSlash(result))
			} else {
				require.Contains(t, result, "evo")
				require.Contains(t, result, "providers.json")
			}
		})
	}
}

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

// TestLoadProvidersKeepsOnlySignInProviders pins 那目录 只保留可登录的
// provider。登录类 provider 的端点与模型列表无法写进 evo.json，目录必须
// 继续提供它们，否则登录后 SetProviderAPIKey 会找不到 provider 而失败；
// 其余 provider 只能来自用户配置文件。
func TestLoadProvidersKeepsOnlySignInProviders(t *testing.T) {
	t.Parallel()

	providers, err := LoadProviders(t.Context(), &Config{}, nil)
	require.NoError(t, err)

	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		ids = append(ids, string(p.ID))
	}
	require.Equal(t, signInProviderIDs, ids, "only the sign-in providers keep an embedded catalog")

	for _, p := range providers {
		require.NotEmpty(t, p.APIEndpoint, "provider %s needs an endpoint", p.ID)
		require.NotEmpty(t, p.Models, "provider %s needs a model catalog", p.ID)
	}
}
