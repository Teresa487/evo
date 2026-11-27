// Package login authenticates Crush 用 OAuth-capable platforms 从
// command line. In 交互式 terminals it runs small Bubble Tea program
// 那 guides 用户 through opening browser 和 waiting 为
// callback. When stdin 是 不 终态, it falls back 到 plain,
// keypress-free flow suitable 为 scripts 和 SSH sessions.
package login

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/pkg/browser"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/oauth/grok"
	"github.com/charmbracelet/crush/internal/oauth/hyper"
	"github.com/charmbracelet/crush/internal/oauth/openai"
)

// Platforms accepted 通过 运行.
const (
	PlatformHyper   = "hyper"
	PlatformCopilot = "copilot"
	PlatformOpenAI  = "openai"
	PlatformGrok    = "grok"
)

// startMessages 是 first lines printed 通过 non-interactive flow.
var startMessages = map[string]string{
	PlatformHyper:   "Initiating device authorization...",
	PlatformCopilot: "Requesting device code from GitHub...",
	PlatformOpenAI:  "Starting browser authorization...",
	PlatformGrok:    "Starting browser authorization...",
}

// titles 是 提供方 names shown 在 mini TUI header.
var titles = map[string]string{
	PlatformHyper:   "Charm Hyper",
	PlatformCopilot: "GitHub Copilot",
	PlatformOpenAI:  "ChatGPT",
	PlatformGrok:    "Grok",
}

// flow 是 提供方-agnostic surface login UIs drive. Start 是
// called once 到 kick off flow; Wait then blocks until 用户
// completes authorization 在 browser, 和 Close releases any
// resources 流程 holds.
type flow interface {
	Start(ctx context.Context) (url string, userCode string, err error)
	Wait(ctx context.Context) (*oauth.Token, error)
	Close()
}

// codeEntryFlow 是 flow whose browser authorization can also finish
// 用 code pasted back 从 提供方's page — 为 当 用户
// declines 权限 或 browser cannot reach loopback
// callback.
type codeEntryFlow interface {
	flow
	CompleteWithCode(ctx context.Context, input string) (*oauth.Token, error)
}

// 运行 authenticates 用 given platform 和 returns resulting
// OAuth token. 它 runs mini TUI 当 stdin 是 终态 和 plain
// 非交互 流程 otherwise.
func Run(ctx context.Context, platform string) (*oauth.Token, error) {
	newFlow, err := flowFor(platform)
	if err != nil {
		return nil, err
	}

	if term.IsTerminal(os.Stdin.Fd()) {
		return runTUI(platform, newFlow)
	}
	return runCLI(ctx, platform, newFlow)
}

