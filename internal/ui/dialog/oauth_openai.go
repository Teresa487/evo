package dialog

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// NewOAuthOpenAI creates OAuth dialog 为 signing 在 用 ChatGPT
// 账户. Unlike device-流程 providers, 浏览器 流程 has no code
// 到 paste: 用户 authorizes 在 browser 和 loopback callback
// delivers 结果.
func NewOAuthOpenAI(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
) (*OAuth, tea.Cmd) {
	return newOAuth(com, isOnboarding, provider, model, modelType, &OAuthOpenAI{})
}

type OAuthOpenAI struct {
	flow       *openai.BrowserFlow
	cancelFunc context.CancelFunc
}

var _ OAuthProvider = (*OAuthOpenAI)(nil)

func (m *OAuthOpenAI) name() string {
	return "ChatGPT"
}

func (m *OAuthOpenAI) initiateAuth() tea.Msg {
	flow, err := openai.StartBrowserFlow()
	if err != nil {
		return ActionOAuthErrored{Error: fmt.Errorf("failed to start browser auth: %w", err)}
	}
	m.flow = flow

	return ActionInitiateOAuth{
		VerificationURL: flow.StartURL(),
	}
}

func (m *OAuthOpenAI) startPolling(_ string, _ int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFunc = cancel

		token, err := m.flow.Wait(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancelled, don't report error.
			}
			return ActionOAuthErrored{Error: err}
		}

		return ActionCompleteOAuth{Token: token}
	}
}

func (m *OAuthOpenAI) stopPolling() tea.Msg {
	if m.cancelFunc != nil {
		m.cancelFunc()
	}
	if m.flow != nil {
		m.flow.Close()
	}
	return nil
}

// supportsCodeEntry reports whether 提供方's flow can finish 用
// pasted code. Only Grok 浏览器 流程 offers manual fallback.
func (m *OAuthOpenAI) supportsCodeEntry() bool {
	return false
}

// submitCode 是 unreachable 为 providers without code 条目.
func (m *OAuthOpenAI) submitCode(string) tea.Cmd {
	return nil
}
