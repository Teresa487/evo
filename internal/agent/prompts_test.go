package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// promptStore returns 配置 存储 whose options.prompts 是 `prompts`.
func promptStore(t *testing.T, prompts map[string]string) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()
	store, err := config.Init(t.Context(), dir, "", false)
	require.NoError(t, err)
	store.Config().Options.Prompts = prompts
	return store
}

// map 在 prompts.go 和 list 配置 validates against must agree, 或
// name passes validation 和 then resolves 到 nothing.
func TestBuiltinPromptsCoverNames(t *testing.T) {
	t.Parallel()

	require.Len(t, builtinPrompts, len(config.PromptNames))
	for _, name := range config.PromptNames {
		require.Contains(t, builtinPrompts, name, "config.PromptNames lists %q but builtinPrompts has no template for it", name)
		require.NotEmpty(t, builtinPrompts[name], "built-in %q prompt is empty", name)
	}
}

func TestPromptTextUsesBuiltinByDefault(t *testing.T) {
	t.Parallel()

	store := promptStore(t, nil)
	for _, name := range config.PromptNames {
		text, err := promptText(store, name)
		require.NoError(t, err)
		require.Equal(t, string(builtinPrompts[name]), text)
	}
}

// TestPromptTextWithoutOptions covers stores built 通过 hand without Options,
// which tests elsewhere do. They must get built-在 提示词, 不 panic.
func TestPromptTextWithoutOptions(t *testing.T) {
	t.Parallel()

	store := config.NewTestStore(&config.Config{})
	text, err := promptText(store, "title")
	require.NoError(t, err)
	require.Equal(t, string(builtinPrompts["title"]), text)
}

func TestPromptTextUsesOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "coder.md.tpl")
	require.NoError(t, os.WriteFile(path, []byte("be brief"), 0o644))

	store := promptStore(t, map[string]string{"coder": path})

	text, err := promptText(store, "coder")
	require.NoError(t, err)
	require.Equal(t, "be brief", text)

	// Overriding one 提示词 leaves others 在 their built-在.
	task, err := promptText(store, "task")
	require.NoError(t, err)
	require.Equal(t, string(taskPromptTmpl), task)
}

func TestPromptTextExpandsPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), []byte("sum it up"), 0o644))
	t.Setenv("CRUSH_TEST_PROMPT_DIR", dir)

	store := promptStore(t, map[string]string{"summary": "$CRUSH_TEST_PROMPT_DIR/summary.md"})

	text, err := promptText(store, "summary")
	require.NoError(t, err)
	require.Equal(t, "sum it up", text)
}

func TestPromptTextMissingFileFails(t *testing.T) {
	t.Parallel()

	store := promptStore(t, map[string]string{"coder": filepath.Join(t.TempDir(), "nope.md")})

	_, err := promptText(store, "coder")
	require.ErrorContains(t, err, "reading coder prompt")
}

func TestPromptTextUnknownNameFails(t *testing.T) {
	t.Parallel()

	_, err := promptText(promptStore(t, nil), "codr")
	require.ErrorContains(t, err, `unknown prompt "codr"`)
}

// 一个 malformed override must fail 当 提示词 是 loaded, 不 在 every turn
// 那 builds it.
func TestLoadPromptRejectsMalformedTemplate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "coder.md.tpl")
	require.NoError(t, os.WriteFile(path, []byte("hi {{.WorkingDir"), 0o644))

	_, err := loadPrompt(promptStore(t, map[string]string{"coder": path}), "coder")
	require.ErrorContains(t, err, "parsing coder prompt template")
}

func TestValidatePromptsRejectsUnknownName(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Options: &config.Options{
		Prompts: config.PromptPaths{"codr": "/tmp/x.md"},
	}}
	require.ErrorContains(t, cfg.ValidatePrompts(), `unknown prompt "codr"`)

	cfg.Options.Prompts = config.PromptPaths{"coder": "/tmp/x.md"}
	require.NoError(t, cfg.ValidatePrompts())
}