// runCLI performs full flow without TUI 和 without requiring any
// keypresses, 所以 it can 是 driven 从 scripts 和 other non-interactive
// sessions.
func runCLI(ctx context.Context, platform string, newFlow func() flow) (*oauth.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	f := newFlow()

	fmt.Println(startMessages[platform])
	url, userCode, err := f.Start(ctx)
	if err != nil {
		return nil, err
	}

	if userCode != "" {
		fmt.Printf("Your code: %s\n", userCode)
	}
	fmt.Printf("Open this URL and enter the code: %s\n", url)

	openBrowser(url)

	fmt.Println("Waiting for authorization...")
	token, err := f.Wait(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Println("Exchanging token...")
	return token, nil
}

// openBrowser opens given URL 在 用户's 默认 browser. If
// browser can't 是 opened, URL 是 printed 为 用户 到 open
// manually.
func openBrowser(rawURL string) {
	if err := browser.OpenURL(rawURL); err != nil {
		fmt.Printf("Please open this URL to authenticate:\n  %s\n", rawURL)
	}
}

// flowFor returns constructor 为 platform's flow.
func flowFor(platform string) (func() flow, error) {
	switch platform {
	case PlatformHyper:
		return func() flow { return &hyperFlow{} }, nil
	case PlatformCopilot:
		return func() flow { return &copilotFlow{} }, nil
	case PlatformOpenAI:
		return func() flow { return &openaiFlow{} }, nil
	case PlatformGrok:
		return func() flow { return &grokFlow{} }, nil
	default:
		return nil, fmt.Errorf("unknown platform: %s", platform)
	}
}

// hyperFlow runs Charm Hyper device code 流程.
type hyperFlow struct {
	deviceCode string
	expiresIn  int
}

func (f *hyperFlow) Start(ctx context.Context) (string, string, error) {
	resp, err := hyper.InitiateDeviceAuth(ctx)
	if err != nil {
		return "", "", err
	}
	f.deviceCode = resp.DeviceCode
	f.expiresIn = resp.ExpiresIn
	return resp.VerificationURL, resp.UserCode, nil
}

func (f *hyperFlow) Wait(ctx context.Context) (*oauth.Token, error) {
	refreshToken, err := hyper.PollForToken(ctx, f.deviceCode, f.expiresIn)
	if err != nil {
		return nil, err
	}

	token, err := hyper.ExchangeToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	introspect, err := hyper.IntrospectToken(ctx, token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("token introspection failed: %w", err)
	}
	if !introspect.Active {
		return nil, errors.New("access token is not active")
	}
	return token, nil
}

func (hyperFlow) Close() {}

// copilotFlow runs GitHub Copilot device code 流程.
type copilotFlow struct {
	deviceCode *copilot.DeviceCode
}

func (f *copilotFlow) Start(ctx context.Context) (string, string, error) {
	dc, err := copilot.RequestDeviceCode(ctx)
	if err != nil {
		return "", "", err
	}
	f.deviceCode = dc
	return dc.VerificationURI, dc.UserCode, nil
}

func (f *copilotFlow) Wait(ctx context.Context) (*oauth.Token, error) {
	return copilot.PollForToken(ctx, f.deviceCode)
}

func (copilotFlow) Close() {}

// openaiFlow runs ChatGPT (OpenAI) authorization code flow 用
// loopback callback 服务端.
type openaiFlow struct {
	f *openai.BrowserFlow
}

func (f *openaiFlow) Start(_ context.Context) (string, string, error) {
	bf, err := openai.StartBrowserFlow()
	if err != nil {
		return "", "", err
	}
	f.f = bf
	// handoff page opens authorization URL 在 tab 那 can close
	// itself 当 finished; opening raw URL would leave tab behind,
	// since browsers refuse 到 close it after consent flow.
	return bf.StartURL(), "", nil
}

func (f *openaiFlow) Wait(ctx context.Context) (*oauth.Token, error) {
	return f.f.Wait(ctx)
}

func (f *openaiFlow) Close() {
	if f.f != nil {
		f.f.Close()
	}
}

// grokFlow runs Grok (xAI) authorization code flow 用 loopback
// callback 服务端. When loopback listener cannot start (remote
// machine, no free port), it falls back 到 device flow, whose code
// 用户 enters 在 accounts.x.ai.
type grokFlow struct {
	f          *grok.BrowserFlow
	deviceCode string
	expiresIn  int
}

func (f *grokFlow) Start(ctx context.Context) (string, string, error) {
	bf, err := grok.StartBrowserFlow()
	if err != nil {
		dc, deviceErr := grok.RequestDeviceCode(ctx)
		if deviceErr != nil {
			return "", "", err
		}
		f.deviceCode = dc.DeviceCode
		f.expiresIn = dc.ExpiresIn
		return dc.VerificationURI, dc.UserCode, nil
	}
	f.f = bf
	// handoff page opens authorization URL 在 tab 那 can close
	// itself 当 finished; opening raw URL would leave tab behind,
	// since browsers refuse 到 close it after consent flow.
	return bf.StartURL(), "", nil
}

func (f *grokFlow) Wait(ctx context.Context) (*oauth.Token, error) {
	if f.deviceCode != "" {
		return grok.PollForToken(ctx, f.deviceCode, f.expiresIn)
	}
	return f.f.Wait(ctx)
}

// CompleteWithCode finishes browser flow 用 code pasted 从
// authorization page. device fallback has nothing 到 paste:
// 它的 code 是 copied out, never entered here.
func (f *grokFlow) CompleteWithCode(ctx context.Context, input string) (*oauth.Token, error) {
	if f.f == nil {
		return nil, errors.New("no browser flow in progress")
	}
	return f.f.CompleteWithCode(ctx, input)
}

func (f *grokFlow) Close() {
	if f.f != nil {
		f.f.Close()
	}
}
