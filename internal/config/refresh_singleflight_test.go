package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func isolateHyperCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("HYPER_API_KEY", "")
	t.Setenv("CRUSH_HYPER_API_KEY", "")
}

// writeTokenToDisk persists token 作为 hyper 提供方 credential 在
// 配置 文件 在 路径, mimicking what another crush instance would leave
// behind after successful 刷新.
func writeTokenToDisk(t *testing.T, path string, token *oauth.Token) {
	t.Helper()
	configContent := fmt.Sprintf(`{
		"providers": {
			"hyper": {
				"api_key": %q,
				"oauth": {
					"access_token": %q,
					"refresh_token": %q,
					"expires_in": %d,
					"expires_at": %d
				}
			}
		}
	}`, token.AccessToken, token.AccessToken, token.RefreshToken, token.ExpiresIn, token.ExpiresAt)
	require.NoError(t, os.WriteFile(path, []byte(configContent), 0o600))
}

// newRefreshTestStore builds ConfigStore whose hyper 提供方 holds
// expired OAuth token, persisted both 在 memory 和 在 disk 在 configPath.
// Stores 那 share configPath also share per-提供方 refresh 锁,
// which lets single 测试 process faithfully simulate two crush instances:
// 锁.文件 opens fresh descriptor per 调用, 所以 two stores block each
// other 在 same 锁 文件 exactly 作为 two processes would.
func newRefreshTestStore(t *testing.T, configPath string, exchange func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error)) *ConfigStore {
	t.Helper()

	expired := &oauth.Token{
		AccessToken:  "at0",
		RefreshToken: "rt0",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
	}
	writeTokenToDisk(t, configPath, expired)

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("hyper", ProviderConfig{
		ID:         "hyper",
		Name:       "Hyper",
		APIKey:     expired.AccessToken,
		OAuthToken: expired,
	})

	return &ConfigStore{
		config:         &Config{Providers: providers},
		globalDataPath: configPath,
		workingDir:     filepath.Dir(configPath),
		exchangeToken:  exchange,
	}
}

// TestRefreshOAuthToken_InProcessSingleFlight verifies 那 storm 的
// concurrent refresh calls 为 same 提供方 collapses into single
// 令牌 exchange.
func TestRefreshOAuthToken_InProcessSingleFlight(t *testing.T) {
	isolateHyperCredentials(t)

	configPath := filepath.Join(t.TempDir(), "crush.json")

	var exchanges atomic.Int64
	store := newRefreshTestStore(t, configPath, func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
		exchanges.Add(1)
		time.Sleep(50 * time.Millisecond) // hold the flight open so peers join
		return &oauth.Token{
			AccessToken:  "at1",
			RefreshToken: "rt1",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		}, nil
	})

	const goroutines = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)
	for range goroutines {
		wg.Go(func() {
			<-start
			errs <- store.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper")
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), exchanges.Load(), "concurrent refreshes should collapse into one exchange")

	pc, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "at1", pc.OAuthToken.AccessToken)
	require.Equal(t, "rt1", pc.OAuthToken.RefreshToken)
}

// TestRefreshOAuthToken_CrossProcessAdopt verifies 那 当 two instances
// share credential, 只 one performs token exchange 和 other
// adopts rotated token 从 disk rather than reusing consumed
// refresh token. fake exchange models rotating 提供方: reusing
// refresh token it has already rotated returns 错误, 所以 second
// exchange would 是 observable 作为 failure.
func TestRefreshOAuthToken_CrossProcessAdopt(t *testing.T) {
	isolateHyperCredentials(t)

	configPath := filepath.Join(t.TempDir(), "crush.json")

	var (
		mu          sync.Mutex
		current     = "rt0" // the only refresh token the server will accept
		exchanges   atomic.Int64
		reuseErrors atomic.Int64
	)
	exchange := func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
		mu.Lock()
		defer mu.Unlock()
		if refreshToken != current {
			reuseErrors.Add(1)
			return nil, fmt.Errorf("refresh token revoked")
		}
		exchanges.Add(1)
		time.Sleep(50 * time.Millisecond) // hold the lock so the peer must wait
		current = "rt1"
		return &oauth.Token{
			AccessToken:  "at1",
			RefreshToken: "rt1",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		}, nil
	}

	// Two stores sharing same 配置 文件 和 refresh 锁 = two
	// "processes".
	a := newRefreshTestStore(t, configPath, exchange)
	b := newRefreshTestStore(t, configPath, exchange)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, s := range []*ConfigStore{a, b} {
		wg.Go(func() {
			<-start
			errs <- s.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper")
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	require.Equal(t, int64(1), exchanges.Load(), "only one instance should exchange")
	require.Equal(t, int64(0), reuseErrors.Load(), "no instance should reuse a rotated refresh token")

	// Both instances converge 在 rotated token.
	for name, s := range map[string]*ConfigStore{"a": a, "b": b} {
		pc, ok := s.config.Providers.Get("hyper")
		require.True(t, ok, name)
		require.Equal(t, "at1", pc.OAuthToken.AccessToken, name)
		require.Equal(t, "rt1", pc.OAuthToken.RefreshToken, name)
	}
}

// rotatingExchange models 提供方 那 rotates refresh tokens 和
// revokes the previous one: presenting anything other than the currently
// live refresh token fails way real reuse-detecting 服务端 would.
// Tokens 是 handed out 作为 在<n>/rt<n> starting 在 next. returned
// counters 报告 successful exchanges 和 reuse attempts.
func rotatingExchange(live string, next int) (exchange func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error), exchanges, reuse *atomic.Int64) {
	var (
		mu        sync.Mutex
		exchanged atomic.Int64
		reused    atomic.Int64
	)
	return func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
		mu.Lock()
		defer mu.Unlock()
		if refreshToken != live {
			reused.Add(1)
			return nil, &oauth.TokenExchangeError{StatusCode: 400, Body: `{"error":"invalid_grant"}`}
		}
		exchanged.Add(1)
		token := &oauth.Token{
			AccessToken:  fmt.Sprintf("at%d", next),
			RefreshToken: fmt.Sprintf("rt%d", next),
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		}
		live = token.RefreshToken
		next++
		return token, nil
	}, &exchanged, &reused
}

