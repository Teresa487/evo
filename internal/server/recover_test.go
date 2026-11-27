package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// TestRecoverHandler_PanicReturns500 verifies 那 panicking 处理器
// surfaces 作为 structured 500 到 客户端, rather than closing
// 连接 silently 和 producing opaque EOF.
func TestRecoverHandler_PanicReturns500(t *testing.T) {
	t.Parallel()

	s := &Server{}
	h := s.recoverHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	require.NotEmpty(t, body)

	var perr proto.Error
	require.NoError(t, json.Unmarshal(body, &perr))
	require.NotEmpty(t, perr.Message)
}

// TestRecoverHandler_NoPanicPassthrough verifies 那 middleware
// does 不 interfere 用 successful responses.
func TestRecoverHandler_NoPanicPassthrough(t *testing.T) {
	t.Parallel()

	s := &Server{}
	h := s.recoverHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTeapot, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

// TestRecoverHandler_PanicAfterWriteHeader verifies 那 如果 处理器
// panics after it has already started writing 响应,
// middleware does 不 attempt 到 overwrite status (which would
// trigger superfluous WriteHeader warning) 但 still logs 和
// recovers.
func TestRecoverHandler_PanicAfterWriteHeader(t *testing.T) {
	t.Parallel()

	s := &Server{}
	h := s.recoverHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late panic")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	require.NotPanics(t, func() { h.ServeHTTP(rec, req) })
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "partial", rec.Body.String())
}

// TestRecoverHandler_AbortHandlerPropagates verifies 那 documented
// http.ErrAbortHandler sentinel 是 re-panicked 所以 net/http 服务端
// can handle it normally (suppress logging, close 连接).
func TestRecoverHandler_AbortHandlerPropagates(t *testing.T) {
	t.Parallel()

	s := &Server{}
	h := s.recoverHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	require.PanicsWithValue(t, http.ErrAbortHandler, func() { h.ServeHTTP(rec, req) })
}
