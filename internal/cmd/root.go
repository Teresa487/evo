// root.go —— CLI 根命令与启动装配，是整条启动链的入口。
//
// 从产物反推（进程怎么变成能对话的 agent）：
//
//	main() → cmd.Execute()（本文件）
//	  → 解析命令行
//	  → setupWorkspaceWithProgressBar
//	      ├─ setupLocalWorkspace      → config.Load → app.New → AppWorkspace
//	      └─ setupClientServerWorkspace → connectToServer → ClientWorkspace
//	  → 进入 TUI（ui/model）
//
// 本文件决定「本地模式还是客户端/服务端模式」，并把 Workspace 交给 UI。

package cmd

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	fang "charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"charm.land/tone"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/lock"
	evolog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/projects"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/exitbanner"
	ui "github.com/charmbracelet/crush/internal/ui/model"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xstrings "github.com/charmbracelet/x/exp/strings"
	"github.com/charmbracelet/x/term"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var clientHost string

// resumeToken 是 终态's Program Resume Protocol (OSC 7577) token. See
// [takeResumeToken].
var resumeToken string

// spawnedServerEnv marks 服务端 那 客户端 process started 在 它的
// behalf. Such 服务端 shuts itself down once 空闲 所以 it cannot outlive
// 客户端; 服务端 started explicitly 用 `evo server` has no such
// marker 和 keeps running. variable 是 set 在 child environment 通过
// [detachedServerCmd] 和 读取 通过 服务端 command.
const spawnedServerEnv = "EVO_SPAWNED_SERVER"

func init() {
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().StringP("data-dir", "D", "", "Custom evo data directory")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.PersistentFlags().StringVarP(&clientHost, "host", "H", server.DefaultHost(), "Connect to a specific evo server host (for advanced users)")
	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.PersistentFlags().CountP("yolo", "y", "Skip permission prompts: -y to stop being asked and block dangerous commands, -yy to allow them too")
	rootCmd.PersistentFlags().StringSlice("channels", nil, "MCP servers to enable as channels (repeatable), e.g. --channels server:webhook")
	_ = rootCmd.PersistentFlags().MarkHidden("channels")
	addPromptFlag(rootCmd.PersistentFlags())
	rootCmd.Flags().StringP("session", "s", "", "Continue a previous session by ID")
	rootCmd.Flags().BoolP("continue", "C", false, "Continue the most recent session")
	rootCmd.MarkFlagsMutuallyExclusive("session", "continue")

	rootCmd.AddCommand(
		runCmd,
		dirsCmd,
		projectsCmd,
		updateProvidersCmd,
		logsCmd,
		logoutCmd,
		schemaCmd,
		loginCmd,
		statsCmd,
		sessionCmd,
	)
}

var rootCmd = &cobra.Command{
	Use:   "evo",
	Short: "A terminal-first AI assistant for software development",
	Long:  "A glamorous, terminal-first AI assistant for software development and adjacent tasks",
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		return resolveFlagPaths(cmd)
	},
	Example: `
# Run in interactive mode
evo

# Run non-interactively
evo run "Guess my 5 favorite Pokémon"

# Run a non-interactively with pipes and redirection
cat README.md | evo run "make this more glamorous" > GLAMOROUS_README.md

# Run with debug logging in a specific directory
evo --debug --cwd /path/to/project

# Run in yolo mode (skip permission prompts, block dangerous commands)
evo -y

# Run in sysadmin mode (skip prompts and allow dangerous commands too)
evo -yy

# Run with custom data directory
evo --data-dir /path/to/custom/.evo

# Continue a previous session
evo --session {session-id}

# Continue the most recent session
evo --continue
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Bail out before loading 配置 或 reporting anything 当 there 是
		// no 终态 到 draw 在, e.g. package manager validators 那 运行
		// 二进制 用 redirected streams 和 no console.
		if !hasTerminal(term.IsTerminal, os.Stdin.Fd(), os.Stdout.Fd()) {
			fmt.Fprintln(cmd.ErrOrStderr(), "Evo needs an interactive terminal. Use `evo run` for non-interactive use.")
			return nil
		}

		sessionID, _ := cmd.Flags().GetString("session")
		continueLast, _ := cmd.Flags().GetBool("continue")

		ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		if sessionID != "" {
			sess, err := resolveWorkspaceSessionID(cmd.Context(), ws, sessionID)
			if err != nil {
				return err
			}
			sessionID = sess.ID
		}

		event.AppInitialized()

		com := common.DefaultCommon(ws)
		model := ui.New(com, sessionID, continueLast)
		model.SetResumeCommand(resumeCommand(cmd))

		inputFilter := ui.NewFilter()
		var env uv.Environ = os.Environ()
		if resumeToken != "" {
			env = append(env, ansi.ProgramResumeTokenEnv+"="+resumeToken)
		}
		// Don't leak errors 到 TUI
		browser.Stderr = nil
		browser.Stdout = nil
		program := tea.NewProgram(
			model,
			tea.WithEnvironment(env),
			tea.WithContext(cmd.Context()),
			tea.WithFilter(inputFilter.Filter),
		)
		model.SetProgram(program)
		go ws.Subscribe(program)

		restoreStderr, err := detachStderr()
		if err != nil {
			slog.Warn("Failed to detach stderr from the terminal", "error", err)
		}
		_, err = program.Run()
		restoreStderr()
		if err != nil {
			event.Error(err)
			slog.Error("TUI run error", "error", err)
			return errors.New("Crush crashed. If metrics are enabled, we were notified about it. If you'd like to report it, please copy the stacktrace above and open an issue at https://github.com/charmbracelet/crush/issues/new?template=bug.yml") //nolint:staticcheck
		}
		var banner config.ExitBanner
		if cfg := com.Config(); cfg != nil {
			banner = cfg.Options.TUI.ExitBanner
		}
		printSessionResume(model, com.Styles, banner)
		return nil
	},
}

var heartbit = lipgloss.NewStyle().Foreground(tone.Dolly).SetString(`
    ▄▄▄▄▄▄▄▄    ▄▄▄▄▄▄▄▄
  ███████████  ███████████
████████████████████████████
████████████████████████████
██████████▀██████▀██████████
██████████ ██████ ██████████
▀▀██████▄████▄▄████▄██████▀▀
  ████████████████████████
    ████████████████████
       ▀▀██████████▀▀
           ▀▀▀▀▀▀
