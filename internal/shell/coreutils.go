package shell

import (
	"charm.land/turtle/shcoreutils"
)

// CoreutilsMode selects how core utilities 运行 在 Crush shells.
type CoreutilsMode string

const (
	// CoreutilsTurtle always uses the Turtle implementations.
	CoreutilsTurtle CoreutilsMode = "turtle"
	// CoreutilsSystem prefers system utilities, falling back 到 Turtle
	// 当 command 是 不 found 在 路径.
	CoreutilsSystem CoreutilsMode = "system"
	// CoreutilsAuto uses system utilities where they exist 和 Turtle
	// 在 Windows.
	CoreutilsAuto CoreutilsMode = "auto"
	// CoreutilsOff skips the coreutils middleware entirely.
	CoreutilsOff CoreutilsMode = "off"
)

// coreutilsHandler builds Turtle coreutils middleware 为 mode, 或 nil
// 当 mode 是 off. An empty 或 unknown mode means no 工作区 配置 是
// available (crushrc execution, standalone helpers) 和 runs Turtle.
func coreutilsHandler(mode CoreutilsMode) execMiddleware {
	switch mode {
	case CoreutilsOff:
		return nil
	case CoreutilsSystem:
		return shcoreutils.New(shcoreutils.WithPriority(shcoreutils.PrioritySystem))
	case CoreutilsAuto:
		return shcoreutils.New(shcoreutils.WithPriority(shcoreutils.PriorityAuto))
	default:
		return shcoreutils.New(shcoreutils.WithPriority(shcoreutils.PriorityTurtle))
	}
}
