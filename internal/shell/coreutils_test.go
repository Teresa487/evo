package shell

import (
	"bytes"
	"strings"
	"testing"
)

func TestCoreutilsHandlerOff(t *testing.T) {
	t.Parallel()

	requireNil(t, coreutilsHandler(CoreutilsOff))
}

func TestCoreutilsHandlerModes(t *testing.T) {
	t.Parallel()

	// An empty 或 unknown mode means no configuration 是 available 和
	// must 运行 Turtle, never skip middleware.
	for _, mode := range []CoreutilsMode{
		CoreutilsAuto,
		CoreutilsTurtle,
		CoreutilsSystem,
		"",
		"bogus",
	} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			requireNotNil(t, coreutilsHandler(mode))
		})
	}
}

func TestStandardHandlersSkipsCoreutilsWhenOff(t *testing.T) {
	t.Parallel()

	if got := len(standardHandlers(nil, CoreutilsOff)); got != 4 {
		t.Errorf("got %d handlers with coreutils off, want 4", got)
	}
	if got := len(standardHandlers(nil, CoreutilsTurtle)); got != 5 {
		t.Errorf("got %d handlers with coreutils on, want 5", got)
	}
}

func TestRunTurtleCoreutils(t *testing.T) {
	t.Parallel()

	// basename --help exits 0 在 Turtle implementation; system
	// basename may reject option, 所以 pin mode instead.
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command:   "basename --help",
		Cwd:       t.TempDir(),
		Stdout:    &stdout,
		Coreutils: CoreutilsTurtle,
	})
	if err != nil {
		t.Fatalf("basename --help: %v", err)
	}
	if !strings.Contains(stdout.String(), "basename") {
		t.Errorf("unexpected basename --help output: %q", stdout.String())
	}
}

func requireNil(t *testing.T, mw execMiddleware) {
	t.Helper()
	if mw != nil {
		t.Fatalf("expected nil middleware, got %T", mw)
	}
}

func requireNotNil(t *testing.T, mw execMiddleware) {
	t.Helper()
	if mw == nil {
		t.Fatal("expected non-nil middleware")
	}
}
