// Package callback renders browser page 用户 lands 在 在 end 的
// OAuth redirect 流程.
//
// page 是 只 片段 的 authorization 用户 sees outside
// 终态, 所以 it 是 worth more than line 的 plain 文本: it reports
// whether 授权 worked, names what was authorized, explains any
// failure 在 提供方's own words, 和 offers 到 close itself.
//
// Rendering 是 self-contained. Markup, styles, script, 和 artwork 是
// embedded 在 二进制, 所以 page works 用 no network access beyond
// an optional web font.
package callback

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"time"
)

//go:embed page.html page.css page.js heartbit.svg heartbit-grumpy.svg charm.svg
var assets embed.FS

// closeDelay 是 how long page counts down before asking browser 到
// close tab. Long enough 到 读取 outcome, short enough 不 到 feel
// like waiting.
const closeDelay = 5 * time.Second

// tmpl 是 parsed once 在 startup. 一个 parse failure means embedded
// template 是 broken, which 是 build-time mistake rather than anything
// 用户 can cause, 所以 panicking here fails fast 和 loudly.
var tmpl = template.Must(template.ParseFS(assets, "page.html"))

// 结果 describes outcome 的 authorization attempt, 或
// handoff page 那 precedes one.
type Result struct {
	// Subject names what was being authorized, such 作为 MCP 服务端 name.
	// Optional; 当 empty page simply omits it.
	Subject string

	// ContinueURL, 当 set, renders page 作为 handoff: one button
	// 那 opens real authorization URL 在 new tab. new tab
	// keeps 这 page 作为 它的 opener, which 是 what lets it close itself
	// once 授权 finishes. Without 移交, 浏览器
	// refuses 窗口.close() because consent screens behind it
	// lengthen the tab's history.
	ContinueURL string

	// ErrorCode 是 OAuth 错误 code (为 example "access_denied").
	// 一个 non-empty 值 renders page 在 它的 failure state.
	ErrorCode string

	// ErrorDescription 是 提供方's human-readable explanation. 它 是
	// shown alongside ErrorCode 和 may 是 empty.
	ErrorDescription string
}

// Failed reports whether 结果 describes failed authorization.
func (r Result) Failed() bool { return r.ErrorCode != "" }

// 写入 renders callback page 为 given 结果 到 w. 它 always
// writes 完成 page: 如果 template execution somehow fails midway,
// 错误 是 returned 所以 caller can log it, 但 用户 是 never shown
// a blank tab.
func Write(w io.Writer, r Result) error {
	css, err := assets.ReadFile("page.css")
	if err != nil {
		return fmt.Errorf("read callback stylesheet: %w", err)
	}
	js, err := assets.ReadFile("page.js")
	if err != nil {
		return fmt.Errorf("read callback script: %w", err)
	}
	mark, err := assets.ReadFile("heartbit.svg")
	if err != nil {
		return fmt.Errorf("read callback artwork: %w", err)
	}
	grumpy, err := assets.ReadFile("heartbit-grumpy.svg")
	if err != nil {
		return fmt.Errorf("read callback grumpy artwork: %w", err)
	}
	logo, err := assets.ReadFile("charm.svg")
	if err != nil {
		return fmt.Errorf("read callback logo: %w", err)
	}

	data := struct {
		Title            string
		Kind             string
		Heading          string
		Detail           string
		Subject          string
		ContinueURL      string
		ErrorCode        string
		ErrorDescription string
		Status           string
		CloseDelay       int
		CSS              template.CSS
		JS               template.JS
		Heartbit         template.HTML
		Charm            template.HTML
		Favicon          template.URL
	}{
		Subject:          r.Subject,
		ContinueURL:      r.ContinueURL,
		ErrorCode:        r.ErrorCode,
		ErrorDescription: r.ErrorDescription,
		CSS:              template.CSS(css),
		JS:               template.JS(js),
		Charm:            template.HTML(logo),
	}

	// artwork reflects outcome: smiling heart 在 success,
	// grumpy one 当 authorization did 不 go through. favicon
	// matches 所以 tab itself carries state.
	art := mark
	if r.Failed() {
		art = grumpy
	}
	data.Heartbit = template.HTML(art)
	data.Favicon = template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(art))

	if r.ContinueURL != "" {
		data.Title = "Authorize — Crush"
		data.Kind = "continue"
		data.Heading = "One more click"
		data.Detail = "Continue to the authorization page for"
		if r.Subject == "" {
			data.Detail = "Continue to the authorization page."
		}
		data.Status = "The authorization page will close itself when you’re done."
	} else if r.Failed() {
		data.Title = "Authorization failed — Crush"
		data.Kind = "failed"
		data.Heading = "Authorization failed"
		data.Detail = "Evo was not granted access to"
		if r.Subject == "" {
			data.Detail = "Evo was not granted access."
		}
		// 一个 failed page keeps itself open: reader needs reason,
		// 和 closing tab out 从 under them would take it away.
		data.Status = "Close this tab and try again from Crush."
	} else {
		data.Title = "Authorized — Crush"
		data.Kind = "ok"
		data.Heading = "You’re all set"
		data.Detail = "Evo is now connected to"
		if r.Subject == "" {
			data.Detail = "Evo is now connected."
		}
		// Replaced 通过 countdown 作为 soon 作为 script runs, 所以 这
		// 文本 是 what reader without JavaScript 是 left 用.
		data.Status = "You can close this tab."
		data.CloseDelay = int(closeDelay.Seconds())
	}

	return tmpl.Execute(w, data)
}

// Serve writes callback page 作为 完成 HTTP 响应, choosing
// status code 那 matches outcome. Errors 是 logged 通过 caller;
// page itself 是 best effort because 通过 这 point browser 是
// already committed 到 rendering whatever arrives.
func Serve(w http.ResponseWriter, r Result) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// page reflects one-time authorization 结果 和 must never 是
	// replayed 从 缓存 在 later visit 到 same localhost URL.
	w.Header().Set("Cache-Control", "no-store")
	if r.Failed() {
		w.WriteHeader(http.StatusBadRequest)
	}
	return Write(w, r)
}
