// prompts.go —— 内置 prompt 模板的加载与覆盖。
//
// coder/task/plan/initialize/title/summary 等模板从这里读取，
// 支持用户自定义 prompt 文件替换。

package agent

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
)

//go:embed templates/coder.md.tpl
var coderPromptTmpl []byte

//go:embed templates/task.md.tpl
var taskPromptTmpl []byte

//go:embed templates/plan.md.tpl
var planPromptTmpl []byte

//go:embed templates/initialize.md.tpl
var initializePromptTmpl []byte

//go:embed templates/title.md
var titlePromptTmpl []byte

//go:embed templates/summary.md
var summaryPromptTmpl []byte

// builtinPrompts holds every 提示词 用户 may replace via options.prompts.
// Its keys must cover 配置.PromptNames, which TestBuiltinPromptsCoverNames
// enforces.
var builtinPrompts = map[string][]byte{
	"coder":      coderPromptTmpl,
	"task":       taskPromptTmpl,
	"plan":       planPromptTmpl,
	"initialize": initializePromptTmpl,
	"title":      titlePromptTmpl,
	"summary":    summaryPromptTmpl,
}

// promptText returns built-在 提示词, 或 文件 那 replaces it.
func promptText(store *config.ConfigStore, name string) (string, error) {
	builtin, ok := builtinPrompts[name]
	if !ok {
		return "", fmt.Errorf("unknown prompt %q", name)
	}
	path := promptPath(store, name)
	if path == "" {
		return string(builtin), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s prompt: %w", name, err)
	}
	return string(data), nil
}

// promptPath returns 文件 replacing named 提示词, 或 "" 为
// built-在. 一个 --提示词 flag beats options.prompts. 配置 loading already
// anchors relative paths 到 配置 文件 那 set them; anything still
// relative ( $VAR 那 expands 到 one) resolves against 工作区.
func promptPath(store *config.ConfigStore, name string) string {
	if path := store.Overrides().Prompts[name]; path != "" {
		return path
	}
	opts := store.Config().Options
	if opts == nil || opts.Prompts[name] == "" {
		return ""
	}
	return filepathext.SmartJoin(store.WorkingDir(), prompt.ExpandPath(opts.Prompts[name], store))
}

// loadPromptTexts reads every 提示词 once, built-在 或 replaced, 所以
// missing 文件 或 broken template fails 在 startup, 和 文件 moved 或
// deleted mid-会话 cannot later break 压缩, titles, 或 sub-agents.
func loadPromptTexts(store *config.ConfigStore) (map[string]string, error) {
	texts := make(map[string]string, len(config.PromptNames))
	for _, name := range config.PromptNames {
		text, err := promptText(store, name)
		if err != nil {
			return nil, err
		}
		if templatedPrompts[name] {
			if _, err := prompt.NewPrompt(name, text); err != nil {
				return nil, err
			}
		}
		texts[name] = text
	}
	return texts, nil
}

// promptOrBuiltin returns named 提示词 从 texts, 作为 loaded 通过
// loadPromptTexts, 或 built-在 当 texts lacks it, 作为 it does 为
// agents built 通过 hand 在 tests.
func promptOrBuiltin(texts map[string]string, name string) string {
	if text, ok := texts[name]; ok {
		return text
	}
	return string(builtinPrompts[name])
}

// newPrompt builds templated 提示词 从 文本 读取 在 startup.
func (c *coordinator) newPrompt(name string, opts ...prompt.Option) (*prompt.Prompt, error) {
	return prompt.NewPrompt(name, promptOrBuiltin(c.promptTexts, name), opts...)
}

// templatedPrompts 是 prompts rendered 作为 Go templates. 标题 和
// 摘要 是 sent verbatim, 所以 "{{" 在 them 是 just 文本.
var templatedPrompts = map[string]bool{
	"coder":      true,
	"task":       true,
	"plan":       true,
	"initialize": true,
}

// loadPrompt builds templated 提示词 通过 name, honoring options.prompts.
func loadPrompt(store *config.ConfigStore, name string, opts ...prompt.Option) (*prompt.Prompt, error) {
	text, err := promptText(store, name)
	if err != nil {
		return nil, err
	}
	return prompt.NewPrompt(name, text, opts...)
}

func InitializePrompt(store *config.ConfigStore) (string, error) {
	systemPrompt, err := loadPrompt(store, "initialize")
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), "", "", store)
}
