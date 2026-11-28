// ui.go —— TUI 主控制器，也是「前端对后端」的入口点。
//
// 从产物反推前端到后端的调用：
//
//	用户输入 → sendMessage/sendMessageInternal（本文件）
//	  → com.Workspace.AgentRun(runCtx, sessionID, content, attachments...)
//	  → AppWorkspace（本地）或 ClientWorkspace（远程，走 HTTP/SSE）
//	  → backend → app.App → agent.Run
//	  → 结果经 pubsub/SSE 事件回到 UI 的消息渲染（ui/chat）
//
// 本文件还持有 UI 与 Workspace 的主要交互：session 切换、消息列表、
// 模型/权限/配置、MCP、skills、通知、agent busy 状态。
package model

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	agenttools "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/clipboard"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/completions"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	fimage "github.com/charmbracelet/crush/internal/ui/image"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/notification"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/charmbracelet/ultraviolet/screen"
	"github.com/charmbracelet/x/editor"
	xstrings "github.com/charmbracelet/x/exp/strings"
)

// Compact mode breakpoints.
const (
	compactModeWidthBreakpoint  = 120
	compactModeHeightBreakpoint = 30
)

// If pasted 文本 has more than 10 newlines, treat it 作为 文件 附件.
const pasteLinesThreshold = 10

// If pasted 文本 has more than 1000 columns, treat it 作为 文件 附件.
const pasteColsThreshold = 1000

// 会话 details panel max height.
const sessionDetailsMaxHeight = 20

// hyperCreditsPollInterval 是 how often Hyper credits balance 是
// refreshed while no 会话 是 running.
const hyperCreditsPollInterval = 60 * time.Second

// gitBranchPollInterval 是 how often 工作区's checked-out branch 是
// re-读取. 一个 checkout emits no 事件 Crush can subscribe 到, 所以 branch
// has 到 是 polled 到 stay current.
const gitBranchPollInterval = 5 * time.Second

// gitBranchFetchTimeout bounds one branch 读取. Locally 这 是 文件
// 读取; 在 客户端/服务端 mode it 是 请求 到 服务端.
const gitBranchFetchTimeout = 10 * time.Second

// TextareaMaxHeight 是 maximum height 的 提示词 textarea.
const TextareaMaxHeight = 15

// editorHeightMargin 是 vertical margin added 到 textarea height 到
// account 为 attachments 行 (top) 和 bottom margin.
const editorHeightMargin = 2

// TextareaMinHeight 是 minimum height 的 提示词 textarea.
const TextareaMinHeight = 3

// uiFocusState represents current focus state 的 UI.
type uiFocusState uint8

// Possible uiFocusState values.
const (
	uiFocusNone uiFocusState = iota
	uiFocusEditor
	uiFocusMain
	uiFocusSidebar
)

type uiState uint8

// Possible uiState values.
const (
	uiOnboarding uiState = iota
	uiInitialize
	uiLanding
	uiChat
)

type uiInputMode uint8

const (
	uiInputModeCode uiInputMode = iota
	uiInputModePlan
)

type openEditorMsg struct {
	Text string
}

type shellResultMsg struct {
	PendingID string // ID of the pending ShellItem to update.
	Command   string
	Output    string
	ExitCode  int
}

// bangRun identifies bang-mode shell command 通过 会话 it ran 在.
type bangRun struct{ sessionID, command string }

// shellStreamMsg carries incremental output 从 streaming shell command.
type shellStreamMsg struct {
	PendingID string
	Chunk     string
	streamCh  <-chan string // unexported; used to continue draining
}

type (
	// cancelTimerExpiredMsg 是 sent 当 取消 timer expires.
	cancelTimerExpiredMsg struct{}
	// userCommandsLoadedMsg 是 sent 当 用户 commands 是 loaded.
	userCommandsLoadedMsg struct {
		Commands []commands.CustomCommand
	}
	// mcpPromptsLoadedMsg 是 sent 当 mcp prompts 是 loaded.
	mcpPromptsLoadedMsg struct {
		Prompts []commands.MCPPrompt
	}
	// mcpStateChangedMsg 是 sent 当 there 是 change 在 MCP 客户端 states.
	mcpStateChangedMsg struct {
		states map[string]mcp.ClientInfo
	}
	// skillsOverridesLoadedMsg carries the repository-scoped skill toggle
	// sets after 工作区 fetch 所以 sidebar 和 toggles dialog
	// can filter without blocking 在 工作区 calls during render.
	skillsOverridesLoadedMsg struct {
		disabled map[string]bool
		enabled  map[string]bool
	}
	// sendMessageMsg 是 sent 到 send 消息.
	// currently 只 used 为 mcp prompts.
	sendMessageMsg struct {
		Content     string
		Attachments []message.Attachment
	}

	// closeDialogMsg 是 sent 到 close current dialog.
	closeDialogMsg struct{}

	// hyperRefreshDoneMsg 是 sent after silent Hyper OAuth refresh
	// finishes. 它 carries original 模型-selection action 所以
	// selection can 是 resumed.
	hyperRefreshDoneMsg struct {
		action dialog.ActionSelectModel
	}

	// copyChatHighlightMsg 是 sent 到 copy current chat highlight 到 clipboard.
	copyChatHighlightMsg struct{}

	// sessionFilesUpdatesMsg 是 sent 当 files 为 这 会话 have been updated
	sessionFilesUpdatesMsg struct {
		sessionFiles []SessionFile
	}

	// creditsUpdatedMsg 是 sent 当 remaining Hyper credits have been
	// fetched 从 API. credits 是 nil 当 team has hypercredit
	// display disabled.
	creditsUpdatedMsg struct {
		credits *int
	}

	// hyperCreditsPollMsg 是 sent 通过 Hyper credits poll timer.
	hyperCreditsPollMsg struct{}

	// gitBranchUpdatedMsg carries 工作区's checked-out branch. branch
	// 是 empty 当 工作区 是 不 Git repository 或 HEAD 是
	// detached.
	gitBranchUpdatedMsg struct {
		branch string
	}

	// gitBranchPollMsg 是 sent 通过 git branch poll timer.
	gitBranchPollMsg struct{}
)

// UI represents main 用户 interface 模型.
type UI struct {
	com          *common.Common
	session      *session.Session
	sessionFiles []SessionFile

	// keeps track 的 读取 files while we don't have 会话 id
	sessionFileReads []string

	// initialSessionID 是 set 当 loading specific 会话 在 startup.
	initialSessionID string
	// continueLastSession 是 set 到 continue most recent 会话 在 startup.
	continueLastSession bool

	lastUserMessageTime int64

	// width 和 height 的 终态 在 cells.
	width  int
	height int
	layout uiLayout

	isTransparent bool

	// mouseEnabled controls whether Bubble Tea mouse reporting 是 活跃.
	// When false, 终态 emulator (或 tmux) handles 文本 selection,
	// copy/paste, right-click, 和 scrolling instead 的 Crush.
	mouseEnabled bool

	// themeKey identifies currently applied theme 所以 applyTheme can
	// skip expensive style rebuild 当 switching 到 提供方 那
	// resolves 到 same theme.
	themeKey string

	// userThemeSelected records 那 用户 explicitly chose theme
	// during 这 会话. 它 guards against 提供方-driven theme swaps
	// discarding 那 choice, even 在 客户端/服务端 mode where
	// 配置 round-trip may 不 reflect selection immediately.
	userThemeSelected bool

	// hostTheme tracks theme 主机 application pushes through
	// 服务端 (see hosttheme.go).
	hostTheme hostThemeState

	focus uiFocusState
	state uiState
	mode  uiInputMode

	// Frame memoization (see framecache.go). scrollOnlyUpdate 是 set 通过
	// handlers 那 change nothing 但 chat scroll position; frameDirty
	// overrides it 当 layout change happens 在 same 更新.
	frames           *frameCache
	scrollOnlyUpdate bool
	frameDirty       bool
	frameSkipPut     bool
	frameGCArmed     bool
	// planReadySessionID holds 会话 whose plan 运行 emitted
	// plan-ready marker 但 has 不 been confirmed 为 execution yet. 它
	// lets 用户 reopen handoff 提示词 after dismissing it.
	planReadySessionID string
	// modeSwitching 是 true while async agent-模型 更新 kicked off
	// 通过 setInputMode 是 still 在 flight; sending 是 blocked meanwhile.
	modeSwitching bool
	// modeAdopted 是 set 当 mode follows switch 的 shared main
	// agent 那 another 客户端 made (see followMainAgent), 和 cleared
	// 当 这 客户端 switches.
	modeAdopted bool

	keyMap KeyMap
	keyenh tea.KeyboardEnhancementsMsg

	dialog *dialog.Overlay
	status *Status

	// isCanceling tracks whether 用户 has pressed escape once 到 取消.
	isCanceling bool

	// bangMode tracks whether editor 是 在 bang (!) shell mode.
	bangMode     bool
	bangWasEmpty bool // true when bang prompt became empty on last keystroke

	// pendingBangCommand holds shell command 那 was issued before
	// 会话 finished loading. loadSessionMsg 处理器 creates
	// pending UI item 和 starts execution once chat list 是
	// stable, eliminating races between 会话 load 和 shell output.
	pendingBangCommand string

	// bangCancel cancels running bang-mode shell command. Nil 当 no
	// bang command 是 在 progress. Set 通过 runShellCommand, cleared 通过
	// shellResultMsg. Checked 通过 isAgentBusy 和 cancelAgent 所以 那
	// Escape works 为 bang commands same way it does 为 agent runs.
	bangCancel context.CancelFunc

	// liveBangRuns counts bang commands 这 UI ran, 和 所以 shows live,
	// whose 存储 copy has 不 arrived yet. That copy 是 skipped 所以
	// command shows once; any other 存储 one, such 作为 command another
	// 客户端 ran 在 会话, 是 shown.
	liveBangRuns map[bangRun]int

	header *header

	// sendProgressBar instructs TUI 到 send progress bar updates 到
	// 终态.
	sendProgressBar    bool
	progressBarEnabled bool

	// turnOutcome 是 结果 的 last agent turn (done 或 错误),
	// reported 到 终态 until 用户 has seen it. See
	// trackTurnOutcome.
	turnOutcome tea.ProgramState

	// caps hold different 终态 capabilities 那 we 查询 为.
	caps common.Capabilities

	// Editor components
	textarea textarea.Model

	// textareaMouseSelecting tracks whether mouse selection gesture 是
	// currently 在 progress within textarea (left button held after
	// click inside the textarea region).
	textareaMouseSelecting bool

	// 活跃 inline editor replaces textarea 当 non-nil.
	activeInline dialog.InlineEditor
	// inlineCursor stores cursor 从 last inline editor
	// Draw 调用, used 通过 cursor positioning logic below.
	inlineCursor *tea.Cursor

	// 附件 list
	attachments *attachments.Attachments

	readyPlaceholder   string
	workingPlaceholder string

	// Completions state
	completions              *completions.Completions
	completionsOpen          bool
	completionsStartIndex    int
	completionsQuery         string
	completionsPositionStart image.Point // x,y where user typed '@'

	// Chat components
	chat *Chat

	// onboarding state
	onboarding struct {
		yesInitializeSelected bool
	}

	// lspStates / lspDiagnostics memoize 工作区 LSP state 和
	// per-服务端 severity counts (each probe behind them 是 synchronous
	// HTTP round-trip 在 客户端/服务端 mode, 和 sidebar, landing view,
	// 和 compact header render them every frame). LSP events refresh them
	// off-thread 用 TTL backstop; see lsp.go.
	lspStates        map[string]workspace.LSPClientInfo
	lspDiagnostics   map[string]lsp.DiagnosticCounts
	lspFetchInFlight bool
	// lspRefreshQueued records 那 LSP 事件 arrived while fetch was
	// already 在 flight; applyLSPStates re-dispatches 所以 freshest state
	// still lands.
	lspRefreshQueued bool
	lspCheckedAt     time.Time

	// mcpStates memoizes 工作区 MCP state ( synchronous probe 在
	// 客户端/服务端 mode) 和 它的 off-thread refresh bookkeeping. MCP
	// state_changed events refresh it off-thread 用 TTL backstop 和
	// 重试 loop while servers 是 starting; see mcp.go.
	mcpStates        map[string]mcp.ClientInfo
	mcpFetchInFlight bool
	// mcpRefreshQueued records 那 refresh was requested while fetch
	// was already 在 flight; applyMCPStates re-dispatches 所以 freshest
	// state still lands.
	mcpRefreshQueued bool
	mcpCheckedAt     time.Time
	// mcpStartingRetries counts the consecutive starting-state re-probes
	// driving 重试 loop's exponential backoff. 事件-driven refreshes
	// renew the budget; settled states reset it.
	mcpStartingRetries int

	// skills
	skillStates []*skills.SkillState
	// skillsDisabledOverrides mirrors the repository-scoped disabled set
	// 所以 sidebar can filter without blocking 在 工作区 calls.
	skillsDisabledOverrides map[string]bool
	// skillsEnabledOverrides mirrors the repository-scoped enabled set
	// (配置-disabled skills re-enabled 为 这 repository).
	skillsEnabledOverrides map[string]bool

	// sidebarLogo keeps cached version 的 sidebar sidebarLogo.
	sidebarLogo string

	// Sidebar scroll state 为 virtual scrolling.
	sidebarOffset           int  // current scroll offset in lines
	sidebarScrollable       bool // true when sidebar content exceeds available height
	sidebarScrollbarVisible bool
	sidebarScrollbarSeq     int    // sequence number for auto-hide timer
	sidebarMaxOffsetVal     int    // max scroll offset, computed in updateSidebarScrollState
	sidebarContent          string // cached rendered sidebar content
	sidebarTotalLines       int    // total lines in sidebarContent
	sidebarContentHeight    int    // available height for sidebar content
	sidebarContentWidth     int    // available width for sidebar content
	sidebarDrawLogo         string // logo to render (may differ from sidebarLogo for short heights)

	// 通知 state
	notifyBackend       notification.Backend
	notifyWindowFocused bool
	// custom commands & mcp commands
	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt

	// forceCompactMode tracks whether compact mode 是 forced 通过 用户 toggle
	forceCompactMode bool

	// isCompact tracks whether we're currently 在 compact layout mode (either
	// 通过 用户 toggle 或 auto-switch based 在 窗口 size)
	isCompact bool

	// detailsOpen tracks whether details panel 是 open (在 compact mode)
	detailsOpen bool

	// pills state
	pillsExpanded      bool
	pillsAutoExpanded  bool
	focusedPillSection pillSection
	// promptQueue 是 what 队列 pill shows. Its 服务端 side 是
	// 事件-driven 用 TTL backstop, fetched off-thread 通过
	// dispatchPromptQueueRefresh (see workspace_cache.go).
	promptQueue          promptQueue
	promptQueueCheckedAt time.Time
	promptQueueInFlight  bool
	// promptQueueGen 是 bumped 通过 every 队列 state transition;
	// 在-flight fetch captures it 在 派发 和 它的 结果 是 discarded
	// 如果 generation has moved 在 (see workspace_cache.go).
	promptQueueGen uint64
	// agentBusyCache / permModeCache memoize 工作区 busy 和
	// 权限-mode probes (synchronous HTTP round-trips 在 客户端/服务端
	// mode). Reads never probe; refreshes happen off-thread (see
	// workspace_cache.go).
	agentBusyCache    ttlCache
	permModeCache     modeTTLCache
	busyFetchInFlight bool
	// agentReady / agentModel memoize coordinator readiness 和
	// selected 模型 (AgentIsReady/AgentModel 是 synchronous HTTP GETs 在
	// 客户端/服务端 mode, 和 modelInfo renders them every frame). Seeded
	// once 在 construction 和 refreshed 通过 same off-thread probe 作为
	// agentBusyCache.
	agentReady bool
	agentModel workspace.AgentModel
	// busyFetchGen 是 bumped 通过 every busy/权限 state transition;
	// like promptQueueGen it lets stale 在-flight probe 结果 是
	// discarded 和 re-fetched instead 的 clobbering newer state.
	busyFetchGen uint64
	// probedSessionBusy 是 last busy probe 的 probedSession, covering
	// runs 这 客户端 did 不 start.
	probedSession     string
	probedSessionBusy bool
	pillsView         string

	// Todo spinner
	todoSpinner    spinner.Model
	todoIsSpinning bool

	// pending 是 turn 这 客户端 sent; placeholder items 在
	// chat 是 derived 从 it 通过 syncPendingItem.
	pending pendingTurn

	// preThemeStyles stores styles before theme preview 所以 we can revert.
	preThemeStyles *styles.Styles

	// program 是 running Bubble Tea program, set via SetProgram. 它 是
	// needed 到 release 和 restore 终态 当 终态-based GPG
	// pinentry dialog takes over the screen.
	program *tea.Program

	// resumeCommand brings Crush back after 终态 restarts, 和
	// quitting 是 set once 用户 quits 所以 终态 forgets it. See
	// programResume.
	resumeCommand []string
	quitting      bool

	// pinentryActive tracks whether 终态 是 currently handed over
	// 到 pinentry dialog; pinentryTouchPending tracks whether status
	// bar shows the security key touch hint.
	pinentryActive       bool
	pinentryTouchPending bool
	// pinentryCtrlL tracks whether 活跃 pinentry dialog 是
	// ncurses flavor 那 repaints 它的 UI 在 Ctrl-L.
	pinentryCtrlL bool
	// pinentryTermMu serializes 终态 release/restore commands, which
	// Bubble Tea runs 在 separate goroutines.
	pinentryTermMu sync.Mutex

	// mouse highlighting related state
	lastClickTime time.Time
	hoverX        int
	hoverY        int

	// subAgentsRunning 是 how many subagents were still working 作为 的
	// last 更新. Counting them walks whole transcript, 和 header
	// needs number 在 every frame, 所以 it 是 counted once per 更新
	// rather than once per frame.
	subAgentsRunning int

	// animClock drives every spinner, 在 transcript 和 chrome.
	animClock animClock

	// hyperCredits 是 remaining Hyper credits 作为 last fetched 从
	// /v1/credits endpoint. 它 是 nil 当 no fetch has reported
	// balance yet, 或 当 team has hypercredit display disabled, 和
	// no balance 是 rendered 在 either case.
	hyperCredits *int

	// gitBranch 是 工作区's checked-out branch 作为 的 last poll,
	// empty 当 there 是 none 到 show. Reading it costs 文件 读取
	// locally 和 请求 在 客户端/服务端 mode, 所以 renders take it 从
	// here rather than asking 工作区 per frame.
	gitBranch string

	// nerdFonts reports whether 终态 renders Nerd Font glyphs.
	nerdFonts bool

	// 提示词 history 为 up/down navigation through previous messages.
	promptHistory struct {
		messages []string
		index    int
		draft    string
	}
}

// New creates new instance 的 [UI] 模型.
func New(com *common.Common, initialSessionID string, continueLast bool) *UI {
	// Editor components
	ta := textarea.New()
	ta.SetStyles(com.Styles.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = TextareaMinHeight
	ta.MaxHeight = TextareaMaxHeight
	// Keep "ctrl+" 为 line-start ( textarea 默认); bind select-all
	// 到 "ctrl+shift+" instead (line-start 是 also available via "home").
	ta.KeyMap.LineStart = key.NewBinding(
		key.WithKeys("home", "ctrl+a"),
		key.WithHelp("home", "line start"),
	)
	ta.KeyMap.SelectAll = key.NewBinding(
		key.WithKeys("ctrl+shift+a"),
		key.WithHelp("ctrl+shift+a", "select all"),
	)
	// Copying 是 handled 通过 crush's keymap (Editor.CopySelection) 所以 it can
	// use crush's clipboard backend 和 用户 feedback; disable
	// textarea's built-在 copy binding.
	ta.KeyMap.CopySelection = key.NewBinding()
	ta.Focus()

	ch := NewChat(com, com.Config().Options.TUI.Scrollbar)

	keyMap := DefaultKeyMap()

	// Completions component
	comp := completions.New(
		com.Styles.Completions.Normal,
		com.Styles.Completions.Focused,
		com.Styles.Completions.Match,
	)

	todoSpinner := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(com.Styles.Pills.TodoSpinner),
	)

	// Attachments component
	attachments := attachments.New(
		attachments.NewRenderer(com.Styles.Attachments),
		attachments.Keymap{
			DeleteMode: keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  keyMap.Editor.DeleteAllAttachments,
			Escape:     keyMap.Editor.Escape,
		},
	)

	header := newHeader(com)

	ui := &UI{
		com:                 com,
		dialog:              dialog.NewOverlay(),
		keyMap:              keyMap,
		textarea:            ta,
		chat:                ch,
		header:              header,
		completions:         comp,
		attachments:         attachments,
		todoSpinner:         todoSpinner,
		frames:              newFrameCache(frameCacheTTL, frameCacheMaxEntries),
		lspStates:           make(map[string]workspace.LSPClientInfo),
		mcpStates:           make(map[string]mcp.ClientInfo),
		notifyBackend:       notification.NoopBackend{},
		notifyWindowFocused: true,
		initialSessionID:    initialSessionID,
		continueLastSession: continueLast,
		skillStates:         skills.GetLatestStates(),
	}

	status := NewStatus(com, ui)

	// Seed 活跃 theme key 从 large 模型 提供方 所以
	// first 模型 selection can correctly skip redundant theme swap.
	if cfg := com.Config(); cfg != nil {
		ui.themeKey = styles.ThemeKeyForProvider(cfg.Models[config.SelectedModelTypeLarge].Provider)
		ui.userThemeSelected = common.ThemeNameFromConfig(cfg) != ""
	}

	// Seed 权限-mode 缓存 once 在 construction; afterwards it 是
	// kept fresh 通过 写入-through toggles 和 off-thread refreshes 所以 更新
	// 和 View never probe 工作区 synchronously.
	permMode := com.Workspace.PermissionMode()
	ui.permModeCache.set(permMode)

	// Seed memoized agent ready/模型 state same way 所以 first
	// frame renders 模型 info; busy probe keeps it fresh
	// afterwards.
	if com.Workspace.AgentIsReady() {
		ui.agentReady = true
		ui.agentModel = com.Workspace.AgentModel()
	}
	ui.mode = uiInputModeCode
	ui.setEditorPrompt(permMode)
	ui.randomizePlaceholders()
	ui.textarea.Placeholder = ui.readyPlaceholder
	ui.status = status

	// Initialize compact mode 从 配置
	ui.forceCompactMode = com.Config().Options.TUI.CompactMode

	// set onboarding state defaults
	ui.onboarding.yesInitializeSelected = true

	desiredState := uiLanding
	desiredFocus := uiFocusEditor
	if !com.Config().IsConfigured() {
		desiredState = uiOnboarding
	} else if n, _ := com.Workspace.ProjectNeedsInitialization(); n {
		desiredState = uiInitialize
	}

	// set initial state
	ui.setState(desiredState, desiredFocus)

	opts := com.Config().Options

	// disable indeterminate progress bar
	ui.progressBarEnabled = opts.Progress == nil || *opts.Progress
	// enable transparent mode
	ui.isTransparent = opts.TUI.IsTransparent()
	// enable mouse support (默认 在)
	ui.mouseEnabled = opts.TUI.Mouse == nil || *opts.TUI.Mouse

	return ui
}

// SetProgram gives 模型 handle 到 running Bubble Tea program.
// 它 是 used 到 release 和 restore 终态 当 终态-based GPG
// pinentry dialog takes over screen. Must 是 called before program
// starts processing pinentry events.
func (m *UI) SetProgram(program *tea.Program) {
	m.program = program
}

// Init initializes UI 模型.
func (m *UI) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.state == uiOnboarding {
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// load 用户 commands async
	cmds = append(cmds, m.loadCustomCommands())
	// Prime the memoized LSP state off-thread.
	if cmd := m.requestLSPRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// load initial 会话 如果 specified
	initialSession := m.loadInitialSession()
	if initialSession != nil {
		cmds = append(cmds, initialSession)
	}
	// loadSessionMsg reloads history 为 whichever 会话 arrives, 所以
	// doing it here too would 是 discarded — 和 用 no 会话 set yet
	// it reads every 用户消息 在 数据库, which pending
	// 会话 load then queues behind 在 single 连接.
	if initialSession == nil {
		cmds = append(cmds, m.loadPromptHistory())
	}
	// Prime ChatGPT 模型 catalog: signed-在 OpenAI 提供方
	// whose catalog 是 missing ( fetch 在 login failed, 或
	// credentials predate it) refills lazily, 所以 models dialog shows
	// subscription section 作为 soon 作为 it 是 opened.
	cmds = append(cmds, m.updateAgentModelCmd(func() tea.Msg {
		_ = m.com.Workspace.UpdateAgentModel(context.TODO())
		return nil
	}))
	// Prime memoized busy/权限 state off-thread.
	if cmd := m.dispatchBusyRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// credits balance 是 shown 从 first frame 在, 所以 fetch it
	// right away 和 keep polling it while Crush sits 空闲. poll runs
	// 为 every 提供方: it 是 no-op unless Hyper 是 selected.
	if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}
	// branch 是 shown 从 first frame 在, 所以 读取 it now 和 keep
	// polling 为 checkouts made outside Crush.
	cmds = append(cmds, m.fetchGitBranch(), m.gitBranchTicker())
	cmds = append(cmds, detectNerdFonts(m.com.Config().Options.TUI))
	cmds = append(cmds, m.hyperCreditsTicker())
	// Prime memoized MCP state off-thread. There 是 deliberately no
	// wait 为 服务端-side MCP initialization: init gate 是
	// process-local 和 只 armed where mcp.Initialize runs ( 服务端),
	// 所以 waiting here 是 no-op 在 客户端/服务端 mode. 重试 loop 和
	// TTL backstop 在 mcp.go converge 在 settled states instead.
	if cmd := m.requestMCPRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// loadInitialSession loads initial 会话 如果 one was specified 在 startup.
func (m *UI) loadInitialSession() tea.Cmd {
	switch {
	case m.state != uiLanding:
		// Only load 如果 we're 在 landing state (i.e., fully configured)
		return nil
	case m.initialSessionID != "":
		return m.loadSession(m.initialSessionID)
	case m.continueLastSession:
		return func() tea.Msg {
			sessions, err := m.com.Workspace.ListSessions(context.Background())
			if err != nil || len(sessions) == 0 {
				return nil
			}
			return m.loadSession(sessions[0].ID)()
		}
	default:
		return nil
	}
}

// sendNotification returns command 那 sends 通知 如果 allowed 通过 policy.
func (m *UI) sendNotification(n notification.Notification) tea.Cmd {
	if !m.shouldSendNotification() {
		return nil
	}

	return m.notifyBackend.Send(n)
}

