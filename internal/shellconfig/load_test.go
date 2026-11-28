package shellconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/stretchr/testify/require"
)

// TestLoadShellConfig_Provider verifies 那 提供方 builtin produces
// correct JSON 为 basic 提供方 definition.
func TestLoadShellConfig_Provider(t *testing.T) {
	dir := t.TempDir()
	script := `provider add openai --api-key "$OPENAI_API_KEY" --base-url "https://api.openai.com/v1"`
	path := filepath.Join(dir, "evorc")

	t.Setenv("OPENAI_API_KEY", "test-key-123")
	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)
	require.NotNil(t, jsonBytes)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers, ok := result["providers"].(map[string]any)
	require.True(t, ok)
	openai, ok := providers["openai"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "test-key-123", openai["api_key"])
	require.Equal(t, "https://api.openai.com/v1", openai["base_url"])
}

// TestLoadShellConfig_FlagBoolCaseInsensitive verifies 那 flag booleans
// 受理 mixed-case values like TRUE/False.
func TestLoadShellConfig_FlagBoolCaseInsensitive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider add openai --api-key key --disable TRUE`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	openai := result["providers"].(map[string]any)["openai"].(map[string]any)
	require.Equal(t, true, openai["disable"])
}

// TestLoadShellConfig_MultipleProviders verifies 那 multiple 提供方 calls
// each produce separate entries.
func TestLoadShellConfig_MultipleProviders(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider add openai --api-key "key1"
provider add anthropic --api-key "key2"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	require.Len(t, providers, 2)
	require.Equal(t, "key1", providers["openai"].(map[string]any)["api_key"])
	require.Equal(t, "key2", providers["anthropic"].(map[string]any)["api_key"])
}

// TestLoadShellConfig_Model verifies 模型 builtin.
func TestLoadShellConfig_Model(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `model large openai/gpt-4o --think
model small anthropic/claude-3-5-haiku`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	models := result["models"].(map[string]any)
	large := models["large"].(map[string]any)
	require.Equal(t, "openai", large["provider"])
	require.Equal(t, "gpt-4o", large["model"])
	require.Equal(t, true, large["think"])

	small := models["small"].(map[string]any)
	require.Equal(t, "anthropic", small["provider"])
	require.Equal(t, "claude-3-5-haiku", small["model"])
}

// TestLoadShellConfig_MCP verifies mcp builtin 用 stdio 和 http types.
func TestLoadShellConfig_MCP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `mcp add github --type stdio --command npx --args "-y" --args "@modelcontextprotocol/server-github" --env GITHUB_TOKEN "ghp_xxx"
mcp add local-server --type http --url "http://localhost:3000/mcp" --header "Authorization" "Bearer token"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	mcps := result["mcp"].(map[string]any)

	github := mcps["github"].(map[string]any)
	require.Equal(t, "stdio", github["type"])
	require.Equal(t, "npx", github["command"])
	args := github["args"].([]any)
	require.Len(t, args, 2)
	require.Equal(t, "-y", args[0])
	require.Equal(t, "@modelcontextprotocol/server-github", args[1])
	env := github["env"].(map[string]any)
	require.Equal(t, "ghp_xxx", env["GITHUB_TOKEN"])

	local := mcps["local-server"].(map[string]any)
	require.Equal(t, "http", local["type"])
	require.Equal(t, "http://localhost:3000/mcp", local["url"])
	headers := local["headers"].(map[string]any)
	require.Equal(t, "Bearer token", headers["Authorization"])
}

// TestLoadShellConfig_LSP verifies the lsp builtin.
func TestLoadShellConfig_LSP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `lsp add gopls --command gopls --filetypes go --filetypes mod --root-markers go.mod --timeout 60`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	lsps := result["lsp"].(map[string]any)
	gopls := lsps["gopls"].(map[string]any)
	require.Equal(t, "gopls", gopls["command"])
	filetypes := gopls["filetypes"].([]any)
	require.Len(t, filetypes, 2)
	require.Equal(t, "go", filetypes[0])
	require.Equal(t, "mod", filetypes[1])
	markers := gopls["root_markers"].([]any)
	require.Len(t, markers, 1)
	require.Equal(t, "go.mod", markers[0])
	require.EqualValues(t, 60, gopls["timeout"])
}

// TestLoadShellConfig_Permissions verifies the permissions builtin.
func TestLoadShellConfig_Permissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `permissions allow bash view`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	perms := result["permissions"].(map[string]any)
	tools := perms["allowed_tools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "bash", tools[0])
	require.Equal(t, "view", tools[1])
}

