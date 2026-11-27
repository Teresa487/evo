package model

import (
	"testing"

	"charm.land/x/nerdfont"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
)

// TestLSPDiagnosticsIcons covers LSP diagnostic counts shown 在
// sidebar: Nerd Font severity glyphs render 只 当 终态 是
// expected 到 render them, 和 E, W, I, 和 H letters remain
// fallback otherwise.
func TestLSPDiagnosticsIcons(t *testing.T) {
	// Not parallel: t.Setenv 是 process-wide, 作为 是 nerdfont probe memo.
	st := styles.CharmtonePantera()
	diagnostics := map[protocol.DiagnosticSeverity]int{
		protocol.SeverityError:       1,
		protocol.SeverityWarning:     2,
		protocol.SeverityInformation: 3,
		protocol.SeverityHint:        4,
	}

	tests := []struct {
		name      string
		nerdFonts string
		want      string
	}{
		{
			name:      "glyphs with nerd font support",
			nerdFonts: "true",
			want: styles.LSPErrorIcon + "1 " +
				styles.LSPWarningIcon + "2 " +
				styles.LSPHintIcon + "4 " +
				styles.LSPInfoIcon + "3",
		},
		{
			name:      "letters without nerd font support",
			nerdFonts: "false",
			want:      "E1 W2 H4 I3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(nerdfont.EnvVar, tt.nerdFonts)
			nerdfont.Reset()
			t.Cleanup(nerdfont.Reset)

			require.Equal(t, tt.want, ansi.Strip(lspDiagnostics(&st, diagnostics)))
		})
	}
}