// selectNotificationBackend chooses appropriate 通知 backend based
// 在 终态 capabilities, environment, 和 用户 configuration. 这 是 pure
// function 那 should 是 called once during initialization 或 当 capabilities
// change.
func selectNotificationBackend(caps common.Capabilities, cfg *config.Config) notification.Backend {
	// Check 为 explicit 用户 preference first.
	if cfg != nil && cfg.Options != nil && cfg.Options.Notifications != "" {
		switch cfg.Options.Notifications {
		case "native":
			if !notification.NativeSupported {
				slog.Debug("Native notifications unavailable on this platform; using OSC backend", "osc99_supported", caps.OSC99Notifications)
				return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
			}
			slog.Debug("Using native backend (user preference)")
			return notification.NewNativeBackend(notification.Icon)
		case "osc":
			slog.Debug("Using OSC backend (user preference)", "osc99_supported", caps.OSC99Notifications)
			return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
		case "bell":
			slog.Debug("Using bell backend (user preference)")
			return notification.NewBellBackend()
		case "disabled":
			slog.Debug("Notifications disabled (user preference)")
			return notification.NoopBackend{}
		case "auto":
			// Fall through 到 auto-detection below.
		default:
			slog.Warn("Unknown notification style, using auto", "style", cfg.Options.Notifications)
		}
	}

	// Auto-detect based 在 environment 和 capabilities.
	_, isSSH := caps.Env.LookupEnv("SSH_TTY")

	// SSH sessions use 终态-based notifications (OSC 99 或 777).
	if isSSH {
		slog.Debug("Selected OSCBackend for SSH session", "osc99_supported", caps.OSC99Notifications)
		return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
	}

	// Local sessions: prefer OSC 在 macOS because native backend (beeep)
	// uses 终态-notifier 或 AppleScript, which 是 slow 和 doesn't display
	// icons properly. Also prefer OSC where native notifications 是 unavailable
	// (illumos/solaris). OSC 99 provides polished experience 用 icon support.
	if runtime.GOOS == "darwin" || !notification.NativeSupported {
		slog.Debug("Selected OSCBackend for local session", "osc99_supported", caps.OSC99Notifications, "native_supported", notification.NativeSupported)
		return notification.NewOSCBackend(notification.Icon, caps.OSC99Notifications)
	}

	// Non-macOS local sessions use native OS notifications 如果 focus events 是 supported.
	// Without focus events, we can't suppress notifications 当 focused, 所以
	// we disable them entirely 到 avoid spamming 用户.
	if caps.ReportFocusEvents {
		slog.Debug("Selected NativeBackend for local session")
		return notification.NewNativeBackend(notification.Icon)
	}

	slog.Debug("Selected NoopBackend (focus events not supported)")
	return notification.NoopBackend{}
}

func (m *UI) updateNotificationBackend() {
	cfg := m.com.Config()
	m.notifyBackend = selectNotificationBackend(m.caps, cfg)
}

// shouldSendNotification returns true 如果 notifications should 是 sent based 在
// current state. Focus reporting must 是 supported, 窗口 must 不 是
// focused, 和 notifications must 不 是 disabled 在 配置.
func (m *UI) shouldSendNotification() bool {
	cfg := m.com.Config()
	if cfg != nil && cfg.Options != nil && cfg.Options.Notifications == "disabled" {
		return false
	}
	return m.caps.ReportFocusEvents && !m.notifyWindowFocused
}

// setState changes UI state 和 focus.
func (m *UI) setState(state uiState, focus uiFocusState) {
	if state == uiLanding {
		// Always turn off compact mode 当 going 到 landing
		m.isCompact = false
	}
	m.state = state
	m.focus = focus
	// Changing state may change layout, 所以 更新 it.
	m.updateLayoutAndSize()
}

// loadCustomCommands loads the custom commands asynchronously.
func (m *UI) loadCustomCommands() tea.Cmd {
	return func() tea.Msg {
		customCommands, err := commands.LoadCustomCommands(m.com.Config())
		if err != nil {
			slog.Error("Failed to load custom commands", "error", err)
		}
		// Append 用户-invocable skills 作为 commands.
		skillEntries, err := m.com.Workspace.ListSkills(context.Background())
		if err != nil {
			slog.Error("Failed to load skill commands", "error", err)
		}
		customCommands = append(customCommands, commands.FromSkillCatalog(skillEntries)...)
		return userCommandsLoadedMsg{Commands: customCommands}
	}
}

// applyChatScroll scrolls chat 通过 lines 和, 如果 selection 是 then
// outside viewport, moves it 到 nearest visible edge. selection
// 是 moved rather than scrolled 到 所以 large coalesced delta 是 applied 在
// full instead 的 being rewound 到 selected item.
func (m *UI) applyChatScroll(lines int) {
	m.chat.ScrollBy(lines)
	if m.chat.SelectedItemInView() {
		return
	}
	if lines > 0 && m.chat.AtBottom() {
		m.chat.SelectLast()
		return
	}
	m.chat.SelectNearestInView(lines < 0)
}

// loadMCPrompts loads the MCP prompts asynchronously.
func (m *UI) loadMCPrompts() tea.Msg {
	prompts, err := m.com.Workspace.ListMCPPrompts(context.Background())
	if err != nil {
		slog.Error("Failed to load MCP prompts", "error", err)
	}
	if prompts == nil {
		// flag them 作为 loaded even 如果 there 是 none 或 错误
		prompts = []commands.MCPPrompt{}
	}
	return mcpPromptsLoadedMsg{Prompts: prompts}
}