// TestLoadShellConfig_PermissionsDeny verifies 那 `permissions deny` writes
// 到 options.disabled_tools (不 permissions.disabled_tools). 这
// cross-section 写入 是 load-bearing: deny wins over allow because
// disabled_tools removes 工具 从 agent entirely. Pin destination
// 所以 rename 或 relocation 的 disabled_tools can't silently break it.
func TestLoadShellConfig_PermissionsDeny(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `permissions deny bash sourcegraph`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	opts := result["options"].(map[string]any)
	disabled := opts["disabled_tools"].([]any)
	require.Equal(t, []any{"bash", "sourcegraph"}, disabled)
	require.NotContains(t, result, "permissions",
		"deny must not create a permissions section")
}

// TestLoadShellConfig_Hook verifies 钩子 builtin.
func TestLoadShellConfig_Hook(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `hook add PreToolUse --command "echo running" --matcher "bash" --timeout 10 --name "my-hook"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	hooks := result["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	require.Len(t, preToolUse, 1)
	hook := preToolUse[0].(map[string]any)
	require.Equal(t, "echo running", hook["command"])
	require.Equal(t, "bash", hook["matcher"])
	require.EqualValues(t, 10, hook["timeout"])
	require.Equal(t, "my-hook", hook["name"])
}

// TestLoadShellConfig_Option verifies the option builtin.
func TestLoadShellConfig_Option(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `option data-directory .crush
option metrics false
option debug`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	opts := result["options"].(map[string]any)
	require.Equal(t, ".evo", opts["data_directory"])
	require.Equal(t, true, opts["disable_metrics"])
	require.Equal(t, true, opts["debug"])
}

// TestLoadShellConfig_SourceInclude verifies 那 source works 为 includes.
func TestLoadShellConfig_SourceInclude(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// 创建 included 文件 用 提供方 definition.
	includeContent := `provider add openai --api-key "included-key"`
	includePath := filepath.Join(dir, "shared.sh")
	require.NoError(t, os.WriteFile(includePath, []byte(includeContent), 0o644))

	// 创建 main script 那 sources include. Use forward
	// slashes 所以 路径 survives bash interpreter 在 Windows,
	// where backslashes would 是 treated 作为 escape characters.
	script := `source ` + filepath.ToSlash(includePath) + `
provider add anthropic --api-key "main-key"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	require.Len(t, providers, 2)
	require.Equal(t, "included-key", providers["openai"].(map[string]any)["api_key"])
	require.Equal(t, "main-key", providers["anthropic"].(map[string]any)["api_key"])
}

// TestLoadShellConfig_Conditionals verifies 那 bash conditionals work.
func TestLoadShellConfig_Conditionals(t *testing.T) {
	dir := t.TempDir()
	script := `if [[ "$USE_ANTHROPIC" == "1" ]]; then
  provider add anthropic --api-key "ant-key"
else
  provider add openai --api-key "oai-key"
fi`
	path := filepath.Join(dir, "evorc")

	t.Setenv("USE_ANTHROPIC", "1")
	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	require.Len(t, providers, 1)
	require.Contains(t, providers, "anthropic")
}

// TestLoadShellConfig_CrushVersionEnv verifies 那 CRUSH_VERSION 是 exposed
// 到 script 所以 it can feature-detect running Crush version.
func TestLoadShellConfig_CrushVersionEnv(t *testing.T) {
	dir := t.TempDir()
	script := `provider add openai --api-key "$CRUSH_VERSION"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	openai := providers["openai"].(map[string]any)
	require.Equal(t, version.Version, openai["api_key"])
}

// TestLoadShellConfig_CommandSubstitution verifies 那 $(...) works 在 配置 values.
func TestLoadShellConfig_CommandSubstitution(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider add openai --api-key "$(echo dynamic-key)"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	openai := providers["openai"].(map[string]any)
	require.Equal(t, "dynamic-key", openai["api_key"])
}

// TestLoadShellConfig_EnvVarExpansion verifies 那 $VAR expansion works.
func TestLoadShellConfig_EnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	script := `provider add openai --api-key "$MY_API_KEY"`
	path := filepath.Join(dir, "evorc")

	t.Setenv("MY_API_KEY", "env-key-456")
	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	openai := providers["openai"].(map[string]any)
	require.Equal(t, "env-key-456", openai["api_key"])
}

// TestLoadShellConfig_UnknownFlag verifies 错误 handling 为 unknown flags.
func TestLoadShellConfig_UnknownFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider add openai --bogus-flag "value"`
	path := filepath.Join(dir, "evorc")

	_, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.Error(t, err)
}

// TestLoadShellConfig_MissingRequiredArgs verifies 错误 handling 为 missing args.
func TestLoadShellConfig_MissingRequiredArgs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider`
	path := filepath.Join(dir, "evorc")

	_, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.Error(t, err)
}

// TestLoadShellConfig_NoBuiltins verifies 那 script 用 no 配置 builtins
// produces no output.
func TestLoadShellConfig_NoBuiltins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `echo "just a normal script"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)
	require.Nil(t, jsonBytes)
}

