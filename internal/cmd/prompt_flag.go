// prompt_flag.go —— 处理 `-p/--prompt` 参数的一次性提问。

package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// addPromptFlag registers --提示词 在 flags.
func addPromptFlag(flags *pflag.FlagSet) {
	flags.StringArray("prompt", nil, "Replace a built-in prompt for this run (repeatable), e.g. --prompt coder=./coder.md.tpl")
}

// promptOverrides parses repeatable --提示词 name=路径 flag. Paths 是
// made absolute against invoking shell's cwd, which 是 what someone
// typing relative 路径 在 command line means, 和 which survives
// being sent 到 服务端 whose cwd 是 somewhere else.
func promptOverrides(cmd *cobra.Command) (config.PromptPaths, error) {
	values, _ := cmd.Flags().GetStringArray("prompt")
	if len(values) == 0 {
		return nil, nil
	}
	prompts := make(config.PromptPaths, len(values))
	for _, value := range values {
		name, path, ok := strings.Cut(value, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("invalid --prompt %q, expected name=path", value)
		}
		abs, err := filepath.Abs(home.Long(path))
		if err != nil {
			return nil, fmt.Errorf("resolving --prompt %s: %w", name, err)
		}
		prompts[name] = abs
	}
	if err := config.ValidatePromptNames(prompts); err != nil {
		return nil, fmt.Errorf("invalid --prompt: %w", err)
	}
	return prompts, nil
}
