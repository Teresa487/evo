package copilot

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

// 一个 GitHub token 那 was revoked must surface 作为 revoked credential, 所以
// coordinator asks 为 new sign-在 instead 的 failing turn.
func TestRefreshTokenReportsRevokedGitHubToken(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := getCopilotToken(t.Context(), srv.URL, "revoked-github-token")

	var exchangeErr *oauth.TokenExchangeError
	require.ErrorAs(t, err, &exchangeErr)
	require.True(t, exchangeErr.IsRefreshTokenRevoked())
}
