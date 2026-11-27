package tools

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// 一个 configured safe command covers exactly what it says 和 no more.
// 值 的 setting 是 那 it 是 predictable: 如果 it quietly widened 到
// 受理 flags 或 operands, "go build" would also mean "go build -o
// /usr/local/bin/anything".
func TestUserSafeCommands(t *testing.T) {
	t.Parallel()

	us := userSafeCommands(&config.Permissions{
		SafeCommands: []string{"go build", "cargo check", "  go   vet  ", "", "   "},
	})

	safe := []string{
		"go build",
		"go vet",
		"cargo check",
		// Composing 用 each other 和 用 built-在 safe commands works,
		// because configured 条目 joins same list rest use.
		"go build && cargo check",
		"go build; ls -la",
		"go build | go vet",
		// 一个 wrapper resolves 到 command it runs, 作为 everywhere else.
		"nice go build",
		"timeout 5 go build",
	}
	for _, c := range safe {
		require.True(t, isSafeReadOnly(c, us), "%q should be safe", c)
	}

	unsafe := []string{
		"go build -o /tmp/x",  // a flag is not covered
		"go build ./...",      // nor an operand
		"go install",          // nor a different subcommand
		"go",                  // nor a prefix of the entry
		"go test",             // nor a sibling
		"go build > /tmp/x",   // nor a redirect
		"go build && curl x",  // nor an unsafe neighbour
		"GOFLAGS=-x go build", // nor an environment prefix
	}
	for _, c := range unsafe {
		require.False(t, isSafeReadOnly(c, us), "%q should not be safe", c)
	}

	// Nothing configured means nothing extra 是 safe.
	require.False(t, isSafeReadOnly("go build", nil))
	require.Empty(t, userSafeCommands(nil))
}

// Configured command names 是 matched way block list matches, 所以
// spelling 那 differs 只 在 case, 路径 或 padding still lines up.
// failure 这 guards against 是 silent: setting looks applied 和 does
// nothing.
func TestResolveBlockedCommandsNormalizesConfig(t *testing.T) {
	t.Parallel()

	t.Run("allowed_commands removes a default however it is spelled", func(t *testing.T) {
		t.Parallel()
		for _, spelling := range []string{"curl", "CURL", " curl ", "/usr/bin/curl", "curl.exe"} {
			blocked := resolveBlockedCommands(&config.Permissions{
				AllowedCommands: []string{spelling},
			})
			require.NotContains(t, blocked, "curl", "spelled %q", spelling)
			require.Contains(t, blocked, "sudo", "spelled %q: only curl should go", spelling)
		}
	})

	t.Run("blocked_commands adds however it is spelled", func(t *testing.T) {
		t.Parallel()
		for _, spelling := range []string{"kubectl", "KUBECTL", " kubectl ", "/usr/local/bin/kubectl"} {
			bf := blockFuncs(resolveBlockedCommands(&config.Permissions{
				BlockedCommands: []string{spelling},
			}))
			require.Equal(t, "kubectl", shell.CheckCommand("kubectl apply -f x", bf).Reason,
				"spelled %q", spelling)
		}
	})

	t.Run("allow wins over block for the same command", func(t *testing.T) {
		t.Parallel()
		blocked := resolveBlockedCommands(&config.Permissions{
			BlockedCommands: []string{"git"},
			AllowedCommands: []string{"GIT"},
		})
		require.NotContains(t, blocked, "git")
	})

	t.Run("empty entries are ignored", func(t *testing.T) {
		t.Parallel()
		require.Equal(t,
			resolveBlockedCommands(&config.Permissions{}),
			resolveBlockedCommands(&config.Permissions{BlockedCommands: []string{"", "  "}}))
	})
}
