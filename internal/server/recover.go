// recover.go —— 请求 panic 的恢复，避免单个请求打挂服务端。

package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// recoverHandler wraps next 处理器 在 panic-recovery middleware.
// If 处理器 panics, panic 是 logged 用 stack trace 和 500
// JSON 错误 是 written 到 客户端 (当 no 响应 has been started
// yet). Without 这, panicking 处理器 closes 连接 silently
// 和 surfaces 作为 opaque EOF 在 客户端 side.
func (s *Server) recoverHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rrw := &recoverResponseWriter{ResponseWriter: w}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler 是 documented way 到 abort
			// 处理器 without logging; preserve 那 contract.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			s.logError(
				r, "Panic in handler",
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			if !rrw.wroteHeader {
				jsonError(rrw, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(rrw, r)
	})
}

// recoverResponseWriter tracks whether 响应 has been started 所以
// recovery middleware knows 如果 it can still 写入 500 错误.
type recoverResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (rrw *recoverResponseWriter) WriteHeader(code int) {
	rrw.wroteHeader = true
	rrw.ResponseWriter.WriteHeader(code)
}

func (rrw *recoverResponseWriter) Write(b []byte) (int, error) {
	rrw.wroteHeader = true
	return rrw.ResponseWriter.Write(b)
}

func (rrw *recoverResponseWriter) Unwrap() http.ResponseWriter {
	return rrw.ResponseWriter
}
