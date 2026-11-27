package mcpoauth

import (
	"log/slog"
	"sync"

	"golang.org/x/oauth2"
)

// NewSavingTokenSource wraps oauth2.TokenSource 和 calls saver whenever
// access token changes (i.e. 在 refresh). 这 ensures refreshed tokens
// 是 persisted automatically without re-prompting 用户.
//
// Returns nil 如果 wrapped 是 nil. Returns wrapped directly 如果 saver 是 nil.
func NewSavingTokenSource(wrapped oauth2.TokenSource, config *oauth2.Config, initialToken *oauth2.Token, saver func(*oauth2.Config, *oauth2.Token)) oauth2.TokenSource {
	if wrapped == nil {
		return nil
	}
	if saver == nil {
		return wrapped
	}
	var accessToken string
	if initialToken != nil {
		accessToken = initialToken.AccessToken
	}
	return &savingTokenSource{
		src:         wrapped,
		saver:       saver,
		config:      config,
		accessToken: accessToken,
	}
}

type savingTokenSource struct {
	mu          sync.Mutex
	src         oauth2.TokenSource
	saver       func(*oauth2.Config, *oauth2.Token)
	config      *oauth2.Config
	accessToken string
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		slog.Debug("Token refresh failed", "error", err)
		return nil, err
	}
	s.mu.Lock()
	changed := s.accessToken != tok.AccessToken
	if changed {
		s.accessToken = tok.AccessToken
	}
	s.mu.Unlock()
	if changed {
		s.saver(s.config, tok)
	}
	return tok, nil
}
