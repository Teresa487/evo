// bash.go —— bash 工具：在 shell 里执行命令，支持前台/后台。

package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
}

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
	// SessionProgram 是 set 当 "allow 为 会话" would approve every
	// later 调用 的 这 program rather than 只 这 exact command, 所以
	// 提示词 can say 所以.
	SessionProgram string `json:"session_program,omitempty"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter = 60 // Commands taking longer automatically become background jobs
	MaxOutputLength            = 30000
	BashNoOutput               = "no output"
)

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	BannedCommands  string
	MaxOutputLength int
	Attribution     config.Attribution
	ModelID         string
	RgAvailable     bool
	GhAvailable     bool
}

// defaultBlockedCommands 是 built-在 dangerous-command list. 它 是
// 默认, 不 hardcoded policy: `permissions` 配置 block can add
// 到 it 和 take away 从 it (see resolveBlockedCommands).
var defaultBlockedCommands = []string{
	// Network/Download tools
	"alias",
	"aria2c",
	"axel",
	"chrome",
	"curl",
	"curlie",
	"firefox",
	"http-prompt",
	"httpie",
	"links",
	"lynx",
	"nc",
	"safari",
	"scp",
	"ssh",
	"telnet",
	"w3m",
	"wget",
	"xh",

	// System administration
	"doas",
	"su",
	"sudo",

	// Indirect execution: these 运行 command name static check can't
	// see, 所以 whatever they wrap would never 是 flagged 在 它的 own.
	"env",
	"eval",
	"nohup",
	"xargs",

	// Package managers
	"apk",
	"apt",
	"apt-cache",
	"apt-get",
	"dnf",
	"dpkg",
	"emerge",
	"home-manager",
	"makepkg",
	"opkg",
	"pacman",
	"paru",
	"pkg",
	"pkg_add",
	"pkg_delete",
	"portage",
	"rpm",
	"yay",
	"yum",
	"zypper",

	// System modification
	"at",
	"batch",
	"chkconfig",
	"crontab",
	"fdisk",
	"mkfs",
	"mount",
	"parted",
	"service",
	"systemctl",
	"umount",

	// Network configuration
	"firewall-cmd",
	"ifconfig",
	"ip",
	"iptables",
	"netstat",
	"pfctl",
	"route",
	"ufw",
}

// resolveBlockedCommands returns the effective dangerous-command list:
// built-在 defaults plus perms.BlockedCommands, minus
// perms.AllowedCommands.
//
// built-在 order 是 preserved 和 用户 additions 是 appended. list 是
// embedded verbatim 在 工具 description, 所以 it has 到 是 deterministic, 但
// sorting it would scramble category grouping defaults 是 written 在
// 和 churn 提示词 为 every existing 用户.
func resolveBlockedCommands(perms *config.Permissions) []string {
	if perms == nil {
		return slices.Clone(defaultBlockedCommands)
	}
	// Both sides 是 normalized way block list itself matches, 所以
	// 配置 条目 spelled "/usr/bin/CURL" 或 " curl " lines up 用
	// "curl" already 在 list. Without 这, allowing command 通过
	// slightly different spelling did nothing 在 all, which 是 worst way
	// 为 权限 setting 到 是 wrong: it looks applied.
	allowed := make(map[string]struct{}, len(perms.AllowedCommands))
	for _, cmd := range perms.AllowedCommands {
		allowed[shell.NormalizeCommandName(cmd)] = struct{}{}
	}

	blocked := make([]string, 0, len(defaultBlockedCommands)+len(perms.BlockedCommands))
	blocked = append(blocked, defaultBlockedCommands...)
	for _, cmd := range perms.BlockedCommands {
		if name := shell.NormalizeCommandName(cmd); name != "" {
			blocked = append(blocked, name)
		}
	}
	blocked = slices.DeleteFunc(blocked, func(c string) bool {
		_, ok := allowed[shell.NormalizeCommandName(c)]
		return ok
	})

	seen := make(map[string]struct{}, len(blocked))
	deduped := blocked[:0]
	for _, cmd := range blocked {
		if _, dup := seen[cmd]; dup {
			continue
		}
		seen[cmd] = struct{}{}
		deduped = append(deduped, cmd)
	}
	return deduped
}

func bashDescription(attribution *config.Attribution, modelID string, blocked []string) string {
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		BannedCommands:  strings.Join(blocked, ", "),
		MaxOutputLength: MaxOutputLength,
		Attribution:     *attribution,
		ModelID:         modelID,
		RgAvailable:     getRg() != "",
		GhAvailable:     ghAvailable,
	}); err != nil {
		// 这 should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

// blockFuncs builds block list 从 resolved dangerous commands.
// flat command list 是 configurable; argument blockers below 是
// structural rules about specific flags rather than list 的 binaries, 所以
// they stay built 在.
func blockFuncs(blocked []string) []shell.BlockFunc {
	return []shell.BlockFunc{
		shell.CommandsBlocker(blocked),

		// System package managers
		shell.ArgumentsBlocker("apk", []string{"add"}, nil),
		shell.ArgumentsBlocker("apt", []string{"install"}, nil),
		shell.ArgumentsBlocker("apt-get", []string{"install"}, nil),
		shell.ArgumentsBlocker("dnf", []string{"install"}, nil),
		shell.ArgumentsBlocker("pacman", nil, []string{"-S"}),
		shell.ArgumentsBlocker("pkg", []string{"install"}, nil),
		shell.ArgumentsBlocker("yum", []string{"install"}, nil),
		shell.ArgumentsBlocker("zypper", []string{"install"}, nil),

		// Language-specific package managers
		shell.ArgumentsBlocker("brew", []string{"install"}, nil),
		shell.ArgumentsBlocker("cargo", []string{"install"}, nil),
		shell.ArgumentsBlocker("gem", []string{"install"}, nil),
		shell.ArgumentsBlocker("go", []string{"install"}, nil),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		shell.ArgumentsBlocker("pip", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pip3", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		shell.ArgumentsBlocker("yarn", []string{"global", "add"}, nil),

		// `go 测试 -exec` can 运行 arbitrary commands
		shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),
	}
}

// NewBashTool returns bash 工具. Every shell it starts 是 tagged 用
// shellOwner, 所以 owner can stop them 用
// [shell.BackgroundShellManager.KillOwned].
func NewBashTool(permissions permission.Service, workingDir, spillDir string, attribution *config.Attribution, modelID string, maxBackgroundJobs int, coreutils shell.CoreutilsMode, perms *config.Permissions, shellOwner any, progress pubsub.Publisher[ToolProgress]) fantasy.AgentTool {
	blocked := resolveBlockedCommands(perms)
	userSafe := userSafeCommands(perms)
	return fantasy.NewAgentTool(
		BashToolName,
		string(bashDescription(attribution, modelID, blocked)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			// Determine working 目录
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			safeReadOnly := isSafeReadOnly(params.Command, userSafe)

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing shell command")
			}
			// Check whether command 是 dangerous 所以 we can surface
			// warning 在 权限 dialog.
			var check shell.CommandCheck
			mode := permissions.PermissionMode()
			if !safeReadOnly {
				check = shell.CheckCommand(params.Command, blockFuncs(blocked))

				if check.Matched && mode.RefusesDangerous() {
					return fantasy.NewTextErrorResponse(yoloBlockedMessage(check.Reason)), nil
				}

				// 路径 是 只 working 目录 here, 所以 key
				// 会话 grant 到 program being 运行, 或 到 exact
				// command 当 program-wide grant would 是 too broad.
				grantKey := bashGrantKey(params.Command, check.Matched)
				permParams := BashPermissionsParams{
					Description:         params.Description,
					Command:             params.Command,
					WorkingDir:          params.WorkingDir,
					RunInBackground:     params.RunInBackground,
					AutoBackgroundAfter: params.AutoBackgroundAfter,
				}
				description := fmt.Sprintf("Execute command: %s", params.Command)
				if grantKey != params.Command {
					permParams.SessionProgram = grantKey
					description += fmt.Sprintf(" (allow for session allows every `%s` command)", grantKey)
				}
				p, err := permissions.Request(
					ctx,
					permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        execWorkingDir,
						ToolCallID:  call.ID,
						ToolName:    BashToolName,
						Action:      "execute",
						Description: description,
						Params:      permParams,
						Danger:      check.Reason,
						GrantKey:    grantKey,
					},
				)
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !p {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// 一个 named match reaching here was approved 在 提示词; keep block list 为 everything else.
			var blocksToUse []shell.BlockFunc
			if !check.Matched && mode != permission.PermissionModeSysadmin {
				blocksToUse = blockFuncs(blocked)
			}
			reporter := newProgressReporter(progress, sessionID, call.ID)
			return executeBashCommand(ctx, params, execWorkingDir, spillDir, blocksToUse, maxBackgroundJobs, coreutils, shellOwner, reporter)
		},
	)
}

// yoloBlockedMessage names way out 所以 模型 doesn't 重试 variations.
func yoloBlockedMessage(command string) string {
	return fmt.Sprintf("blocked: %s is a dangerous command, and yolo mode blocks dangerous commands without asking. Tell the user: they can switch to normal mode to be asked about it, use sysadmin mode to allow it, or mark it safe with `permissions safe`.", command)
}

func executeBashCommand(ctx context.Context, params BashParams, execWorkingDir, spillDir string, blockFuncsToUse []shell.BlockFunc, maxBackgroundJobs int, coreutils shell.CoreutilsMode, shellOwner any, progress *progressReporter) (fantasy.ToolResponse, error) {
	// If explicitly requested 作为 background, start immediately 用 detached 上下文
	if params.RunInBackground {
		startTime := time.Now()
		bgManager := shell.GetBackgroundShellManager()
		// Use background 上下文 所以 it continues after 工具 returns
		bgShell, err := bgManager.Start(context.Background(), shellOwner, execWorkingDir, blockFuncsToUse, coreutils, params.Command, params.Description, maxBackgroundJobs)
		if err != nil {
			return fantasy.NewTextErrorResponse("Failed to start background shell: " + err.Error()), nil
		}

		// Wait short time 到 detect fast failures (blocked commands, syntax errors, etc.)
		time.Sleep(1 * time.Second)
		stdout, stderr, done, execErr := bgShell.GetOutput()

		if done {
			// Command failed 或 completed very quickly
			bgManager.Remove(bgShell.ID)

			interrupted := shell.IsInterrupt(execErr)
			exitCode := shell.ExitCode(execErr)
			if exitCode == 0 && !interrupted && execErr != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("[Job %s] Error executing command: %s", bgShell.ID, execErr)), nil
			}

			stdout = formatOutput(stdout, stderr, execErr, spillDir)

			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Output:           stdout,
				Description:      params.Description,
				Background:       params.RunInBackground,
				WorkingDirectory: bgShell.WorkingDir,
			}
			if stdout == "" {
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
			}
			stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
		}

		// Still running after fast-failure check - return 作为 background job
		metadata := BashResponseMetadata{
			StartTime:        startTime.UnixMilli(),
			EndTime:          time.Now().UnixMilli(),
			Description:      params.Description,
			WorkingDirectory: bgShell.WorkingDir,
			Background:       true,
			ShellID:          bgShell.ID,
		}
		response := fmt.Sprintf("Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
	}

	// Start synchronous execution 用 auto-background support
	startTime := time.Now()

	// Start 用 detached 上下文 所以 it can survive 如果 moved 到 background
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(context.Background(), shellOwner, execWorkingDir, blockFuncsToUse, coreutils, params.Command, params.Description, maxBackgroundJobs)
	if err != nil {
		return fantasy.NewTextErrorResponse("Failed to start shell: " + err.Error()), nil
	}

	// Wait 为 either completion, auto-background threshold, 或 上下文 cancellation
	autoBackgroundAfter := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)
	autoBackgroundThreshold := time.Duration(autoBackgroundAfter) * time.Second
	timer := time.NewTimer(autoBackgroundThreshold)
	defer timer.Stop()
	progressTicker := time.NewTicker(progressPollInterval)
	defer progressTicker.Stop()

wait:
	for {
		select {
		case <-bgShell.Done():
			break wait
		case <-timer.C:
			break wait
		case <-progressTicker.C:
			progress.report(bgShell)
		case <-ctx.Done():
			// Incoming 上下文 was cancelled before we moved 到 background
			// Kill shell 和 return 错误
			bgManager.Kill(ctx, bgShell.ID)
			return fantasy.ToolResponse{}, ctx.Err()
		}
	}
	stdout, stderr, done, execErr := bgShell.GetOutput()

	if done {
		// Command completed within threshold - return synchronously
		// Remove 从 background manager since we're returning directly
		// Don't 调用 Kill() 作为 it cancels 上下文 和 corrupts exit code
		bgManager.Remove(bgShell.ID)

		interrupted := shell.IsInterrupt(execErr)
		exitCode := shell.ExitCode(execErr)
		if exitCode == 0 && !interrupted && execErr != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("[Job %s] Error executing command: %s", bgShell.ID, execErr)), nil
		}

		stdout = formatOutput(stdout, stderr, execErr, spillDir)

		metadata := BashResponseMetadata{
			StartTime:        startTime.UnixMilli(),
			EndTime:          time.Now().UnixMilli(),
			Output:           stdout,
			Description:      params.Description,
			Background:       params.RunInBackground,
			WorkingDirectory: bgShell.WorkingDir,
		}
		if stdout == "" {
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
		}
		stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
	}

	// Still running - keep 作为 background job
	metadata := BashResponseMetadata{
		StartTime:        startTime.UnixMilli(),
		EndTime:          time.Now().UnixMilli(),
		Description:      params.Description,
		WorkingDirectory: bgShell.WorkingDir,
		Background:       true,
		ShellID:          bgShell.ID,
	}
	response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
}

// formatOutput formats output 的 completed command 用 错误 handling
func formatOutput(stdout, stderr string, execErr error, spillDir string) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	stdout = truncateOutput(stdout, spillDir)
	stderr = truncateOutput(stderr, spillDir)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
}

// TruncateOutput keeps head 和 tail 的 oversized 工具 output.
// dropped middle 是 written 在 full 到 文件 under spillDir 所以 agent
// can 读取 it back rather than losing it 为 good.
func TruncateOutput(content, spillDir string) string {
	if ansi.StringWidth(content) <= MaxOutputLength {
		return content
	}

	halfLength := MaxOutputLength / 2
	start := ansi.Truncate(content, halfLength, "")
	end := ansi.TruncateLeft(content, ansi.StringWidth(content)-halfLength, "")

	truncatedLinesCount := max(strings.Count(content, "\n")-strings.Count(start, "\n")-strings.Count(end, "\n"), 0)
	marker := fmt.Sprintf("... [%d lines truncated] ...", truncatedLinesCount)
	if path, err := shell.SpillOutput(ansi.Strip(content), spillDir); err == nil {
		marker = fmt.Sprintf("... [%d lines truncated, full output: %s] ...", truncatedLinesCount, path)
	} else {
		slog.Debug("Could not spill tool output", "dir", spillDir, "error", err)
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s", start, marker, end)
}

func truncateOutput(content, spillDir string) string {
	return TruncateOutput(content, spillDir)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}
