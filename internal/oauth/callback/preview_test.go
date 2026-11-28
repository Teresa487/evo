package callback

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWritePreview 是 developer aid, 不 assertion: set
// EVO_CALLBACK_PREVIEW 到 目录 和 it writes each page state
// there 所以 结果 can 是 opened 在 browser 和 eyeballed.
func TestWritePreview(t *testing.T) {
	dir := os.Getenv("EVO_CALLBACK_PREVIEW")
	if dir == "" {
		t.Skip("set EVO_CALLBACK_PREVIEW=<dir> to render preview pages")
	}
	cases := map[string]Result{
		"ok":     {Subject: "linear"},
		"bare":   {},
		"denied": {Subject: "linear", ErrorCode: "access_denied", ErrorDescription: "The resource owner or authorization server denied the request."},
		"terse":  {Subject: "sentry", ErrorCode: "server_error"},
	}
	for name, result := range cases {
		f, err := os.Create(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(f, result); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