func TestLoadShellConfig_ProviderJSONFlagsRequireObjects(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "evorc")
	_, err := LoadShellConfig(t.Context(), path, []byte(`provider add custom --extra-body '[]'`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expects a JSON object")
}

// TestLoadShellConfig_ExtraHeader verifies --extra-请求头 flag.
func TestLoadShellConfig_ExtraHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := `provider add custom --api-key "key" --extra-header "X-Custom" "value123"`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	providers := result["providers"].(map[string]any)
	custom := providers["custom"].(map[string]any)
	headers := custom["extra_headers"].(map[string]any)
	require.Equal(t, "value123", headers["X-Custom"])
}

// TestLoadShellConfig_FullConfig verifies 完成 配置 用 all builtins.
func TestLoadShellConfig_FullConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")

	script := `#!/usr/bin/env bash

# Providers
provider add openai --api-key "$OPENAI_API_KEY" --base-url "https://api.openai.com/v1"
provider add anthropic --api-key "$ANTHROPIC_API_KEY"
provider add my-llm --type openai --api-key "ollama" --base-url "http://localhost:11434/v1"

# Models
model large openai/gpt-4o --think
model small anthropic/claude-3-5-haiku

# MCP
mcp add github --type stdio --command npx --args "-y" --args "@modelcontextprotocol/server-github"

# LSP
lsp add gopls --command gopls --filetypes go --root-markers go.mod

# Permissions
permissions allow bash view

# Hooks
hook add PreToolUse --command "echo running" --matcher "bash" --timeout 10

# Options
option data-directory .crush
option metrics false`
	path := filepath.Join(dir, "evorc")

	jsonBytes, err := LoadShellConfig(t.Context(), path, []byte(script))
	require.NoError(t, err)
	require.NotNil(t, jsonBytes)

	var result map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &result))

	// Verify providers
	providers := result["providers"].(map[string]any)
	require.Len(t, providers, 3)
	require.Equal(t, "oai-key", providers["openai"].(map[string]any)["api_key"])
	require.Equal(t, "ant-key", providers["anthropic"].(map[string]any)["api_key"])
	myLLM := providers["my-llm"].(map[string]any)
	require.Equal(t, "ollama", myLLM["api_key"])
	require.Equal(t, "http://localhost:11434/v1", myLLM["base_url"])

	// Verify models
	models := result["models"].(map[string]any)
	large := models["large"].(map[string]any)
	require.Equal(t, "openai", large["provider"])
	require.Equal(t, "gpt-4o", large["model"])
	require.Equal(t, true, large["think"])

	// Verify MCP
	mcps := result["mcp"].(map[string]any)
	github := mcps["github"].(map[string]any)
	require.Equal(t, "npx", github["command"])

	// Verify LSP
	lsps := result["lsp"].(map[string]any)
	require.Contains(t, lsps, "gopls")

	// Verify permissions
	perms := result["permissions"].(map[string]any)
	require.Contains(t, perms, "allowed_tools")

	// Verify hooks
	hooks := result["hooks"].(map[string]any)
	require.Contains(t, hooks, "PreToolUse")

	// Verify options
	opts := result["options"].(map[string]any)
	require.Equal(t, ".evo", opts["data_directory"])
	require.Equal(t, true, opts["disable_metrics"])
}

// TestConfigBuilder_NoBuilderInContext verifies 那 builtins 是 no-ops
// 当 no ConfigBuilder 是 在 上下文 (normal bash 工具 execution).
func TestConfigBuilder_NoBuilderInContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// "提供方" without ConfigBuilder should 是 no-op (return nil),
	// 不 错误. builtins check 为 builder 和 silently skip.
	err := shell.Run(t.Context(), shell.RunOptions{
		Command: `provider add openai --api-key "test"`,
		Cwd:     dir,
		Env:     os.Environ(),
	})
	require.NoError(t, err)
}

// TestLoadShellConfig_RespectsContextCancellation verifies 那 hanging
// crushrc cannot block 配置 loading indefinitely. 配置 loads 运行 在
// startup 和 reload critical paths while 配置 存储's 写入 锁 是
// held, 所以 runaway script ( busy loop, hung command substitution) must
// 是 interruptible via 上下文 rather than wedging whole 存储.
// 测试 bounds 它的 own wait 所以 regression can't hang CI.
func TestLoadShellConfig_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "evorc")
	script := `while true; do :; done`

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() {
		_, err := LoadShellConfig(ctx, path, []byte(script))
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a cancelled crushrc must fail, not succeed")
		require.True(t, shell.IsInterrupt(err),
			"expected an interrupt/cancellation error, got: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("LoadShellConfig did not return after context cancellation")
	}
}