// 更新 handles updates 到 UI 模型.
func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.beginFrameUpdate()
	// 更新 终态 capabilities
	m.caps.Update(msg)
	m.trackTurnOutcome(msg)
	switch msg := msg.(type) {
	case tea.EnvMsg:
		// Is 这 Windows 终态?
		if !m.sendProgressBar {
			m.sendProgressBar = slices.Contains(msg, "WT_SESSION")
		}
		cmds = append(cmds, common.QueryCmd(uv.Environ(msg)))
	case tea.ModeReportMsg:
		m.updateNotificationBackend()
	case uv.UnknownOscEvent:
		m.updateNotificationBackend()
	case tea.FocusMsg:
		m.notifyWindowFocused = true
	case tea.BlurMsg:
		m.notifyWindowFocused = false
	case dialog.CollapseInlineMsg:
		m.focusActiveInline(uiFocusMain)
	case pubsub.Event[notify.Notification]:
		if cmd := m.handleAgentNotification(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[notify.RunComplete]:
		m.endPendingTurn(msg.Payload.SessionID)
		if cmd := m.handlePlanHandoff(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case busyStateMsg:
		cmds = append(cmds, m.applyBusyState(msg)...)
	case promptQueueMsg:
		cmds = append(cmds, m.applyPromptQueue(msg)...)
	case lspStatesMsg:
		if cmd := m.applyLSPStates(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case agentModelChangedMsg:
		// coordinator 模型 changed (selection, thinking, reasoning):
		// re-fetch memoized ready/模型 state off-thread.
		m.invalidateBusyCaches()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case agentRunSubmittedMsg:
		// fetch below shows 提示词 如果 it was queued.
		m.promptQueue.Settle(msg.pendingID)
		if msg.err != nil {
			// 运行 never started, 所以 no 终态 事件 是 coming 到
			// retire 它的 spinner.
			m.endPendingTurn(msg.sessionID)
			cmds = append(cmds, util.ReportError(msg.err))
		}
		// 一个 提示词 was just accepted (运行 started 或 enqueued): fetch
		// authoritative busy/队列 state 到 confirm optimistic values
		// sendMessage wrote.
		m.invalidateBusyCaches()
		m.invalidatePromptQueue()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case loadSessionMsg:
		if m.forceCompactMode {
			m.isCompact = true
		}
		// Plan mode 是 scoped 到 会话 it was enabled 在: switching
		// 到 another 会话 falls back 到 code mode 和 drops any pending
		// plan handoff. (Loading 会话 那 was just created 为
		// first plan-mode 提示词 是 不 switch; IDs match then.)
		if m.session == nil || m.session.ID != msg.session.ID {
			if cmd := m.resetPlanModeState(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.turnOutcome = ""
		}
		m.setState(uiChat, m.focus)
		m.session = msg.session
		m.sidebarOffset = 0
		m.sessionFiles = msg.files
		// 会话 switch: memoized busy state 和 queued prompts
		// belong 到 previous 会话. Drop them 和 re-fetch
		// off-thread 所以 队列 pill 和 esc behavior track new
		// 会话 instead 的 stale one.
		m.invalidateBusyCaches()
		m.invalidatePromptQueue()
		m.promptQueue.Reset()
		m.promptQueueCheckedAt = time.Time{}
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.startLSPs(msg.lspFilePaths()))
		msgs := msg.messages
		if cmd := m.setSessionMessages(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.restoreModelFromSession(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.autoExpandPillsIfReasonable(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// If bang command was issued before 会话 finished
		// loading, start it now 那 chat list 是 stable.
		if m.pendingBangCommand != "" {
			cmds = append(cmds, m.runShellCommandInternal(m.pendingBangCommand, true))
			m.pendingBangCommand = ""
		}
		if hasInProgressTodo(m.session.Todos) {
			// 只 start spinner 如果 there 是 在-progress todo
			if m.isAgentBusy() {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			m.updateLayoutAndSize()
		}
		// Reload 提示词 history 为 new 会话.
		m.historyReset()
		cmds = append(cmds, m.loadPromptHistory())
		m.updateLayoutAndSize()

	case sessionFilesUpdatesMsg:
		m.sessionFiles = msg.sessionFiles
		var paths []string
		for _, f := range msg.sessionFiles {
			paths = append(paths, f.LatestVersion.Path)
		}
		cmds = append(cmds, m.startLSPs(paths))

	case modeSwitchedMsg:
		m.modeSwitching = false
		cmds = append(cmds, m.applyModeSwitch(msg)...)

	case sendMessageMsg:
		cmds = append(cmds, m.sendMessage(msg.Content, msg.Attachments...))

	case userCommandsLoadedMsg:
		m.customCommands = msg.Commands
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetCustomCommands(m.customCommands)
		}

	case mcpStateChangedMsg:
		if dia := m.dialog.Dialog(dialog.MCPTogglesID); dia != nil {
			if toggles, ok := dia.(*dialog.MCPToggles); ok {
				for name, info := range msg.states {
					toggles.SetItemStatus(name, mcpStatusText(info))
				}
			}
		}
		if channels, ok := m.dialog.Dialog(dialog.ChannelsID).(*dialog.Channels); ok {
			channels.SetStates(msg.states)
		}
		cmds = append(cmds, m.applyMCPStates(msg)...)
	case mcpStartingRetryMsg:
		// 一个 服务端 was still connecting 当 last fetch landed; re-probe
		// 所以 "starting..." converges without depending 在 state_changed
		// events (which can 是 missed 在 客户端/服务端 mode).
		if cmd := m.requestMCPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case skillsOverridesLoadedMsg:
		m.skillsDisabledOverrides = msg.disabled
		m.skillsEnabledOverrides = msg.enabled
		if dia := m.dialog.Dialog(dialog.SkillsTogglesID); dia != nil {
			if toggles, ok := dia.(*dialog.SkillToggles); ok {
				toggles.SetItems(m.skillsTogglesList())
			}
		}
	case mcpPromptsLoadedMsg:
		m.mcpPrompts = msg.Prompts
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetMCPPrompts(m.mcpPrompts)
		}

	case promptHistoryLoadedMsg:
		m.promptHistory.messages = msg.messages
		m.promptHistory.index = -1
		m.promptHistory.draft = ""

	case closeDialogMsg:
		m.dialog.CloseFrontDialog()

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.DeletedEvent {
			if m.session != nil && m.session.ID == msg.Payload.ID {
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			break
		}
		if m.session != nil && msg.Payload.ID == m.session.ID {
			prevHasInProgress := hasInProgressTodo(m.session.Todos)
			prevPillsHeight := m.pillsAreaHeight()
			m.updateHyperCredits()
			m.session = &msg.Payload
			if !prevHasInProgress && hasInProgressTodo(m.session.Todos) {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			// pills panel reserves vertical space 那 chat area
			// must yield. Recompute layout whenever 那 footprint
			// changes (todos appearing, list growing, etc.) 所以
			// box renders 在 first paint rather than waiting 为 toggle.
			// When footprint 是 unchanged we still re-render pill
			// content 所以 status changes (e.g. 在-progress spinner)
			// show up.
			if m.pillsAreaHeight() != prevPillsHeight {
				m.updateLayoutAndSize()
			} else {
				m.renderPills()
			}
			m.autoExpandPillsIfReasonable()
		}
	case pubsub.Event[message.Message]:
		// Check 如果 这 是 child 会话 消息 为 agent 工具.
		if m.session == nil {
			break
		}
		if msg.Payload.SessionID != m.session.ID {
			// 这 might 是 child 会话 消息 从 agent 工具.
			if cmd := m.handleChildSessionMessage(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			break
		}
		switch msg.Type {
		case pubsub.CreatedEvent:
			cmds = append(cmds, m.appendSessionMessage(msg.Payload))
			// 一个 new 消息 是 运行 boundary — 用户 提示词 starting
			// turn 或 agent replying/dequeueing. Drop
			// memoized busy state 和 re-fetch it 和 队列
			// off-thread. Per-chunk UpdatedEvents deliberately do NOT
			// trigger 这: during streaming 那 would put 工作区
			// probes 在 every token.
			m.invalidateBusyCaches()
			m.invalidatePromptQueue()
			if cmd := m.dispatchBusyRefresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		case pubsub.UpdatedEvent:
			cmds = append(cmds, m.updateSessionMessage(msg.Payload))
		case pubsub.DeletedEvent:
			m.chat.RemoveMessage(msg.Payload.ID)
		}
		// start spinner 如果 there 是 new 消息
		if hasInProgressTodo(m.session.Todos) && m.isAgentBusy() && !m.todoIsSpinning {
			m.todoIsSpinning = true
			cmds = append(cmds, m.todoSpinner.Tick)
		}
		// stop spinner 如果 agent 是 不 busy anymore
		if m.todoIsSpinning && !m.isAgentBusy() {
			m.todoIsSpinning = false
		}
		// there 是 number 的 things 那 could change pills here 所以 we want 到 re-render
		m.renderPills()
	case pubsub.Event[history.File]:
		cmds = append(cmds, m.handleFileEvent(msg.Payload))
	case pubsub.Event[app.LSPEvent]:
		// Refresh memoized LSP state off-thread: LSPGetStates 是
		// synchronous HTTP round-trip 在 客户端/服务端 mode 和 diagnostics
		// events can arrive per edited 文件.
		if cmd := m.requestLSPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[workspace.LSPEvent]:
		if cmd := m.requestLSPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[skills.Event]:
		m.skillStates = msg.Payload.States
	case pubsub.Event[agenttools.ToolProgress]:
		if m.session == nil {
			break
		}
		if msg.Payload.SessionID != "" && msg.Payload.SessionID != m.session.ID {
			break
		}
		if item := m.chat.MessageItem(msg.Payload.ToolCallID); item != nil && item.ID() == msg.Payload.ToolCallID {
			if live, ok := item.(chat.LiveOutputItem); ok && live.SetLiveOutput(msg.Payload.Output) {
				if m.chat.Follow() {
					m.chat.ScrollToBottom()
				}
			}
		}
	case pubsub.Event[mcp.Event]:
		switch msg.Payload.Type {
		case mcp.EventStateChanged:
			return m, tea.Batch(m.handleStateChanged()...)
		case mcp.EventPromptsListChanged:
			return m, handleMCPPromptsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventToolsListChanged:
			return m, handleMCPToolsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventResourcesListChanged:
			return m, handleMCPResourcesEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventChannelMessage:
			return m, m.handleChannelMessage(msg.Payload)
		}
	case pubsub.Event[permission.PermissionRequest]:
		// 服务端 re-sends pending 请求 whenever 客户端 reports
		// 它的 会话; keep dialog already showing it, 所以 它的 scroll,
		// diff mode, 和 selected button survive, 和 don't notify again.
		if d, ok := m.dialog.Dialog(dialog.PermissionsID).(*dialog.Permissions); ok && d.RequestID() == msg.Payload.ID {
			break
		}
		if cmd := m.openPermissionsDialog(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: fmt.Sprintf("Permission required to execute \"%s\"", msg.Payload.ToolName),
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[permission.PermissionNotification]:
		m.handlePermissionNotification(msg.Payload)
	case pubsub.Event[pinentry.Event]:
		cmds = append(cmds, m.handlePinentryEvent(msg.Payload))
	case pubsub.Event[pinentry.PromptRequest]:
		cmds = append(cmds, m.openPinentryDialog(msg.Payload))
	case pubsub.Event[pinentry.Notification]:
		m.handlePinentryNotification(msg.Payload)
	case pubsub.Event[question.Request]:
		// CRUSH_QUESTIONS_PIPED=1: another 客户端 ( editor Crush runs 在)
		// shows questions 和 answers them, 所以 这 UI doesn't ask twice.
		// Skip scroll 和 通知 too: 服务端 re-sends pending
		// batch whenever 客户端 reports 它的 会话, 所以 both would repeat.
		if piped, _ := strconv.ParseBool(os.Getenv("CRUSH_QUESTIONS_PIPED")); piped {
			break
		}
		if !m.openBatchFormDialog(msg.Payload) {
			break // a replay of the batch the form already shows
		}
		m.chat.ScrollToBottom()
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: fmt.Sprintf("%d questions need your input", len(msg.Payload.Questions)),
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[question.Notification]:
		m.handleQuestionNotification(msg.Payload)
	case pubsub.Event[sshaskpass.PromptRequest]:
		cmds = append(cmds, m.handleSSHPrompt(msg.Payload)...)
	case pubsub.Event[sshaskpass.Notification]:
		m.handleSSHNotification(msg.Payload)
	case cancelTimerExpiredMsg:
		m.isCanceling = false
	case tea.TerminalVersionMsg:
		termVersion := strings.ToLower(msg.Name)
		// Only enable progress bar 为 following terminals.
		if !m.sendProgressBar {
			m.sendProgressBar = xstrings.ContainsAnyOf(termVersion, "ghostty", "iterm2", "rio")
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Suppress chat's full-height scan during resize 所以 drag
		// 只 reflows visible items; it settles (和 recomputes) shortly
		// after last resize 事件.
		if m.state == uiChat {
			cmds = append(cmds, m.chat.BeginResize())
		}
		m.updateLayoutAndSize()
		if m.state == uiChat && m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
	case tea.KeyboardEnhancementsMsg:
		m.keyenh = msg
		if msg.SupportsKeyDisambiguation() {
			m.keyMap.Models.SetHelp("ctrl+m", "models")
			m.keyMap.Editor.Newline.SetHelp("shift+enter", "newline")
		}
	case copyChatHighlightMsg:
		cmds = append(cmds, m.copyChatHighlight())
	case DelayedClickMsg:
		// Handle delayed single-click action (e.g., expansion).
		m.chat.HandleDelayedClick(msg)
	case tea.MouseClickMsg:
		// Pass mouse events 到 dialogs first 如果 any 是 open.
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		// Route clicks 到 inline editors 那 support mouse interaction.
		if m.activeInline != nil {
			if selectable, ok := m.activeInline.(dialog.MouseSelectableEditor); ok &&
				selectable.HandleMouseDown(msg.X, msg.Y) {
				return m, tea.Batch(cmds...)
			}
			if clickable, ok := m.activeInline.(dialog.MouseClickableEditor); ok {
				if done, handled := clickable.HandleMouseClick(msg.X, msg.Y); handled {
					if done {
						prev := m.activeInline
						m.activeInline = nil
						m.textarea.Focus()
						m.updateLayoutAndSize()
						if cod, ok := prev.(dialog.CmdOnDone); ok {
							if c := cod.PendingCmd(); c != nil {
								cmds = append(cmds, c)
							}
						}
					}
					return m, tea.Batch(cmds...)
				}
			}
		}

		if cmd := m.handleClickFocus(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

		// Check 如果 click landed 在 附件's remove button.
		// 附件 chips 是 rendered 在 first 行 的
		// editor layout area, above the textarea.
		if m.activeInline == nil && msg.Button == uv.MouseLeft && len(m.attachments.List()) > 0 && msg.Y == m.layout.editor.Min.Y {
			relX := msg.X - m.layout.editor.Min.X
			if m.changeAttachments(func() bool { return m.attachments.HandleClick(relX) }) {
				return m, tea.Batch(cmds...)
			}
		}

		// Forward clicks within textarea region 到 textarea 所以 it
		// can position cursor 和 start selection.
		if m.activeInline == nil {
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust 为 chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if !image.Pt(msg.X, msg.Y).In(m.layout.sidebar) {
				if handled, cmd := m.chat.HandleMouseDown(x, y); handled {
					m.lastClickTime = time.Now()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}

	case tea.MouseMotionMsg:
		// Pass mouse events 到 dialogs first 如果 any 是 open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Track hover position 为 inline editors.
		if m.activeInline != nil {
			if selectable, ok := m.activeInline.(dialog.MouseSelectableEditor); ok &&
				selectable.HandleMouseDrag(msg.X, msg.Y) {
				return m, tea.Batch(cmds...)
			}
			if m.hoverX != msg.X || m.hoverY != msg.Y {
				m.hoverX = msg.X
				m.hoverY = msg.Y
				if clickable, ok := m.activeInline.(dialog.MouseClickableEditor); ok {
					clickable.SetHover(msg.X, msg.Y)
				}
			}
		}

		// While mouse selection 是 在 progress 在 textarea, forward
		// motion events 到 it 和 skip chat drag handling.
		if m.activeInline == nil && m.textareaMouseSelecting {
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			// Skip chat edge-scrolling 当 inline editor 是
			// 活跃 到 prevent accidental scrolling while hovering
			// over question forms 或 other inline components.
			if m.activeInline == nil || m.focus != uiFocusEditor {
				if msg.Y <= 0 {
					m.chat.ScrollBy(-1)
					if !m.chat.SelectedItemInView() {
						m.chat.SelectPrev()
						m.chat.ScrollToSelected()
					}
				} else if msg.Y >= m.chat.Height()-1 {
					m.chat.ScrollBy(1)
					if !m.chat.SelectedItemInView() {
						m.chat.SelectNext()
						m.chat.ScrollToSelected()
					}
				}
			}

			x, y := msg.X, msg.Y
			// Adjust 为 chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			m.chat.HandleMouseDrag(x, y)
		}

	case tea.MouseReleaseMsg:
		// Pass mouse events 到 dialogs first 如果 any 是 open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		if m.activeInline != nil {
			if selectable, ok := m.activeInline.(dialog.MouseSelectableEditor); ok {
				if handled, cmd := selectable.HandleMouseRelease(msg.X, msg.Y); handled {
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}
			}
		}

		// End any 在-progress textarea mouse selection.
		if m.activeInline == nil && m.textareaMouseSelecting {
			m.textareaMouseSelecting = false
			if handled, cmd := m.forwardMouseToTextarea(msg); handled {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust 为 chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if m.chat.HandleMouseUp(x, y) && m.chat.HasHighlight() {
				cmds = append(cmds, tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
					if time.Since(m.lastClickTime) >= doubleClickThreshold {
						return copyChatHighlightMsg{}
					}
					return nil
				}))
			}
		}
	case common.CoalescedWheelMsg:
		// Route wheel events 到 活跃 inline editor 只 当
		// mouse 是 over editor area, 所以 scrolling over chat
		// still scrolls the chat.
		if m.activeInline != nil && image.Pt(msg.Mouse.X, msg.Mouse.Y).In(m.layout.editor) {
			if we, ok := m.activeInline.(common.WheelScrollable); ok {
				we.HandleWheel(msg.DeltaX, msg.DeltaY)
				return m, tea.Batch(cmds...)
			}
		}

		// Pass mouse events 到 dialogs first 如果 any 是 open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Otherwise handle mouse wheel 为 chat. Use coalesced delta
		// directly 作为 line count. Terminals like Ghostty send DeltaY=3
		// per physical wheel tick (matching their native scrollback), while
		// others send DeltaY=1.
		switch m.state {
		case uiChat:
			// When sidebar 是 focused, route wheel events 到 sidebar scrolling.
			if m.focus == uiFocusSidebar {
				lines := int(msg.DeltaY)
				if lines != 0 {
					m.sidebarOffset = max(0, min(m.sidebarOffset+lines, m.sidebarMaxOffsetVal))
					m.sidebarScrollbarSeq++
					m.sidebarScrollbarVisible = true
					cmds = append(cmds, sidebarScrollbarHideCmd(m.sidebarScrollbarSeq))
				}
				break
			}
			if msg.DeltaX != 0 {
				m.chat.ScrollSelectedShellHorizontal(int(msg.DeltaX))
			}
			lines := int(msg.DeltaY)
			if lines == 0 {
				break
			}
			m.markScrollOnly()
			m.applyChatScroll(lines)
		}
	case frameGCMsg:
		m.handleFrameGC()
	case animTickMsg:
		m.handleAnimTick(msg)
	case scrollbarHideMsg:
		if m.state == uiChat {
			m.chat.HideScrollbar(msg.seq)
		}
	case chatWarmMsg:
		// 一个 resize has settled; warm 消息 缓存 one batch 在 time
		// 所以 scrollbar recompute never blocks UI thread.
		if m.state == uiChat {
			cmd, done := m.chat.WarmStep(msg.seq)
			if cmd != nil {
				cmds = append(cmds, cmd)
			} else if done {
				// Heights 是 cached now, 所以 final layout pass (scrollbar
				// 占位) 是 cheap.
				m.updateLayoutAndSize()
			}
		}
	case sidebarScrollbarHideMsg:
		if msg.seq == m.sidebarScrollbarSeq && m.focus != uiFocusSidebar {
			m.sidebarScrollbarVisible = false
		}
	case spinner.TickMsg:
		if m.dialog.HasDialogs() {
			// route 到 dialog
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.state == uiChat && m.hasSession() && hasInProgressTodo(m.session.Todos) && m.todoIsSpinning {
			var cmd tea.Cmd
			m.todoSpinner, cmd = m.todoSpinner.Update(msg)
			if cmd != nil {
				m.renderPills()
				cmds = append(cmds, cmd)
			}
		}

	case tea.KeyPressMsg:
		if cmd := m.handleKeyPressMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		if m.activeInline != nil && m.focus == uiFocusEditor {
			if p, ok := m.activeInline.(dialog.PasteableEditor); ok {
				if cmd := p.HandlePaste(msg); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}
		}
		if cmd := m.handlePasteMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case openEditorMsg:
		prevHeight := m.textarea.Height()
		m.textarea.SetValue(msg.Text)
		m.textarea.MoveToEnd()
		m.syncBangModeFromTextarea()
		cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
	case shellStreamMsg:
		if item := m.chat.MessageItem(msg.PendingID); item != nil {
			if shellItem, ok := item.(*chat.ShellItem); ok {
				shellItem.AppendOutput(msg.Chunk)
				m.chat.ScrollToBottom()
			}
		}
		// Continue draining 流 渠道.
		if msg.streamCh != nil {
			ch := msg.streamCh
			pid := msg.PendingID
			cmds = append(cmds, func() tea.Msg {
				chunk, ok := <-ch
				if !ok {
					return nil
				}
				return shellStreamMsg{PendingID: pid, Chunk: chunk, streamCh: ch}
			})
		}
	case shellResultMsg:
		// Clear bang 取消 func — command 是 done.
		if m.bangCancel != nil {
			m.bangCancel()
			m.bangCancel = nil
		}
		// 完成 pending shell item 如果 it exists, otherwise 创建 new one.
		completed := false
		if msg.PendingID != "" {
			if item := m.chat.MessageItem(msg.PendingID); item != nil {
				if shellItem, ok := item.(*chat.ShellItem); ok {
					shellItem.Complete(msg.Output, msg.ExitCode)
					m.chat.ScrollToBottom()
					completed = true
				}
			}
		}
		if !completed {
			item := chat.NewShellItem(m.com.Styles, msg.Command, msg.Output, msg.ExitCode)
			m.chat.AppendMessages(item)
			m.chat.ScrollToBottom()
		}
		cmds = append(cmds, m.loadPromptHistory())
	case hyperRefreshDoneMsg:
		if cmd := m.handleSelectModel(msg.action); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case creditsUpdatedMsg:
		m.hyperCredits = msg.credits
	case hyperCreditsPollMsg:
		// While 会话 runs every 响应 refreshes balance, 所以
		// poll 只 has 到 cover 空闲 time. Re-arm it either way:
		// next poll may well land after agent went 空闲 again.
		if m.com.IsHyper() && !m.isAgentBusy() {
			cmds = append(cmds, m.fetchHyperCredits())
		}
		cmds = append(cmds, m.hyperCreditsTicker())
	case gitBranchUpdatedMsg:
		m.gitBranch = msg.branch
	case nerdFontsMsg:
		m.nerdFonts = bool(msg)
	case gitBranchPollMsg:
		cmds = append(cmds, m.fetchGitBranch(), m.gitBranchTicker())
	case util.InfoMsg:
		if msg.Type == util.InfoTypeError {
			slog.Error("Error reported", "error", msg.Msg)
		}
		cmds = append(cmds, m.status.Show(msg))
	case app.UpdateAvailableMsg:
		text := fmt.Sprintf("Crush update available: v%s → v%s.", msg.CurrentVersion, msg.LatestVersion)
		if msg.IsDevelopment {
			text = fmt.Sprintf("This is a development version of Crush. The latest version is v%s.", msg.LatestVersion)
		}
		cmds = append(cmds, m.status.Show(util.InfoMsg{
			Type: util.InfoTypeUpdate,
			Msg:  text,
			TTL:  10 * time.Second,
		}))
	case workspace.ConnectionEvent:
		cmds = append(cmds, m.handleConnectionEvent(msg)...)
	case workspace.HostThemeEvent:
		if cmd := m.handleHostThemeEvent(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case hostThemeStylesMsg:
		if cmd := m.handleHostThemeStyles(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case transparencyToggledMsg:
		// 用户's choice wins over host theme's override.
		m.isTransparent = msg.transparent
		m.hostTheme.transparent = false
		status := "disabled"
		if msg.transparent {
			status = "enabled"
		}
		cmds = append(cmds, util.ReportInfo("Transparent background "+status))
	case util.ClearStatusMsg:
		m.status.ClearInfoMsg(msg.Seq)
	case completions.CompletionItemsLoadedMsg:
		if m.completionsOpen {
			m.completions.SetItems(msg.Files, msg.Resources)
		}
	case uv.KittyGraphicsEvent:
		if !bytes.HasPrefix(msg.Payload, []byte("OK")) {
			slog.Warn("Unexpected Kitty graphics response",
				"response", string(msg.Payload),
				"options", msg.Options)
		}
	case dialog.ActionMCPAuthStarted:
		cmds = append(cmds, m.authenticateMCP(msg.Ctx, msg.Name))
	case dialog.ActionMCPAuthComplete, dialog.ActionMCPAuthErrored:
		// OAuth flow finished 服务端-side: refresh memoized states
		// 所以 sidebar reflects outcome even 当 state_changed
		// 事件 was missed.
		if cmd := m.requestMCPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	default:
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// 这 logic gets triggered 在 any 消息 type, 但 should it?
	prevPlaceholder := m.textarea.Placeholder
	switch m.focus {
	case uiFocusMain:
	case uiFocusEditor:
		// Textarea placeholder logic
		if m.bangMode {
			m.textarea.Placeholder = "Run a shell command"
		} else if m.isAgentBusy() {
			m.textarea.Placeholder = m.workingPlaceholder
		} else if m.mode == uiInputModePlan {
			m.textarea.Placeholder = "Let's plan"
		} else {
			m.textarea.Placeholder = m.readyPlaceholder
		}
		if !m.bangMode && m.mode != uiInputModePlan {
			switch m.permModeCached() {
			case permission.PermissionModeYolo:
				m.textarea.Placeholder = "Go crazy"
			case permission.PermissionModeSysadmin:
				m.textarea.Placeholder = "Sysadmin mode!"
			}
		}
	}
	if m.textarea.Placeholder != prevPlaceholder {
		m.invalidateFrames()
	}

	// TTL backstop: schedule off-thread re-probe 为 any memoized
	// 工作区 state 那 has gone stale. Never does IO 在 这
	// goroutine.
	cmds = append(cmds, m.staleWorkspaceRefreshCmds()...)

	// 在 这 point 这 can 只 handle [消息.附件] 消息, 和 we
	// 应当 return all cmds anyway.
	if _, isAttachment := msg.(message.Attachment); isAttachment && m.attachmentsLimitReached() {
		max := m.currentModelMaxAttachments()
		cmds = append(cmds, util.ReportWarn(fmt.Sprintf("Maximum attachment reached for this session (%d)", max)))
	} else if m.changeAttachments(func() bool { return m.attachments.Update(msg) }) {
		m.invalidateFrames()
	}
	// Any 更新 may have put spinner 在 screen (new 消息, 工具 更新,
	// scroll, 会话 load); make sure clock 是 running. 这 是
	// sole place clock 是 armed 所以 tick never sits inside caller's
	// tea.序号.
	m.subAgentsRunning = m.runningSubAgents()
	if m.state == uiChat {
		if cmd := m.ensureAnimating(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Same 为 chat scrollbar's auto-hide: scroll may 是 applied
		// 从 anywhere, 所以 期限 是 armed here instead 的 being
		// handed back 到 each 调用 site 到 forward.
		if cmd := m.chat.EnsureScrollbarHide(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if cmd := m.endFrameUpdate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// ensureAnimating arms shared clock while visible transcript item 是
// spinning 或 子代理 是 running, whose spinners live 在 chrome.
func (m *UI) ensureAnimating() tea.Cmd {
	return m.animClock.ensure(func() bool {
		return m.subAgentsRunning > 0 || m.chat.hasVisibleAnimation()
	})
}

// handleAnimTick advances every visible spinner 通过 one frame. 一个 tick 那
// changed nothing visible 是 scroll-只 所以 frame 缓存 survives; one
// 那 did keeps view pinned 到 bottom while following, since
// animated items can change height. 更新 tail re-arms clock.
func (m *UI) handleAnimTick(msg animTickMsg) {
	if !m.animClock.accept(msg) || m.state != uiChat {
		m.markScrollOnly()
		return
	}
	if !m.chat.advanceVisible() && m.subAgentsRunning == 0 {
		// Nothing moved 在 screen, 所以 frame can 是 reused. 它 cannot 是
		// 当 header 是 spinning: 那 spinner 是 outside list 和
		// reusing the frame would freeze it.
		m.markScrollOnly()
		return
	}
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
}

// setSessionMessages sets messages 为 current 会话 在 chat
func (m *UI) setSessionMessages(msgs []message.Message) tea.Cmd {
	var cmds []tea.Cmd
	// Build 工具结果 map 到 link 工具 calls 用 their results
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	if len(msgPtrs) > 0 {
		m.lastUserMessageTime = msgPtrs[0].CreatedAt
	}

	// Add messages 到 chat 用 linked 工具 results
	items := make([]chat.MessageItem, 0, len(msgs)*2)
	for _, msg := range msgPtrs {
		switch msg.Role {
		case message.User:
			m.lastUserMessageTime = msg.CreatedAt
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
		case message.Assistant:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
			if chat.ShouldShowAssistantInfo(msg) {
				infoItem := chat.NewAssistantInfoItem(m.com.Styles, msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
				items = append(items, infoItem)
			}
		default:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.com.Workspace.WorkingDir())...)
		}
	}

	// Load nested 工具 calls 为 agent/agentic_fetch tools.
	m.loadNestedToolCalls(items)
	m.setMessagePlanFlags(items)
	replayDispatchReports(items, msgPtrs, func(messageID, toolCallID string) bool {
		return m.com.Workspace.AgentIsSessionBusy(m.com.Workspace.CreateAgentToolSessionID(messageID, toolCallID))
	})

	// If 用户 switches between sessions while agent 是 working we
	// want 到 make sure animations 是 shown. Gate 在 agent actually
	// being busy: 会话 那 was killed mid-generation can persist
	// 助手消息 用 no Finish 片段, which still reports Spinning()
	// even though nothing 是 running. Allowing clock 为 it here would
	// leave ghost "working" spinner (和 second one alongside any 工具
	// spinner) after 会话 是 reloaded. Messages arriving 为
	// 会话 re-enable clock.
	m.animClock.frozen = !m.isAgentBusy()

	if cmd := m.chat.SetMessages(items...); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// SetMessages rebuilds items 从 存储 messages, which cannot
	// include placeholder. Put it back 当 这 会话 是 still
	// waiting 在 one, 所以 switching away 和 back does 不 lose
	// spinner 为 rest 的 turn.
	if m.syncPendingItem() {
		m.animClock.frozen = false
	}
	m.chat.SelectLast()
	return tea.Sequence(cmds...)
}

// replayDispatchReports settles 派发 items whose 子代理 是 no longer
// working.
//
// 一个 dispatched 子代理 outlives 它的 own 工具结果, 所以 报告 landing
// later 是 只 sign it finished. On live 运行 那 报告 arrives 作为
// 事件, 但 会话 读取 back 从 storage has 到 recover it 从
// transcript: without 这 every 子代理 那 finished long ago would look
// like it 是 still working, 在 transcript, sidebar, header count
// 和 warning shown 当 quitting.
//
// Only latest 派发 under label can still 是 working, 和 只 如果
// it has 不 reported since. Even then it may have been cut off 当 Crush
// last exited, 所以 busy asks backend whether 它的 会话 是 live.
func replayDispatchReports(items []chat.MessageItem, msgs []*message.Message, busy func(messageID, toolCallID string) bool) {
	latest := agent.LatestDispatches(msgs)
	forEachToolItem(items, func(item chat.ToolMessageItem) {
		dispatch, ok := item.(*chat.AgentToolMessageItem)
		if !ok || dispatch.DispatchLabel() == "" {
			return
		}
		state, ok := latest[dispatch.DispatchLabel()]
		callID := dispatch.ToolCall().ID
		if ok && state.CallID == callID && state.Open && busy(dispatch.MessageID(), callID) {
			return
		}
		dispatch.MarkDispatchReported()
	})
}

// forEachToolItem visits every 工具 item 在 items, including those nested
// inside another 工具.
func forEachToolItem(items []chat.MessageItem, visit func(chat.ToolMessageItem)) {
	for _, item := range items {
		tool, ok := item.(chat.ToolMessageItem)
		if !ok {
			continue
		}
		visit(tool)
		if container, ok := item.(chat.NestedToolContainer); ok {
			nested := make([]chat.MessageItem, 0, len(container.NestedTools()))
			for _, n := range container.NestedTools() {
				nested = append(nested, n)
			}
			forEachToolItem(nested, visit)
		}
	}
}

// handleConnectionEvent reports health 的 客户端-服务端 link 和,
// once it recovers, reloads open 会话. 一个 reload 是 always needed
// after degraded episode: events published while 流 was down 是
// gone, 和 如果 工作区 itself was re-created any 运行 died 用 it.
func (m *UI) handleConnectionEvent(msg workspace.ConnectionEvent) []tea.Cmd {
	info := util.InfoMsg{
		Type: util.InfoTypeWarn,
		Msg:  "Lost connection to the Crush server — reconnecting…",
		TTL:  30 * time.Second,
	}
	switch msg.State {
	case workspace.ConnectionDegraded:
		slog.Warn("Server connection degraded", "error", msg.Err, "stuck", msg.Stuck)
		if msg.Stuck {
			info.Type = util.InfoTypeError
			info.Msg = "Can't restore the connection to the Crush server. Restart Crush to recover."
			info.TTL = time.Minute
		}
	case workspace.ConnectionRecovered:
		info = util.InfoMsg{
			Type: util.InfoTypeSuccess,
			Msg:  "Reconnected to the Crush server.",
			TTL:  DefaultStatusTTL,
		}
	}
	cmds := []tea.Cmd{m.status.Show(info)}
	if msg.State == workspace.ConnectionRecovered {
		// Events published while 流 was down 是 gone: re-sync
		// memoized MCP states alongside 会话 reload.
		if cmd := m.requestMCPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.session != nil {
			cmds = append(cmds, m.loadSession(m.session.ID))
		}
	}
	return cmds
}

// loadNestedToolCalls recursively loads nested 工具 calls 为 agent/agentic_fetch tools.
func (m *UI) loadNestedToolCalls(items []chat.MessageItem) {
	for _, item := range items {
		nestedContainer, ok := item.(chat.NestedToolContainer)
		if !ok {
			continue
		}
		toolItem, ok := item.(chat.ToolMessageItem)
		if !ok {
			continue
		}

		tc := toolItem.ToolCall()
		messageID := toolItem.MessageID()

		// Get agent 工具 会话 ID.
		agentSessionID := m.com.Workspace.CreateAgentToolSessionID(messageID, tc.ID)

		// Fetch nested messages.
		nestedMsgs, err := m.com.Workspace.ListMessages(context.Background(), agentSessionID)
		if err != nil || len(nestedMsgs) == 0 {
			continue
		}

		// Build 工具结果 map 为 nested messages.
		nestedMsgPtrs := make([]*message.Message, len(nestedMsgs))
		for i := range nestedMsgs {
			nestedMsgPtrs[i] = &nestedMsgs[i]
		}
		nestedToolResultMap := chat.BuildToolResultMap(nestedMsgPtrs)

		// Extract nested 工具 items.
		var nestedTools []chat.ToolMessageItem
		for _, nestedMsg := range nestedMsgPtrs {
			nestedItems := chat.ExtractMessageItems(m.com.Styles, nestedMsg, nestedToolResultMap, m.com.Workspace.WorkingDir())
			for _, nestedItem := range nestedItems {
				if nestedToolItem, ok := nestedItem.(chat.ToolMessageItem); ok {
					// 标记 nested tools 作为 simple (compact) rendering.
					if simplifiable, ok := nestedToolItem.(chat.Compactable); ok {
						simplifiable.SetCompact(true)
					}
					nestedTools = append(nestedTools, nestedToolItem)
				}
			}
		}

		// Recursively load nested 工具 calls 为 any agent tools within.
		nestedMessageItems := make([]chat.MessageItem, len(nestedTools))
		for i, nt := range nestedTools {
			nestedMessageItems[i] = nt
		}
		m.loadNestedToolCalls(nestedMessageItems)

		// Set nested tools 在 parent.
		nestedContainer.SetNestedTools(nestedTools)
	}
}

// setMessagePlanFlags marks 助手消息 items 作为 plan-agent output
// while UI 是 在 plan mode, 所以 their streaming content renders 作为
// open plan card. Finished non-plan messages ignore flag, 所以 reloading
// old 会话 在 plan mode never grows spurious cards.
func (m *UI) setMessagePlanFlags(items []chat.MessageItem) {
	if m.mode != uiInputModePlan {
		return
	}
	for _, item := range items {
		if a, ok := item.(*chat.AssistantMessageItem); ok {
			a.SetPlanAgent(true)
		}
	}
}

// appendSessionMessage appends new 消息 到 current 会话 在 chat
// 如果 消息 是 工具结果 it will 更新 corresponding 工具调用 消息
func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd {
	var cmds []tea.Cmd

	// real 消息 carries 它的 own spinner 从 here 在.
	if msg.Role == message.Assistant && m.pending.sessionID == msg.SessionID {
		m.handOffPendingTurn()
	}
	defer m.syncPendingItem()

	existing := m.chat.MessageItem(msg.ID)
	if existing != nil {
		// 消息 already exists, skip
		return nil
	}

	switch msg.Role {
	case message.User:
		if m.takeLiveBangRun(msg) {
			return nil
		}
		// 一个 子代理 报告 arrives 在 用户-角色 消息 但 是 不
		// 用户 speaking, 所以 it does 不 move last-用户 标记. 它
		// does end 派发 item's spinner, since 报告 landing
		// 是 只 signal 那 子代理 finished.
		if reports := msg.SubAgentReports(); len(reports) > 0 {
			for _, report := range reports {
				m.markDispatchReported(report.Label)
			}
		} else {
			m.lastUserMessageTime = msg.CreatedAt
		}
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil, m.com.Workspace.WorkingDir())
		m.chat.AppendMessages(items...)
		m.chat.ScrollToBottom()
	case message.Assistant:
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil, m.com.Workspace.WorkingDir())
		m.setMessagePlanFlags(items)
		m.chat.AppendMessages(items...)
		if m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
		if chat.ShouldShowAssistantInfo(&msg) {
			infoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(infoItem)
			if m.chat.Follow() {
				m.chat.ScrollToBottom()
			}
		}
	case message.Tool:
		for _, tr := range msg.ToolResults() {
			toolItem := m.chat.MessageItem(tr.ToolCallID)
			if toolItem == nil {
				// we 应当 have item!
				continue
			}
			if toolMsgItem, ok := toolItem.(chat.ToolMessageItem); ok {
				toolMsgItem.SetResult(&tr)
				// Reopening finished 子代理 puts it back 到 work, 所以
				// 它的 派发 item has 到 start spinning again.
				if tr.Name == agent.AgentSendToolName && tr.Metadata != "" {
					var meta agent.AgentSendResponseMetadata
					if json.Unmarshal([]byte(tr.Metadata), &meta) == nil && meta.Reopened {
						m.markDispatchRunning(meta.Label)
					}
				}
				if m.chat.Follow() {
					m.chat.ScrollToBottom()
				}
			}
		}
	}
	return tea.Sequence(cmds...)
}

// takeLiveBangRun reports whether msg 是 存储 copy 的 bang command
// 这 UI ran, 和 所以 already shows live, 和 如果 所以 stops waiting 为 it.
func (m *UI) takeLiveBangRun(msg message.Message) bool {
	cmds := msg.ShellCommands()
	if len(cmds) == 0 {
		return false
	}
	run := bangRun{msg.SessionID, cmds[0].Command}
	if m.liveBangRuns[run] == 0 {
		return false
	}
	m.liveBangRuns[run]--
	return true
}

func (m *UI) handleClickFocus(msg tea.MouseClickMsg) (cmd tea.Cmd) {
	switch {
	case m.state != uiChat:
		return nil
	case m.focus != uiFocusSidebar && image.Pt(msg.X, msg.Y).In(m.layout.sidebar) && m.sidebarScrollable:
		m.focus = uiFocusSidebar
		m.textarea.Blur()
		m.chat.Blur()
		return nil
	case m.focus != uiFocusEditor && image.Pt(msg.X, msg.Y).In(m.layout.editor):
		if m.activeInline != nil {
			m.focusActiveInline(uiFocusEditor)
		} else {
			m.focus = uiFocusEditor
			cmd = m.textarea.Focus()
			m.chat.Blur()
		}
		m.sidebarScrollbarVisible = false
	case m.focus != uiFocusMain && image.Pt(msg.X, msg.Y).In(m.layout.main):
		if m.activeInline != nil {
			m.focusActiveInline(uiFocusMain)
		} else {
			m.focus = uiFocusMain
			m.textarea.Blur()
			m.chat.Focus()
		}
		m.sidebarScrollbarVisible = false
	}
	return cmd
}

// focusActiveInline moves focus between inline editor 和 chat while
// preserving editor 和 reconciling any collapsed layout.
func (m *UI) focusActiveInline(focus uiFocusState) {
	if m.activeInline == nil {
		return
	}

	m.focus = focus
	m.textarea.Blur()
	switch focus {
	case uiFocusEditor:
		m.activeInline.SetFocused(true)
		if m.chat != nil {
			m.chat.Blur()
		}
	case uiFocusMain:
		m.activeInline.SetFocused(false)
		if m.chat != nil {
			m.chat.Focus()
			m.chat.SetSelected(m.chat.Len() - 1)
		}
	}

	if m.status != nil && m.chat != nil {
		m.updateLayoutAndSize()
	}
}

// updateSessionMessage updates existing 消息 在 current 会话 在
// chat 当 助手消息 是 updated it may include updated 工具
// calls 作为 well 那 是 why we need 到 handle creating/updating each 工具调用
// 消息 too.
func (m *UI) updateSessionMessage(msg message.Message) tea.Cmd {
	// 一个 消息 更新 means work 是 活跃; animation clock may have
	// been frozen 通过 non-busy 会话 reload (ghost-spinner guard).
	m.animClock.frozen = false
	var cmds []tea.Cmd
	existingItem := m.chat.MessageItem(msg.ID)

	if existingItem != nil {
		if assistantItem, ok := existingItem.(*chat.AssistantMessageItem); ok {
			assistantItem.SetMessage(&msg)
			assistantItem.SetPlanAgent(m.mode == uiInputModePlan)
		}
	}

	shouldRenderAssistant := chat.ShouldRenderAssistantMessage(&msg)
	// If 消息 的 助手 does 不 have any 响应 just 工具
	// calls we need 到 remove it, 但 keep info item per finished turn
	// renders 所以 footer (模型/提供方/duration) remains visible.
	if !shouldRenderAssistant && len(msg.ToolCalls()) > 0 && existingItem != nil {
		m.chat.RemoveMessage(msg.ID)
	}

	// info item shows 为 every turn 用 Prism-routed 模型, 和
	// 为 final turn 的 提示词. 它 是 removed again 当
	// turn no longer qualifies (e.g. 重试 reset 流).
	if infoItem := m.chat.MessageItem(chat.AssistantInfoID(msg.ID)); chat.ShouldShowAssistantInfo(&msg) {
		if infoItem == nil {
			newInfoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(newInfoItem)
		}
	} else if infoItem != nil {
		m.chat.RemoveMessage(chat.AssistantInfoID(msg.ID))
	}

	m.chat.RemoveStaleToolCalls(msg.ID, msg.ToolCalls())

	var items []chat.MessageItem
	for _, tc := range msg.ToolCalls() {
		existingToolItem := m.chat.MessageItem(tc.ID)
		if toolItem, ok := existingToolItem.(chat.ToolMessageItem); ok {
			existingToolCall := toolItem.ToolCall()
			// 只 更新 如果 finished state changed 或 input changed
			// 到 avoid clearing 缓存
			if (tc.Finished && !existingToolCall.Finished) || tc.Input != existingToolCall.Input {
				toolItem.SetToolCall(tc)
			}
		}
		if existingToolItem == nil {
			items = append(items, chat.NewToolMessageItem(m.com.Styles, msg.ID, tc, nil, false, m.com.Workspace.WorkingDir()))
		}
	}

	m.chat.AppendMessages(items...)
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
		m.chat.SelectLast()
	}

	return tea.Sequence(cmds...)
}

// handleChildSessionMessage handles messages 从 child sessions (agent tools).
func (m *UI) handleChildSessionMessage(event pubsub.Event[message.Message]) tea.Cmd {
	var cmds []tea.Cmd

	// Only process messages 用 工具 calls 或 results.
	if len(event.Payload.ToolCalls()) == 0 && len(event.Payload.ToolResults()) == 0 {
		return nil
	}

	// Check 如果 这 是 agent 工具 会话 和 parse it.
	childSessionID := event.Payload.SessionID
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
	if !ok {
		return nil
	}
	// Nested 工具 activity means agent 是 running; animation clock
	// may have been frozen 通过 non-busy 会话 reload.
	m.animClock.frozen = false

	// Find parent agent 工具 item. 一个 nested 工具's ID also maps 到 它的
	// parent, 所以 ID check keeps 这 到 item 那 owns 会话.
	item := m.chat.MessageItem(toolCallID)
	agentItem, ok := item.(chat.NestedToolContainer)
	if tool, isTool := item.(chat.ToolMessageItem); !ok || !isTool || tool.ToolCall().ID != toolCallID {
		return nil
	}

	// Get existing nested tools.
	nestedTools := agentItem.NestedTools()

	// 更新 或 创建 nested 工具 calls.
	for _, tc := range event.Payload.ToolCalls() {
		found := false
		for _, existingTool := range nestedTools {
			if existingTool.ToolCall().ID == tc.ID {
				existingTool.SetToolCall(tc)
				found = true
				break
			}
		}
		if !found {
			// 创建 new nested 工具 item.
			nestedItem := chat.NewToolMessageItem(m.com.Styles, event.Payload.ID, tc, nil, false, m.com.Workspace.WorkingDir())
			if simplifiable, ok := nestedItem.(chat.Compactable); ok {
				simplifiable.SetCompact(true)
			}
			nestedTools = append(nestedTools, nestedItem)
		}
	}

	// 更新 nested 工具 results.
	for _, tr := range event.Payload.ToolResults() {
		for _, nestedTool := range nestedTools {
			if nestedTool.ToolCall().ID == tr.ToolCallID {
				nestedTool.SetResult(&tr)
				break
			}
		}
	}

	// 更新 agent item 用 new nested tools.
	agentItem.SetNestedTools(nestedTools)

	// 更新 chat 所以 it updates index map 为 animations 到 work 作为 expected
	m.chat.UpdateNestedToolIDs(toolCallID)

	if m.chat.Follow() {
		m.chat.ScrollToBottom()
		m.chat.SelectLast()
	}

	return tea.Sequence(cmds...)
}

func (m *UI) handleDialogMsg(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	action := m.dialog.Update(msg)
	if action == nil {
		return tea.Batch(cmds...)
	}

	isOnboarding := m.state == uiOnboarding

	switch msg := action.(type) {
	// Generic dialog messages
	case dialog.ActionClose:
		if isOnboarding && m.dialog.ContainsDialog(dialog.ModelsID) {
			break
		}

		if m.dialog.ContainsDialog(dialog.FilePickerID) {
			defer fimage.ResetCache()
		}

		m.dialog.CloseFrontDialog()

		if isOnboarding {
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if m.focus == uiFocusEditor {
			cmds = append(cmds, m.textarea.Focus())
		}
	case dialog.ActionCloseOAuth:
		// Same 作为 ActionClose, 但 用 清理 command 那 cancels
		// 在-flight authorization before dialog goes away.
		m.dialog.CloseFrontDialog()

		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}

		if isOnboarding {
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if m.focus == uiFocusEditor {
			cmds = append(cmds, m.textarea.Focus())
		}
	case dialog.ActionSelectAuthMethod:
		m.dialog.CloseDialog(dialog.AuthMethodID)
		if cmd := m.openAuthenticationDialogWithMethod(msg.Provider, msg.Model, msg.ModelType, msg.UseOAuth); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionCmd:
		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}

	// 会话 dialog messages.
	case dialog.ActionSelectSession:
		m.dialog.CloseDialog(dialog.SessionsID)
		cmds = append(cmds, m.loadSession(msg.Session.ID))

	// Open dialog 消息.
	case dialog.ActionOpenDialog:
		m.dialog.CloseDialog(dialog.CommandsID)
		if cmd := m.openDialog(msg.DialogID); cmd != nil {
			cmds = append(cmds, cmd)
		}

	// Command dialog messages.
	case dialog.ActionToggleYoloMode:
		if m.mode == uiInputModePlan {
			// Same 作为 Ctrl+Y 在 plan mode: YOLO 只 exists 作为 YOLO
			// coding, 所以 activating it leaves plan mode.
			if cmd := m.switchPlanToYolo(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else {
			cmds = append(cmds, m.toggleModeAndReport(permission.PermissionModeYolo))
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleSysadminMode:
		cmds = append(cmds, m.toggleModeAndReport(permission.PermissionModeSysadmin))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSelectNotificationStyle:
		cfg := m.com.Config()
		if cfg != nil && cfg.Options != nil {
			cfg.Options.Notifications = msg.Style
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.notifications", msg.Style); err != nil {
				cmds = append(cmds, util.ReportError(err))
			} else {
				cmds = append(cmds, util.CmdHandler(util.NewInfoMsg("Notifications set to: "+msg.Style)))
			}
			// Reinitialize 通知 backend 用 new style.
			m.notifyBackend = selectNotificationBackend(m.caps, cfg)
		}
		m.dialog.CloseDialog(dialog.NotificationsID)
	case dialog.ActionNewSession:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
			break
		}
		if cmd := m.newSession(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSummarize:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, func() tea.Msg {
			err := m.com.Workspace.AgentSummarize(context.Background(), msg.SessionID)
			if err != nil {
				return util.ReportError(err)()
			}
			return nil
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleHelp:
		m.status.ToggleHelp()
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionExternalEditor:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
			break
		}
		editorValue := m.textarea.Value()
		if m.bangMode {
			editorValue = "!" + editorValue
		}
		cmds = append(cmds, m.openEditor(editorValue))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleCompactMode:
		cmds = append(cmds, m.toggleCompactMode())
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionTogglePills:
		if cmd := m.togglePillsExpanded(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleThinking:
		cmds = append(cmds, m.updateCoderModel(func(model *config.SelectedModel) string {
			model.Think = !model.Think
			if model.Think {
				return "Thinking mode enabled"
			}
			return "Thinking mode disabled"
		}))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleTransparentBackground:
		// Flip what 是 在 screen, which host theme may have overridden,
		// rather than 配置 值.
		newValue := !m.isTransparent
		ws := m.com.Workspace
		cmds = append(cmds, func() tea.Msg {
			if ws.Config() == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}
			if err := ws.SetConfigField(config.ScopeGlobal, "options.tui.transparent", newValue); err != nil {
				return util.ReportError(err)()
			}
			return transparencyToggledMsg{transparent: newValue}
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSwitchTheme:
		themeName := msg.Theme
		newStyles, err := styles.LoadTheme(themeName)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.active_theme", themeName); err != nil {
			if m.preThemeStyles != nil {
				m.applyTheme(*m.preThemeStyles)
				m.preThemeStyles = nil
			}
			cmds = append(cmds, util.ReportError(err))
			break
		}
		m.applyTheme(newStyles)
		m.preThemeStyles = nil
		m.userAppliedTheme()
		cmds = append(cmds, util.ReportInfo("Theme switched to "+themeName))
		m.userThemeSelected = true
		m.dialog.CloseDialog(dialog.ThemeID)
	case dialog.ActionPreviewTheme:
		if newStyles, err := styles.LoadTheme(msg.Theme); err == nil {
			m.previewTheme(newStyles)
		}
	case dialog.ActionRevertThemePreview:
		if m.preThemeStyles != nil {
			m.applyTheme(*m.preThemeStyles)
			m.preThemeStyles = nil
		}
		m.dialog.CloseDialog(dialog.ThemeID)
	case dialog.ActionPreviewThemePalette:
		if newStyles, err := styles.LoadPaletteTheme(msg.Base, msg.Palette); err == nil {
			m.previewTheme(newStyles)
		}
	case dialog.ActionSaveThemePalette:
		// theme 是 存储 under 它的 own name; Base 只 identifies
		// built-在 palette 它的 colors 是 derived 从.
		themeName := msg.Name
		if themeName == "" {
			themeName = msg.Base
		}

		// 写入 文件 before touching UI 所以 failed save never
		// leaves colors applied 那 did 不 reach disk.
		savePath, err := styles.ThemePath(themeName)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		tf := &styles.ThemeFile{Base: msg.Base, Palette: msg.Palette}
		if err := styles.SaveThemeFile(savePath, tf); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}

		// Only take over whole UI 当 saved theme 是 活跃
		// one (或 implicit 默认). Otherwise preview backup
		// stays intact 所以 esc restores 用户's real theme instead 的
		// leaving the edited colors applied until restart.
		activeTheme := common.ThemeNameFromConfig(m.com.Config())
		isActive := strings.EqualFold(activeTheme, themeName)
		if activeTheme == "" {
			isActive = strings.EqualFold(themeName, "charmtone-panther")
		}
		if isActive {
			newStyles, err := styles.LoadTheme(themeName)
			if err != nil {
				cmds = append(cmds, util.ReportError(err))
				break
			}
			m.applyTheme(newStyles)
			m.preThemeStyles = nil
			m.userAppliedTheme()
		}
		cmds = append(cmds, util.ReportInfo("Theme saved"))
		m.dialog.CloseDialog(dialog.ThemeEditorID)
		if td, ok := m.dialog.Dialog(dialog.ThemeID).(*dialog.Theme); ok {
			td.RefreshThemes(themeName)
		}
	case dialog.ActionEditTheme:
		m.openThemeEditorDialog(msg.Name)
	case dialog.ActionRevertThemePalette:
		if m.preThemeStyles != nil {
			m.applyTheme(*m.preThemeStyles)
			m.preThemeStyles = nil
		}
		m.dialog.CloseDialog(dialog.ThemeEditorID)
	case dialog.ActionRevertOverriddenTheme:
		// Drop any 用户 override layered 在 top 的 built-在:
		// shadowing theme 文件 和 配置 palette 条目.
		if path, err := styles.FindThemeFile(msg.Name); err == nil {
			if err := os.Remove(path); err != nil {
				cmds = append(cmds, util.ReportError(fmt.Errorf("revert theme: %w", err)))
				break
			}
		}
		// If reverted theme 是 活跃 one, re-apply pristine
		// built-在 所以 change 是 visible immediately.
		if strings.EqualFold(common.ThemeNameFromConfig(m.com.Config()), msg.Name) {
			if newStyles, err := styles.LoadTheme(msg.Name); err == nil {
				m.applyTheme(newStyles)
				// 一个 preview snapshot holds styles 从 before
				// revert; esc must 不 bring them back.
				m.preThemeStyles = nil
				m.userAppliedTheme()
			}
		}
		cmds = append(cmds, util.ReportInfo("Reverted "+msg.Name+" to its built-in colors"))
		m.dialog.CloseDialog(dialog.ThemeID)
		m.openThemeDialog()
	case dialog.ActionCreateTheme:
		base := msg.Base
		if base == "" {
			base = "charmtone-panther"
		}
		name := msg.Name
		exported, err := styles.ExportResolvedPalette(base)
		if err != nil {
			// Fall back 到 默认 theme 当 base theme 是 no
			// longer resolvable (e.g. 用户 theme 那 was since deleted).
			base = "charmtone-panther"
			exported, err = styles.ExportResolvedPalette(base)
			if err != nil {
				cmds = append(cmds, util.ReportError(err))
				break
			}
		}
		savePath, err := styles.ThemePath(name)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		// ExportResolvedPalette already pins Base 到 built-在 root
		// palette was resolved 从. Keep it 所以 new theme stays
		// loadable even 如果 用户 theme used 作为 source 是 later
		// deleted 或 renamed.
		if err := styles.SaveThemeFile(savePath, exported); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.active_theme", name); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		cmds = append(cmds, util.ReportInfo("Created new theme: "+name))
		m.dialog.CloseDialog(dialog.ThemeNewID)
		m.dialog.CloseDialog(dialog.ThemeID)
		m.openThemeEditorDialog(name)
	case dialog.ActionRenameTheme:
		oldName := msg.OldName
		newName := strings.ToLower(msg.NewName)
		oldPath, newPath, err := styles.RenameThemeFile(oldName, newName)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		cfg := m.com.Config()
		if cfg != nil && cfg.Options != nil && cfg.Options.TUI != nil && strings.EqualFold(cfg.Options.TUI.ActiveTheme, oldName) {
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.active_theme", newName); err != nil {
				if rollbackErr := os.Rename(newPath, oldPath); rollbackErr != nil {
					slog.Error("Failed to roll back theme rename", "error", rollbackErr)
				}
				cmds = append(cmds, util.ReportError(err))
				break
			}
		}
		cmds = append(cmds, util.ReportInfo("Renamed theme "+oldName+" to "+newName))
		m.dialog.CloseDialog(dialog.ThemeID)
		m.openThemeDialog()
	case dialog.ActionDeleteTheme:
		if err := styles.DeleteThemeFile(msg.Name); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		// If deleted theme was 活跃, reset 到 默认 theme.
		if strings.EqualFold(common.ThemeNameFromConfig(m.com.Config()), msg.Name) {
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.active_theme", "charmtone-panther"); err != nil {
				cmds = append(cmds, util.ReportError(err))
				break
			}
			newStyles, err := styles.LoadTheme("charmtone-panther")
			if err != nil {
				cmds = append(cmds, util.ReportError(err))
				break
			}
			m.applyTheme(newStyles)
			m.preThemeStyles = nil
			m.userAppliedTheme()
		}
		cmds = append(cmds, util.ReportInfo("Deleted theme "+msg.Name))
		m.dialog.CloseDialog(dialog.ThemeID)
		m.openThemeDialog()
	case dialog.ActionToggleMouseSupport:
		cfg := m.com.Config()
		if cfg == nil {
			cmds = append(cmds, util.ReportError(errors.New("configuration not found")))
			break
		}
		// Flip 字段 在 main 更新 路径 所以 it never races 用
		// View() reading m.mouseEnabled 从 background command's
		// goroutine; 只 (possibly slow) 配置 写入 是 deferred.
		mouseEnabled := cfg.Options == nil || cfg.Options.TUI.Mouse == nil || *cfg.Options.TUI.Mouse
		newValue := !mouseEnabled
		m.mouseEnabled = newValue
		cmds = append(cmds, func() tea.Msg {
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.mouse", newValue); err != nil {
				return util.ReportError(err)()
			}

			status := "disabled"
			if newValue {
				status = "enabled"
			}
			return util.NewInfoMsg("Mouse support " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionQuit:
		m.quitting = true
		cmds = append(cmds, tea.Quit)
	case dialog.ActionEnableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.enableDockerMCP)
	case dialog.ActionDisableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.disableDockerMCP)
	case dialog.ActionToggleMCP:
		cmds = append(cmds, m.applyMCPToggle(msg))
	case dialog.ActionToggleSkill:
		// 序号 apply then refresh: refresh must 读取
		// overrides after toggle commits, 或 dialog shows stale
		// state.
		cmds = append(cmds, tea.Sequence(m.applySkillToggle(msg), m.refreshSkillOverrides()))
	case dialog.ActionInitializeProject:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, m.initializeProject())
		m.dialog.CloseDialog(dialog.CommandsID)

	case dialog.ActionSelectModel:
		if cmd := m.handleSelectModel(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionSelectReasoningEffort:
		cmds = append(cmds, m.updateCoderModel(func(model *config.SelectedModel) string {
			model.ReasoningEffort = msg.Effort
			return "Reasoning effort set to " + msg.Effort
		}))
		m.dialog.CloseDialog(dialog.ReasoningID)
	case dialog.ActionPermissionResponse:
		m.dialog.CloseDialog(dialog.PermissionsID)
		switch msg.Action {
		case dialog.PermissionAllow:
			m.com.Workspace.PermissionGrant(msg.Permission)
		case dialog.PermissionAllowForSession:
			m.com.Workspace.PermissionGrantPersistent(msg.Permission)
		case dialog.PermissionDeny:
			m.com.Workspace.PermissionDeny(msg.Permission)
		}

	case dialog.ActionPinentrySubmit:
		m.dialog.CloseDialog(dialog.PinentryID)
		m.com.Workspace.PinentryRespond(msg.RequestID, msg.Secret)
	case dialog.ActionPinentryCancel:
		m.dialog.CloseDialog(dialog.PinentryID)
		m.com.Workspace.PinentryCancel(msg.RequestID)
	case dialog.ActionSSHSubmit:
		m.dialog.CloseDialog(dialog.SSHID)
		m.com.Workspace.SSHRespond(msg.RequestID, msg.Secret)
	case dialog.ActionSSHCancel:
		m.dialog.CloseDialog(dialog.SSHID)
		m.com.Workspace.SSHCancel(msg.RequestID)

	case dialog.ActionFilePickerSelected:
		cmds = append(cmds, tea.Sequence(
			msg.Cmd(),
			func() tea.Msg {
				m.dialog.CloseDialog(dialog.FilePickerID)
				return nil
			},
			func() tea.Msg {
				fimage.ResetCache()
				return nil
			},
		))

	case dialog.ActionRunCustomCommand:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			argsDialog := dialog.NewArguments(
				m.com,
				"Custom Command Arguments",
				"",
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		content := msg.Content
		if msg.Args != nil {
			content = substituteArgs(content, msg.Args)
		}
		// If 这 是 skill command, format it using skill's FormatInvocation method
		if msg.Skill != nil {
			content = msg.Skill.FormatInvocation()
		}
		cmds = append(cmds, m.sendMessage(content))
		m.dialog.CloseFrontDialog()
	case dialog.ActionAttachSkill:
		m.dialog.CloseFrontDialog()
		cmds = append(cmds, m.attachSkill(msg.ID, msg.Name))
	case dialog.ActionRunMCPPrompt:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			title := cmp.Or(msg.Title, "MCP Prompt Arguments")
			argsDialog := dialog.NewArguments(
				m.com,
				title,
				msg.Description,
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		cmds = append(cmds, m.runMCPPrompt(msg.ClientID, msg.PromptID, msg.Args))
	default:
		cmds = append(cmds, util.CmdHandler(msg))
	}

	return tea.Batch(cmds...)
}

// substituteArgs replaces $ARG_NAME placeholders 在 content 用 actual values.
func substituteArgs(content string, args map[string]string) string {
	for name, value := range args {
		placeholder := "$" + name
		content = strings.ReplaceAll(content, placeholder, value)
	}
	return content
}

// refreshHyperAndRetrySelect returns command 那 silently refreshes
// Hyper OAuth token 和 then re-runs 模型 selection. If
// refresh fails, selection resumes 用 ReAuthenticate set 所以
// OAuth dialog opens.
func (m *UI) refreshHyperAndRetrySelect(msg dialog.ActionSelectModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.com.Workspace.RefreshOAuthToken(ctx, config.ScopeGlobal, "hyper"); err != nil {
			slog.Warn("Hyper OAuth refresh failed, requesting re-auth", "error", err)
			msg.ReAuthenticate = true
		}
		return hyperRefreshDoneMsg{action: msg}
	}
}

// updateHyperCredits refreshes displayed Hyper balance 从 most
// recent /v1/credits fetch. balance 是 fetched 在 startup, polled
// while 空闲 和 refreshed 在 every 响应 during 会话, 所以 reading
// 存储 值 here 是 enough: until first fetch lands
// balance 是 unknown 和 stays hidden.
func (m *UI) updateHyperCredits() {
	if !m.com.IsHyper() {
		return
	}
	m.hyperCredits = hyper.Balance()
}

// fetchHyperCredits returns command 那 asynchronously fetches
// remaining Hyper credits 从 /v1/credits endpoint. An expired
// OAuth token 是 refreshed first 所以 long-running 会话 keeps
// reporting a balance.
func (m *UI) fetchHyperCredits() tea.Cmd {
	return func() tea.Msg {
		var (
			apiKey      string
			cfg         *config.Config
			providerCfg config.ProviderConfig
		)
		getAPIKey := func() (ok bool) {
			if cfg = m.com.Config(); cfg == nil || cfg.Providers == nil {
				return false
			}
			if providerCfg, ok = cfg.Providers.Get(hyper.Name); !ok {
				return false
			}
			var err error
			apiKey, err = m.com.Workspace.Resolver().ResolveValue(providerCfg.APIKey)
			return err == nil && apiKey != ""
		}
		if !getAPIKey() {
			return nil
		}

		if providerCfg.OAuthToken != nil && providerCfg.OAuthToken.IsExpired() {
			ctxRefresh, cancelRefresh := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancelRefresh()
			if err := m.com.Workspace.RefreshOAuthToken(ctxRefresh, config.ScopeGlobal, hyper.Name); err != nil {
				slog.Warn("Hyper OAuth refresh failed before fetching credits, trying with existing token", "error", err)
			} else if !getAPIKey() {
				return nil
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		credits, err := hyper.FetchCredits(ctx, apiKey)
		if err != nil {
			slog.Warn("Failed to fetch Hyper credits", "error", err)
			return nil
		}
		return creditsUpdatedMsg{credits: credits}
	}
}

// hyperCreditsTicker schedules the next Hyper credits poll.
func (m *UI) hyperCreditsTicker() tea.Cmd {
	return tea.Tick(hyperCreditsPollInterval, func(time.Time) tea.Msg {
		return hyperCreditsPollMsg{}
	})
}

// fetchGitBranch reads 工作区's checked-out branch off render
// 路径. 一个 failed 读取 keeps whatever last poll reported.
func (m *UI) fetchGitBranch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitBranchFetchTimeout)
		defer cancel()
		branch, err := m.com.Workspace.GitBranch(ctx)
		if err != nil {
			slog.Warn("Failed to read the git branch", "error", err)
			return nil
		}
		return gitBranchUpdatedMsg{branch: branch}
	}
}

// gitBranchTicker schedules the next git branch poll.
func (m *UI) gitBranchTicker() tea.Cmd {
	return tea.Tick(gitBranchPollInterval, func(time.Time) tea.Msg {
		return gitBranchPollMsg{}
	})
}

// restoreModelFromSession checks last 助手消息 在
// loaded 会话 和, 如果 it used different 提供方/模型 than
// current 配置, restores 那 模型/提供方 provided it 是 still
// available. Returns tea.Cmd 那 rebuilds agent models 如果
// switch was made, 或 nil 如果 no switch was needed.
func (m *UI) restoreModelFromSession(msgs []message.Message) tea.Cmd {
	var lastAssistant *message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant && !msgs[i].IsSummaryMessage {
			lastAssistant = &msgs[i]
			break
		}
	}
	if lastAssistant == nil || lastAssistant.Provider == "" || lastAssistant.Model == "" {
		return nil
	}

	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}

	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastAssistant.Provider && currentLarge.Model == lastAssistant.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastAssistant.Provider, lastAssistant.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	}

	selectedModel := config.SelectedModel{
		Provider: lastAssistant.Provider,
		Model:    lastAssistant.Model,
	}
	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeLarge, selectedModel); err != nil {
		slog.Error("Failed to restore model from session", "error", err)
		return nil
	}

	m.applyThemeForProvider(lastAssistant.Provider)

	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		smallModel := m.com.Workspace.GetDefaultSmallModel(lastAssistant.Provider)
		if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, smallModel); err != nil {
			slog.Error("Failed to set small model during session restore", "error", err)
		}
	}

	return m.updateAgentModelCmd(func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)()
		}
		slog.Info("Restored model from session",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	})
}

// updateCoderModel applies change 到 coder 模型's settings, saves them
// 和 rebuilds agent's models. change returns 消息 到 报告.
func (m *UI) updateCoderModel(change func(*config.SelectedModel) string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return util.ReportError(errors.New("agent configuration not found"))
	}

	model := cfg.Models[agentCfg.Model]
	info := change(&model)
	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, agentCfg.Model, model); err != nil {
		return util.ReportError(err)
	}

	info += m.nextMessageNote()
	return m.updateAgentModelCmd(func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)()
		}
		return util.NewInfoMsg(info)
	})
}

// handleSelectModel performs 模型 selection after any 提供方
// pre-checks (such 作为 silent Hyper OAuth refresh) have completed.
func (m *UI) handleSelectModel(msg dialog.ActionSelectModel) tea.Cmd {
	var cmds []tea.Cmd

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	var (
		providerID   = msg.Model.Provider
		isCopilot    = providerID == string(catwalk.InferenceProviderCopilot)
		isConfigured = func() bool { _, ok := cfg.Providers.Get(providerID); return ok }
		isOnboarding = m.state == uiOnboarding
	)

	// For Hyper, 如果 存储 OAuth token 是 expired, try silent
	// refresh before deciding whether 提供方 是 configured. Keeps
	// users 从 hitting 401 在 their first 消息 after
	// short-lived 访问令牌 ages out.
	if !msg.ReAuthenticate && providerID == "hyper" {
		if pc, ok := cfg.Providers.Get(providerID); ok && pc.OAuthToken != nil && pc.OAuthToken.IsExpired() {
			return m.refreshHyperAndRetrySelect(msg)
		}
	}

	// Attempt 到 import GitHub Copilot tokens 从 VSCode 如果 available.
	if isCopilot && !isConfigured() && !msg.ReAuthenticate {
		m.com.Workspace.ImportCopilot()
	}

	// OpenAI 和 xAI providers hold exactly one credential:
	// account login 或 API key. empty 模型 ID marks OAuth
	// flow's hand-off 消息 (sign-在 completed, 或 method choice
	// going 到 OAuth), 和 catalog 模型 needs one 的 credentials
	// before it 可以 serve.
	if providerID == string(catwalk.InferenceProviderOpenAI) ||
		providerID == string(catwalk.InferenceProviderXAI) {
		providerCfg, _ := cfg.Providers.Get(providerID)
		if msg.Model.Model == "" {
			m.dialog.CloseDialog(dialog.ModelsID)
			if providerCfg.OAuthToken != nil && !msg.ReAuthenticate {
				// 一个 sign-在 just completed: reopen list 所以 用户
				// can pick 从 now-available catalog.
				m.dialog.CloseDialog(dialog.OAuthID)
				if cmd := m.openModelsDialog(); cmd != nil {
					return cmd
				}
				return nil
			}
			return m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType)
		}
		if providerCfg.OAuthToken == nil && !providerCfg.HasAPIKey(m.com.Workspace.Resolver()) {
			m.dialog.CloseDialog(dialog.ModelsID)
			return m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType)
		}
	}

	if !isConfigured() || msg.ReAuthenticate {
		m.dialog.CloseDialog(dialog.ModelsID)
		if cmd := m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}

	// 一个 ChatGPT 或 Grok sign-在 swaps 提供方's catalog 为 one
	// account serves, 所以 模型 chosen before flow may no longer
	// exist afterward. Apply remembered choice 只 当 refreshed
	// catalog still offers it; otherwise reopen list 和 say 所以 rather
	// than silently falling back 到 默认.
	if providerID == string(catwalk.InferenceProviderOpenAI) ||
		providerID == string(catwalk.InferenceProviderXAI) {
		if !cfg.IsModelAvailable(providerID, msg.Model.Model) {
			m.dialog.CloseDialog(dialog.ModelsID)
			cmds = append(cmds, util.ReportError(fmt.Errorf(
				"%s isn't offered by your %s account; choose another model",
				msg.Model.Model, cmp.Or(msg.Provider.Name, providerID),
			)))
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return tea.Batch(cmds...)
		}
	}

	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, msg.ModelType, msg.Model); err != nil {
		cmds = append(cmds, util.ReportError(err))
	} else {
		if msg.ModelType == config.SelectedModelTypeLarge {
			// Swap theme live based 在 newly selected large
			// 模型's 提供方. Skipped 当 提供方 resolves 到
			// already-活跃 theme, which avoids full markdown
			// re-render 的 transcript 在 every selection.
			m.applyThemeForProvider(providerID)
		}
		if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
			// Ensure small 模型 是 set 是 unset.
			smallModel := m.com.Workspace.GetDefaultSmallModel(providerID)
			if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, smallModel); err != nil {
				cmds = append(cmds, util.ReportError(err))
			}
		}
	}

	note := m.nextMessageNote()
	cmds = append(cmds, m.updateAgentModelCmd(func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)()
		}

		var (
			modelType = stringext.Capitalize(string(msg.ModelType))
			modelName = msg.Model.Model
		)
		if catwalkModel := cfg.GetModel(msg.Model.Provider, msg.Model.Model); catwalkModel != nil && catwalkModel.Name != "" {
			modelName = catwalkModel.Name
		}
		modelMsg := fmt.Sprintf("%s model changed to %s%s", modelType, modelName, note)

		return util.NewInfoMsg(modelMsg)
	}))

	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ModelsID)

	if isOnboarding {
		m.setState(uiLanding, uiFocusEditor)
		m.com.Config().SetupAgents()
		if err := m.com.Workspace.InitCoderAgent(context.TODO()); err != nil {
			cmds = append(cmds, util.ReportError(err))
		}
		// agent just came up: re-fetch memoized ready/模型 state
		// 所以 landing view shows selected 模型 without waiting 为
		// the TTL backstop.
		m.invalidateBusyCaches()
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	} else if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}

	return tea.Batch(cmds...)
}

