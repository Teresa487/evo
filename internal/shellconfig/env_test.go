package shellconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func loadEnv(t *testing.T, script string) map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evorc")
	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))
	env, _ := result["env"].(map[string]any)
	return env
}

// requireNoEnv fails 如果 any name 是 在 env. 它 reports names 只: 在
// failure, printing the map would dump the developer's whole environment.
func requireNoEnv(t *testing.T, env map[string]any, names ...string) {
	t.Helper()
	for _, name := range names {
		_, ok := env[name]
		require.False(t, ok, "unexpected env var %s", name)
	}
}

func TestExportBecomesEnv(t *testing.T) {
	t.Parallel()

	env := loadEnv(t, `export AWS_PROFILE=default
export AWS_PROFILE=work
FOO=bar
export FOO
NOT_EXPORTED=1
cd /
option debug`)

	require.Equal(t, "work", env["AWS_PROFILE"])
	require.Equal(t, "bar", env["FOO"])
	requireNoEnv(t, env, "NOT_EXPORTED", "PWD", "OLDPWD", "IFS", "OPTIND",
		"HOME", "PATH", "CRUSH_VERSION", "GIT_TERMINAL_PROMPT")
	require.Len(t, env, 2, "only the two exports should be reported")
}

func TestExportUnchangedInheritedVarIsSkipped(t *testing.T) {
	t.Setenv("CRUSH_TEST_INHERITED", "same")

	env := loadEnv(t, `export CRUSH_TEST_INHERITED
option debug`)

	requireNoEnv(t, env, "CRUSH_TEST_INHERITED")
	require.Zero(t, len(env), "no env vars should be reported")
}

func TestExportOnlyScriptIsNotEmpty(t *testing.T) {
	t.Parallel()

	env := loadEnv(t, `export AWS_PROFILE=work`)
	require.Equal(t, "work", env["AWS_PROFILE"])
}

// TestEnvValuesSurviveReexpansion checks 那 env values, which 配置
// loader expands second time, come out exactly 作为 script wrote them.
func TestEnvValuesSurviveReexpansion(t *testing.T) {
	tricky := `p@$$w0rd \n $(echo nope) ` + "`id`" + ` "q" 'x' \`
	t.Setenv("CRUSH_TEST_TRICKY", tricky)

	env := loadEnv(t, `export SECRET="$CRUSH_TEST_TRICKY"`)

	got, err := shell.ExpandValue(t.Context(), env["SECRET"].(string), nil)
	require.NoError(t, err)
	require.Equal(t, tricky, got)
}
