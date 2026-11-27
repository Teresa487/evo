package config

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

// 一个 background poll 那 refreshes 在 timer used 到 send same dead
// refresh token 到 提供方 once minute 为 hours. Once refused, it
// must keep failing 作为 revoked without another round trip.
func TestRefreshOAuthToken_RefusedRefreshTokenIsNotRetried(t *testing.T) {
	isolateHyperCredentials(t)
	configPath := filepath.Join(t.TempDir(), "crush.json")

	var exchanges atomic.Int64
	store := newRefreshTestStore(t, configPath, func(context.Context, string, string) (*oauth.Token, error) {
		exchanges.Add(1)
		return nil, &oauth.TokenExchangeError{StatusCode: 401, Body: `{"error":"refresh token revoked"}`}
	})

	for range 3 {
		err := store.RefreshOAuthToken(t.Context(), ScopeGlobal, "hyper")
		var exchangeErr *oauth.TokenExchangeError
		require.True(t, errors.As(err, &exchangeErr))
		require.True(t, exchangeErr.IsRefreshTokenRevoked())
	}
	require.Equal(t, int64(1), exchanges.Load())
}

// 一个 failure 那 是 不 拒绝, such 作为 outage, says nothing about
// credential, 所以 next refresh must try again.
func TestRefreshOAuthToken_TransientFailureIsRetried(t *testing.T) {
	isolateHyperCredentials(t)
	configPath := filepath.Join(t.TempDir(), "crush.json")

	var exchanges atomic.Int64
	store := newRefreshTestStore(t, configPath, func(context.Context, string, string) (*oauth.Token, error) {
		exchanges.Add(1)
		return nil, &oauth.TokenExchangeError{StatusCode: 503, Body: `unavailable`}
	})

	for range 2 {
		require.Error(t, store.RefreshOAuthToken(t.Context(), ScopeGlobal, "hyper"))
	}
	require.Equal(t, int64(2), exchanges.Load())
}

// 一个 拒绝 could 是 提供方's mistake, 所以 it 是 只 trusted 为 few
// minutes before token 是 offered again.
func TestRefreshOAuthToken_RefusalExpires(t *testing.T) {
	isolateHyperCredentials(t)
	configPath := filepath.Join(t.TempDir(), "crush.json")

	var exchanges atomic.Int64
	store := newRefreshTestStore(t, configPath, func(context.Context, string, string) (*oauth.Token, error) {
		exchanges.Add(1)
		return nil, &oauth.TokenExchangeError{StatusCode: 401, Body: `{"error":"refresh token revoked"}`}
	})
	clock := time.Now()
	store.now = func() time.Time { return clock }

	require.Error(t, store.RefreshOAuthToken(t.Context(), ScopeGlobal, "hyper"))
	clock = clock.Add(rejectedRefreshTokenTTL - time.Second)
	require.Error(t, store.RefreshOAuthToken(t.Context(), ScopeGlobal, "hyper"))
	require.Equal(t, int64(1), exchanges.Load())

	clock = clock.Add(time.Second)
	require.Error(t, store.RefreshOAuthToken(t.Context(), ScopeGlobal, "hyper"))
	require.Equal(t, int64(2), exchanges.Load())
}