func (m *UI) openAuthenticationDialog(provider catwalk.Provider, model config.SelectedModel, modelType config.SelectedModelType) tea.Cmd {
	var (
		dlg dialog.Dialog
		cmd tea.Cmd

		isOnboarding = m.state == uiOnboarding
	)

	switch provider.ID {
	case "hyper":
		dlg, cmd = dialog.NewOAuthHyper(m.com, isOnboarding, provider, model, modelType)
	case catwalk.InferenceProviderCopilot:
		dlg, cmd = dialog.NewOAuthCopilot(m.com, isOnboarding, provider, model, modelType)
	case catwalk.InferenceProviderOpenAI:
		providerCfg, _ := m.com.Config().Providers.Get(string(provider.ID))
		hasAPIKey := providerCfg.HasAPIKey(m.com.Workspace.Resolver())
		switch {
		case model.Model == "" || providerCfg.OAuthToken != nil:
			// sign-在 placeholder, 或 re-authentication while
			// ChatGPT login 是 credential 在 force.
			dlg, cmd = dialog.NewOAuthOpenAI(m.com, isOnboarding, provider, model, modelType)
		case !hasAPIKey:
			// No credential 在 all: let 用户 pick method.
			dlg = dialog.NewAuthMethod(m.com, isOnboarding, provider, model, modelType)
		default:
			// An API key 是 credential 在 force: edit it.
			dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
		}
	case catwalk.InferenceProviderXAI:
		providerCfg, _ := m.com.Config().Providers.Get(string(provider.ID))
		hasAPIKey := providerCfg.HasAPIKey(m.com.Workspace.Resolver())
		switch {
		case model.Model == "" || providerCfg.OAuthToken != nil:
			// sign-在 flow's hand-off, 或 re-authentication while
			// Grok login 是 credential 在 force.
			dlg, cmd = dialog.NewOAuthGrok(m.com, isOnboarding, provider, model, modelType)
		case !hasAPIKey:
			// No credential 在 all: let 用户 pick method.
			dlg = dialog.NewAuthMethod(m.com, isOnboarding, provider, model, modelType)
		default:
			// An API key 是 credential 在 force: edit it.
			dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
		}
	default:
		dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
	}

	if m.dialog.ContainsDialog(dlg.ID()) {
		m.dialog.BringToFront(dlg.ID())
		return nil
	}

	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

// openAuthenticationDialogWithMethod opens authentication dialog 为
// method 用户 chose 在 auth method picker. 模型
// 用户 selected 是 carried through OAuth flow 所以 choice persists;
// handleSelectModel applies it 只 当 signed-在 catalog still
// offers it 和 errors otherwise.
func (m *UI) openAuthenticationDialogWithMethod(provider catwalk.Provider, model config.SelectedModel, modelType config.SelectedModelType, useOAuth bool) tea.Cmd {
	isOnboarding := m.state == uiOnboarding

	var (
		dlg dialog.Dialog
		cmd tea.Cmd
	)
	if useOAuth {
		switch provider.ID {
		case catwalk.InferenceProviderOpenAI:
			dlg, cmd = dialog.NewOAuthOpenAI(m.com, isOnboarding, provider, model, modelType)
		case catwalk.InferenceProviderXAI:
			dlg, cmd = dialog.NewOAuthGrok(m.com, isOnboarding, provider, model, modelType)
		}
	} else {
		dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
	}

	if m.dialog.ContainsDialog(dlg.ID()) {
		m.dialog.BringToFront(dlg.ID())
		return nil
	}

	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

func (m *UI) handleKeyPressMsg(msg tea.KeyPressMsg) tea.Cmd {
	var cmds []tea.Cmd

	handleGlobalKeys := func(msg tea.KeyPressMsg) bool {
		switch {
		case key.Matches(msg, m.keyMap.Help):
			m.status.ToggleHelp()
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Commands):
			if cmd := m.openCommandsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Models):
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Sessions):
			if cmd := m.openSessionsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Chat.Details) && m.isCompact:
			m.detailsOpen = !m.detailsOpen
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Chat.ToggleSidebar):
			if m.canToggleSidebar() {
				cmds = append(cmds, m.toggleCompactMode())
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.EndFollow):
			if m.state == uiChat && m.hasSession() {
				m.chat.ScrollToBottomAndSelectLast()
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.TogglePills):
			if m.state == uiChat && m.hasSession() {
				if cmd := m.togglePillsExpanded(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillLeft):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor {
				if cmd := m.switchPillSection(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillRight):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor {
				if cmd := m.switchPillSection(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Suspend):
			if m.isAgentBusy() {
				cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
				return true
			}
			cmds = append(cmds, tea.Suspend)
			return true
		case key.Matches(msg, m.keyMap.ToggleYolo):
			if m.mode == uiInputModePlan {
				// YOLO has no meaning while planning; activating it
				// switches straight 到 YOLO coding.
				if cmd := m.switchPlanToYolo(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
			if m.toggleMode(permission.PermissionModeYolo) {
				cmds = append(cmds, util.CmdHandler(util.InfoMsg{Type: util.InfoTypeYolo, Msg: yoloModeBannerMsg}))
			} else {
				cmds = append(cmds, util.ReportInfo("Yolo mode disabled"))
			}
			return true
		}
		return false
	}

	if key.Matches(msg, m.keyMap.Quit) && !m.dialog.ContainsDialog(dialog.QuitID) {
		// Always handle quit keys first
		if cmd := m.openQuitDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}

		return tea.Batch(cmds...)
	}

	// Route all messages 到 dialog 如果 one 是 open.
	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	// Tab always toggles focus between editor 和 chat, even 当
	// inline editor 是 活跃. 这 lets users collapse
	// question form 到 view chat.
	if m.activeInline != nil && key.Matches(msg, m.keyMap.Tab) {
		if m.focus == uiFocusEditor {
			m.focusActiveInline(uiFocusMain)
		} else {
			m.focusActiveInline(uiFocusEditor)
		}
		return tea.Batch(cmds...)
	}

	// Route keys 到 活跃 inline editor 如果 one 是 showing.
	if m.activeInline != nil && m.focus == uiFocusEditor {
		if done, cmd := m.activeInline.HandleKey(msg); done {
			prev := m.activeInline
			m.activeInline = nil
			m.textarea.Focus()
			m.updateLayoutAndSize()
			if cmd != nil {
				cmds = append(cmds, cmd)
			} else if cod, ok := prev.(dialog.CmdOnDone); ok {
				if c := cod.PendingCmd(); c != nil {
					cmds = append(cmds, c)
				}
			}
		} else {
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.activeInline.HeightChanged() {
				m.updateLayoutAndSize()
			}
		}
		return tea.Batch(cmds...)
	}

	// Handle 取消 key 当 agent 是 busy.
	if key.Matches(msg, m.keyMap.Chat.Cancel) {
		if m.isAgentBusy() {
			if cmd := m.cancelAgent(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return tea.Batch(cmds...)
		}
	}

	switch m.state {
	case uiOnboarding:
		return tea.Batch(cmds...)
	case uiInitialize:
		cmds = append(cmds, m.updateInitializeView(msg)...)
		return tea.Batch(cmds...)
	case uiChat, uiLanding:
		switch m.focus {
		case uiFocusEditor:
			// Handle completions 如果 open.
			if m.completionsOpen {
				if msg, ok := m.completions.Update(msg); ok {
					switch msg := msg.(type) {
					case completions.SelectionMsg[completions.FileCompletionValue]:
						cmds = append(cmds, m.insertFileCompletion(msg.Value.Path))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.SelectionMsg[completions.ResourceCompletionValue]:
						cmds = append(cmds, m.insertMCPResourceCompletion(msg.Value))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.ClosedMsg:
						m.completionsOpen = false
					}
					return tea.Batch(cmds...)
				}
			}

			if m.changeAttachments(func() bool { return m.attachments.Update(msg) }) {
				return tea.Batch(cmds...)
			}

			switch {
			case key.Matches(msg, m.keyMap.ShiftTab):
				if cmd := m.toggleInputMode(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.AddImage):
				if !m.currentModelSupportsImages() {
					break
				}
				if cmd := m.openFilesDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}

			case key.Matches(msg, m.keyMap.Editor.Paste):
				cmds = append(cmds, m.pasteFromClipboard)

			case key.Matches(msg, m.keyMap.Editor.SendMessage):
				if m.modeSwitching {
					cmds = append(cmds, util.ReportInfo("Switching input mode, one moment..."))
					break
				}
				prevHeight := m.textarea.Height()
				value := m.textarea.Value()
				if before, ok := strings.CutSuffix(value, "\\"); ok {
					// If last character 是 backslash, remove it 和 add newline.
					m.textarea.SetValue(before)
					if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
						cmds = append(cmds, cmd)
					}
					break
				}

				// Otherwise, send 消息
				m.textarea.Reset()
				if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
					cmds = append(cmds, cmd)
				}

				value = strings.TrimSpace(value)
				if value == "exit" || value == "quit" {
					return m.openQuitDialog()
				}

				if m.bangMode && value != "" {
					m.bangMode = false
					m.setEditorPrompt(m.permModeCached())
					m.randomizePlaceholders()
					m.historyReset()
					return tea.Batch(m.runShellCommand(value))
				}

				attachments := m.attachments.List()
				m.changeAttachments(func() bool { m.attachments.Reset(); return true })
				if len(value) == 0 && !message.ContainsTextAttachment(attachments) {
					// Enter 在 empty editor while ready plan 是 pending
					// reopens dismissed handoff 提示词.
					if m.mode == uiInputModePlan && m.hasSession() && m.planReadySessionID == m.session.ID {
						m.openPlanHandoff()
					}
					return nil
				}

				m.randomizePlaceholders()
				m.historyReset()

				return tea.Batch(m.sendMessage(value, attachments...), m.loadPromptHistory())
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				if m.state != uiLanding {
					m.setState(m.state, uiFocusMain)
					m.textarea.Blur()
					m.chat.Focus()
					m.chat.SetSelected(m.chat.Len() - 1)
				}
			case key.Matches(msg, m.keyMap.Editor.OpenEditor):
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
					break
				}
				editorValue := m.textarea.Value()
				if m.bangMode {
					editorValue = "!" + editorValue
				}
				cmds = append(cmds, m.openEditor(editorValue))
			case key.Matches(msg, m.keyMap.Editor.Newline):
				prevHeight := m.textarea.Height()
				m.textarea.InsertRune('\n')
				m.closeCompletions()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
			case key.Matches(msg, m.keyMap.Editor.CopySelection):
				if m.textarea.HasSelection() {
					cmds = append(cmds, common.CopyToClipboardWithCallback(
						m.textarea.SelectedText(),
						"Selection copied to clipboard",
						nil,
					))
					m.textarea.ClearSelection()
				}
			case key.Matches(msg, m.keyMap.Editor.CutSelection):
				if m.textarea.HasSelection() {
					cmds = append(cmds, common.CopyToClipboardWithCallback(
						m.textarea.SelectedText(),
						"Selection cut to clipboard",
						nil,
					))
					m.textarea.DeleteSelection()
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryPrev):
				cmd := m.handleHistoryUp(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryNext):
				cmd := m.handleHistoryDown(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Escape):
				cmd := m.handleHistoryEscape(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Commands) && m.textarea.Value() == "":
				if cmd := m.openCommandsDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			default:
				if handleGlobalKeys(msg) {
					// Handle global keys first before passing 到 textarea.
					break
				}

				// Bang mode: backspace 在 already-empty 提示词 exits.
				if m.bangMode && m.bangWasEmpty && msg.Code == tea.KeyBackspace {
					m.bangMode = false
					m.bangWasEmpty = false
					m.setEditorPrompt(m.permModeCached())
					break
				}

				// Check 为 @ trigger before passing 到 textarea.
				curValue := m.textarea.Value()
				curIdx := len(curValue)

				// Trigger completions 在 @.
				if msg.String() == "@" && !m.completionsOpen {
					// Only show 如果 beginning 的 提示词 或 after whitespace.
					if curIdx == 0 || (curIdx > 0 && isWhitespace(curValue[curIdx-1])) {
						m.completionsOpen = true
						m.completionsQuery = ""
						m.completionsStartIndex = curIdx
						m.completionsPositionStart = m.completionsPosition()
						depth, limit := m.com.Config().Options.TUI.Completions.Limits()
						cmds = append(cmds, m.completions.Open(depth, limit, hostOpenFiles(m.com.Workspace)))
					}
				}

				// remove details 如果 they 是 open 当 用户 starts typing
				if m.detailsOpen {
					m.detailsOpen = false
					m.updateLayoutAndSize()
				}

				prevHeight := m.textarea.Height()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))

				// Bang mode: enter 当 "!" 是 typed 在 start 的
				// 提示词, optionally preceded 通过 whitespace (either 在
				// empty/whitespace-只 提示词 或 prepended 到 existing 文本).
				// Exit 在 backspace clearing last character.
				newVal := m.textarea.Value()
				trimmedNew := strings.TrimLeftFunc(newVal, unicode.IsSpace)
				trimmedCur := strings.TrimLeftFunc(curValue, unicode.IsSpace)
				if !m.bangMode && strings.HasPrefix(trimmedNew, "!") && !strings.HasPrefix(trimmedCur, "!") {
					m.bangMode = true
					m.bangWasEmpty = len(strings.TrimSpace(curValue)) == 0
					// Strip leading whitespace 和 "!" 从 textarea
					// while preserving cursor position relative 到
					// command 文本.
					col := m.textarea.Column()
					line := m.textarea.Line()
					stripped := trimmedNew[1:]
					m.textarea.SetValue(stripped)
					m.textarea.SetCursorColumn(max(0, col-(len(newVal)-len(stripped))))
					_ = line // cursor line doesn't change; prefix removed
					m.setEditorPrompt(m.permModeCached())
				} else if m.bangMode && newVal == "" && curValue != "" {
					// Just cleared last character; 标记 empty, stay 在 bang mode.
					m.bangWasEmpty = true
				} else if m.bangMode && newVal != "" {
					m.bangWasEmpty = false
				}

				// Any 文本 modification becomes current draft.
				m.updateHistoryDraft(curValue)

				// After updating textarea, check 如果 we need 到 filter completions.
				// Skip filtering 在 initial @ keystroke since items 是 loading async.
				if m.completionsOpen && msg.String() != "@" {
					newValue := m.textarea.Value()
					newIdx := len(newValue)

					// Close completions 如果 cursor moved before start.
					if newIdx <= m.completionsStartIndex {
						m.closeCompletions()
					} else if msg.String() == "space" {
						// Close 在 space.
						m.closeCompletions()
					} else {
						// Extract current word 和 filter.
						word := m.textareaWord()
						if strings.HasPrefix(word, "@") {
							m.completionsQuery = word[1:]
							m.completions.Filter(m.completionsQuery)
						} else if m.completionsOpen {
							m.closeCompletions()
						}
					}
				}
			}
		case uiFocusMain:
			switch {
			case key.Matches(msg, m.keyMap.ShiftTab):
				if cmd := m.toggleInputMode(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				m.focus = uiFocusEditor
				m.sidebarScrollbarVisible = false
				cmds = append(cmds, m.textarea.Focus())
				m.chat.Blur()
			case key.Matches(msg, m.keyMap.Chat.FocusSidebar):
				if m.state == uiChat && !m.isCompact && m.hasSession() && m.sidebarScrollable {
					m.focus = uiFocusSidebar
					m.chat.Blur()
				}
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				m.focus = uiFocusEditor
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.Expand):
				m.chat.ToggleExpandedSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.Up):
				m.markScrollOnly()
				m.chat.ScrollBy(-1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectPrev()
					m.chat.ScrollToSelected()
				}
			case key.Matches(msg, m.keyMap.Chat.Down):
				m.markScrollOnly()
				m.chat.ScrollBy(1)
				if !m.chat.SelectedItemInView() {
					m.chat.SelectNext()
					m.chat.ScrollToSelected()
				}
			case key.Matches(msg, m.keyMap.Chat.UpOneItem):
				m.chat.SelectPrev()
				m.chat.ScrollToSelected()
			case key.Matches(msg, m.keyMap.Chat.DownOneItem):
				m.chat.SelectNext()
				m.chat.ScrollToSelected()
			case key.Matches(msg, m.keyMap.Chat.HalfPageUp):
				m.markScrollOnly()
				m.chat.ScrollBy(-m.chat.Height() / 2)
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.HalfPageDown):
				m.markScrollOnly()
				m.chat.ScrollBy(m.chat.Height() / 2)
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.PageUp):
				m.markScrollOnly()
				m.chat.ScrollBy(-m.chat.Height())
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.PageDown):
				m.markScrollOnly()
				m.chat.ScrollBy(m.chat.Height())
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.Home):
				m.chat.ScrollToTop()
				m.chat.SelectFirst()
			case key.Matches(msg, m.keyMap.Chat.End):
				m.chat.ScrollToBottomAndSelectLast()
			default:
				if ok, cmd := m.chat.HandleKeyMsg(msg); ok {
					cmds = append(cmds, cmd)
				} else {
					handleGlobalKeys(msg)
				}
			}
		case uiFocusSidebar:
			if m.state != uiChat || m.isCompact || !m.hasSession() {
				break
			}
			switch {
			case key.Matches(msg, m.keyMap.Chat.Up):
				m.sidebarOffset = max(0, m.sidebarOffset-4)
				m.sidebarScrollbarSeq++
			case key.Matches(msg, m.keyMap.Chat.Down):
				maxOffset := m.sidebarMaxOffsetVal
				if m.sidebarOffset < maxOffset {
					m.sidebarOffset = min(m.sidebarOffset+4, maxOffset)
					m.sidebarScrollbarSeq++
				}
			case key.Matches(msg, m.keyMap.Chat.Home):
				m.sidebarOffset = 0
				m.sidebarScrollbarSeq++
			case key.Matches(msg, m.keyMap.Chat.End):
				m.sidebarOffset = m.sidebarMaxOffsetVal
				m.sidebarScrollbarSeq++
			case key.Matches(msg, m.keyMap.Chat.FocusChat):
				m.focus = uiFocusMain
				m.sidebarScrollbarVisible = false
				m.chat.Focus()
			case key.Matches(msg, m.keyMap.Tab):
				m.focus = uiFocusEditor
				m.sidebarScrollbarVisible = false
				cmds = append(cmds, m.textarea.Focus())
				m.chat.Blur()
			default:
				handleGlobalKeys(msg)
			}
		default:
			handleGlobalKeys(msg)
		}
	default:
		handleGlobalKeys(msg)
	}

	return tea.Sequence(cmds...)
}

