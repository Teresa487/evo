package agent

// Hyper accepts brotli 和 gzip 请求 bodies, 和 every turn re-uploads
// whole conversation, 所以 Hyper 提供方 compresses what it sends,
// brotli first. Other openai-compat providers must 不, because nothing
// tells us they can 读取 it.

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/config"
	brrr "github.com/molecule-man/go-brrr"
	"github.com/stretchr/testify/require"
)

// encodingRecorder answers chat completion requests 和 remembers how each
// arriving 正文 was encoded.
type encodingRecorder struct {
	mu        sync.Mutex
	encodings []string
	bodies    []string
	server    *httptest.Server
}

// When rejectBrotli 是 set, recorder answers 400 到 brotli body,
// way Hyper 用 gzip-只 support refuses one.
func newEncodingRecorder(t *testing.T, rejectBrotli bool) *encodingRecorder {
	t.Helper()
	rec := &encodingRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		encoding := r.Header.Get("Content-Encoding")
		body := string(raw)
		switch encoding {
		case "gzip":
			zr, gzErr := gzip.NewReader(bytes.NewReader(raw))
			if gzErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			inflated, gzErr := io.ReadAll(zr)
			require.NoError(t, gzErr)
			body = string(inflated)
		case "br":
			inflated, brErr := io.ReadAll(brrr.NewReader(bytes.NewReader(raw)))
			require.NoError(t, brErr)
			body = string(inflated)
		}

		rec.mu.Lock()
		rec.encodings = append(rec.encodings, encoding)
		rec.bodies = append(rec.bodies, body)
		rec.mu.Unlock()

		if rejectBrotli && encoding == "br" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"no","type":"invalid_request_error"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"z","created":1,"model":"test-model","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`))
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *encodingRecorder) received() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.encodings...), append([]string(nil), r.bodies...)
}

// longPrompt clears compression floor, which 是 what any 会话 用
// history 在 it does.
func longPrompt() fantasy.Prompt {
	return fantasy.Prompt{
		{
			Role:    fantasy.MessageRoleSystem,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: strings.Repeat("You are Crush. ", 200)}},
		},
		{
			Role:    fantasy.MessageRoleUser,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: "hello"}},
		},
	}
}

func testCoordinator(t *testing.T) *coordinator {
	t.Helper()
	cfg, err := config.Init(t.Context(), t.TempDir(), "", false)
	require.NoError(t, err)
	return &coordinator{cfg: cfg}
}

func generateAgainst(t *testing.T, coord *coordinator, baseURL, providerID string, prompt fantasy.Prompt) error {
	t.Helper()
	provider, err := coord.buildOpenaiCompatProvider(baseURL, "test-key", nil, nil, providerID, false, catwalk.TypeCompletions)
	require.NoError(t, err)
	lm, err := provider.LanguageModel(t.Context(), "test-model")
	require.NoError(t, err)
	_, err = lm.Generate(t.Context(), fantasy.Call{Prompt: prompt})
	return err
}

func TestHyperProviderCompressesRequests(t *testing.T) {
	t.Parallel()

	t.Run("hyper uploads a brotli body", func(t *testing.T) {
		t.Parallel()
		rec := newEncodingRecorder(t, false)

		require.NoError(t, generateAgainst(t, testCoordinator(t), rec.server.URL+"/v1", hyper.Name, longPrompt()))

		encodings, bodies := rec.received()
		require.Equal(t, []string{"br"}, encodings, "one compressed request, no retry")
		require.Contains(t, bodies[0], `"model":"test-model"`,
			"the server must be able to inflate what Hyper was handed")
	})

	// 一个 compressed body 是 contract 用 服务端. An endpoint 那 was
	// never told about it should 不 have 到 guess.
	t.Run("other openai-compat providers upload plain JSON", func(t *testing.T) {
		t.Parallel()
		rec := newEncodingRecorder(t, false)

		require.NoError(t, generateAgainst(t, testCoordinator(t), rec.server.URL+"/v1", "some-other-provider", longPrompt()))

		encodings, _ := rec.received()
		require.Equal(t, []string{""}, encodings)
	})

	t.Run("a gzip-only hyper gets the fallback", func(t *testing.T) {
		t.Parallel()
		rec := newEncodingRecorder(t, true)

		require.NoError(t, generateAgainst(t, testCoordinator(t), rec.server.URL+"/v1", hyper.Name, longPrompt()))

		encodings, bodies := rec.received()
		require.Equal(t, []string{"br", "gzip"}, encodings,
			"a refused brotli body is retried as the next encoding in the list")
		require.Contains(t, bodies[1], `"model":"test-model"`,
			"the retry must carry the same request, not an empty one")
	})

	t.Run("short hyper prompts are not worth compressing", func(t *testing.T) {
		t.Parallel()
		rec := newEncodingRecorder(t, false)

		prompt := fantasy.Prompt{
			{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "hi"}}},
		}
		require.NoError(t, generateAgainst(t, testCoordinator(t), rec.server.URL+"/v1", hyper.Name, prompt))

		encodings, _ := rec.received()
		require.Equal(t, []string{""}, encodings)
	})
}
