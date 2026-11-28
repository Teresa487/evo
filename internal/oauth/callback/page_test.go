package callback

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrite_Success(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{Subject: "linear"}))
	page := b.String()

	require.Contains(t, page, `class="card ok"`)
	require.Contains(t, page, "You’re all set")
	require.Contains(t, page, "linear")
	// 一个 successful page counts itself down 和 closes.
	require.Contains(t, page, `data-delay="5"`)
	// Everything needed 到 render must 是 inlined, 所以 page still
	// works 用 no network beyond optional web font.
	require.Contains(t, page, "<svg")
	require.Contains(t, page, "data:image/svg")
}

// TestWrite_FailureDoesNotAutoClose guards choice 不 到 yank 错误
// 消息 away 从 whoever 是 reading it.
func TestWrite_FailureDoesNotAutoClose(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{
		Subject:          "sentry",
		ErrorCode:        "access_denied",
		ErrorDescription: "The user declined the request.",
	}))
	page := b.String()

	require.Contains(t, page, `class="card failed"`)
	require.Contains(t, page, "access_denied")
	require.Contains(t, page, "The user declined the request.")
	// 一个 failed page has no countdown rail, 所以 nothing takes reason away.
	require.NotContains(t, page, `id="rail"`)
	require.NotContains(t, page, `data-delay=`)
}

// TestWrite_GrumpyOnFailure 证明 页面内容 matches outcome:
// grumpy heart 当 authorization fails, smiling one 当 it works.
// two 是 told apart 通过 路径 unique 到 each drawing.
func TestWrite_GrumpyOnFailure(t *testing.T) {
	t.Parallel()

	// grumpy drawing carries dark red accent (#ab2454) 那
	// smiling heart does 不.
	const grumpy = "#ab2454"

	var failed strings.Builder
	require.NoError(t, Write(&failed, Result{ErrorCode: "access_denied"}))
	require.Contains(t, failed.String(), grumpy)

	var ok strings.Builder
	require.NoError(t, Write(&ok, Result{Subject: "linear"}))
	require.NotContains(t, ok.String(), grumpy)
}

// TestWrite_TerseFailure covers providers 那 报告 错误 code 用 no
// description: page 必须 still explain itself rather than trailing off.
func TestWrite_TerseFailure(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{ErrorCode: "server_error"}))
	page := b.String()

	require.Contains(t, page, "server_error")
	require.Contains(t, page, "did not")
	// With no subject sentence must 不 dangle 在 preposition.
	require.NotContains(t, page, "access to <span")
	require.Contains(t, page, "Evo was not granted access.")
}

// TestWrite_EscapesUntrustedText proves 提供方-supplied strings cannot
// inject markup. 错误 descriptions come straight off 查询 string.
func TestWrite_EscapesUntrustedText(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{
		Subject:          `<img src=x onerror=alert(1)>`,
		ErrorCode:        `<script>`,
		ErrorDescription: `</div><script>alert(2)</script>`,
	}))
	page := b.String()

	require.NotContains(t, page, "<img src=x")
	require.NotContains(t, page, "<script>alert(2)")
	require.Contains(t, page, "&lt;img")
}

func TestServe_StatusCodes(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		result Result
		want   int
	}{
		"success": {Result{Subject: "linear"}, http.StatusOK},
		"failure": {Result{ErrorCode: "access_denied"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			require.NoError(t, Serve(rec, tc.result))
			require.Equal(t, tc.want, rec.Code)
			require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			// page reports one-time 结果 和 must 不 是 replayed
			// 从 缓存 在 later visit 到 same localhost URL.
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		})
	}
}

// TestWrite_ContinueHandoff 证明 移交 page 渲染 continue
// button 和 none 的 outcome machinery. link deliberately omits
// rel=noopener: tab it opens may 只 close itself 当 it keeps
// opener, which 是 whole point 的 handoff.
func TestWrite_ContinueHandoff(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	require.NoError(t, Write(&b, Result{
		Subject:     "OpenAI",
		ContinueURL: "https://auth.openai.com/oauth/authorize?client_id=x",
	}))
	page := b.String()

	require.Contains(t, page, `class="card continue"`)
	require.Contains(t, page, "One more click")
	require.Contains(t, page, "OpenAI")
	require.Contains(t, page, `id="continue"`)
	require.Contains(t, page, `href="https://auth.openai.com/oauth/authorize?client_id=x"`)
	require.Contains(t, page, `target="_blank"`)
	// 锚点 必须 keep opener relationship; quoted 属性
	// form must 不 appear ( CSS comments mention noopener unquoted).
	require.NotContains(t, page, `rel="noopener"`)
	// 一个 handoff page has no countdown 和 no outcome 文本.
	require.NotContains(t, page, `id="rail"`)
	require.NotContains(t, page, "You’re all set")
}

func TestServe_ContinueStatusCode(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	require.NoError(t, Serve(rec, Result{ContinueURL: "https://auth.openai.com/x"}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
