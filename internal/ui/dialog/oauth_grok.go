// oauth_grok.go —— Grok 的 OAuth 登录弹窗。

package dialog

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth/grok"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// NewOAuthGrok creates OAuth dialog 为 signing 在 用 Grok
// account. sign-在 starts 用 ChatGPT-style browser flow; 当
// 用户 declines 那 权限 (或 loopback callback 是 out 的
// reach), it falls back 到 device flow, showing code 用户
// enters 在 accounts.x.ai.
func NewOAuthGrok(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
) (*OAuth, tea.Cmd) {
	return newOAuth(com, isOnboarding, provider, model, modelType, &OAuthGrok{})
}

type OAuthGrok struct {
	flow       *grok.BrowserFlow
	cancelFunc context.CancelFunc
}

var _ OAuthProvider = (*OAuthGrok)(nil)

func (m *OAuthGrok) name() string {
	return "Grok"
}

func (m *OAuthGrok) initiateAuth() tea.Msg {
	flow, err := grok.StartBrowserFlow()
	if err == nil {
		m.flow = flow

		return ActionInitiateOAuth{
			VerificationURL: flow.StartURL(),
		}
	}

	// loopback callback listener could 不 start (port unavailable,
	// remote machine): go straight 到 device flow, where 用户
	// opens verification page 和 enters code themselves.
	deviceCode, deviceErr := grok.RequestDeviceCode(context.Background())
	if deviceErr != nil {
		return ActionOAuthErrored{Error: fmt.Errorf("failed to start browser auth: %w (device auth: %v)", err, deviceErr)}
	}
	return deviceCodeInitiate(deviceCode)
}

func (m *OAuthGrok) startPolling(deviceCode string, expiresIn int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFunc = cancel

		if deviceCode != "" {
			token, err := grok.PollForToken(ctx, deviceCode, expiresIn)
			if err != nil {
				if ctx.Err() != nil {
					return nil // cancelled, don't report error.
				}
				return ActionOAuthErrored{Error: err}
			}
			return ActionCompleteOAuth{Token: token}
		}

		token, err := m.flow.Wait(ctx)
		if err == nil {
			return ActionCompleteOAuth{Token: token}
		}
		if ctx.Err() != nil {
			return nil // cancelled, don't report error.
		}

		// browser 权限 was declined (或 callback never
		// arrived): offer device flow instead, whose code 用户
		// enters 在 accounts.x.ai 到 finish signing 在.
		deviceCode, deviceErr := grok.RequestDeviceCode(ctx)
		if deviceErr != nil {
			return ActionOAuthErrored{Error: err}
		}
		return deviceCodeInitiate(deviceCode)
	}
}

func (m *OAuthGrok) stopPolling() tea.Msg {
	if m.cancelFunc != nil {
		m.cancelFunc()
	}
	if m.flow != nil {
		m.flow.Close()
	}
	return nil
}

func (m *OAuthGrok) supportsCodeEntry() bool {
	return true
}

func (m *OAuthGrok) submitCode(input string) tea.Cmd {
	return func() tea.Msg {
		if m.flow == nil {
			return ActionOAuthErrored{Error: fmt.Errorf("no browser flow in progress")}
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFunc = cancel

		token, err := m.flow.CompleteWithCode(ctx, input)
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancelled, don't report error.
			}
			return ActionOAuthErrored{Error: err}
		}
		return ActionCompleteOAuth{Token: token}
	}
}

// deviceCodeInitiate turns device authorization into 消息 那
// moves dialog 到 它的 code-display state.
func deviceCodeInitiate(deviceCode *grok.DeviceCode) tea.Msg {
	return ActionInitiateOAuth{
		DeviceCode:      deviceCode.DeviceCode,
		UserCode:        deviceCode.UserCode,
		VerificationURL: deviceCode.VerificationURI,
		ExpiresIn:       deviceCode.ExpiresIn,
		Interval:        deviceCode.Interval,
	}
}
