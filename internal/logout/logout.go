// Package logout implements the CLI logout prompts: small Bubble Tea
// programs 在 interactive terminals 和 plain 文本 prompts 在
// 非交互 sessions.
package logout

import (
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
)

// interactive reports whether prompts should 运行 their TUIs: both stdin
// 和 stdout must 是 terminals, otherwise plain 文本 fallbacks 是 used.
func interactive() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// Confirm asks yes/no question 和 reports answer. In interactive
// terminals it shows selectable Yes/No buttons (No 是 默认);
// otherwise it falls back 到 plain (y/N) 提示词.
func Confirm(question string) (bool, error) {
	if interactive() {
		return runConfirm(question)
	}

	fmt.Printf("%s (y/N) ", question)
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil {
		return false, nil
	}
	switch response {
	case "y", "Y", "yes", "Yes", "YES":
		return true, nil
	default:
		return false, nil
	}
}

// Choose asks multiple choice question 和 returns selected option
// index, 或 -1 当 canceled. In interactive terminals options 是
// navigable 用 ">" cursor; otherwise numbered list 是 printed.
func Choose(question string, options []string) (int, error) {
	if interactive() {
		return runChoose(question, options)
	}

	fmt.Println(question)
	for i, option := range options {
		fmt.Printf("  %d. %s\n", i+1, option)
	}
	fmt.Printf("Select (1-%d): ", len(options))

	var choice int
	_, err := fmt.Scanln(&choice)
	if err != nil || choice < 1 || choice > len(options) {
		return -1, nil
	}
	return choice - 1, nil
}