// drawHeader draws header section 的 UI.
func (m *UI) drawHeader(scr uv.Screen, area uv.Rectangle) {
	m.header.drawHeader(
		scr,
		area,
		m.session,
		m.isCompact,
		m.detailsOpen,
		area.Dx(),
		m.lspErrorCount(),
		m.hyperCredits,
		m.gitBranchLabel(),
		m.subAgentsRunning,
	)
}

// Draw implements [uv.Drawable] 和 draws UI 模型.
func (m *UI) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	layout := m.generateLayout(area.Dx(), area.Dy())

	if m.layout != layout {
		m.layout = layout
		m.updateSize()
	} else if m.state == uiChat && m.hasSession() {
		// Re-render pills 在 every draw 所以 box appears even 当
		// the layout footprint hasn't changed (e.g. todos arrived
		// while the panel was collapsed). updateSize already calls
		// renderPills, 但 只 当 layout actually differs;
		// 这 catches steady-state case.
		m.renderPills()
	}

	if m.state == uiChat && m.hasSession() && !m.isCompact {
		m.updateSidebarScrollState()
	}

	// Clear the screen first
	screen.Clear(scr)

	switch m.state {
	case uiOnboarding:
		m.drawHeader(scr, layout.header)

		// NOTE: Onboarding flow will 是 rendered 作为 dialogs below, 但
		// positioned 在 bottom left 的 screen.

	case uiInitialize:
		m.drawHeader(scr, layout.header)

		main := uv.NewStyledString(m.initializeView())
		main.Draw(scr, layout.main)

	case uiLanding:
		m.drawHeader(scr, layout.header)
		main := uv.NewStyledString(m.landingView())
		main.Draw(scr, layout.main)

		if m.activeInline != nil {
			m.activeInline.SetFocused(m.focus == uiFocusEditor)
			if collapsed, ok := m.collapsedInlineEditor(); ok {
				collapsed.DrawCollapsed(scr, layout.editor)
				m.inlineCursor = nil
			} else {
				m.inlineCursor = m.activeInline.Draw(scr, layout.editor)
			}
		} else {
			editor := uv.NewStyledString(m.renderEditorView(scr.Bounds().Dx()))
			editor.Draw(scr, layout.editor)
			m.inlineCursor = nil
		}

	case uiChat:
		if m.isCompact {
			m.drawHeader(scr, layout.header)
		} else {
			m.drawSidebar(scr, layout.sidebar)
		}

		m.chat.Draw(scr, layout.main)
		if layout.pills.Dy() > 0 && m.pillsView != "" {
			uv.NewStyledString(m.pillsView).Draw(scr, layout.pills)
		}

		if m.activeInline != nil {
			m.activeInline.SetFocused(m.focus == uiFocusEditor)
			if collapsed, ok := m.collapsedInlineEditor(); ok {
				collapsed.DrawCollapsed(scr, layout.editor)
				m.inlineCursor = nil
			} else {
				m.inlineCursor = m.activeInline.Draw(scr, layout.editor)
			}
		} else {
			editorWidth := scr.Bounds().Dx()
			if !m.isCompact {
				editorWidth -= layout.sidebar.Dx()
			}
			editor := uv.NewStyledString(m.renderEditorView(editorWidth))
			editor.Draw(scr, layout.editor)
			m.inlineCursor = nil
		}

		// Draw details overlay 在 compact mode 当 open
		if m.isCompact && m.detailsOpen {
			m.drawSessionDetails(scr, layout.sessionDetails)
		}
	}

	isOnboarding := m.state == uiOnboarding

	// Add status 和 help layer
	m.status.SetHideHelp(isOnboarding)
	m.status.SetMode(m.mode, m.permModeCached())
	m.status.Draw(scr, layout.status)

	// Draw completions popup 如果 open
	if !isOnboarding && m.completionsOpen && m.completions.HasItems() {
		w, h := m.completions.Size()
		x := m.completionsPositionStart.X
		y := m.completionsPositionStart.Y - h

		screenW := area.Dx()
		if x+w > screenW {
			x = screenW - w
		}
		x = max(0, x)
		y = max(0, y+1) // Offset for attachments row

		completionsView := uv.NewStyledString(m.completions.Render())
		completionsView.Draw(scr, image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+w, y+h),
		})
	}

	// Debugging rendering (visually see 当 tui rerenders)
	if os.Getenv("CRUSH_UI_DEBUG") == "true" {
		debugView := lipgloss.NewStyle().Background(lipgloss.ANSIColor(rand.Intn(256))).Width(4).Height(2)
		debug := uv.NewStyledString(debugView.String())
		debug.Draw(scr, image.Rectangle{
			Min: image.Pt(4, 1),
			Max: image.Pt(8, 3),
		})
	}

	// 这 needs 到 come last 到 overlay 在 top 的 everything. We always pass
	// full screen bounds because dialogs 将 position themselves
	// accordingly.
	if m.dialog.HasDialogs() {
		return m.dialog.Draw(scr, scr.Bounds())
	}

	switch m.focus {
	case uiFocusEditor:
		if m.layout.editor.Dy() <= 0 {
			// Don't show cursor 如果 editor 是 不 visible
			return nil
		}
		if m.detailsOpen && m.isCompact {
			// Don't show cursor 如果 details overlay 是 open
			return nil
		}

		if m.activeInline != nil {
			if cur := m.inlineCursor; cur != nil {
				cur.X++                        // Adjust for app margins
				cur.Y += m.layout.editor.Min.Y // Inline editor draws from area top
				return cur
			}
			return nil
		}

		if m.textarea.Focused() {
			cur := m.textarea.Cursor()
			cur.X++                            // Adjust for app margins
			cur.Y += m.layout.editor.Min.Y + 1 // Offset for attachments row
			return cur
		}
	}
	return nil
}

// mouseMode determines Bubble Tea mouse reporting mode 到 请求 为
// current frame. When mouse support 是 disabled via configuration, no
// mouse mode 是 requested 所以 终态 emulator (或 tmux) can handle
// 文本 selection, copy/paste, 和 scrolling natively. Inline editors 和
// hoverable dialogs need motion events even without a button pressed (e.g.
// 为 hover/drag), 所以 they use MouseModeAllMotion; everything else 只
// needs click/drag tracking via MouseModeCellMotion.
func mouseMode(enabled, wantsMotion bool) tea.MouseMode {
	switch {
	case !enabled:
		return tea.MouseModeNone
	case wantsMotion:
		return tea.MouseModeAllMotion
	default:
		return tea.MouseModeCellMotion
	}
}

// View renders UI 模型's view.
func (m *UI) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if !m.isTransparent {
		v.BackgroundColor = m.com.Styles.Background
	}
	v.MouseMode = mouseMode(m.mouseEnabled, m.activeInline != nil || m.dialog.HandlesHover())
	v.ReportFocus = m.caps.ReportFocusEvents
	v.WindowTitle = "crush " + home.Short(m.com.Workspace.WorkingDir())

	key, cacheable := m.currentFrameKey()
	if cacheable {
		if content, cursor, ok := m.frames.get(key); ok {
			v.Content = content
			v.Cursor = cursor
			m.applyTerminalStatus(&v)
			return v
		}
	}

	canvas := uv.NewScreenBuffer(m.width, m.height)
	v.Cursor = m.Draw(canvas, canvas.Bounds())

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n") // normalize newlines
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		// Trim trailing spaces 为 concise rendering
		contentLines[i] = strings.TrimRight(line, " ")
	}

	content = strings.Join(contentLines, "\n")

	v.Content = content
	if cacheable {
		m.storeFrame(key, content, v.Cursor)
	}
	m.applyTerminalStatus(&v)

	return v
}

// applyTerminalStatus attaches program status, how 到 resume Crush, 和
// 终态 progress bar while agent 是 busy. Kept outside frame
// 缓存 所以 randomized progress 值 stays fresh.
func (m *UI) applyTerminalStatus(v *tea.View) {
	v.ProgramStatus = m.programStatus()
	v.ProgramResume = m.programResume()
	if m.progressBarEnabled && m.sendProgressBar && m.isAgentBusy() {
		// HACK: use random percentage 到 prevent ghostty 从 hiding it
		// after 超时.
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, rand.Intn(100))
	}
}

// ShortHelp implements [help.KeyMap].
func (m *UI) ShortHelp() []key.Binding {
	var binds []key.Binding
	k := &m.keyMap

	// When inline editor 是 活跃, show 它的 help.
	if m.activeInline != nil {
		if m.focus == uiFocusEditor {
			return m.activeInline.ShortHelp()
		}
		return []key.Binding{
			m.inlineFocusHelp(),
			k.Chat.UpDown,
			k.Chat.PageUp,
			k.Chat.PageDown,
		}
	}

	tab := k.Tab
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
	case uiChat:
		// Show 取消 binding 如果 agent 是 busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp("esc", "press again to cancel")
			} else if m.promptQueue.ServerQueued() {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, cancelBinding)
		}

		switch m.focus {
		case uiFocusEditor:
			tab.SetHelp("tab", "focus chat")
		default:
			tab.SetHelp("tab", "focus editor")
		}

		binds = append(
			binds,
			tab,
			k.ShiftTab,
			commands,
			k.Models,
		)

		if m.canToggleSidebar() {
			binds = append(binds, k.Chat.ToggleSidebar)
		}

		switch m.focus {
		case uiFocusEditor:
			binds = append(
				binds,
				k.Editor.Newline,
			)
		case uiFocusSidebar:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.FocusChat,
			)
		case uiFocusMain:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.UpDownOneItem,
				k.Chat.PageUp,
				k.Chat.PageDown,
				k.Chat.Copy,
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue.Shown() > 0 {
				binds = append(binds, k.Chat.PillLeft)
			}
		}
	default:
		// TODO: other states
		// 如果 m.会话 == nil {
		// no 会话 selected
		binds = append(binds,
			k.Tab,
			commands,
			k.ShiftTab,
			k.Models,
			k.Editor.Newline,
		)
	}

	binds = append(
		binds,
		k.Quit,
		k.Help,
	)

	return binds
}