`)

// printSessionResume prints the exit banner after the TUI exits, including the
// 会话 标题 和 hint 到 resume it 用 `evo -s <id>`. banner
// style decides how much 的 那 是 shown; see 配置.ExitBanner.
func printSessionResume(model *ui.UI, theme *styles.Styles, banner config.ExitBanner) {
	tw, _, _ := term.GetSize(os.Stdout.Fd())
	body := exitbanner.Render(theme, banner, model.CurrentSession(), tw)
	if body == "" {
		return
	}
	fmt.Fprintln(colorprofile.NewWriter(os.Stderr, os.Environ()), body)
}

// takeResumeToken returns 终态's Program Resume Protocol token 和
// removes it 从 environment. 终态 gave it 到 Crush alone:
// tools, hooks, MCP servers, 和 服务端 Crush starts must 不 inherit it, 或
// they could 是 resumed 在 Crush's place.
func takeResumeToken() string {
	token := os.Getenv(ansi.ProgramResumeTokenEnv)
	_ = os.Unsetenv(ansi.ProgramResumeTokenEnv)
	return token
}

// resumeCommand returns command 终态 runs 到 bring Crush back after
// it restarts, without 会话, which UI adds. 它 keeps flags
// 那 say where sessions live, 所以 会话 是 found again, 和
// 提示词 overrides, 所以 agent behaves same. 它 drops --yolo 和
// --channels 在 purpose: 终态 resumes without asking 用户, 所以
// resumed Crush must 不 skip 权限 prompts 或 act 在 outside events
// unless 用户 opts 在 again.
func resumeCommand(cmd *cobra.Command) []string {
	exe := os.Args[0]
	if !filepath.IsAbs(exe) && filepath.Base(exe) != exe {
		// 一个 relative 路径 breaks once 终态 starts somewhere else.
		// os.Executable 是 只 fallback: it resolves links, 和
		// versioned 文件 behind Homebrew 或 Nix link 是 often gone after
		// upgrade, which 是 当 people restart their 终态.
		if abs, err := os.Executable(); err == nil {
			exe = abs
		}
	}
	args := []string{exe}
	if clientHost != server.DefaultHost() {
		args = append(args, "--host", clientHost)
	}
	if dataDir, _ := cmd.Flags().GetString("data-dir"); dataDir != "" {
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
		args = append(args, "--data-dir", dataDir)
	}
	// resolveFlagPaths already made these paths absolute.
	prompts, _ := cmd.Flags().GetStringArray("prompt")
	for _, p := range prompts {
		args = append(args, "--prompt", p)
	}
	return args
}

// copied 从 cobra:
const defaultVersionTemplate = `{{with .DisplayName}}{{printf "%s " .}}{{end}}{{printf "version %s" .Version}}
`

// hasTerminal reports whether TUI has 终态 到 attach 到. Either
// stdin 或 stdout being 终态 是 enough: 当 只 stdout 是 (作为 在
// `echo foo | crush`), Bubble Tea reopens controlling TTY 为 input.
func hasTerminal(isTerminal func(uintptr) bool, stdin, stdout uintptr) bool {
	return isTerminal(stdin) || isTerminal(stdout)
}

func Execute() {
	// FIXME: 配置.Load uses slog internally during 提供方 resolution,
	// 但 文件-based logger isn't set up until after 配置 是 loaded
	// (because log 路径 depends 在 data 目录 从 配置).
	// 这 creates 窗口 where slog calls 在 配置.Load leak 到
	// stderr. We discard early logs here 作为 workaround. proper
	// fix 是 到 remove slog calls 从 配置.Load 和 have it return
	// warnings/diagnostics instead 的 logging them 作为 side effect.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	resumeToken = takeResumeToken()

	// NOTE: very hacky: we 创建 colorprofile writer 用 STDOUT, then make
	// it forward 到 bytes.缓冲, 写入 colored heartbit 到 it, 和 then
	// finally prepend it 在 version template.
	// Unfortunately cobra doesn't give us way 到 set function 到 handle
	// printing version, 和 PreRunE runs after version 是 already
	// handled, 所以 那 doesn't work either.
	// 这 是 只 way I could find 那 works relatively well.
	if term.IsTerminal(os.Stdout.Fd()) {
		var b bytes.Buffer
		w := colorprofile.NewWriter(os.Stdout, os.Environ())
		w.Forward = &b
		_, _ = w.WriteString(heartbit.String())
		rootCmd.SetVersionTemplate(b.String() + "\n" + defaultVersionTemplate)
	}
	ctx, stop := notifyContext(context.Background(), terminationSignals()...)
	err := fang.Execute(
		ctx,
		rootCmd,
		fang.WithVersion(version.Version),
		fang.WithErrorHandler(func(w io.Writer, styles fang.Styles, err error) {
			if ctx.Err() == nil {
				fang.DefaultErrorHandler(w, styles, err)
			}
		}),
	)
	code := signalExitCode(ctx)
	stop()
	switch {
	case code != 0:
		// 一个 signal 是 不 failure worth printing; exit like shell would.
		os.Exit(code)
	case err != nil:
		os.Exit(1)
	}
}

// supportsProgressBar tries 到 determine whether current 终态 supports
// progress bars 通过 looking into environment variables.
func supportsProgressBar() bool {
	if !term.IsTerminal(os.Stderr.Fd()) {
		return false
	}
	termProg := os.Getenv("TERM_PROGRAM")
	_, isWindowsTerminal := os.LookupEnv("WT_SESSION")

	return isWindowsTerminal || xstrings.ContainsAnyOf(strings.ToLower(termProg), "ghostty", "iterm2", "rio")
}

// useClientServer reports whether 到 use 客户端/服务端 architecture
// without reading 配置: 当 EVO_CLIENT_SERVER environment
// variable enables it, 或 当 --host names 服务端 到 connect 到.
func useClientServer(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("host") || clientServerEnabled(nil)
}

// clientServerEnabled reports whether 客户端/服务端 architecture 是
// 在. EVO_CLIENT_SERVER environment variable decides 当 set;
// otherwise options.experimental.client_server does.
func clientServerEnabled(opts *config.ExperimentalOptions) bool {
	if v := os.Getenv("EVO_CLIENT_SERVER"); v != "" {
		on, _ := strconv.ParseBool(v)
		return on
	}
	return opts.GetClientServer()
}

// clientServerMode decides between local 和 客户端/服务端 mode. When
// environment does 不 decide, it fully loads 配置, which local mode
// needs anyway, 和 returns 存储 为 reuse (nil otherwise). --host
// always picks 客户端/服务端 mode. When 客户端/服务端 mode wins,
// experimental options 是 exported 所以 服务端 timeouts apply here 和
// 在 any 服务端 这 process spawns; 只 then, 和 只 如果
// environment 或 --host chose mode, 是 配置 读取 second,
// lighter way.
func clientServerMode(cmd *cobra.Command) (bool, *config.ConfigStore, error) {
	if cmd.Flags().Changed("host") || os.Getenv("EVO_CLIENT_SERVER") != "" {
		if !useClientServer(cmd) {
			return false, nil, nil
		}
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return false, nil, err
		}
		opts, err := config.LoadExperimental(cmd.Context(), cwd)
		if err != nil {
			slog.Warn("Failed to read experimental options", "error", err)
		}
		opts.ExportEnv()
		return true, nil, nil
	}

	store, err := loadLocalConfig(cmd)
	if err != nil {
		return false, nil, err
	}
	opts := store.Config().Options.Experimental
	if !opts.GetClientServer() {
		return false, store, nil
	}
	opts.ExportEnv()
	return true, nil, nil
}

// loadLocalConfig loads full 配置 为 command's working
// 目录.
func loadLocalConfig(cmd *cobra.Command) (*config.ConfigStore, error) {
	debug, _ := cmd.Flags().GetBool("debug")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, err
	}
	return config.Init(cmd.Context(), cwd, dataDir, debug)
}

// setupWorkspaceWithProgressBar wraps setupWorkspace 用 optional
// 终态 progress bar shown during initialization.
func setupWorkspaceWithProgressBar(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	showProgress := supportsProgressBar()
	if showProgress {
		_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
	}

	ws, cleanup, err := setupWorkspace(cmd)

	if showProgress {
		_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
	}

	return ws, cleanup, err
}

// setupWorkspace returns 工作区 和 清理 function. In 客户端/服务端
// mode (see [clientServerMode]) it connects 到 服务端 process 和 returns
// ClientWorkspace. Otherwise it creates 在-process app.App 和
// returns an AppWorkspace.
func setupWorkspace(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	clientServer, store, err := clientServerMode(cmd)
	if err != nil {
		return nil, nil, err
	}
	if clientServer {
		return setupClientServerWorkspace(cmd)
	}
	return setupLocalWorkspace(cmd, store)
}

// sysadminModeWarning 是 printed 到 stderr whenever -yy puts 会话 在
// sysadmin mode. Both local 和 客户端 paths print it, 所以 it lives here
// rather than being spelled out twice.
const sysadminModeWarning = "Warning: sysadmin mode is active. Every command is auto-approved, and the block list that catches dangerous commands at run time is switched off."

// setupLocalWorkspace creates 在-process app.App 和 wraps it 在
// AppWorkspace. 一个 nil 存储 是 loaded here.
func setupLocalWorkspace(cmd *cobra.Command, store *config.ConfigStore) (workspace.Workspace, func(), error) {
	debug, _ := cmd.Flags().GetBool("debug")
	yoloCount, _ := cmd.Flags().GetCount("yolo")
	channels, _ := cmd.Flags().GetStringSlice("channels")
	ctx := cmd.Context()

	prompts, err := promptOverrides(cmd)
	if err != nil {
		return nil, nil, err
	}

	if store == nil {
		if store, err = loadLocalConfig(cmd); err != nil {
			return nil, nil, err
		}
	}
	cwd := store.WorkingDir()

	cfg := store.Config()
	store.Overrides().EnabledChannels = channels
	store.Overrides().Prompts = prompts
	if yoloCount > 1 {
		store.Overrides().PermissionMode = permission.PermissionModeSysadmin
		fmt.Fprintln(os.Stderr, sysadminModeWarning)
	} else if yoloCount == 1 {
		store.Overrides().PermissionMode = permission.PermissionModeYolo
	}

	if err := os.MkdirAll(cfg.Options.DataDirectory, 0o700); err != nil {
		return nil, nil, fmt.Errorf("failed to create data directory: %q %w", cfg.Options.DataDirectory, err)
	}

	gitIgnorePath := filepath.Join(cfg.Options.DataDirectory, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); os.IsNotExist(err) {
		if err := os.WriteFile(gitIgnorePath, []byte("*\n"), 0o644); err != nil {
			return nil, nil, fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	if err := projects.Register(cwd, cfg.Options.DataDirectory); err != nil {
		slog.Warn("Failed to register project", "error", err)
	}

	conn, err := db.Connect(ctx, cfg.Options.DataDirectory)
	if err != nil {
		return nil, nil, err
	}

	logFile := filepath.Join(cfg.Options.DataDirectory, "logs", "evo.log")
	evolog.Setup(logFile, debug)

	// Discover skills once before app.New. Local mode hosts a single
	// 工作区 per process, 所以 WithGlobalMirror keeps package
	// globals (which TUI reads via skills.GetLatestStates) 在 sync
	// 用 manager.
	discoveryCfg := localSkillsDiscoveryConfig(store)
	allSkills, activeSkills, skillStates := skills.DiscoverFromConfig(discoveryCfg)
	skillsMgr := skills.NewManager(
		allSkills, activeSkills, skillStates,
		skills.WithGlobalMirror(),
		skills.WithResolvedPaths(discoveryCfg.ResolvePaths()),
		skills.WithWorkingDir(discoveryCfg.WorkingDir),
	)

	appInstance, err := app.New(ctx, conn, store, skillsMgr)
	if err != nil {
		_ = conn.Close()
		slog.Error("Failed to create app instance", "error", err)
		return nil, nil, err
	}

	if shouldEnableMetrics(cfg) {
		event.Init()
	}

	ws := workspace.NewAppWorkspace(appInstance, store)
	cleanup := func() { appInstance.Shutdown() }
	return ws, cleanup, nil
}

// localSkillsDiscoveryConfig adapts *配置.ConfigStore 到 inputs
// skills.DiscoverFromConfig expects.
func localSkillsDiscoveryConfig(store *config.ConfigStore) skills.DiscoveryConfig {
	opts := store.Config().Options
	var paths, disabled []string
	if opts != nil {
		paths = opts.SkillsPaths
		disabled = opts.DisabledSkills
	}
	var resolver func(string) (string, error)
	if r := store.Resolver(); r != nil {
		resolver = r.ResolveValue
	}
	return skills.DiscoveryConfig{
		SkillsPaths:    paths,
		DisabledSkills: disabled,
		WorkingDir:     store.WorkingDir(),
		Resolver:       resolver,
	}
}

// setupClientServerWorkspace connects 到 服务端 process 和 wraps
// 结果 在 ClientWorkspace.
func setupClientServerWorkspace(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	c, protoWs, _, err := connectToServer(cmd)
	if err != nil {
		return nil, nil, err
	}

	// 一个 服务端 那 dies under TUI 是 brought back way startup
	// brings one up. 它 runs 到 completion under command's 上下文, 作为
	// 在 startup: abandoning it halfway would 不 stop 服务端 it spawned.
	clientWs := workspace.NewClientWorkspace(c, *protoWs, workspace.WithServerRespawn(
		func(context.Context) error {
			hostURL, err := server.ParseHostURL(clientHost)
			if err != nil {
				return fmt.Errorf("invalid host URL: %v", err)
			}
			return ensureServer(cmd, hostURL)
		},
	))

	if protoWs.Config.IsConfigured() {
		if err := clientWs.InitCoderAgent(cmd.Context()); err != nil {
			slog.Error("Failed to initialize coder agent", "error", err)
		}
	}

	// Clean up via Shutdown rather than connectToServer's closure: it stops
	// subscription's reconnect/recovery loop first, 所以 our own exit
	// cannot 是 mistaken 为 lost 工作区 和 re-created mid-quit.
	return clientWs, clientWs.Shutdown, nil
}

// connectToServer ensures 服务端 是 running, creates 客户端 和
// 工作区, 和 returns 清理 function 那 deletes 工作区.
func connectToServer(cmd *cobra.Command) (*client.Client, *proto.Workspace, func(), error) {
	hostURL, err := server.ParseHostURL(clientHost)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid host URL: %v", err)
	}

	prompts, err := promptOverrides(cmd)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := ensureServer(cmd, hostURL); err != nil {
		return nil, nil, nil, err
	}

	debug, _ := cmd.Flags().GetBool("debug")
	yoloCount, _ := cmd.Flags().GetCount("yolo")
	channels, _ := cmd.Flags().GetStringSlice("channels")
	dataDir, _ := cmd.Flags().GetString("data-dir")

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, nil, nil, err
	}

	c, err := client.NewClient(cwd, hostURL.Scheme, hostURL.Host)
	if err != nil {
		return nil, nil, nil, err
	}

	wsPermMode := proto.WorkspacePermissionModeNormal
	if yoloCount > 1 {
		wsPermMode = proto.WorkspacePermissionModeSysadmin
	} else if yoloCount == 1 {
		wsPermMode = proto.WorkspacePermissionModeYolo
	}

	wsReq := proto.Workspace{
		Path:           cwd,
		DataDir:        dataDir,
		Debug:          debug,
		PermissionMode: wsPermMode,
		Channels:       channels,
		Prompts:        prompts,
		Version:        version.Version,
		Env:            os.Environ(),
	}

	ws, err := createWorkspaceOnLiveServer(cmd.Context(), c, wsReq, func() error {
		return replaceExitingServer(cmd, hostURL)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	warnPermissionMode(os.Stderr, wsPermMode, ws.PermissionMode)

	if shouldEnableMetrics(ws.Config) {
		event.Init()
	}

	if ws.Config != nil {
		logFile := filepath.Join(ws.Config.Options.DataDirectory, "logs", "evo.log")
		if err := evolog.SetFile(logFile); err != nil {
			slog.Warn("Failed to close the client startup log", "error", err)
		}
	}

	// Retiring 客户端 releases every claim it holds, 所以 it covers
	// workspaces 这 process created 但 never learned ID 的.
	cleanup := func() { sayGoodbye(c, ws.ID) }
	return c, ws, cleanup, nil
}

// goodbyeTimeout bounds goodbye command sends 服务端 在 exit.
// 客户端 sets no 请求 超时 的 它的 own, 和 通过 then root signal
// 处理器 swallows Ctrl+C, 所以 服务端 那 accepts 请求 和 never
// answers would hang exit forever. Giving up 是 safe: 服务端
// releases 客户端 那 left without goodbye once 它的 grace runs out. 一个
// var, 不 const, 所以 tests can shrink it.
var goodbyeTimeout = 5 * time.Second

// sayGoodbye releases every claim 这 process holds 在 服务端,
// falling back 到 releasing workspaceID 在 服务端 那 cannot retire
// clients.
func sayGoodbye(c *client.Client, workspaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), goodbyeTimeout)
	defer cancel()
	if err := c.RetireClient(ctx); err != nil {
		_ = c.DeleteWorkspace(ctx, workspaceID)
	}
}

// warnPermissionMode tells 用户 which 权限 mode project runs
// 在 在 服务端. 一个 project 服务端 already has open keeps 它的 mode,
// whatever -y 或 -yy asked 为, 和 earlier 客户端 may have left it more
// permissive than 这 one asked 为.
func warnPermissionMode(w io.Writer, requested, actual proto.WorkspacePermissionMode) {
	if actual == "" {
		// 服务端 predates reporting mode.
		actual = requested
	}
	if actual != requested {
		fmt.Fprintf(w, "Warning: this project is already open on the server in %s mode, and it stays in that mode.\n", actual)
	}
	if actual == proto.WorkspacePermissionModeSysadmin {
		fmt.Fprintln(w, sysadminModeWarning)
	}
}

// maxStaleServerRetries bounds how many times 工作区 creation may 是
// retried against replacement 服务端. Only one 客户端 can lose race
// against given 服务端's shutdown, 所以 single 重试 是 normally enough;
// bound just 保持 pathological loop finite.
const maxStaleServerRetries = 3

// createWorkspaceOnLiveServer creates 工作区, retrying against
// replacement 当 服务端 it reached has already committed 到 shutting
// itself down 为 being 空闲.
//
// That race 是 unavoidable: 服务端 decides 到 exit while no 客户端 是
// talking 到 it, 和 客户端 can arrive between 那 decision 和
// socket going away. decision 是 final 在 服务端's side, 所以
// 只 correct 响应 是 到 bring up fresh 服务端 和 ask again
// instead 的 failing command.
func createWorkspaceOnLiveServer(
	ctx context.Context, c *client.Client, req proto.Workspace, replace func() error,
) (*proto.Workspace, error) {
	for attempt := range maxStaleServerRetries {
		ws, err := c.CreateWorkspace(ctx, req)
		if err == nil {
			return ws, nil
		}
		if !errors.Is(err, client.ErrServerShuttingDown) || attempt == maxStaleServerRetries-1 {
			return nil, err
		}
		slog.Warn("Server is shutting down; retrying against a replacement",
			"attempt", attempt+1, "error", err)
		if err := replace(); err != nil {
			return nil, err
		}
		// refused 创建 left kept-alive 连接 到 exiting
		// 服务端, which can outlive handover 和 would refuse
		// 重试 作为 well. Drop it 所以 重试 dials replacement.
		c.CloseIdleConnections()
	}
	return nil, fmt.Errorf("failed to create workspace: server kept shutting down")
}

// replaceExitingServer waits out address 的 服务端 那 has
// committed 到 exiting, then brings up fresh one.
func replaceExitingServer(cmd *cobra.Command, hostURL *url.URL) error {
	if hostURL.Scheme == "unix" {
		if err := awaitSocketGone(cmd.Context(), hostURL); err != nil {
			return err
		}
	} else if err := awaitExitingServerGone(cmd.Context(), hostURL); err != nil {
		return err
	}
	if err := spawnAndWaitReady(cmd, hostURL); err != nil {
		return fmt.Errorf("failed to initialize evo server: %v", err)
	}
	return nil
}

// exitingServerTimeout bounds how long 客户端 waits 为 服务端 那
// has committed 到 exiting 到 release named pipe 或 tcp address. 一个
// stopping 服务端 gives 它的 workspaces 15s 到 shut down before it lets go.
const exitingServerTimeout = 20 * time.Second

// awaitExitingServerGone waits until 服务端 那 has committed 到
// exiting no longer holds 它的 named pipe 或 tcp address. Unlike unix
// socket 文件, neither can 是 taken over 从 live process, 所以
// replacement spawned any earlier would fail 到 bind.
//
// wait ends once nothing listens 在 address ( pipe 是 gone 或
// 连接 是 refused), 或 once 服务端 那 是 不 shutting down
// answers there because another 客户端 already replaced it.
func awaitExitingServerGone(ctx context.Context, hostURL *url.URL) error {
	httpClient, reqURL, err := readinessHTTPClient(hostURL)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(exitingServerTimeout)
	for {
		attemptCtx, cancel := context.WithDeadline(ctx, deadline)
		err := probeHealth(attemptCtx, httpClient, reqURL, hostURL)
		cancel()
		if err == nil || errors.Is(err, fs.ErrNotExist) || isConnRefused(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("server is shutting down but did not release %s: %v", hostURL.Host, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ensureServer auto-starts detached 服务端 如果 socket 文件 does 不
// exist. When socket exists, it verifies 那 running 服务端
// version matches 客户端; 在 mismatch it shuts down old 服务端
// 和 starts fresh one.
func ensureServer(cmd *cobra.Command, hostURL *url.URL) error {
	// Initialize persistent log here 所以 stale-socket diagnostics
	// emitted before connectToServer runs 是 captured 在 disk. They go 到
	// clients' own 文件 rather than 服务端's log, which 只
	// 服务端 may 写入 (see serverLogFile). connectToServer moves log
	// 到 工作区 once it 是 known.
	debug, _ := cmd.Flags().GetBool("debug")
	evolog.Setup(clientLogFile(hostURL), debug)

	switch hostURL.Scheme {
	case "unix", "npipe":
		needsStart := false
		statErr := statServerEndpoint(cmd.Context(), hostURL)
		switch {
		case statErr == nil:
			// Probe the socket explicitly before the version-check
			// 路径. 一个 stale unix socket 文件 ( previous 服务端
			// exited without cleaning up) would otherwise make
			// restartIfStale spin 在 non-responsive endpoint; here
			// we detect it 用 short DialTimeout 和 remove
			// orphaned 文件 所以 normal spawn 路径 can 运行.
			if hostURL.Scheme == "unix" {
				conn, dialErr := net.DialTimeout( //nolint:noctx
					hostURL.Scheme, hostURL.Host, 200*time.Millisecond,
				)
				if dialErr == nil {
					conn.Close()
				} else if server.IsStaleSocketErr(dialErr) {
					slog.Warn("Stale socket detected, removing",
						"path", hostURL.Host, "error", dialErr)
					if err := os.Remove(hostURL.Host); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return fmt.Errorf("failed to remove stale server socket %q: %v", hostURL.Host, err)
					}
					needsStart = true
					break
				}
			}
			restarted, err := restartIfStale(cmd, hostURL)
			if err != nil {
				slog.Warn("Failed to check server version", "error", err)
			}
			needsStart = restarted || err != nil
		case errors.Is(statErr, fs.ErrNotExist):
			needsStart = true
		default:
			slog.Warn("Unexpected error stat'ing server socket, attempting cleanup",
				"path", hostURL.Host, "error", statErr)
			if err := os.Remove(hostURL.Host); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("failed to remove stale server socket %q: %v", hostURL.Host, err)
			}
			needsStart = true
		}

		if needsStart {
			if err := spawnAndWaitReady(cmd, hostURL); err != nil {
				return fmt.Errorf("failed to initialize evo server: %v", err)
			}
			return nil
		}

		if err := waitForServerReady(cmd.Context(), hostURL, nil); err != nil {
			// 服务端 found here has committed 到 exiting 和 will
			// never serve 这 客户端, 所以 replace it.
			if errors.Is(err, client.ErrServerShuttingDown) {
				slog.Warn("Server is shutting down; replacing it", "error", err)
				return replaceExitingServer(cmd, hostURL)
			}
			return fmt.Errorf("failed to initialize evo server: %v", err)
		}
	}

	return nil
}

// spawnAndWaitReady serializes spawn-和-wait-为-readiness 序号
// across concurrent clients via exclusive flock 在
// $XDG_CACHE_HOME/crush/服务端-<safeHost>/start.锁.
//
// After acquiring 锁 it re-probes readiness 所以 那 客户端 那
// blocked while another 客户端 was spawning can skip 它的 own spawn 和
// just use now-running 服务端. 锁 是 held 只 为
// duration 的 "spawn + readiness probe" 和 released before caller
// resumes 它的 normal lifetime.
//
// 一个 healthy holder therefore releases 锁 within about one readiness
// budget 为 服务端 it spawned, 所以 wait 为 it 是 bounded 通过 twice
// 那: holder 那 keeps it longer 是 stuck (e.g. suspended 用
// Ctrl+Z) 和 must 不 hang every other 客户端 too.
func spawnAndWaitReady(cmd *cobra.Command, hostURL *url.URL) error {
	chDir, err := perHostServerDir(hostURL)
	if err != nil {
		return err
	}
	waitCtx, cancelWait := context.WithTimeout(cmd.Context(), 2*serverReadyTimeout(true))
	release, err := lock.File(waitCtx, filepath.Join(chDir, "start.lock"))
	cancelWait()
	if err != nil {
		// 用户 gave up (e.g. Ctrl+C) while we waited: spawning now
		// would leave behind 服务端 they aborted, racing 锁
		// holder's own spawn.
		if ctxErr := cmd.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		// If 锁 itself 是 unavailable 或 它的 holder 是 stuck, fall
		// back 到 unsynchronized 路径 rather than blocking 用户.
		slog.Warn("Failed to acquire spawn lock, proceeding without single-flight", "error", err)
		child, err := startDetachedServer(cmd, hostURL)
		if err != nil {
			return err
		}
		return waitForServerReady(cmd.Context(), hostURL, child)
	}
	defer release()

	// Another 客户端 may have just finished spawning while we were
	// waiting 在 锁; 如果 服务端 是 already responsive, skip
	// the spawn entirely.
	probeCtx, cancel := context.WithTimeout(cmd.Context(), 200*time.Millisecond)
	probeErr := quickHealthProbe(probeCtx, hostURL)
	cancel()
	if probeErr == nil {
		return nil
	}

	child, err := startDetachedServer(cmd, hostURL)
	if err != nil {
		return err
	}
	return waitForServerReady(cmd.Context(), hostURL, child)
}

// quickHealthProbe issues single readiness 请求 用 caller's
// 上下文 和 returns nil iff 服务端 是 responsive right now.
func quickHealthProbe(ctx context.Context, hostURL *url.URL) error {
	httpClient, reqURL, err := readinessHTTPClient(hostURL)
	if err != nil {
		return err
	}
	return probeHealth(ctx, httpClient, reqURL, hostURL)
}

// perHostServerDir returns (和 creates) 缓存 目录 used 为
// per-host 服务端 state (logs, start.锁, etc.). 路径 是 derived
// 从 parsed host URL rather than global flag 所以 same key
// 是 computed regardless 的 where host came 从.
func perHostServerDir(hostURL *url.URL) (string, error) {
	chDir := filepath.Join(config.GlobalCacheDir(), "server-"+safeHostName(hostURL))
	if err := os.MkdirAll(chDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create server working directory: %v", err)
	}
	return chDir, nil
}

// serverLogFile returns log 文件 的 服务端 在 hostURL. Only
// 服务端 may 写入 it: it rotates 文件 通过 renaming it, 和 Windows
// refuses 到 rename 文件 another process holds open, which stops
// 服务端's logging.
func serverLogFile(hostURL *url.URL) string {
	return filepath.Join(config.GlobalCacheDir(), "server-"+safeHostName(hostURL), "evo.log")
}

// clientLogFile returns log 文件 clients 的 服务端 在 hostURL 写入
// 到 until they know their 工作区.
func clientLogFile(hostURL *url.URL) string {
	return filepath.Join(config.GlobalCacheDir(), "server-"+safeHostName(hostURL), "client.log")
}

// safeHostName returns filesystem-safe identifier 为 hostURL,
// suitable 为 use 作为 目录 name. 它 mirrors input shape 的
// --host flag 所以 客户端 和 服务端 compute same key.
func safeHostName(hostURL *url.URL) string {
	return safeNameRegexp.ReplaceAllString(
		hostURL.Scheme+"://"+hostURL.Host+hostURL.Path, "_",
	)
}

// serverReadyTimeout returns total budget 为 readiness probe.
// Overridable via EVO_SERVER_READY_TIMEOUT (parsed 作为 Go duration).
//
// Without override, 服务端 这 客户端 has just spawned gets far
// longer than one it found running: it binds 只 after loading 它的 配置,
// which can spend tens 的 seconds fetching 提供方 catalog 在 slow
// network, 和 waitForServerReady stops early 如果 it exits.
func serverReadyTimeout(spawned bool) time.Duration {
	def := 10 * time.Second
	if spawned {
		def = 2 * time.Minute
	}
	v := os.Getenv("EVO_SERVER_READY_TIMEOUT")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// waitForServerReady polls GET /v1/health until 服务端 responds 用
// any 2xx status 或 total 超时 elapses. Each attempt uses short
// per-attempt 超时 所以 hung listener doesn't burn whole budget.
// 一个 服务端 那 answers it 是 shutting down will never become ready, 所以
// 那 ends wait 在 once 用 错误 wrapping
// [客户端.ErrServerShuttingDown].
//
// child 是 服务端 这 客户端 just spawned, 如果 any: wait then gets
// longer budget 为 such 服务端, 和 fails 作为 soon 作为 那 process
// exits instead 的 running out clock.
//
// HTTP transport 是 built 到 mirror how *客户端.客户端 dials 所以
// same unix socket / npipe / tcp setups all work uniformly here.
func waitForServerReady(ctx context.Context, hostURL *url.URL, child *spawnedServer) error {
	httpClient, reqURL, err := readinessHTTPClient(hostURL)
	if err != nil {
		return err
	}

	const perAttempt = 100 * time.Millisecond
	deadline := time.Now().Add(serverReadyTimeout(child != nil))

	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("timed out waiting for server readiness")
		}

		// Check 为 exit before probing, 所以 那 服务端 which beat
		// child 到 address, 和 所以 made it exit, 是 still found.
		exited := child != nil && child.exited()
		attemptCtx, cancel := context.WithTimeout(ctx, perAttempt)
		err := probeHealth(attemptCtx, httpClient, reqURL, hostURL)
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, client.ErrServerShuttingDown) {
			return err
		}
		if exited {
			return child.exitError()
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(perAttempt):
		}
	}
}

// serverRunning reports whether 服务端 是 already answering 在
// configured 主机, without spawning one.
func serverRunning(ctx context.Context) bool {
	hostURL, err := server.ParseHostURL(clientHost)
	if err != nil {
		return false
	}
	httpClient, reqURL, err := readinessHTTPClient(hostURL)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return probeHealth(ctx, httpClient, reqURL, hostURL) == nil
}

// readinessHTTPClient builds *http.客户端 whose transport dials
// 服务端 using same scheme-aware logic 作为 *客户端.客户端 (unix
// socket, named pipe, 或 tcp).
func readinessHTTPClient(hostURL *url.URL) (*http.Client, string, error) {
	c, err := client.NewClient("", hostURL.Scheme, hostURL.Host)
	if err != nil {
		return nil, "", err
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return c.Dial(ctx, network, addr)
	}
	if hostURL.Scheme == "unix" || hostURL.Scheme == "npipe" {
		tr.DisableCompression = true
	}

	httpClient := &http.Client{Transport: tr}

	// For unix sockets / named pipes we still need a syntactically valid
	// HTTP URL; actual address 是 resolved 通过 dialer.
	host := hostURL.Host
	if hostURL.Scheme == "unix" || hostURL.Scheme == "npipe" {
		host = client.DummyHost
	}
	reqURL := (&url.URL{Scheme: "http", Host: host, Path: "/v1/health"}).String()
	return httpClient, reqURL, nil
}

// probeHealth issues single GET 到 readiness endpoint 和 treats
// any 2xx 响应 作为 success. 一个 服务端 那 是 shutting down answers
// 503, reported 作为 错误 wrapping [客户端.ErrServerShuttingDown].
func probeHealth(ctx context.Context, h *http.Client, reqURL string, hostURL *url.URL) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	if hostURL.Scheme == "unix" || hostURL.Scheme == "npipe" {
		req.Host = client.DummyHost
	}
	rsp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	_, _ = io.Copy(io.Discard, rsp.Body)
	if rsp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("%w: %s", client.ErrServerShuttingDown, rsp.Status)
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("server health check failed: %s", rsp.Status)
	}
	return nil
}

// versionCheckTimeout bounds requests restartIfStale sends 到 running
// 服务端. Connecting 到 stopped 或 wedged 服务端 still succeeds, because
// kernel accepts 在 它的 behalf, 所以 without 期限 客户端 would
// wait 为 reply forever. 一个 var, 不 const, 所以 tests can shrink it.
var versionCheckTimeout = 5 * time.Second

// restartIfStale checks whether running 服务端 matches current
// 客户端 version. When they differ it asks 服务端 到 stand down 和,
// 如果 it agrees, removes stale socket 所以 caller can start fresh
// 服务端.
//
// 请求 是 conditional 和 服务端 has last word: it refuses
// while it 是 hosting anything, because BuildID derives 从
// executable's mtime, 所以 any rebuild (including every `go 运行`) makes
// second 会话 look like upgrade 和 would otherwise kill first
// 会话's workspaces. Servers too old 到 understand conditional
// command 是 left running 为 same reason — 请求 they do
// understand 是 unconditional. They shut themselves down 当 they go
// 空闲, 和 next 客户端 then finds no socket 和 spawns current one.
//
// 它 returns restarted=true 只 当 服务端 accepted shutdown 和
// caller 必须 spawn replacement.
func restartIfStale(cmd *cobra.Command, hostURL *url.URL) (restarted bool, err error) {
	c, err := client.NewClient("", hostURL.Scheme, hostURL.Host)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), versionCheckTimeout)
	defer cancel()
	vi, err := c.VersionInfo(ctx)
	if err != nil {
		return false, err
	}
	if vi.Version == version.Version && vi.BuildID == version.BuildID {
		return false, nil
	}
	versionFields := []any{
		"server_version", vi.Version,
		"client_version", version.Version,
		"server_build_id", vi.BuildID,
		"client_build_id", version.BuildID,
	}
	// Every 拒绝 — 在 use, too old 到 是 asked, 或 unreachable — leads 到
	// same safe outcome: keep using running 服务端. wrapped
	// 错误 says which it was.
	if err := c.ShutdownServerIfIdle(ctx); err != nil {
		if !errors.Is(err, client.ErrUnsupported) {
			slog.Warn("Server version differs but it will not stand down; reusing it",
				append(versionFields, "error", err)...)
			return false, nil
		}
		// 服务端 predates shutdown_if_idle. Fall back 到
		// unconditional command, 但 只 after verifying it 是 空闲.
		if !shutdownLegacyStaleServer(ctx, c, versionFields) {
			return false, nil
		}
	}
	slog.Info("Stale server accepted shutdown, restarting", versionFields...)
	if err := awaitSocketGone(cmd.Context(), hostURL); err != nil {
		return true, err
	}
	return true, nil
}

// shutdownLegacyStaleServer handles stale 服务端 too old 到 understand
// 空闲-checked shutdown. That 服务端's 只 "shutdown" command 是
// unconditional 和 would take live sessions down, 所以 it 是 used 只 after
// listing workspaces confirms 服务端 是 空闲. 它 reports whether
// 服务端 accepted shutdown; every other outcome (unreachable, busy, 或
// refused shutdown) 是 logged 和 reported 作为 false 所以 caller reuses
// running 服务端.
func shutdownLegacyStaleServer(ctx context.Context, c *client.Client, versionFields []any) bool {
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("Server version differs but it will not stand down; reusing it",
			append(versionFields, "list_error", err)...)
		return false
	}
	if len(workspaces) > 0 {
		slog.Warn("Server version differs and has active workspaces; reusing it",
			append(versionFields, "workspaces", len(workspaces))...)
		return false
	}
	if err := c.ShutdownServer(ctx); err != nil {
		slog.Warn("Server version differs but it will not stand down; reusing it",
			append(versionFields, "error", err)...)
		return false
	}
	return true
}

// awaitSocketGone gives 服务端 那 has committed 到 exiting moment 到
// release 它的 socket, then force-removes socket 文件 那 是 left over:
// old process has latched 它的 decision 和 will 不 serve requests
// again. 一个 named pipe 是 never removed; it goes away 用 old 服务端.
func awaitSocketGone(ctx context.Context, hostURL *url.URL) error {
	for range 20 {
		if err := statServerEndpoint(ctx, hostURL); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if hostURL.Scheme != "npipe" {
		_ = os.Remove(hostURL.Host)
	}
	return nil
}

// pipeProbeTimeout bounds dial 那 decides whether named pipe
// exists. dial retries while every instance 是 busy, 和 serving
// 服务端 offers new instance right after each 受理, 所以 it 只 runs
// out once 服务端 has stopped accepting, e.g. while it drains 它的 last
// connections 在 way out.
const pipeProbeTimeout = 200 * time.Millisecond

// probePipe connects 到 named pipe 在 addr 和 hangs up again, giving
// up after pipeProbeTimeout. 它 是 variable 所以 tests can simulate
// states 的 pipe 在 any platform.
var probePipe = func(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, pipeProbeTimeout)
	defer cancel()
	c, err := client.NewClient("", "npipe", addr)
	if err != nil {
		return err
	}
	conn, err := c.Dial(ctx, "npipe", addr)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// statServerEndpoint reports whether 服务端's endpoint exists way
// os.Stat does: nil 如果 it does, 错误 matching fs.ErrNotExist 如果 it does
// 不, 和 any other 错误 如果 那 cannot 是 told.
//
// 一个 named pipe 是 不 文件. Stat'ing 或 removing one opens it 作为
// 客户端, which fails 用 ERROR_PIPE_BUSY whenever no instance 是
// listening, e.g. between two accepts 或 while exiting 服务端 drains
// 它的 connections. So pipe 是 dialed instead, 和 anything 但 "不
// found" means 服务端 still holds name.
func statServerEndpoint(ctx context.Context, hostURL *url.URL) error {
	if hostURL.Scheme != "npipe" {
		_, err := os.Stat(hostURL.Host)
		return err
	}
	if err := probePipe(ctx, hostURL.Host); errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

var safeNameRegexp = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// spawnedServer 是 服务端 process 这 客户端 started 和 waits 在, 所以
// 那 waitForServerReady can tell 服务端 那 是 slow 到 start 从 one
// 那 has died.
type spawnedServer struct {
	// stderrPath 是 log 服务端's stderr goes 到.
	stderrPath string
	// done 是 closed once process has exited.
	done chan struct{}
	// err 是 process's exit status, set before done 是 closed.
	err error
}

// exited reports whether 服务端 process has exited.
func (s *spawnedServer) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// exitError reports 那 服务端 exited before it was ready, along 用
// last lines it wrote 到 stderr, which say why.
func (s *spawnedServer) exitError() error {
	err := errors.New("evo server exited before it was ready")
	if s.err != nil {
		err = fmt.Errorf("evo server exited before it was ready: %v", s.err)
	}
	data, _ := os.ReadFile(s.stderrPath)
	var lines []string
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return err
	}
	const maxLines = 10
	lines = lines[max(0, len(lines)-maxLines):]
	return fmt.Errorf("%w; last lines of %s:\n%s", err, s.stderrPath, strings.Join(lines, "\n"))
}

// startDetachedServer launches `evo server` detached 从 这 process.
// 它 是 variable 所以 tests can observe spawns without starting 服务端.
var startDetachedServer = func(cmd *cobra.Command, hostURL *url.URL) (*spawnedServer, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %v", err)
	}

	chDir, err := perHostServerDir(hostURL)
	if err != nil {
		return nil, err
	}

	// Pass parsed host rather than flag 作为 typed, 所以 relative
	// socket 路径 reaches 服务端 already resolved against 目录
	// evo was started 在, 不 against --cwd.
	cmdArgs := []string{"server"}
	if clientHost != server.DefaultHost() {
		cmdArgs = append(cmdArgs, "--host", hostURL.Scheme+"://"+hostURL.Host+hostURL.Path)
	}

	stdoutPath := filepath.Join(chDir, "stdout.log")
	stderrPath := filepath.Join(chDir, "stderr.log")

	// Append rather than truncate: stderr.log 是 只 record 的
	// previous 服务端's panic 或 other runtime crash, 和 spawning 它的
	// replacement must 不 erase 那 before anyone has 读取 it.
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout log file: %v", err)
	}
	defer stdout.Close()

	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr log file: %v", err)
	}
	defer stderr.Close()

	c, err := startDetached(func() *exec.Cmd {
		c := detachedServerCmd(exe, chDir, cmdArgs...)
		c.Stdout = stdout
		c.Stderr = stderr
		return c
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start evo server: %v", err)
	}

	// Wait 在 process rather than releasing it, 所以 readiness wait
	// can see it exit. Waiting does 不 tie 服务端 到 这 客户端: it
	// stays detached 和 outlives 客户端 作为 before.
	child := &spawnedServer{stderrPath: stderrPath, done: make(chan struct{})}
	go func() {
		child.err = c.Wait()
		close(child.done)
	}()
	return child, nil
}

// detachedServerCmd returns command 那 runs detached 服务端 从
// dir. 服务端 outlives 客户端 那 spawns it, 所以 inheriting 那
// 客户端's working 目录 would keep it 在 use 为 作为 long 作为
// 服务端 runs: 在 Windows it could 不 是 deleted 或 renamed, 和
// elsewhere 它的 volume could 不 是 unmounted. Panic logs, which 是
// written relative 到 working 目录, would also land there.
func detachedServerCmd(exe, dir string, args ...string) *exec.Cmd {
	// Use 上下文.Background() 所以 parent's 上下文 cancellation does 不
	// kill spawned 服务端. startDetached (Setsid 在 !windows; 在
	// windows new process group 用 hidden console 的 它的 own, outside
	// 这 process's job) 是 what truly detaches child 从 这
	// process's 有效期.
	c := exec.CommandContext(context.Background(), exe, args...)
	c.Dir = dir
	// Tell 服务端 客户端 spawned it, 所以 it keeps 空闲 shutdown
	// 那 stops it 从 outliving 那 客户端.
	c.Env = append(os.Environ(), spawnedServerEnv+"=1")
	return c
}

func shouldEnableMetrics(cfg *config.Config) bool {
	if v, _ := strconv.ParseBool(os.Getenv("EVO_DISABLE_METRICS")); v {
		return false
	}
	if v, _ := strconv.ParseBool(os.Getenv("DO_NOT_TRACK")); v {
		return false
	}
	if cfg.Options.DisableMetrics {
		return false
	}
	return true
}

func MaybePrependStdin(prompt string) (string, error) {
	if term.IsTerminal(os.Stdin.Fd()) {
		return prompt, nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return prompt, err
	}
	// Check 如果 stdin 是 named pipe ( | ) 或 regular 文件 ( < ).
	if fi.Mode()&os.ModeNamedPipe == 0 && !fi.Mode().IsRegular() {
		return prompt, nil
	}
	bts, err := io.ReadAll(os.Stdin)
	if err != nil {
		return prompt, err
	}
	return string(bts) + "\n\n" + prompt, nil
}

// resolveWorkspaceSessionID resolves 会话 ID 那 may 是 full
// UUID, full hash, 或 hash prefix. Works against 工作区
// interface 所以 both local 和 客户端/服务端 paths get hash prefix
// support.
func resolveWorkspaceSessionID(ctx context.Context, ws workspace.Workspace, id string) (session.Session, error) {
	if sess, err := ws.GetSession(ctx, id); err == nil {
		return sess, nil
	}

	sessions, err := ws.ListSessions(ctx)
	if err != nil {
		return session.Session{}, err
	}

	var matches []session.Session
	for _, s := range sessions {
		hash := session.HashID(s.ID)
		if hash == id || strings.HasPrefix(hash, id) {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return session.Session{}, fmt.Errorf("session not found: %s", id)
	case 1:
		return matches[0], nil
	default:
		return session.Session{}, fmt.Errorf("session ID %q is ambiguous (%d matches)", id, len(matches))
	}
}

// ResolveCwd changes into --cwd 目录, 如果 one 是 given, 和 returns
// absolute working 目录. 它 must 是 absolute because 客户端/服务端
// mode sends it 到 服务端, which would resolve relative 路径 against
// 它的 own working 目录.
func ResolveCwd(cmd *cobra.Command) (string, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	if cwd != "" {
		// Resolve the flag before changing into it, while a relative
		// 值 still means what it did 在 用户's shell.
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return "", fmt.Errorf("failed to resolve directory: %v", err)
		}
		err = os.Chdir(abs)
		if err != nil {
			return "", fmt.Errorf("failed to change directory: %v", err)
		}
		// 存储 absolute 路径 所以 later 调用 does 不 resolve
		// relative flag against 目录 just changed into.
		_ = cmd.Flags().Set("cwd", abs)
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %v", err)
	}
	return cwd, nil
}

// resolveFlagPaths makes relative paths 在 --host 和 --提示词
// absolute against 目录 evo was started 在, which 是 what they
// mean 在 command line. 它 runs before anything changes into --cwd.
func resolveFlagPaths(cmd *cobra.Command) error {
	if strings.HasPrefix(clientHost, "unix://") {
		if hostURL, err := server.ParseHostURL(clientHost); err == nil {
			clientHost = "unix://" + hostURL.Host
		}
	}
	prompts, err := promptOverrides(cmd)
	if err != nil || len(prompts) == 0 {
		return err
	}
	values := make([]string, 0, len(prompts))
	for name, path := range prompts {
		values = append(values, name+"="+path)
	}
	slices.Sort(values)
	if f, ok := cmd.Flags().Lookup("prompt").Value.(pflag.SliceValue); ok {
		return f.Replace(values)
	}
	return nil
}

func createDotEvoDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %q %w", dir, err)
	}

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitIgnorePath)

	// 创建 或 更新 如果 old version
	if os.IsNotExist(err) || string(content) == oldGitIgnore {
		if err := os.WriteFile(gitIgnorePath, []byte(defaultGitIgnore), 0o644); err != nil {
			return fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	return nil
}

//go:embed gitignore/old
var oldGitIgnore string

//go:embed gitignore/default
var defaultGitIgnore string
