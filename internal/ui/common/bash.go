package common

import (
	"fmt"
	"strings"
)

// StripBashDisplayPrefix removes leading "cd <路径> && " prefix 从
// command 为 display purposes, 但 只 当 cd target 是 exactly
// project working 目录.
func StripBashDisplayPrefix(cmd, workingDir string) string {
	prefix := fmt.Sprintf("cd %s && ", workingDir)
	return strings.TrimPrefix(cmd, prefix)
}