// FullHelp implements [help.KeyMap].
func (m *UI) FullHelp() [][]key.Binding {
	// When inline editor 是 活跃, show 它的 help.
	if m.activeInline != nil {
		if m.focus == uiFocusEditor {
			return [][]key.Binding{m.activeInline.ShortHelp()}
		}
		return [][]key.Binding{
			{m.inlineFocusHelp()},
			{
				m.keyMap.Chat.UpDown,
				m.keyMap.Chat.UpDownOneItem,
				m.keyMap.Chat.PageUp,
				m.keyMap.Chat.PageDown,
			},
			{
				m.keyMap.Chat.HalfPageUp,
				m.keyMap.Chat.HalfPageDown,
				m.keyMap.Chat.Home,
				m.keyMap.Chat.End,
			},
		}
	}

	var binds [][]key.Binding
	k := &m.keyMap
	help := k.Help
	help.SetHelp("ctrl+g", "less")
	hasAttachments := len(m.attachments.List()) > 0
	hasSession := m.hasSession()
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
	case uiChat:
		// Show 取消 binding 如果 agent 是 busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp("esc", "press again to cancel")
			} else if m.promptQueue.ServerQueued() {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, []key.Binding{cancelBinding})
		}

		mainBinds := []key.Binding{}
		tab := k.Tab
		switch m.focus {
		case uiFocusEditor:
			tab.SetHelp("tab", "focus chat")
		default:
			tab.SetHelp("tab", "focus editor")
		}

		mainBinds = append(
			mainBinds,
			tab,
			k.ShiftTab,
			commands,
			k.Models,
			k.Sessions,
			k.ToggleYolo,
		)
		if hasSession {
			mainBinds = append(mainBinds, k.Chat.NewSession, k.Chat.EndFollow)
		}
		if m.canToggleSidebar() {
			mainBinds = append(mainBinds, k.Chat.ToggleSidebar)
		}

		binds = append(binds, mainBinds)

		switch m.focus {
		case uiFocusEditor:
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
				k.Editor.Paste,
				k.Editor.SelectAll,
				k.Editor.CopySelection,
				k.Editor.CutSelection,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		case uiFocusSidebar:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
				},
				[]key.Binding{
					k.Chat.FocusChat,
				},
				[]key.Binding{
					k.Chat.Home,
					k.Chat.End,
				},
			)
		case uiFocusMain:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
					k.Chat.UpDownOneItem,
					k.Chat.PageUp,
					k.Chat.PageDown,
				},
				[]key.Binding{
					k.Chat.HalfPageUp,
					k.Chat.HalfPageDown,
					k.Chat.Home,
					k.Chat.End,
					k.Chat.EndFollow,
					k.Chat.FocusSidebar,
				},
				[]key.Binding{
					k.Chat.Copy,
					k.Chat.ClearHighlight,
				},
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue.Shown() > 0 {
				binds = append(binds, []key.Binding{k.Chat.PillLeft})
			}
		}
	default:
		if m.session == nil {
			// no 会话 selected
			binds = append(
				binds,
				[]key.Binding{
					k.ShiftTab,
					commands,
					k.Models,
					k.Sessions,
					k.ToggleYolo,
				},
			)
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
				k.Editor.Paste,
				k.Editor.SelectAll,
				k.Editor.CopySelection,
				k.Editor.CutSelection,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		}
	}

	binds = append(
		binds,
		[]key.Binding{
			help,
			k.Quit,
		},
	)

	return binds
}

// inlineFocusHelp returns Tab binding used 到 restore blurred inline
// editor. Collapsible editors provide 上下文-specific wording.
func (m *UI) inlineFocusHelp() key.Binding {
	tab := m.keyMap.Tab
	description := "focus editor"
	if collapsed, ok := m.activeInline.(dialog.CollapsibleInlineEditor); ok {
		description = collapsed.CollapsedHelp()
	}
	tab.SetHelp("tab", description)
	return tab
}

func (m *UI) currentModelSupportsImages() bool {
	cfg := m.com.Config()
	if cfg == nil {
		return false
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return false
	}
	model := cfg.GetModelByType(agentCfg.Model)
	return model != nil && model.Capabilities.Vision
}

func (m *UI) currentModelSupportsAudio() bool {
	cfg := m.com.Config()
	if cfg == nil {
		return false
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return false
	}
	model := cfg.GetModelByType(agentCfg.Model)
	return model != nil && model.Capabilities.Audio
}

// currentModelMaxAttachments returns maximum number 的 attachments
// current 模型 accepts 在 single 请求. Zero means 提供方 does
// 不 enforce hard limit.
func (m *UI) currentModelMaxAttachments() int {
	cfg := m.com.Config()
	if cfg == nil {
		return 0
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return 0
	}
	model := cfg.GetModelByType(agentCfg.Model)
	if model == nil {
		return 0
	}
	return model.MaxAttachments
}

// attachmentsLimitReached reports whether current 模型's 附件
// limit 是 already exhausted 通过 pending attachments.
func (m *UI) attachmentsLimitReached() bool {
	max := m.currentModelMaxAttachments()
	return max > 0 && len(m.attachments.List()) >= max
}

// toggleCompactMode toggles compact mode between uiChat 和 uiChatCompact states.
func (m *UI) toggleCompactMode() tea.Cmd {
	m.forceCompactMode = !m.forceCompactMode

	err := m.com.Workspace.SetCompactMode(config.ScopeGlobal, m.forceCompactMode)
	if err != nil {
		return util.ReportError(err)
	}

	var cmds []tea.Cmd
	if m.forceCompactMode && m.focus == uiFocusSidebar {
		// sidebar 是 going away, 所以 focus editor again 到 keep key
		// events routed somewhere useful.
		m.sidebarScrollbarVisible = false
		if m.activeInline != nil {
			m.focusActiveInline(uiFocusEditor)
		} else {
			m.focus = uiFocusEditor
			cmds = append(cmds, m.textarea.Focus())
		}
	}

	m.updateLayoutAndSize()

	return tea.Batch(cmds...)
}

// canToggleSidebar reports whether sidebar can 是 shown right now, i.e.
// chat 会话 是 活跃 和 终态 是 large enough 为 full
// layout.
func (m *UI) canToggleSidebar() bool {
	return m.state == uiChat && m.hasSession() &&
		m.width >= compactModeWidthBreakpoint &&
		m.height >= compactModeHeightBreakpoint
}

// updateLayoutAndSize updates layout 和 sizes 的 UI components.
func (m *UI) updateLayoutAndSize() {
	// Determine 如果 we should 是 在 compact mode
	if m.state == uiChat {
		if m.forceCompactMode {
			m.isCompact = true
		} else if m.width < compactModeWidthBreakpoint || m.height < compactModeHeightBreakpoint {
			m.isCompact = true
		} else {
			m.isCompact = false
		}
	}

	// First pass sizes components 从 their current heights.
	previousInlineHeight := -1
	if m.activeInline != nil {
		previousInlineHeight = m.activeInline.Height(m.editorContentWidth())
	}
	m.layout = m.generateLayout(m.width, m.height)
	prevHeight := m.textarea.Height()
	m.updateSize()

	// SetWidth can change textarea 或 inline-editor height due 到 soft-wrap
	// recalculation. If 那 happens, reconcile once 用 new height.
	inlineHeightChanged := m.activeInline != nil &&
		m.activeInline.Height(m.editorContentWidth()) != previousInlineHeight
	if m.textarea.Height() != prevHeight || inlineHeightChanged {
		m.layout = m.generateLayout(m.width, m.height)
		m.updateSize()
	}
}

// handleTextareaHeightChange checks whether textarea height changed 和,
// 如果 所以, recalculates layout. When chat 是 在 follow mode it keeps
// view scrolled 到 bottom. returned command, 如果 non-nil, must 是
// batched 通过 caller.
func (m *UI) handleTextareaHeightChange(prevHeight int) tea.Cmd {
	if m.textarea.Height() == prevHeight {
		return nil
	}
	m.updateLayoutAndSize()
	if m.state == uiChat && m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
	return nil
}

// updateTextarea updates textarea 为 msg 和 then reconciles layout 如果
// textarea height changed 作为 结果.
func (m *UI) updateTextarea(msg tea.Msg) tea.Cmd {
	return m.updateTextareaWithPrevHeight(msg, m.textarea.Height())
}

// forwardMouseToTextarea forwards mouse 事件 到 textarea 用
// coordinates translated into textarea's local space. 它 reports whether
// 事件 landed within textarea's rendered region 和 was forwarded.
func (m *UI) forwardMouseToTextarea(msg tea.MouseMsg) (bool, tea.Cmd) {
	mouse := msg.Mouse()

	// textarea 是 rendered inside layout.editor below attachments
	// 行. renderEditorView always reserves first 行 为 attachments
	// ( empty line 当 there 是 none), 所以 textarea always starts
	// one 行 below editor top.
	const attachmentsRow = 1
	origin := image.Pt(m.layout.editor.Min.X, m.layout.editor.Min.Y+attachmentsRow)

	// textarea occupies 它的 own height starting 在 origin.
	area := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(m.layout.editor.Dx(), m.textarea.Height()))}
	if !image.Pt(mouse.X, mouse.Y).In(area) {
		return false, nil
	}

	rel := tea.Mouse{
		X:      mouse.X - origin.X,
		Y:      mouse.Y - origin.Y,
		Button: mouse.Button,
		Mod:    mouse.Mod,
	}

	switch msg.(type) {
	case tea.MouseClickMsg:
		if rel.Button != uv.MouseLeft {
			return false, nil
		}
		m.textareaMouseSelecting = true
		m.textarea.BeginSelection(rel.X, rel.Y)
		return true, nil
	case tea.MouseMotionMsg:
		if !m.textareaMouseSelecting {
			return true, nil
		}
		m.textarea.ExtendSelection(rel.X, rel.Y)
		return true, nil
	case tea.MouseReleaseMsg:
		m.textarea.EndSelection()
		m.textareaMouseSelecting = false
		return true, nil
	default:
		return false, nil
	}
}

// updateTextareaWithPrevHeight 是 为 cases 当 height 的 layout may
// have changed.
//
// Particularly, it's 为 cases where textarea changes before
// textarea.更新 是 called (为 example, SetValue, Reset, 和 InsertRune). We
// pass height 从 before those changes took place 所以 we can compare
// "before" vs "after" sizing 和 recalculate layout 如果 textarea grew
// 或 shrank.
func (m *UI) updateTextareaWithPrevHeight(msg tea.Msg, prevHeight int) tea.Cmd {
	ta, cmd := m.textarea.Update(msg)
	m.textarea = ta
	return tea.Batch(cmd, m.handleTextareaHeightChange(prevHeight))
}

// updateSize updates sizes 的 UI components based 在 current layout.
func (m *UI) updateSize() {
	m.invalidateFrames()

	// Set status width
	m.status.SetWidth(m.layout.status.Dx())

	m.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
	m.textarea.MaxHeight = TextareaMaxHeight
	m.textarea.SetWidth(m.layout.editor.Dx())
	if resizable, ok := m.activeInline.(dialog.ResizableInlineEditor); ok {
		resizable.SetWidth(m.layout.editor.Dx())
	}
	m.renderPills()

	// Handle different app states
	switch m.state {
	case uiChat:
		if !m.isCompact {
			m.cacheSidebarLogo(m.layout.sidebar.Dx())
		}
	}
}

// generateLayout calculates layout rectangles 为 all UI components based
// 在 current UI state 和 终态 dimensions.
func (m *UI) generateLayout(w, h int) uiLayout {
	// screen area we're working 用
	area := image.Rect(0, 0, w, h)

	// The help height
	helpHeight := 1
	// editor height: textarea height + margin 为 attachments 和 bottom spacing.
	// When inline editor 是 活跃, use 它的 height instead.
	editorHeight := m.textarea.Height() + editorHeightMargin
	if m.activeInline != nil {
		// editor content width depends 只 在 终态 width
		// 和 layout (不 在 editor height), 所以 passing current
		// frame's width 到 Height() keeps layout 在 sync 用
		// width Draw 将 use, preventing flicker during fast resize.
		editorWidth := m.editorContentWidth()
		if collapsed, ok := m.collapsedInlineEditor(); ok {
			editorHeight = collapsed.CollapsedHeight() + 1
		} else {
			editorHeight = m.activeInline.Height(editorWidth)
		}
	}
	// The sidebar width
	sidebarWidth := 32
	// 请求头 height
	const landingHeaderHeight = 4

	var helpKeyMap help.KeyMap = m
	if m.status != nil && m.status.ShowingAll() {
		for _, row := range helpKeyMap.FullHelp() {
			helpHeight = max(helpHeight, len(row))
		}
	}

	// Add app margins
	var appRect, helpRect image.Rectangle
	layout.Vertical(
		layout.Len(area.Dy()-helpHeight),
		layout.Fill(1),
	).Split(area).Assign(&appRect, &helpRect)
	appRect.Min.Y += 1
	appRect.Max.Y -= 1
	helpRect.Min.Y -= 1
	appRect.Min.X += 1
	appRect.Max.X -= 1

	if slices.Contains([]uiState{uiOnboarding, uiInitialize, uiLanding}, m.state) {
		// extra padding 在 left 和 right 为 these states
		appRect.Min.X += 1
		appRect.Max.X -= 1
	}

	uiLayout := uiLayout{
		area:   area,
		status: helpRect,
	}

	// Handle different app states
	switch m.state {
	case uiOnboarding, uiInitialize:
		// Layout
		//
		// 请求头
		// ------
		// main
		// ------
		// help

		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		uiLayout.header = headerRect
		uiLayout.main = mainRect

	case uiLanding:
		// Layout
		//
		// 请求头
		// ------
		// main
		// ------
		// editor
		// ------
		// help
		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		var editorRect image.Rectangle
		layout.Vertical(
			layout.Len(mainRect.Dy()-editorHeight),
			layout.Fill(1),
		).Split(mainRect).Assign(&mainRect, &editorRect)
		// Remove extra padding 从 editor (但 keep it 为 header 和 main)
		editorRect.Min.X -= 1
		editorRect.Max.X += 1
		uiLayout.header = headerRect
		uiLayout.main = mainRect
		uiLayout.editor = editorRect

	case uiChat:
		if m.isCompact {
			// Layout
			//
			// compact-请求头
			// ------
			// main
			// ------
			// editor
			// ------
			// help
			const compactHeaderHeight = 1
			var headerRect, mainRect image.Rectangle
			layout.Vertical(
				layout.Len(compactHeaderHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&headerRect, &mainRect)
			detailsHeight := min(sessionDetailsMaxHeight, area.Dy()-1) // One row for the header
			var sessionDetailsArea image.Rectangle
			layout.Vertical(
				layout.Len(detailsHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&sessionDetailsArea, new(image.Rectangle))
			uiLayout.sessionDetails = sessionDetailsArea
			uiLayout.sessionDetails.Min.Y += compactHeaderHeight // adjust for header
			// Add one line gap between header 和 main content
			mainRect.Min.Y += 1
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.header = headerRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin 到 main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		} else {
			// Layout
			//
			// ------|---
			// main  |
			// ------| side
			// editor|
			// ------|
			// help  |

			var mainRect, sideRect image.Rectangle
			layout.Horizontal(
				layout.Len(appRect.Dx()-sidebarWidth),
				layout.Fill(1),
			).Split(appRect).Assign(&mainRect, &sideRect)
			sideRect.Max.Y = helpRect.Max.Y - 1
			uiLayout.status.Max.X = mainRect.Max.X
			// Add padding left
			sideRect.Min.X += 1
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.sidebar = sideRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin 到 main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		}
	}

	return uiLayout
}

// uiLayout defines positioning 的 UI elements.
type uiLayout struct {
	// area 是 overall available area.
	area uv.Rectangle

	// header 是 header shown 在 special cases
	// e.x 当 sidebar 是 collapsed
	// 或 当 在 landing page
	// 或 在 init/配置
	header uv.Rectangle

	// main 是 area 为 main pane. (e.x chat, configure, landing)
	main uv.Rectangle

	// pills 是 area 为 pills panel.
	pills uv.Rectangle

	// editor 是 area 为 editor pane.
	editor uv.Rectangle

	// sidebar 是 area 为 sidebar.
	sidebar uv.Rectangle

	// status 是 area 为 status view.
	status uv.Rectangle

	// 会话 details 是 area 为 会话 details overlay 在 compact mode.
	sessionDetails uv.Rectangle
}

func (m *UI) openEditor(value string) tea.Cmd {
	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	tmpPath := tmpfile.Name()
	defer tmpfile.Close() //nolint:errcheck
	if _, err := tmpfile.WriteString(value); err != nil {
		return util.ReportError(err)
	}
	cmd, err := editor.Command(
		"evo",
		tmpPath,
		editor.AtPosition(
			m.textarea.Line()+1,
			m.textarea.Column()+1,
		),
	)
	if err != nil {
		return util.ReportError(err)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer func() {
			_ = os.Remove(tmpPath)
		}()

		if err != nil {
			return util.ReportError(err)
		}
		return editorFileMsg(tmpPath)
	})
}

// editorFileMsg reads 文件 external editor was supposed 到 写入 和
// turns it into 消息 那 replaces composer 文本. An emptied 缓冲
// yields empty 文本, which clears composer just like deleting every
// character 在-app does.
func editorFileMsg(path string) tea.Msg {
	content, err := os.ReadFile(path)
	if err != nil {
		return util.ReportError(err)
	}
	return openEditorMsg{
		Text: strings.TrimSpace(string(content)),
	}
}

// setPermissionMode writes mode 到 工作区 和 through
// 权限-mode 缓存 (no re-probe needed), then refreshes editor
// 提示词. Every 权限-mode 写入 goes through here, 所以 缓存 是
// never left trailing 工作区.
func (m *UI) setPermissionMode(mode permission.PermissionMode) {
	m.com.Workspace.PermissionSetMode(mode)
	m.permModeCache.set(mode)
	// Supersede any 在-flight busy/权限 probe: 它的 结果 carries
	// old generation 和 would otherwise overwrite 值 we just wrote.
	// Bump the generation (rather than invalidateBusyCaches, which would
	// clear fresh 值) 所以 applyBusyState's guard discards 和
	// re-dispatches the stale probe.
	m.busyFetchGen++
	m.setEditorPrompt(mode)
}

// setYolo turns YOLO 在 或 off. Turning it off goes all way back 到
// normal, 所以 sysadmin 会话 那 declines YOLO really does get 它的
// prompts back rather than landing one rung down.
func (m *UI) setYolo(on bool) {
	mode := permission.PermissionModeNormal
	if on {
		mode = permission.PermissionModeYolo
	}
	m.setPermissionMode(mode)
}

// toggleMode flips 权限 mode between target 和 Normal. 它 reports
// whether target 是 now 活跃 mode.
func (m *UI) toggleMode(target permission.PermissionMode) (enabled bool) {
	mode := permission.PermissionModeNormal
	if m.com.Workspace.PermissionMode() != target {
		mode = target
		enabled = true
	}
	m.setPermissionMode(mode)
	return enabled
}

// permissionModeName 是 human-readable name 为 权限 mode.
func permissionModeName(mode permission.PermissionMode) string {
	switch mode {
	case permission.PermissionModeYolo:
		return "yolo"
	case permission.PermissionModeSysadmin:
		return "sysadmin"
	default:
		return "normal"
	}
}

// toggleModeAndReport flips 权限 mode 和 announces where it
// landed. Reaching mode 从 command palette shows same badge
// banner 作为 reaching it 从 Shift+Tab cycle, 所以 one mode does 不
// announce itself two different ways depending 在 road 在. Dropping
// back 到 normal 是 ordinary news 和 reads 作为 such: it names mode
// rather than saying "disabled", which 是 lie 当 toggle landed
// somewhere rather than switching something off.
func (m *UI) toggleModeAndReport(target permission.PermissionMode) tea.Cmd {
	if !m.toggleMode(target) {
		return util.ReportInfo("Permission mode: " + permissionModeName(permission.PermissionModeNormal))
	}
	switch target {
	case permission.PermissionModeSysadmin:
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeSysadmin, Msg: sysadminModeBannerMsg})
	case permission.PermissionModeYolo:
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeYolo, Msg: yoloModeBannerMsg})
	default:
		return util.ReportInfo("Permission mode: " + permissionModeName(target))
	}
}

// setEditorPrompt configures textarea 提示词 function based 在 current
// 权限 mode, 或 whether plan 或 bang mode 是 enabled.
func (m *UI) setEditorPrompt(mode permission.PermissionMode) {
	if m.bangMode {
		m.textarea.SetPromptFunc(4, m.bangPromptFunc)
		return
	}
	if m.mode == uiInputModePlan {
		m.textarea.SetPromptFunc(4, m.planPromptFunc)
		return
	}
	switch mode {
	case permission.PermissionModeSysadmin:
		m.textarea.SetPromptFunc(4, m.sysadminPromptFunc)
	case permission.PermissionModeYolo:
		m.textarea.SetPromptFunc(4, m.yoloPromptFunc)
	default:
		m.textarea.SetPromptFunc(4, m.normalPromptFunc)
	}
}

// normalPromptFunc returns normal editor 提示词 style ("> " 在
// first line, "::: " 在 subsequent lines).
func (m *UI) normalPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptNormalIconFocused.Render()
		}
		return "::: "
	}
	if info.Focused {
		return t.Editor.PromptNormalFocused.Render()
	}
	return t.Editor.PromptNormalBlurred.Render()
}

// planPromptFunc marks planning 用 badge beside editor.
func (m *UI) planPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptPlanIconFocused.Render()
		}
		return t.Editor.PromptPlanIconBlurred.Render()
	}
	if info.Focused {
		return t.Editor.PromptPlanDotsFocused.Render()
	}
	return t.Editor.PromptPlanDotsBlurred.Render()
}

// yoloPromptFunc returns yolo mode editor 提示词 style 用 warning icon
// 和 colored dots.
func (m *UI) yoloPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptYoloIconFocused.Render()
		} else {
			return t.Editor.PromptYoloIconBlurred.Render()
		}
	}
	if info.Focused {
		return t.Editor.PromptYoloDotsFocused.Render()
	}
	return t.Editor.PromptYoloDotsBlurred.Render()
}

// bangPromptFunc returns bang mode editor 提示词 style 用 Turtle-colored
// icon 和 dots.
func (m *UI) bangPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptBangIconFocused.Render()
		}
		return t.Editor.PromptBangIconBlurred.Render()
	}
	if info.Focused {
		return t.Editor.PromptBangDotsFocused.Render()
	}
	return t.Editor.PromptBangDotsBlurred.Render()
}

// toggleInputMode advances the Shift+Tab cycle (code -> plan -> YOLO -> code),
// reading position off current state. Sysadmin 是 palette-只.
func (m *UI) toggleInputMode() tea.Cmd {
	if m.isAgentBusy() || m.modeSwitching {
		return util.ReportWarn("Agent is busy, please wait before switching input mode...")
	}
	if m.mode == uiInputModePlan {
		// Second step: plan -> YOLO coding. 一个 permissive mode carried into
		// plan keeps 它的 rung rather than being knocked down 到 plain YOLO.
		if !m.yoloActive() {
			m.setYolo(true)
		}
		return m.setInputMode(uiInputModeCode)
	}
	if m.yoloActive() {
		// Third step: YOLO, 或 palette-chosen sysadmin, back 到 code.
		m.setYolo(false)
		return util.ReportInfo("input mode: code")
	}
	return m.setInputMode(uiInputModePlan)
}

// switchPlanToYolo handles activating YOLO while 在 plan mode: YOLO 是
// coding concern, 所以 instead 的 "plan + yolo" state UI switches
// straight 到 coder 用 YOLO enabled. Activation 是 idempotent — YOLO
// carried into plan mode stays 在, 和 用户 ends up 在 full YOLO mode
// either way.
func (m *UI) switchPlanToYolo() tea.Cmd {
	if m.isAgentBusy() || m.modeSwitching {
		return util.ReportWarn("Agent is busy, please wait before switching input mode...")
	}
	// Idempotent 通过 design: sysadmin 会话 carried into plan mode keeps
	// 它的 mode rather than being knocked down 到 plain YOLO.
	if !m.yoloActive() {
		m.setYolo(true)
	}
	return m.setInputMode(uiInputModeCode)
}

// Mode banner copy shown 在 status bar after switching modes.
const (
	planModeBannerMsg     = "Plan with Crush before generating any code."
	yoloModeBannerMsg     = "System level commands stay blocked. The rest run unprompted."
	sysadminModeBannerMsg = "All commands are unblocked. Use wisely."
)

func (m *UI) setInputMode(target uiInputMode) tea.Cmd {
	agentID := config.AgentPlan
	if target == uiInputModeCode {
		agentID = config.AgentCoder
	}

	// 权限 mode 是 orthogonal 到 input mode, 所以 报告 it
	// alongside mode instead 的 treating YOLO-enabled coder 作为 plain
	// "code".
	perm := permission.PermissionModeNormal
	if target == uiInputModeCode {
		perm = m.permModeCached()
	}

	// agent switch 是 HTTP round-trip 在 客户端/服务端 mode, 所以 it
	// runs off 更新 loop together 用 模型 更新. mode 和
	// editor 提示词 只 change once switch succeeds (applyModeSwitch),
	// 所以 failed switch never leaves editor claiming mode
	// 服务端's 活跃 agent does 不 match.
	m.modeSwitching = true
	return func() tea.Msg {
		err := m.com.Workspace.AgentSetMain(agentID)
		if err == nil {
			err = m.com.Workspace.UpdateAgentModel(context.Background())
		}
		return modeSwitchedMsg{
			mode: target,
			perm: perm,
			err:  err,
		}
	}
}

