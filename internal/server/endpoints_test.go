package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var braceParam = regexp.MustCompile(`\{(\w+)\}`)

// TestEndpointsPathParams verifies every {param} 在 route pattern 是
// documented 通过 PathParam 的 same name, 和 vice versa.
func TestEndpointsPathParams(t *testing.T) {
	t.Parallel()
	c := &controllerV1{}
	for _, e := range c.endpoints() {
		declared := map[string]bool{}
		for _, p := range e.Params() {
			if p.In == "path" {
				declared[p.Name] = true
			}
		}
		seen := map[string]bool{}
		for _, m := range braceParam.FindAllStringSubmatch(e.Path(), -1) {
			seen[m[1]] = true
			require.True(t, declared[m[1]],
				"%s %s: path param {%s} missing PathParam documentation", e.Method(), e.Path(), m[1])
		}
		for name := range declared {
			require.True(t, seen[name],
				"%s %s: PathParam %q has no matching {%s} in path", e.Method(), e.Path(), name, name)
		}
	}
}

// TestEndpointsUnique verifies no method+路径 pair 是 registered twice.
func TestEndpointsUnique(t *testing.T) {
	t.Parallel()
	c := &controllerV1{}
	seen := map[string]bool{}
	for _, e := range c.endpoints() {
		key := e.Method() + " " + e.Path()
		require.False(t, seen[key], "duplicate endpoint %s", key)
		seen[key] = true
	}
}

// TestEndpointsComplete verifies registry covers every 处理器
// method 在 controllerV1 和 那 handlers 是 always set.
func TestEndpointsHandlers(t *testing.T) {
	t.Parallel()
	c := &controllerV1{}
	for _, e := range c.endpoints() {
		require.NotNil(t, e.Handler(), "%s %s: nil handler", e.Method(), e.Path())
		require.NotEmpty(t, e.Summary(), "%s %s: missing summary", e.Method(), e.Path())
	}
}

// TestDocsEndpoints serves docs routes 从 bare 服务端 和
// checks HTML page 和 OpenAPI spec.
func TestDocsEndpoints(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	get := func(path string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+path, nil)
		require.NoError(t, err)
		resp, err := hs.Client().Do(req)
		require.NoError(t, err)
		return resp
	}

	resp := get("/v1/docs/")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	resp = get("/v1/docs/openapi.json")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var spec struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&spec))
	require.Equal(t, "3.2.1", spec.OpenAPI)

	// One spec operation per registered endpoint (paths 用 multiple
	// methods contribute one operation each).
	c := &controllerV1{}
	count := 0
	for _, e := range c.endpoints() {
		path := strings.TrimPrefix(e.Path(), apiBase)
		item, ok := spec.Paths[path]
		require.True(t, ok, "spec missing path %s", path)
		require.Contains(t, item, strings.ToLower(e.Method()), "spec missing %s %s", e.Method(), path)
		count++
	}
	require.Len(t, c.endpoints(), count)
}