// TestRefreshOAuthToken_StalePeerBorrowsRotatedRefreshToken covers the
// instance 那 has been 空闲 while peer rotated credential several
// times. Its own refresh token 是 long dead, 和 token 在 disk has
// itself aged out, 所以 there 是 nothing 到 adopt outright. stale
// instance must still recover 通过 exchanging 用 refresh token 从
// disk rather than presenting 它的 own revoked one, which would revoke
// whole token family 和 force 用户 到 log 在 again.
func TestRefreshOAuthToken_StalePeerBorrowsRotatedRefreshToken(t *testing.T) {
	isolateHyperCredentials(t)

	configPath := filepath.Join(t.TempDir(), "crush.json")
	exchange, exchanges, reuse := rotatingExchange("rt3", 4)
	store := newRefreshTestStore(t, configPath, exchange)

	// Disk holds peer's third rotation, whose 访问令牌 has also
	// expired. In memory we 是 still back 在 original credential.
	writeTokenToDisk(t, configPath, &oauth.Token{
		AccessToken:  "at3",
		RefreshToken: "rt3",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
	})

	require.NoError(t, store.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper"))
	require.Equal(t, int64(1), exchanges.Load())
	require.Equal(t, int64(0), reuse.Load(), "must not present its own revoked refresh token")

	pc, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "at4", pc.OAuthToken.AccessToken)
	require.Equal(t, "rt4", pc.OAuthToken.RefreshToken)
	require.Equal(t, "at4", pc.APIKey)
}

// TestRefreshOAuthToken_AdoptsFresherDiskToken verifies 那 instance
// whose 在-memory credential has aged out adopts peer's still-valid
// token 从 disk without spending exchange 在 all.
func TestRefreshOAuthToken_AdoptsFresherDiskToken(t *testing.T) {
	isolateHyperCredentials(t)

	configPath := filepath.Join(t.TempDir(), "crush.json")
	exchange, exchanges, _ := rotatingExchange("rt9", 10)
	store := newRefreshTestStore(t, configPath, exchange)

	writeTokenToDisk(t, configPath, &oauth.Token{
		AccessToken:  "at9",
		RefreshToken: "rt9",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})

	require.NoError(t, store.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper"))
	require.Equal(t, int64(0), exchanges.Load(), "a usable peer token needs no exchange")

	pc, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "at9", pc.OAuthToken.AccessToken)
	require.Equal(t, "at9", pc.APIKey)
}

// TestRefreshOAuthToken_IgnoresOlderDiskToken guards against walking
// backwards: 配置 文件 holding older credential than one we
// already have must 不 是 adopted 或 borrowed 从.
func TestRefreshOAuthToken_IgnoresOlderDiskToken(t *testing.T) {
	isolateHyperCredentials(t)

	configPath := filepath.Join(t.TempDir(), "crush.json")
	exchange, exchanges, reuse := rotatingExchange("rt0", 1)
	store := newRefreshTestStore(t, configPath, exchange)

	writeTokenToDisk(t, configPath, &oauth.Token{
		AccessToken:  "ancient",
		RefreshToken: "ancient-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-24 * time.Hour).Unix(),
	})

	require.NoError(t, store.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper"))
	require.Equal(t, int64(1), exchanges.Load())
	require.Equal(t, int64(0), reuse.Load())

	pc, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "rt1", pc.OAuthToken.RefreshToken)
}