// applyModeSwitch finalizes an input-mode switch once the backend has
// settled. On 错误 previous mode 是 kept 所以 editor never claims
// mode 服务端's 活跃 agent does 不 match.
func (m *UI) applyModeSwitch(msg modeSwitchedMsg) []tea.Cmd {
	if msg.err != nil {
		return []tea.Cmd{util.ReportError(msg.err)}
	}
	m.mode = msg.mode
	m.modeAdopted = false
	// Supersede any 在-flight busy probe: it may have 读取 agent 从
	// before 这 switch, 和 followMainAgent would then switch back.
	m.busyFetchGen++
	m.setEditorPrompt(m.permModeCached())
	var cmds []tea.Cmd
	if msg.continueSessionID != "" && m.session != nil && m.session.ID == msg.continueSessionID {
		cmds = append(cmds, m.sendMessageInternal("Implement the plan.", true))
	}
	switch {
	case msg.mode == uiInputModePlan:
		cmds = append(cmds, util.CmdHandler(util.InfoMsg{Type: util.InfoTypePlan, Msg: planModeBannerMsg}))
	case msg.perm == permission.PermissionModeSysadmin:
		cmds = append(cmds, util.CmdHandler(util.InfoMsg{Type: util.InfoTypeSysadmin, Msg: sysadminModeBannerMsg}))
	case msg.perm == permission.PermissionModeYolo:
		cmds = append(cmds, util.CmdHandler(util.InfoMsg{Type: util.InfoTypeYolo, Msg: yoloModeBannerMsg}))
	default:
		cmds = append(cmds, util.ReportInfo("input mode: code"))
	}
	return cmds
}

// modeSwitchedMsg reports 那 async agent switch started 通过
// setInputMode has finished (successfully 或 不).
type modeSwitchedMsg struct {
	continueSessionID string
	mode              uiInputMode
	perm              permission.PermissionMode
	err               error
}

// followMainAgent adopts input mode 的 工作区's main agent 和
// reports whether mode changed. Every 客户端 的 服务端 工作区
// shares 那 agent, 所以 这 是 how switch another 客户端 made shows
// here. 它 never switches agent itself, 和 it leaves mode alone
// while 这 客户端's own switch 是 在 flight 或 当 agent 是 unknown,
// 作为 用 服务端 too old 到 报告 it.
func (m *UI) followMainAgent(agent string) bool {
	if m.modeSwitching {
		return false
	}
	mode := m.mode
	switch agent {
	case config.AgentCoder:
		mode = uiInputModeCode
	case config.AgentPlan:
		mode = uiInputModePlan
	}
	if mode == m.mode {
		return false
	}
	m.mode = mode
	m.modeAdopted = true
	return true
}

// sysadminPromptFunc returns sysadmin mode editor 提示词 style 用 red
// warning icon 和 red dots.
func (m *UI) sysadminPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptSysadminIconFocused.Render()
		} else {
			return t.Editor.PromptSysadminIconBlurred.Render()
		}
	}
	if info.Focused {
		return t.Editor.PromptSysadminDotsFocused.Render()
	}
	return t.Editor.PromptSysadminDotsBlurred.Render()
}

// closeCompletions closes completions popup 和 resets state.
func (m *UI) closeCompletions() {
	m.completionsOpen = false
	m.completionsQuery = ""
	m.completionsStartIndex = 0
	m.completions.Close()
}

// insertCompletionText replaces @查询 在 textarea 用 given 文本.
// Returns false 如果 replacement cannot 是 performed.
func (m *UI) insertCompletionText(text string) bool {
	value := m.textarea.Value()
	if m.completionsStartIndex > len(value) {
		return false
	}

	word := m.textareaWord()
	endIdx := min(m.completionsStartIndex+len(word), len(value))
	newValue := value[:m.completionsStartIndex] + text + value[endIdx:]
	m.textarea.SetValue(newValue)
	m.textarea.MoveToEnd()
	m.textarea.InsertRune(' ')
	return true
}

// insertFileCompletion inserts selected 文件 路径 into textarea,
// replacing @查询, 和 adds 文件 作为 附件.
func (m *UI) insertFileCompletion(path string) tea.Cmd {
	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(path) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	fileCmd := func() tea.Msg {
		if !m.currentModelSupportsImages() && common.IsImagePath(path) {
			return util.NewWarnMsg("The current model does not support image attachments")
		}
		if !m.currentModelSupportsAudio() && common.IsAudioPath(path) {
			return util.NewWarnMsg("The current model does not support audio attachments")
		}

		absPath, _ := filepath.Abs(path)

		if m.hasSession() {
			// Skip 附件 如果 文件 was already 读取 和 hasn't been modified.
			lastRead := m.com.Workspace.FileTrackerLastReadTime(context.Background(), m.session.ID, absPath)
			if !lastRead.IsZero() {
				if info, err := os.Stat(path); err == nil && !info.ModTime().After(lastRead) {
					return nil
				}
			}
		} else if slices.Contains(m.sessionFileReads, absPath) {
			return nil
		}

		m.sessionFileReads = append(m.sessionFileReads, absPath)

		// Add 文件 作为 附件.
		content, err := os.ReadFile(path)
		if err != nil {
			// If it fails, let the LLM handle it later.
			return nil
		}

		return message.Attachment{
			FilePath: path,
			FileName: filepath.Base(path),
			MimeType: mimeOf(content),
			Content:  content,
		}
	}
	return tea.Batch(heightCmd, fileCmd)
}

// insertMCPResourceCompletion inserts the selected resource into the textarea,
// replacing @查询, 和 adds resource 作为 附件.
func (m *UI) insertMCPResourceCompletion(item completions.ResourceCompletionValue) tea.Cmd {
	displayText := cmp.Or(item.Title, item.URI)

	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(displayText) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	resourceCmd := func() tea.Msg {
		contents, err := m.com.Workspace.ReadMCPResource(
			context.Background(),
			item.MCPName,
			item.URI,
		)
		if err != nil {
			slog.Warn("Failed to read MCP resource", "uri", item.URI, "error", err)
			return nil
		}
		if len(contents) == 0 {
			return nil
		}

		content := contents[0]
		var data []byte
		if content.Text != "" {
			data = []byte(content.Text)
		} else if len(content.Blob) > 0 {
			data = content.Blob
		}
		if len(data) == 0 {
			return nil
		}

		mimeType := item.MIMEType
		if mimeType == "" && content.MIMEType != "" {
			mimeType = content.MIMEType
		}
		if mimeType == "" {
			mimeType = "text/plain"
		}

		if !m.currentModelSupportsImages() && strings.HasPrefix(mimeType, "image/") {
			return util.NewWarnMsg("The current model does not support image attachments")
		}
		if !m.currentModelSupportsAudio() && strings.HasPrefix(mimeType, "audio/") {
			return util.NewWarnMsg("The current model does not support audio attachments")
		}

		return message.Attachment{
			FilePath: item.URI,
			FileName: displayText,
			MimeType: mimeType,
			Content:  data,
		}
	}
	return tea.Batch(heightCmd, resourceCmd)
}

// completionsPosition returns X 和 Y position 为 completions popup.
func (m *UI) completionsPosition() image.Point {
	cur := m.textarea.Cursor()
	if cur == nil {
		return image.Point{
			X: m.layout.editor.Min.X,
			Y: m.layout.editor.Min.Y,
		}
	}
	return image.Point{
		X: cur.X + m.layout.editor.Min.X,
		Y: m.layout.editor.Min.Y + cur.Y,
	}
}

// textareaWord returns current word 在 cursor position.
func (m *UI) textareaWord() string {
	return m.textarea.Word()
}

// isWhitespace returns true 如果 byte 是 whitespace character.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isAgentBusy returns true 如果 agent coordinator exists 和 是 currently
// busy processing 请求. 它 只 reads memoized state (it runs 在
// per-消息 paths like textarea placeholder, where 工作区 probe
// would 是 HTTP round-trip per keystroke 在 客户端/服务端 mode);
// 值 是 refreshed off-thread, see workspace_cache.go.
func (m *UI) isAgentBusy() bool {
	if m.bangCancel != nil {
		return true
	}
	return m.agentBusyCache.val
}

// nextMessageNote tells 用户 模型 change made mid-运行 skips
// running turn.
func (m *UI) nextMessageNote() string {
	if m.isAgentBusy() {
		return " (applies to your next message)"
	}
	return ""
}

// hasSession returns true 如果 there 是 活跃 会话 用 valid ID.
func (m *UI) hasSession() bool {
	return m.session != nil && m.session.ID != ""
}

// CurrentSession returns 活跃 会话, 或 nil 当 there 是 none.
// 它 是 safe 到 调用 after TUI has exited.
func (m *UI) CurrentSession() *session.Session {
	return m.session
}

// mimeOf detects MIME type 的 given content.
func mimeOf(content []byte) string {
	mimeBufferSize := min(512, len(content))
	return http.DetectContentType(content[:mimeBufferSize])
}

var readyPlaceholders = [...]string{
	"Ready!",
	"Ready...",
	"Ready?",
	"Ready for instructions",
}

var workingPlaceholders = [...]string{
	"Working!",
	"Working...",
	"Brrrrr...",
	"Prrrrrrrr...",
	"Processing...",
	"Thinking...",
}

// randomizePlaceholders selects random placeholder 文本 为 textarea's
// ready 和 working states.
func (m *UI) randomizePlaceholders() {
	m.workingPlaceholder = workingPlaceholders[rand.Intn(len(workingPlaceholders))]
	m.readyPlaceholder = readyPlaceholders[rand.Intn(len(readyPlaceholders))]
}

// renderEditorView renders editor view 用 attachments 如果 any.
func (m *UI) renderEditorView(width int) string {
	var attachmentsView string
	if len(m.attachments.List()) > 0 {
		attachmentsView = m.attachments.Render(width)
	}
	return strings.Join([]string{
		attachmentsView,
		m.textarea.View(),
		"", // margin at bottom of editor
	}, "\n")
}

// cacheSidebarLogo renders 和 caches sidebar logo 在 specified width.
func (m *UI) cacheSidebarLogo(width int) {
	m.sidebarLogo = renderLogo(m.com.Styles, true, m.com.IsHyper(), width)
}

// applyThemeForProvider swaps 活跃 theme 到 one associated 用
// given 提供方, 但 只 当 那 theme differs 从 one
// already applied. Most providers share single theme, 所以 re-selecting
// 模型 从 same theme family would otherwise pay full cost 的
// invalidating markdown renderer 缓存 和 re-rendering entire
// transcript 为 no visible change.
// 一个 theme explicitly selected 在 配置 always wins, 所以 提供方
// changes never discard 用户's choice. An 活跃 host theme wins too:
// 提供方's theme 是 只 recorded, 到 revert 到 once host clears
// 它的 theme.
func (m *UI) applyThemeForProvider(providerID string) {
	// 一个 theme 用户 explicitly selected always wins over
	// per-提供方 默认, 所以 提供方 或 会话 changes never discard
	// their choice. 在-memory flag covers 客户端/服务端 mode, where
	// 配置 round-trip may 不 surface selection right away.
	if m.userThemeSelected || common.ThemeNameFromConfig(m.com.Config()) != "" {
		return
	}
	key := styles.ThemeKeyForProvider(providerID)
	if key == m.themeKey {
		return
	}
	m.themeKey = key
	if m.hostTheme.active {
		return
	}
	m.applyTheme(styles.ThemeForKey(key))
}

// applyTheme replaces 活跃 styles 用 given theme, drops
// shared style caches, 和 refreshes every component 那 caches style
// data.
func (m *UI) applyTheme(s styles.Styles) {
	*m.com.Styles = s
	common.InvalidateStyleCaches()
	m.refreshStyles()
	m.chat.Restyle()
}

// previewTheme shows theme 从 open theme dialog, saving theme 在
// effect beforehand 所以 closing dialog can restore it.
func (m *UI) previewTheme(s styles.Styles) {
	if m.preThemeStyles == nil {
		saved := m.com.Styles.Clone()
		m.preThemeStyles = &saved
	}
	m.applyTheme(s)
}

// refreshStyles pushes the current *m.com.Styles into every subcomponent
// 那 copies 或 pre-renders style-dependent values 在 construction time.
// Callers 是 responsible 为 invalidating chat render caches.
func (m *UI) refreshStyles() {
	t := m.com.Styles
	m.header.refresh()
	if m.layout.sidebar.Dx() > 0 {
		m.cacheSidebarLogo(m.layout.sidebar.Dx())
	}
	m.textarea.SetStyles(t.Editor.Textarea)
	m.completions.SetStyles(t.Completions.Normal, t.Completions.Focused, t.Completions.Match)
	m.attachments.SetStyles(t.Attachments)
	m.todoSpinner.Style = t.Pills.TodoSpinner
	m.status.help.Styles = t.Help
	m.dialog.RefreshStyles()
	if r, ok := m.activeInline.(dialog.StyleRefresher); ok {
		r.RefreshStyles()
	}
}

// attachSkill reads skill's content 通过 ID 和 returns it 作为 markdown
// 附件 到 是 added 到 附件 toolbar. 用户 can then
// compose 消息 和 send it 用 skill attached.
// name parameter 是 used 作为 fallback 当 服务端 does 不
// return one.
func (m *UI) attachSkill(skillID, name string) tea.Cmd {
	return func() tea.Msg {
		content, result, err := m.com.Workspace.ReadSkill(context.Background(), skillID)
		if err != nil {
			return util.NewErrorMsg(err)
		}
		fileName := result.Name
		if fileName == "" {
			fileName = name
		}
		return message.Attachment{
			FilePath: fileName,
			FileName: fileName,
			MimeType: "text/markdown",
			Content:  content,
		}
	}
}

// openThemeNewDialog opens the new theme naming dialog. The new theme
// inherits 它的 palette 从 currently 活跃 theme.
func (m *UI) openThemeNewDialog() {
	if m.dialog.ContainsDialog(dialog.ThemeNewID) {
		m.dialog.BringToFront(dialog.ThemeNewID)
		return
	}
	base := common.ThemeNameFromConfig(m.com.Config())
	m.dialog.OpenDialog(dialog.NewThemeNew(m.com, base))
}

// openThemeDialog opens the theme picker dialog.
func (m *UI) openThemeDialog() {
	if m.dialog.ContainsDialog(dialog.ThemeID) {
		m.dialog.BringToFront(dialog.ThemeID)
		return
	}
	themeDialog := dialog.NewTheme(m.com)
	m.dialog.OpenDialog(themeDialog)
}

// openThemeEditorDialog opens theme editor dialog 为 given theme.
// An empty themeName edits currently 活跃 theme.
func (m *UI) openThemeEditorDialog(themeName string) {
	if m.dialog.ContainsDialog(dialog.ThemeEditorID) {
		m.dialog.BringToFront(dialog.ThemeEditorID)
		return
	}
	themeDialog := dialog.NewThemeEditor(m.com, themeName)
	m.dialog.OpenDialog(themeDialog)
}

// ensureSession makes sure 会话 是 活跃, creating one 如果 none 是. 它
// returns command 那 loads freshly created 会话 (nil 当 会话
// already existed) 和 错误 如果 creation failed. 它 mutates UI state, 所以
// callers must 运行 在 更新 goroutine.
func (m *UI) ensureSession() (tea.Cmd, error) {
	if m.hasSession() {
		return nil, nil
	}
	newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
	if err != nil {
		return nil, err
	}
	if m.forceCompactMode {
		m.isCompact = true
	}
	var cmd tea.Cmd
	if newSession.ID != "" {
		m.session = &newSession
		cmd = m.loadSession(newSession.ID)
	}
	m.setState(uiChat, m.focus)
	return cmd, nil
}

// sendMessage sends 消息 用 given content 和 attachments.
func (m *UI) sendMessage(content string, attachments ...message.Attachment) tea.Cmd {
	return m.sendMessageInternal(content, false, attachments...)
}

// sendMessageInternal can hide generated continuation 从 chat.
func (m *UI) sendMessageInternal(content string, hidden bool, attachments ...message.Attachment) tea.Cmd {
	if err := m.com.Workspace.AgentReadyErr(); err != nil {
		return util.ReportError(err)
	}

	// Any new 提示词 supersedes pending, unconfirmed plan.
	m.setPlanReadyPending("")
	m.turnOutcome = ""

	var cmds []tea.Cmd
	loadCmd, err := m.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	if loadCmd != nil {
		cmds = append(cmds, loadCmd)
	}

	// 一个 queued 提示词 belongs 在 队列 pill 和 keeps running
	// turn's spinner 和 clock. 一个 fresh turn gets 它的 own, 从 now.
	sessionID := m.currentSessionID()
	queued := m.turnInFlight(sessionID)
	// Loading 空闲 会话 freezes ghost spinners; sending unfreezes.
	m.animClock.frozen = false
	if !queued {
		common.StartTurn(sessionID)
		echo := content
		echoAttachments := attachments
		if hidden {
			echo, echoAttachments = "", nil
		}
		if m.showPendingTurn(sessionID, echo, echoAttachments) {
			m.chat.ScrollToBottom()
		}
	}

	ctx := context.Background()
	cmds = append(cmds, func() tea.Msg {
		for _, path := range m.sessionFileReads {
			m.com.Workspace.FileTrackerRecordRead(ctx, m.session.ID, path)
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	})

	// sessionID 是 captured above, before commands below 运行, 所以 it
	// cannot race main goroutine updating m.会话.
	// Optimistically 标记 agent busy: 提示词 we 是 about 到 submit
	// either starts 运行 或 是 enqueued behind one. 这 keeps esc pressed
	// right after enter routing 到 cancelAgent instead 的 reading stale
	// 空闲 值; authoritative state arrives via agentRunSubmittedMsg.
	// Bump busy/队列 generations 所以 any probe started before 这
	// optimistic 写入 是 discarded rather than reverting us 到 空闲.
	m.agentBusyCache.set(true)
	m.busyFetchGen++
	m.invalidatePromptQueue()
	// 一个 提示词 sent behind busy turn shows 在 队列 pill right away
	// rather than once agent has built 它的 运行 和 enqueued it. Hidden
	// prompts never show, matching 服务端's list.
	var pendingID uint64
	if queued && !hidden {
		pendingID = m.promptQueue.Add(content)
		m.updateLayoutAndSize()
	}
	cmds = append(cmds, func() tea.Msg {
		// AgentRun 是 fire-和-forget: it returns once 提示词 has
		// been accepted (HTTP 202) 或 synchronously 用 validation
		// 或 transport 错误. 运行 failures 和 cancellation surface
		// through SSE-derived events, 不 这 return 值.
		runCtx := context.Background()
		if hidden {
			runCtx = message.WithHiddenUserMessage(runCtx)
		}
		err := m.com.Workspace.AgentRun(runCtx, sessionID, content, attachments...)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		return agentRunSubmittedMsg{sessionID: sessionID, pendingID: pendingID, err: err}
	})
	return tea.Batch(cmds...)
}

// handleChannelMessage injects 渠道 事件 pushed 通过 MCP 服务端 into
// 会话 所以 agent reacts 到 it 在 它的 next turn. rendered <渠道>
// element 是 already validated 和 escaped 通过 mcp package. If no 会话 是
// 活跃 yet, one 是 created 所以 pushed 事件 是 never silently dropped; 如果
// agent 是 busy, AgentRun enqueues 消息 和 it 是 picked up 在 next
// step.
//
// Injection 是 skipped entirely 当 工作区 routes 渠道 events
// itself (客户端/服务端 mode): 服务端 injects each 事件 exactly once,
// 和 injecting here 作为 well would duplicate turn once per attached
// 客户端. injected turn still reaches 这 客户端 through normal
// 会话/消息 事件 流.
func (m *UI) handleChannelMessage(ev mcp.Event) tea.Cmd {
	if m.com.Workspace.RoutesChannelEvents() {
		return nil
	}
	if ev.ChannelMessage == "" || !m.com.Workspace.AgentIsReady() {
		return nil
	}
	loadCmd, err := m.ensureSession()
	if err != nil {
		slog.Debug("Failed to create session for channel message", "error", err)
		return nil
	}
	if !m.hasSession() {
		slog.Debug("Channel message dropped: no active session after ensureSession", "channel", ev.Name)
		return loadCmd
	}
	// coordinator sets 渠道 binding during turn
	// (syncSessionChannel), 所以 there 是 no need 到 写入 it here —
	// doing 所以 would race 用 coordinator's own 写入 和
	// 发布 duplicate 会话 更新.
	sessionID := m.session.ID
	channel := ev.Name
	content := ev.ChannelMessage
	runCmd := func() tea.Msg {
		if err := m.com.Workspace.AgentRunChannel(context.Background(), channel, sessionID, content); err != nil {
			slog.Debug("Failed to inject channel message", "error", err, "session", sessionID)
		}
		return nil
	}
	if loadCmd != nil {
		return tea.Batch(loadCmd, runCmd)
	}
	return runCmd
}

// runShellCommand executes shell command 服务端-side without triggering
// LLM. 结果 是 displayed 作为 工具-style item 在 chat.
func (m *UI) runShellCommand(command string) tea.Cmd {
	return m.runShellCommandInternal(command, false)
}

// runShellCommandInternal 是 shared implementation 为 bang-mode shell
// execution. isFirstMessage indicates command 是 first 用户消息
// 在 newly created 会话, which triggers 标题 generation.
func (m *UI) runShellCommandInternal(command string, isFirstMessage bool) tea.Cmd {
	var cmds []tea.Cmd
	if !m.hasSession() {
		newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		if m.forceCompactMode {
			m.isCompact = true
		}
		if newSession.ID != "" {
			m.session = &newSession
			cmds = append(cmds, m.loadSession(newSession.ID))
		}
		m.setState(uiChat, m.focus)
		// Defer shell execution until loadSessionMsg fires 所以 chat
		// list 是 stable before we add items 或 start streaming.
		m.pendingBangCommand = command
		return tea.Batch(cmds...)
	}

	sessionID := m.session.ID
	contentWidth := min(m.layout.main.Dx()-2, 120)

	if m.liveBangRuns == nil {
		m.liveBangRuns = make(map[bangRun]int)
	}
	m.liveBangRuns[bangRun{sessionID, command}]++

	// Append pending shell item immediately 所以 用户 sees feedback.
	pendingItem := chat.NewPendingShellItem(m.com.Styles, command)
	// Bang mode runs without agent, 所以 re-enable animation clock
	// 那 non-busy 会话 reload may have frozen.
	m.animClock.frozen = false
	m.chat.AppendMessages(pendingItem)
	m.chat.ScrollToBottom()

	// 流 output via 渠道. progress callback writes chunks
	// 到 streamCh; reader cmd converts them 到 shellStreamMsg values.
	streamCh := make(chan string, 64)
	pendingID := pendingItem.ID()

	onProgress := func(chunk string) {
		select {
		case streamCh <- chunk:
		default:
			// Drop 如果 UI can't keep up.
		}
	}

	// Reader cmd: drains streamCh into shellStreamMsg until closed.
	cmds = append(cmds, func() tea.Msg {
		chunk, ok := <-streamCh
		if !ok {
			return nil
		}
		return shellStreamMsg{PendingID: pendingID, Chunk: chunk, streamCh: streamCh}
	})

	ctx, cancel := context.WithCancel(context.Background())
	m.bangCancel = cancel
	m.turnOutcome = ""

	cmds = append(cmds, func() tea.Msg {
		resp, err := m.com.Workspace.AgentRunShellCommand(ctx, sessionID, command, contentWidth, onProgress, isFirstMessage)
		close(streamCh)
		if err != nil && !errors.Is(err, context.Canceled) {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("shell: %v", err),
			}
		}
		exitCode := resp.ExitCode
		if errors.Is(err, context.Canceled) {
			exitCode = 130 // conventional SIGINT exit code
		}
		return shellResultMsg{
			PendingID: pendingID,
			Command:   command,
			Output:    resp.Output,
			ExitCode:  exitCode,
		}
	})
	return tea.Batch(cmds...)
}

const cancelTimerDuration = 2 * time.Second

// cancelTimerCmd creates command 那 expires 取消 timer.
func cancelTimerCmd() tea.Cmd {
	return tea.Tick(cancelTimerDuration, func(time.Time) tea.Msg {
		return cancelTimerExpiredMsg{}
	})
}

// cancelAgent handles 取消 key press. first press sets isCanceling 到 true
// 和 starts timer. second press (before timer expires) actually
// cancels the agent.
func (m *UI) cancelAgent() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	// Gate 在 memoized ready state: esc 是 hot key 和 AgentIsReady
	// 是 synchronous HTTP round-trip 在 客户端/服务端 mode.
	if !m.agentReady {
		return nil
	}

	if m.isCanceling {
		// Second escape press — actually 取消.
		m.isCanceling = false

		// 取消 running bang command 如果 one 是 在 progress.
		if m.bangCancel != nil {
			m.bangCancel()
			m.bangCancel = nil
		}

		m.com.Workspace.AgentCancel(m.session.ID)
		// Stop spinning todo indicator 和 drop memoized busy
		// state 取消 just changed; pill re-renders now 从
		// last-known state 和 again 当 off-thread refresh (和
		// the agent's own events) land.
		m.todoIsSpinning = false
		m.invalidateBusyCaches()
		m.renderPills()
		return m.dispatchBusyRefresh()
	}

	// Queued prompts pending: esc clears 队列, decided 从 cached
	// 队列 rather than synchronous 工作区 probe.
	if m.promptQueue.ServerQueued() {
		m.com.Workspace.AgentClearQueue(m.session.ID)
		m.promptQueue.ClearServer()
		m.promptQueueCheckedAt = time.Now()
		// Bump 队列 generation 所以 fetch started before 这 clear
		// cannot land 和 repopulate pill we just emptied.
		m.invalidatePromptQueue()
		m.updateLayoutAndSize()
		return nil
	}

	// First escape press - set canceling state 和 start timer.
	m.isCanceling = true
	return cancelTimerCmd()
}

// openDialog opens dialog 通过 它的 ID.
func (m *UI) openDialog(id string) tea.Cmd {
	var cmds []tea.Cmd
	switch id {
	case dialog.SessionsID:
		if cmd := m.openSessionsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ModelsID:
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.CommandsID:
		if cmd := m.openCommandsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ReasoningID:
		if cmd := m.openReasoningDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.NotificationsID:
		if cmd := m.openNotificationsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.MCPTogglesID:
		if cmd := m.openMCPTogglesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.SkillsTogglesID:
		if cmd := m.openSkillsTogglesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.FilePickerID:
		if cmd := m.openFilesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ThemeID:
		m.openThemeDialog()
	case dialog.ThemeNewID:
		m.openThemeNewDialog()
	case dialog.ThemeEditorID:
		m.openThemeEditorDialog("")
	case dialog.ChannelsID:
		m.openChannelsDialog()
	case dialog.QuitID:
		if cmd := m.openQuitDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	default:
		// Unknown dialog
		break
	}
	return tea.Batch(cmds...)
}

// openQuitDialog opens the quit confirmation dialog.
func (m *UI) openQuitDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.QuitID) {
		// Bring 到 front
		m.dialog.BringToFront(dialog.QuitID)
		return nil
	}

	quitDialog := dialog.NewQuit(m.com, m.runningSubAgents())
	m.dialog.OpenDialog(quitDialog)
	return nil
}

// openModelsDialog opens the models dialog.
func (m *UI) openModelsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ModelsID) {
		// Bring 到 front
		m.dialog.BringToFront(dialog.ModelsID)
		return nil
	}

	isOnboarding := m.state == uiOnboarding
	modelsDialog, err := dialog.NewModels(m.com, isOnboarding)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(modelsDialog)

	return nil
}

