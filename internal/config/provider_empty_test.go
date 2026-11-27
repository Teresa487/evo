package config

import (
	"context"
	"os"
	"testing"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

type emptyProviderClient struct{}

func (m *emptyProviderClient) GetProviders(context.Context, string) ([]catwalk.Provider, error) {
	return []catwalk.Provider{}, nil
}

// TestCatwalkSync_GetEmptyResultFromClient tests 那 当 客户端 returns
// empty list, we fall back 到 cached providers 和 return 错误.
func TestCatwalkSync_GetEmptyResultFromClient(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	syncer := &catwalkSync{}
	client := &emptyProviderClient{}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty providers list from catwalk")
	require.NotEmpty(t, providers) // Should have embedded providers as fallback.

	// Check 那 no 缓存 文件 was created 为 empty results.
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "Cache file should not exist for empty results")
}