// 一个 relative options.prompts 路径 belongs 到 工作区, 不 到 whatever
// 目录 process happened 到 start 在 (--cwd, 服务端 workspaces).
func TestPromptTextResolvesRelativeToWorkspace(t *testing.T) {
	t.Parallel()

	store := promptStore(t, map[string]string{"coder": ".crush/coder.md.tpl"})
	dir := filepath.Join(store.WorkingDir(), ".crush")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coder.md.tpl"), []byte("workspace coder"), 0o644))

	text, err := promptText(store, "coder")
	require.NoError(t, err)
	require.Equal(t, "workspace coder", text)
}

func TestPromptTextRuntimeOverrideWins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configured := filepath.Join(dir, "configured.md")
	flagged := filepath.Join(dir, "flagged.md")
	require.NoError(t, os.WriteFile(configured, []byte("configured"), 0o644))
	require.NoError(t, os.WriteFile(flagged, []byte("flagged"), 0o644))

	store := promptStore(t, map[string]string{"coder": configured, "task": configured})
	store.Overrides().Prompts = config.PromptPaths{"coder": flagged}

	coder, err := promptText(store, "coder")
	require.NoError(t, err)
	require.Equal(t, "flagged", coder)

	// 一个 flag 为 one 提示词 leaves 配置 为 others alone.
	task, err := promptText(store, "task")
	require.NoError(t, err)
	require.Equal(t, "configured", task)
}

// 摘要 和 标题 是 只 used mid-会话, 所以 their files must 是 读取
// up front 或 typo surfaces first time 会话 compacts.
func TestLoadPromptTextsCatchesMissingLazyPrompt(t *testing.T) {
	t.Parallel()

	store := promptStore(t, map[string]string{"summary": filepath.Join(t.TempDir(), "nope.md")})
	_, err := loadPromptTexts(store)
	require.ErrorContains(t, err, "reading summary prompt")
}

func TestLoadPromptTextsCatchesMalformedTemplate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "plan.md.tpl")
	require.NoError(t, os.WriteFile(path, []byte("{{.WorkingDir"), 0o644))

	store := promptStore(t, map[string]string{"plan": path})
	_, err := loadPromptTexts(store)
	require.ErrorContains(t, err, "parsing plan prompt template")
}

// 标题 和 摘要 是 sent verbatim, 所以 braces 在 them 是 just 文本.
func TestLoadPromptTextsAllowsBracesInPlainPrompts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "title.md")
	require.NoError(t, os.WriteFile(path, []byte("name it {{ like this"), 0o644))

	store := promptStore(t, map[string]string{"title": path})
	_, err := loadPromptTexts(store)
	require.NoError(t, err)
}

func TestLoadPromptTextsWithoutOverrides(t *testing.T) {
	t.Parallel()

	texts, err := loadPromptTexts(promptStore(t, nil))
	require.NoError(t, err)
	for _, name := range config.PromptNames {
		require.Equal(t, string(builtinPrompts[name]), texts[name])
	}
}

// 一个 replacement 是 读取 once 在 startup, 所以 moving 或 deleting 文件
// mid-会话 must 不 break 压缩 或 titles.
func TestSessionAgentKeepsPromptsAfterFileIsGone(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	require.NoError(t, os.WriteFile(path, []byte("sum it up"), 0o644))

	texts, err := loadPromptTexts(promptStore(t, map[string]string{"summary": path}))
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))

	a := NewSessionAgent(SessionAgentOptions{Prompts: texts}).(*sessionAgent)
	require.Equal(t, "sum it up", promptOrBuiltin(a.prompts, "summary"))
	require.Equal(t, string(titlePromptTmpl), promptOrBuiltin(a.prompts, "title"))

	bare := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	require.Equal(t, string(summaryPromptTmpl), promptOrBuiltin(bare.prompts, "summary"))
}
