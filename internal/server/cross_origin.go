// cross_origin.go —— 跨域访问控制：限制哪些来源可以连接服务端。

package server

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// crossOriginHandler rejects requests 那 web page could have made
// 用户's browser send. API has no authentication, 所以 over TCP any
// page 用户 visits could otherwise 读取 工作区 environments 和 运行
// shell commands. Browsers cannot reach Unix socket 或 named pipe, 所以
// requests over those pass through 原样, whatever 主机 they carry.
//
// Host check defeats DNS rebinding: page 在 domain attacker
// controls 那 re-resolves 它的 own name 到 这 machine 是 same-origin
// 用 服务端 作为 far 作为 browser 是 concerned, 但 它的 requests
// still carry 那 name 作为 their Host. Sec-Fetch-Site 和 Origin
// checks reject cross-site requests, such 作为 form 或 fetch POST 用
// 文本/plain body, which browsers send without CORS preflight. Clients
// other than browsers normally send neither 请求头.
func (s *Server) crossOriginHandler(next http.Handler) http.Handler {
	if s.network == "unix" || s.network == "npipe" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAllowedHost(r.Host) {
			s.logError(r, "Rejected request with an unrecognized Host header", "host", r.Host)
			jsonError(w, http.StatusForbidden,
				"host not allowed: connect via localhost, an IP address, or the host the server listens on")
			return
		}
		if isCrossOrigin(r) {
			s.logError(r, "Rejected cross-origin request",
				"origin", r.Header.Get("Origin"),
				"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
			jsonError(w, http.StatusForbidden, "cross-origin requests are not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAllowedHost reports whether host, 请求's Host header, names
// 服务端 在 way DNS rebinding cannot forge: localhost, IP literal, 或
// host 服务端 listens 在. crush 客户端 sends 它的 --host
// address, which passes whenever 服务端 was started 用 same one.
func (s *Server) isAllowedHost(host string) bool {
	name := (&url.URL{Host: host}).Hostname()
	if strings.EqualFold(name, "localhost") {
		return true
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	return strings.EqualFold(name, (&url.URL{Host: s.Addr}).Hostname())
}

// isCrossOrigin reports whether browser marked r 作为 sent 通过 page 从
// another origin. Fetch metadata 是 checked where browser sends it,
// 和 Origin header covers older browsers.
func isCrossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err != nil || u.Host != r.Host
}