// openCommandsDialog opens the commands dialog.
func (m *UI) openCommandsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.CommandsID) {
		// Bring 到 front
		m.dialog.BringToFront(dialog.CommandsID)
		return nil
	}

	var sessionID string
	hasSession := m.session != nil
	if hasSession {
		sessionID = m.session.ID
	}
	hasTodos := hasSession && hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue.Shown() > 0

	commands, err := dialog.NewCommands(m.com, sessionID, hasSession, hasTodos, hasQueue, m.isTransparent, m.customCommands, m.mcpPrompts)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(commands)

	return commands.InitialCmd()
}

// openReasoningDialog opens the reasoning effort dialog.
func (m *UI) openReasoningDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ReasoningID) {
		m.dialog.BringToFront(dialog.ReasoningID)
		return nil
	}

	reasoningDialog, err := dialog.NewReasoning(m.com)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(reasoningDialog)
	return nil
}

// openNotificationsDialog opens 通知 style picker dialog.
func (m *UI) openNotificationsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.NotificationsID) {
		m.dialog.BringToFront(dialog.NotificationsID)
		return nil
	}

	notificationsDialog := dialog.NewNotifications(m.com)
	m.dialog.OpenDialog(notificationsDialog)
	return nil
}

// openChannelsDialog opens the channels management dialog.
func (m *UI) openChannelsDialog() {
	if m.dialog.ContainsDialog(dialog.ChannelsID) {
		m.dialog.BringToFront(dialog.ChannelsID)
		return
	}
	channelsDialog := dialog.NewChannels(m.com, m.mcpStates)
	m.dialog.OpenDialog(channelsDialog)
}

// openSessionsDialog opens sessions dialog. If dialog 是 already open,
// it brings it 到 front. Otherwise, it will list all sessions 和 open
// the dialog.
func (m *UI) openSessionsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SessionsID) {
		// Bring 到 front
		m.dialog.BringToFront(dialog.SessionsID)
		return nil
	}

	selectedSessionID := ""
	if m.session != nil {
		selectedSessionID = m.session.ID
	}

	dialog, err := dialog.NewSessions(m.com, selectedSessionID)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(dialog)
	return nil
}

// openFilesDialog opens 文件 picker dialog.
func (m *UI) openFilesDialog() tea.Cmd {
	if !m.currentModelSupportsImages() {
		return util.ReportWarn("The current model does not support image attachments")
	}
	if m.dialog.ContainsDialog(dialog.FilePickerID) {
		// Bring 到 front
		m.dialog.BringToFront(dialog.FilePickerID)
		return nil
	}

	filePicker, cmd := dialog.NewFilePicker(m.com)
	filePicker.SetImageCapabilities(&m.caps)
	m.dialog.OpenDialog(filePicker)
	event.FilePickerOpened()

	return cmd
}

// openPermissionsDialog opens permissions dialog 为 权限 请求.
func (m *UI) openPermissionsDialog(perm permission.PermissionRequest) tea.Cmd {
	// CRUSH_PERMISSIONS_PIPED=1: another 客户端 ( editor Crush runs 在)
	// shows prompts 和 answers them, 所以 这 UI doesn't ask twice.
	if piped, _ := strconv.ParseBool(os.Getenv("CRUSH_PERMISSIONS_PIPED")); piped {
		return nil
	}
	// Close any existing permissions dialog first.
	m.dialog.CloseDialog(dialog.PermissionsID)

	// Get diff mode 从 配置.
	var opts []dialog.PermissionsOption
	if diffMode := m.com.Config().Options.TUI.DiffMode; diffMode != "" {
		opts = append(opts, dialog.WithDiffMode(diffMode == "split"))
	}

	permDialog := dialog.NewPermissions(m.com, perm, opts...)
	m.dialog.OpenDialogWithGrace(permDialog)
	return nil
}

// openBatchFormDialog activates tabbed multi-question form 在
// editor area. Single questions render without tabs 或 confirm.
// 它 reports whether it opened form, which it doesn't 为 batch
// the open form already shows.
func (m *UI) openBatchFormDialog(batch question.Request) bool {
	// Close any existing question form first 到 prevent stacking.
	if qf, ok := m.activeInline.(*dialog.QuestionForm); ok && qf != nil {
		// 服务端 re-sends pending batch whenever 客户端 reports
		// 它的 会话; keep form already showing it 所以 answers
		// typed 所以 far survive.
		if qf.BatchID == batch.ID {
			return false
		}
		m.activeInline = nil
	}

	form := dialog.NewQuestionForm(m.com.Styles, batch)
	form.OnAnswer = func(responses []question.Answer) {
		m.com.Workspace.QuestionAnswer(batch.ID, responses)
	}
	form.OnCancel = func() {
		m.com.Workspace.QuestionCancel(batch.ID)
	}
	m.activeInline = form
	m.textarea.Blur()
	m.focus = uiFocusEditor
	m.activeInline.SetFocused(true)
	m.updateLayoutAndSize()
	return true
}

// handleQuestionNotification dismisses open question form 当
// any 客户端 resolved pending batch. Only one question can 是
// pending 在 time, 所以 any 通知 means current form
// 是 stale regardless 的 BatchID.
func (m *UI) handleQuestionNotification(_ question.Notification) {
	if _, ok := m.activeInline.(*dialog.QuestionForm); ok {
		m.activeInline = nil
		m.textarea.Focus()
		m.updateLayoutAndSize()
	}
}

// editorContentWidth returns content width available 到
// editor area 为 current state. 它 depends 只 在 终态
// width 和 layout (不 在 editor height), 所以 it can 是 computed
// before editor's height 是 known. 这 是 single source
// 的 truth 为 inline editor width used 通过 both layout sizing
// 和 Height() queries.
func (m *UI) editorContentWidth() int {
	width := m.width - 2 // appRect horizontal margins
	if m.state == uiChat && !m.isCompact {
		width -= 30 // sidebar column
	}
	return width
}

// collapsedInlineEditor returns 活跃 inline editor 当 it should use
// 它的 compact representation while chat has focus.
func (m *UI) collapsedInlineEditor() (dialog.CollapsibleInlineEditor, bool) {
	if m.focus == uiFocusEditor {
		return nil, false
	}
	collapsible, ok := m.activeInline.(dialog.CollapsibleInlineEditor)
	if !ok || !collapsible.ShouldCollapse(m.editorContentWidth(), m.height) {
		return nil, false
	}
	return collapsible, true
}

// handlePermissionNotification updates 工具 items 当 权限 state changes.
func (m *UI) handlePermissionNotification(notification permission.PermissionNotification) {
	if toolItem := m.chat.MessageItem(notification.ToolCallID); toolItem != nil {
		if permItem, ok := toolItem.(chat.ToolMessageItem); ok {
			if notification.Granted {
				permItem.SetStatus(chat.ToolStatusRunning)
			} else if !notification.Cancelled {
				permItem.SetStatus(chat.ToolStatusAwaitingPermission)
			}
		}
	}

	// If 这 通知 reflects final resolution (granted 或 denied),
	// dismiss any open permissions dialog whose 工具调用 ID matches. 这
	// covers case where another 客户端 resolved 请求 remotely.
	if !notification.Granted && !notification.Denied && !notification.Cancelled {
		return
	}
	if d := m.dialog.Dialog(dialog.PermissionsID); d != nil {
		if perm, ok := d.(*dialog.Permissions); ok && perm.ToolCallID() == notification.ToolCallID {
			m.dialog.CloseDialog(dialog.PermissionsID)
		}
	}
}

// handlePlanHandoff checks whether completed 运行 在 plan mode contained
// plan-ready sentinel marker 和, 如果 所以, opens plan handoff dialog.
func (m *UI) handlePlanHandoff(rc notify.RunComplete) tea.Cmd {
	if m.mode != uiInputModePlan {
		return nil
	}
	if rc.Error != "" || rc.Cancelled {
		return nil
	}
	if m.session == nil || rc.SessionID != m.session.ID {
		return nil
	}
	if !common.PlanReadyMarkerPresent(rc.Text) {
		slog.Debug("Plan run completed without ready marker", "session_id", rc.SessionID)
		return nil
	}
	m.setPlanReadyPending(rc.SessionID)
	if _, ok := m.activeInline.(*dialog.PlanHandoffInline); ok {
		return nil
	}
	m.openPlanHandoff()
	return nil
}

// resetPlanModeState drops any pending plan handoff 和, 当 plan mode 是
// 活跃, switches back 到 code mode. Used 当 UI moves 到 different
// 会话, since plan mode 是 scoped 到 会话 it was enabled 在. Plan
// mode followed 从 another 客户端 是 kept: agent 是 shared, 所以
// switching it back would end planning 在 客户端 那 enabled it.
func (m *UI) resetPlanModeState() tea.Cmd {
	m.setPlanReadyPending("")
	if _, ok := m.activeInline.(*dialog.PlanHandoffInline); ok {
		m.activeInline = nil
		m.textarea.Focus()
	}
	if m.mode != uiInputModePlan || m.modeAdopted {
		return nil
	}
	// backend rejects agent switches while 运行 是 活跃 (409). When
	// one 是, keep mode 作为-是: 服务端's 活跃 agent still matches
	// what editor shows, 和 next Shift+Tab lands back 在 code mode
	// once 运行 finishes.
	if m.isAgentBusy() {
		return nil
	}
	return m.setInputMode(uiInputModeCode)
}

// setPlanReadyPending records (或 clears, 用 empty ID) 会话 那
// has an unconfirmed ready plan.
func (m *UI) setPlanReadyPending(sessionID string) {
	m.planReadySessionID = sessionID
}

// openPlanHandoff replaces textarea 用 inline "switch 到 code"
// 提示词. Dismissing it keeps pending plan, 所以 提示词 can 是 reopened
// 通过 pressing enter 在 empty editor while still 在 plan mode.
func (m *UI) openPlanHandoff() {
	inline := dialog.NewPlanHandoffInline(m.com)
	inline.OnConfirm = func(yolo bool) tea.Cmd {
		// handoff asks yes/no question about YOLO, 所以 answer it
		// literally: declining drops 会话 back 到 normal.
		if m.yoloActive() != yolo {
			m.setYolo(yolo)
		}
		m.setPlanReadyPending("")
		sessionID := m.session.ID
		cmd := m.setInputMode(uiInputModeCode)
		return func() tea.Msg {
			result := cmd()
			if switched, ok := result.(modeSwitchedMsg); ok {
				switched.continueSessionID = sessionID
				return switched
			}
			return result
		}
	}
	inline.OnRequestChanges = func(feedback string) tea.Cmd {
		return m.sendMessage(feedback)
	}
	m.activeInline = inline
	m.textarea.Blur()
	m.focus = uiFocusEditor
	m.activeInline.SetFocused(true)
	if m.status != nil {
		m.updateLayoutAndSize()
	}
}

// handleAgentNotification translates domain agent events into desktop
// notifications using UI 通知 backend.
func (m *UI) handleAgentNotification(n notify.Notification) tea.Cmd {
	var cmds []tea.Cmd
	switch n.Type {
	case notify.TypeAgentFinished:
		cmds = append(cmds, m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: fmt.Sprintf("Agent's turn completed in \"%s\"", n.SessionTitle),
		}))
		// Show what 存储 balance says right away, 和 fetch again:
		// refresh 为 turn's last 响应 是 只 kicked off once
		// 它的 请求 finishes, 所以 it may still 是 在 flight here.
		m.updateHyperCredits()
		if m.com.IsHyper() {
			cmds = append(cmds, m.fetchHyperCredits())
		}
	case notify.TypeAgentError:
		// 终态 edge like TypeAgentFinished; fall through 到
		// busy/队列 refresh below.
		m.endPendingTurn(n.SessionID)
	case notify.TypeReAuthenticate:
		return m.handleReAuthenticate(n.ProviderID)
	case notify.TypeAWSSSOAuth:
		return m.handleAWSSSOAuth(n.AWSSOCommand, n.AWSSOURL)
	case notify.TypeAWSSSOAuthResult:
		return m.handleAWSSSOAuthResult(n.Message)
	default:
		return nil
	}
	// TypeAgentFinished / TypeAgentError 是 busy→空闲 edge: agent
	// clears 它的 活跃 请求 before publishing precisely 所以 observers
	// can re-probe. Drop memoized busy state 和 re-fetch it 和
	// 提示词 队列 off-thread.
	m.invalidateBusyCaches()
	m.invalidatePromptQueue()
	if cmd := m.dispatchBusyRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *UI) handleReAuthenticate(providerID string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	providerCfg, ok := cfg.Providers.Get(providerID)
	if !ok {
		return nil
	}
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return nil
	}
	return m.openAuthenticationDialog(providerCfg.ToProvider(), cfg.Models[agentCfg.Model], agentCfg.Model)
}

// handleAWSSSOAuth opens AWS SSO progress dialog (或 updates SSO URL
// 在 already-open one). refresh command runs 在 coordinator; 这
// dialog 是 display surface driven 通过 agent notifications.
func (m *UI) handleAWSSSOAuth(command, url string) tea.Cmd {
	// 更新 URL 在 already-open dialog.
	if existing := m.dialog.Dialog(dialog.AWSSSOID); existing != nil {
		if awsDlg, ok := existing.(*dialog.AWSSSO); ok && url != "" {
			awsDlg.SetURL(url)
		}
		m.dialog.BringToFront(dialog.AWSSSOID)
		return nil
	}
	if command == "" {
		return nil
	}
	dlg, cmd := dialog.NewAWSSSO(m.com, command)
	if url != "" {
		dlg.SetURL(url)
	}
	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

// handleAWSSSOAuthResult finishes AWS SSO dialog once 刷新 command
// exits: it closes 在 success 或 shows 错误 所以 用户 can dismiss it.
func (m *UI) handleAWSSSOAuthResult(errMsg string) tea.Cmd {
	existing := m.dialog.Dialog(dialog.AWSSSOID)
	if existing == nil {
		return nil
	}
	awsDlg, ok := existing.(*dialog.AWSSSO)
	if !ok {
		return nil
	}
	if errMsg == "" {
		// Success: turn retries transparently, 所以 no need 到 linger.
		m.dialog.CloseDialog(dialog.AWSSSOID)
		return nil
	}
	awsDlg.Finish(errMsg)
	return nil
}

// newSession clears current 会话 state 和 prepares 为 new 会话.
// actual 会话 creation happens 当 用户 sends their first 消息.
// Returns command 到 reload 提示词 history.
func (m *UI) newSession() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	planCmd := m.resetPlanModeState()
	m.turnOutcome = ""
	m.session = nil
	m.sidebarOffset = 0
	m.sessionFiles = nil
	m.sessionFileReads = nil
	m.setState(uiLanding, uiFocusEditor)
	m.textarea.Focus()
	m.chat.Blur()
	m.chat.ClearMessages()
	m.pillsExpanded = false
	m.pillsAutoExpanded = false
	m.promptQueue.Reset()
	m.promptQueueCheckedAt = time.Now()
	m.invalidateBusyCaches()
	m.invalidatePromptQueue()
	m.pillsView = ""
	m.historyReset()
	agenttools.ResetCache()
	return tea.Batch(
		planCmd,
		func() tea.Msg {
			m.com.Workspace.LSPStopAll(context.Background())
			return nil
		},
		m.loadPromptHistory(),
		m.reportCurrentSession(""),
	)
}

// checkBangModeAfterPaste engages bang mode 当 pasted 文本 starts 用
// optional whitespace followed 通过 "!". 它 strips prefix 和 adjusts
// cursor, mirroring keypress bang-mode 条目 logic.
func (m *UI) checkBangModeAfterPaste() {
	if m.bangMode {
		return
	}
	val := m.textarea.Value()
	trimmed := strings.TrimLeftFunc(val, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "!") {
		return
	}
	m.bangMode = true
	m.bangWasEmpty = true
	stripped := trimmed[1:]
	m.textarea.SetValue(stripped)
	col := m.textarea.Column()
	m.textarea.SetCursorColumn(max(0, col-(len(val)-len(stripped))))
	m.setEditorPrompt(m.permModeCached())
}

// handlePasteMsg handles paste 消息.
func (m *UI) handlePasteMsg(msg tea.PasteMsg) tea.Cmd {
	// Normalize \r\n before the textarea sanitizer sees it.
	msg.Content = strings.ReplaceAll(msg.Content, "\r\n", "\n")

	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	if m.focus != uiFocusEditor {
		return nil
	}

	if hasPasteExceededThreshold(msg) {
		return func() tea.Msg {
			content := []byte(msg.Content)
			if int64(len(content)) > common.MaxAttachmentSize {
				return util.ReportWarn("Paste is too big (>5mb)")
			}
			name := fmt.Sprintf("paste_%d.txt", m.pasteIdx())
			mimeBufferSize := min(512, len(content))
			mimeType := http.DetectContentType(content[:mimeBufferSize])
			return message.Attachment{
				FileName: name,
				FilePath: name,
				MimeType: mimeType,
				Content:  content,
			}
		}
	}

	// Attempt 到 parse pasted content 作为 文件 paths. If possible 到 parse,
	// all files exist 和 是 valid, add 作为 attachments.
	// Otherwise, paste 作为 文本.
	paths := fsext.ParsePastedFiles(msg.Content)
	allExistsAndValid := func() bool {
		if len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return false
			}
			if !common.IsImagePath(path) && !common.IsAudioPath(path) {
				return false
			}
		}
		return true
	}
	if !allExistsAndValid() {
		prevHeight := m.textarea.Height()
		cmd := m.updateTextareaWithPrevHeight(msg, prevHeight)
		m.checkBangModeAfterPaste()
		return cmd
	}
	if !m.currentModelSupportsImages() && slices.ContainsFunc(paths, common.IsImagePath) {
		return util.ReportWarn("The current model does not support image attachments")
	}
	if !m.currentModelSupportsAudio() && slices.ContainsFunc(paths, common.IsAudioPath) {
		return util.ReportWarn("The current model does not support audio attachments")
	}

	var cmds []tea.Cmd
	for _, path := range paths {
		cmds = append(cmds, m.handleFilePathPaste(path))
	}
	return tea.Batch(cmds...)
}

func hasPasteExceededThreshold(msg tea.PasteMsg) bool {
	var (
		lineCount = 0
		colCount  = 0
	)
	for line := range strings.SplitSeq(msg.Content, "\n") {
		lineCount++
		colCount = max(colCount, len(line))

		if lineCount > pasteLinesThreshold || colCount > pasteColsThreshold {
			return true
		}
	}
	return false
}

// handleFilePathPaste handles pasted 文件 路径.
func (m *UI) handleFilePathPaste(path string) tea.Cmd {
	return func() tea.Msg {
		fileInfo, err := os.Stat(path)
		if err != nil {
			return util.ReportError(err)
		}
		if fileInfo.IsDir() {
			return util.ReportWarn("Cannot attach a directory")
		}
		if fileInfo.Size() > common.MaxAttachmentSize {
			return util.ReportWarn("File is too big (>5mb)")
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.ReportError(err)
		}

		mimeBufferSize := min(512, len(content))
		mimeType := http.DetectContentType(content[:mimeBufferSize])
		fileName := filepath.Base(path)
		return message.Attachment{
			FilePath: path,
			FileName: fileName,
			MimeType: mimeType,
			Content:  content,
		}
	}
}

// pasteFromClipboard 把 ctrl+v 变成一次粘贴：剪贴板里有图片就建图片
// 附件，否则按文本粘贴，文本走和 bracketed paste 完全相同的逻辑。
func (m *UI) pasteFromClipboard() tea.Msg {
	if m.currentModelSupportsImages() {
		if imageData, err := clipboard.Read(clipboard.FormatImage); err == nil {
			if int64(len(imageData)) > common.MaxAttachmentSize {
				return util.InfoMsg{
					Type: util.InfoTypeError,
					Msg:  "File too large, max 5MB",
				}
			}
			name := fmt.Sprintf("paste_%d.png", m.pasteIdx())
			return message.Attachment{
				FilePath: name,
				FileName: name,
				MimeType: mimeOf(imageData),
				Content:  imageData,
			}
		}
	}
	return m.pasteTextFromClipboard()
}

// pasteTextFromClipboard 读系统剪贴板里的文本并包成 tea.PasteMsg，让
// ctrl+v 走和 bracketed paste 完全相同的粘贴逻辑。
func (m *UI) pasteTextFromClipboard() tea.Msg {
	textData, err := clipboard.Read(clipboard.FormatText)
	if errors.Is(err, clipboard.ErrUnsupported) {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "This platform has no clipboard access; paste with your terminal instead",
		}
	}
	if err != nil || len(textData) == 0 {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "Clipboard is empty or does not contain text",
		}
	}
	return tea.PasteMsg{Content: string(textData)}
}

var pasteRE = regexp.MustCompile(`paste_(\d+).txt`)

func (m *UI) pasteIdx() int {
	result := 0
	for _, at := range m.attachments.List() {
		found := pasteRE.FindStringSubmatch(at.FileName)
		if len(found) == 0 {
			continue
		}
		idx, err := strconv.Atoi(found[1])
		if err == nil {
			result = max(result, idx)
		}
	}
	return result + 1
}

// drawSessionDetails draws 会话 details 在 compact mode.
func (m *UI) drawSessionDetails(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	s := m.com.Styles

	width := area.Dx() - s.CompactDetails.View.GetHorizontalFrameSize()
	height := area.Dy() - s.CompactDetails.View.GetVerticalFrameSize()

	title := s.CompactDetails.Title.Width(width).MaxHeight(2).Render(m.session.Title)
	blocks := []string{
		title,
		"",
		m.modelInfo(width),
		"",
	}

	detailsHeader := lipgloss.JoinVertical(
		lipgloss.Left,
		blocks...,
	)

	version := s.CompactDetails.Version.Width(width).AlignHorizontal(lipgloss.Right).Render(version.Version)

	remainingHeight := height - lipgloss.Height(detailsHeader) - lipgloss.Height(version)

	const maxSectionWidth = 50
	// Subagents take leading column 当 any exist, since running
	// 子代理 是 one thing here 那 changes while you watch.
	subAgents := m.subAgentStatuses()
	columns := 4
	if len(subAgents) > 0 {
		columns++
	}
	sectionWidth := max(1, min(maxSectionWidth, (width-(columns-1))/columns)) // one space between columns
	maxItemsPerSection := max(0, remainingHeight-3)                           // Account for section title and spacing

	lspSection := m.lspInfo(sectionWidth, maxItemsPerSection, false)
	mcpSection := m.mcpInfo(sectionWidth, maxItemsPerSection, false)
	skillsSection := m.skillsInfo(sectionWidth, maxItemsPerSection, false)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), sectionWidth, maxItemsPerSection, false)
	sections := lipgloss.JoinHorizontal(lipgloss.Top, filesSection, " ", lspSection, " ", mcpSection, " ", skillsSection)
	if len(subAgents) > 0 {
		sections = lipgloss.JoinHorizontal(lipgloss.Top, m.subAgentsInfo(subAgents, sectionWidth, maxItemsPerSection, false), " ", sections)
	}
	sections = lipgloss.NewStyle().MaxWidth(width).MaxHeight(max(0, remainingHeight)).Render(sections)
	uv.NewStyledString(
		s.CompactDetails.View.
			Width(area.Dx()).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Left,
					detailsHeader,
					sections,
					version,
				),
			),
	).Draw(scr, area)
}

func (m *UI) runMCPPrompt(clientID, promptID string, arguments map[string]string) tea.Cmd {
	load := func() tea.Msg {
		prompt, err := m.com.Workspace.GetMCPPrompt(clientID, promptID, arguments)
		if err != nil {
			// TODO: make 这 better
			return util.ReportError(err)()
		}

		if prompt == "" {
			return nil
		}
		return sendMessageMsg{
			Content: prompt,
		}
	}

	var cmds []tea.Cmd
	if cmd := m.dialog.StartLoading(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, load, func() tea.Msg {
		return closeDialogMsg{}
	})

	return tea.Sequence(cmds...)
}

// handleStateChanged reacts 到 MCP state change: memoized sidebar
// states refresh off-thread ( authoritative re-fetch rather than 事件
// payload, 所以 missed 字段 cannot drift), coordinator rebuilds 到 pick
// up newly registered tools, 和 MCP prompts reload.
func (m *UI) handleStateChanged() []tea.Cmd {
	return []tea.Cmd{
		m.requestMCPRefresh(),
		m.updateAgentModelCmd(func() tea.Msg {
			m.com.Workspace.UpdateAgentModel(context.Background())
			return nil
		}),
		m.loadMCPrompts,
	}
}

func handleMCPPromptsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshPrompts(context.Background(), name)
		return nil
	}
}

func handleMCPToolsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.RefreshMCPTools(context.Background(), name)
		return nil
	}
}

func handleMCPResourcesEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshResources(context.Background(), name)
		return nil
	}
}

func (m *UI) copyChatHighlight() tea.Cmd {
	text := m.chat.HighlightContent()
	return common.CopyToClipboardWithCallback(
		text,
		"Selected text copied to clipboard",
		func() tea.Msg {
			m.chat.ClearMouse()
			return nil
		},
	)
}

func (m *UI) enableDockerMCP() tea.Msg {
	ctx := context.Background()
	if err := m.com.Workspace.EnableDockerMCP(ctx); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP enabled and started successfully")
}

func (m *UI) disableDockerMCP() tea.Msg {
	if err := m.com.Workspace.DisableDockerMCP(); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP disabled successfully")
}

// renderLogo renders Crush logo 用 given styles 和 dimensions.
func renderLogo(t *styles.Styles, compact, hyper bool, width int) string {
	return logo.Render(t.Logo.GradCanvas, version.Version, compact, logo.Opts{
		FieldColor:   t.Logo.FieldColor,
		TitleColorA:  t.Logo.TitleColorA,
		TitleColorB:  t.Logo.TitleColorB,
		CharmColor:   t.Logo.CharmColor,
		VersionColor: t.Logo.VersionColor,
		Width:        width,
		Hyper:        hyper,
	})
}

// markDispatchRunning puts 派发 item 为 这 label back into 它的
// working state after 它的 子代理 was reopened 用 follow-up. 一个 label
// can 是 reused, 和 只 它的 latest 派发 是 one reopened.
func (m *UI) markDispatchRunning(label string) {
	if label == "" {
		return
	}
	for i := m.chat.Len() - 1; i >= 0; i-- {
		item, ok := m.chat.MessageItemAt(i).(*chat.AgentToolMessageItem)
		if ok && item.DispatchLabel() == label {
			item.MarkDispatchRunning()
			break
		}
	}
	m.animClock.frozen = false
}

// markDispatchReported stops spinner 在 agent_dispatch item 那
// launched 子代理 用 这 label.
func (m *UI) markDispatchReported(label string) {
	if label == "" {
		return
	}
	for i := range m.chat.Len() {
		item, ok := m.chat.MessageItemAt(i).(*chat.AgentToolMessageItem)
		if ok && item.DispatchLabel() == label {
			item.MarkDispatchReported()
		}
	}
}
