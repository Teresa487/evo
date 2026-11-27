package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Draining body 到 log it waits 为 流 到 end, which turns every
// 流 into one blocking 读取.
func TestHTTPLoggerDoesNotBufferStreamingResponses(t *testing.T) {
	t.Parallel()

	slog.SetLogLoggerLevel(slog.LevelDebug)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: ping\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		// Hold 响应 open, way 提供方 does mid-turn.
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := NewHTTPClient()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 读取 through local rather than resp.Body directly. body 是 读取
	// 从 goroutine, 和 bodyclose loses track 的 响应 那 escapes
	// into closure, 所以 it reports body 作为 never closed even though
	// defer above closes it.
	body := resp.Body
	first := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := body.Read(buf)
		first <- string(buf[:n])
	}()

	select {
	case got := <-first:
		require.Contains(t, got, "ping", "the first event should arrive before the response ends")
	case <-time.After(2 * time.Second):
		t.Fatal("the first streamed event never arrived: the logger buffered the whole body")
	}
}

// tail 是 what matters 当 diagnosing stall, 所以 head 是 dropped.
func TestRecordingBodyKeepsTail(t *testing.T) {
	t.Parallel()

	var body string
	var truncated bool
	r := &recordingBody{
		inner: io.NopCloser(io.LimitReader(repeatReader{'a'}, maxRecordedBodyBytes+1024)),
		log:   func(b string, tr bool) { body, truncated = b, tr },
	}
	_, err := io.Copy(io.Discard, r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	require.True(t, truncated, "an over-long body should report truncation")
	require.Len(t, body, maxRecordedBodyBytes)
}

// 一个 流 many times cap 是 trimmed 作为 it goes, 所以 what 是 held stays
// bounded without recopying tail 在 every 读取.
func TestRecordingBodyTrimsLongStreams(t *testing.T) {
	t.Parallel()

	var body string
	var truncated bool
	r := &recordingBody{
		inner: io.NopCloser(io.LimitReader(repeatReader{'a'}, 5*maxRecordedBodyBytes)),
		log:   func(b string, tr bool) { body, truncated = b, tr },
	}
	_, err := io.Copy(io.Discard, r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	require.True(t, truncated)
	require.Len(t, body, maxRecordedBodyBytes, "only the tail is kept, however long the stream")
}

// Closing early ( cancelled turn) 必须 still produce exactly one log line.
func TestRecordingBodyLogsOnceOnEarlyClose(t *testing.T) {
	t.Parallel()

	var calls int
	r := &recordingBody{
		inner: io.NopCloser(io.LimitReader(repeatReader{'x'}, 1<<20)),
		log:   func(string, bool) { calls++ },
	}
	buf := make([]byte, 16)
	_, err := r.Read(buf)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, r.Close())
	require.Equal(t, 1, calls)
}

type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// 一个 body can 是 closed 从 one goroutine while another 是 blocked 在 读取;
// 那 是 how 流 gets aborted mid-turn.
func TestRecordingBodyCloseDuringRead(t *testing.T) {
	t.Parallel()

	var calls int
	r := &recordingBody{
		inner: io.NopCloser(io.LimitReader(repeatReader{'x'}, 1<<24)),
		log:   func(string, bool) { calls++ },
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		for range 20000 {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}()

	time.Sleep(time.Millisecond)
	require.NoError(t, r.Close())
	<-done
	require.Equal(t, 1, calls, "an aborted stream still logs exactly once")
}
