// Package styles define styling 和 theming 为 project.
package styles

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log/slog"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/crush/internal/ui/diffview"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	CheckIcon       string = "✓"
	SpinnerIcon     string = "⋯"
	LoadingIcon     string = "⟳"
	ModelIcon       string = "◇"
	HypercreditIcon string = "◆"

	// GitBranchIcon 是 Powerline branch glyph, shown 只 用 Nerd Fonts.
	GitBranchIcon string = "\ue0a0"

	ArrowRightIcon string = "→"

	// CodespanPadding 是 padding rendered around inline code spans 在
	// markdown. 它 displays identically 到 blank padding it replaced,
	// 但 selection copies recognize it 和 turn it back into original
	// backticks (see list.HighlightContent).
	//
	// 它 是 no-break space tagged 用 文本-presentation variation
	// selector (U+FE0E): pair 是 single one-cell grapheme 那
	// renders 作为 blank 和, being non-breaking, keeps word wrap 从
	// tearing codespan between 它的 padding 和 它的 文本. selector
	// makes sentinel distinct 从 real no-break space 在 消息
	// 文本 (pasted 文本, some LLM output), which selection copy must
	// preserve verbatim.
	//
	// selector must 是 U+FE0E, 不 emoji-presentation U+FE0F:
	// ansi.StringWidth measures any cluster ending 在 U+FE0F 作为 two
	// cells wide, while terminals render it 作为 one, 和 那 mismatch
	// makes frame differ repaint line 在 every frame (visible
	// 作为 flicker).
	CodespanPadding string = "\u00a0\ufe0e"

	ToolPending string = "●"
	ToolSuccess string = "✓"
	ToolError   string = "×"

	RadioOn  string = "◉"
	RadioOff string = "○"

	BorderThin  string = "│"
	BorderThick string = "▌"

	SectionSeparator string = "─"

	TodoCompletedIcon  string = "✓"
	TodoPendingIcon    string = "•"
	TodoInProgressIcon string = "→"

	ImageIcon  string = "■"
	TextIcon   string = "≡"
	SkillIcon  string = "▲"
	RemoveIcon string = "✕"

	ColorSwatchIcon string = "■"

	ScrollbarThumb string = "┃"
	ScrollbarTrack string = "│"

	ChannelIcon string = "↳"

	// LSP severity icons 是 Nerd Font glyphs, shown 只 当 终态
	// 是 expected 到 render them. Each glyph carries trailing space 作为
	// separator 从 count 那 follows it. Terminals without Nerd
	// Font support fall back 到 letters E, W, I, 和 H through
	// nerdfont.Glyph 在 调用 sites, which supply their own spacing.
	LSPErrorIcon   string = "\U000F0159 " // nf-md-close_circle
	LSPWarningIcon string = "\U000F0028 " // nf-md-alert
	LSPInfoIcon    string = "\U000F02FC " // nf-md-information
	LSPHintIcon    string = "\U000F0335 " // nf-md-bug_outline
)

const (
	defaultMargin     = 2
	defaultListIndent = 2
)

type Styles struct {
	// ANSI holds the 16 standard ANSI colors (0-7 normal, 8-15 bright)
	// used 到 remap legible colors onto raw 终态 output, such 作为
	// output 的 bang-mode shell commands. 终态 programs emit
	// basic 16-color SGR codes (red, green, blue, …) 和 leave actual
	// colors up 到 终态; without 这 palette they fall through
	// 到 用户's 终态 defaults, which 是 often illegible 在
	// Crush's background. Defining them here keeps output readable 和
	// 在-brand regardless 的 终态 configuration.
	ANSI [16]color.Color

	// 请求头
	Header struct {
		Charm             lipgloss.Style // Style for "Charm™" label
		Diagonals         lipgloss.Style // Style for diagonal separators (╱)
		Percentage        lipgloss.Style // Style for context percentage
		HypercreditIcon   lipgloss.Style // Style for Hypercredit count (◆ N)
		Keystroke         lipgloss.Style // Style for keystroke hints (e.g., "ctrl+d")
		KeystrokeTip      lipgloss.Style // Style for keystroke action text (e.g., "open", "close")
		WorkingDir        lipgloss.Style // Style for current working directory
		GitBranch         lipgloss.Style // Style for the current git branch name
		Separator         lipgloss.Style // Style for separator dots (•)
		Wrapper           lipgloss.Style // Outer container for the entire header row
		LogoGradCanvas    lipgloss.Style // Canvas for the compact "CRUSH" gradient
		LogoGradFromColor color.Color    // "CRUSH" wordmark gradient start
		LogoGradToColor   color.Color    // "CRUSH" wordmark gradient end
	}

	CompactDetails struct {
		View    lipgloss.Style
		Version lipgloss.Style
		Title   lipgloss.Style
	}

	// 工具 calls
	ToolCallSuccess lipgloss.Style

	// 文本 selection
	TextSelection lipgloss.Style

	// Markdown & Chroma
	Markdown      ansi.StyleConfig
	QuietMarkdown ansi.StyleConfig
	PlanMarkdown  ansi.StyleConfig

	// Inputs
	TextInput textinput.Styles

	// Help
	Help help.Styles

	// Diff
	Diff diffview.Style

	// FilePicker
	FilePicker filepicker.Styles

	// Buttons
	Button struct {
		Focused lipgloss.Style
		Blurred lipgloss.Style
		// Inactive styles buttons 的 提示词 那 是 不 在
		// 活跃 pane: slightly lighter than Blurred 所以 choices
		// stay legible while the chat has focus.
		Inactive lipgloss.Style
		Hovered  lipgloss.Style
		Negative lipgloss.Style // Selected negative/destructive action.
	}

	// Editor
	Editor struct {
		Textarea textarea.Styles

		// Normal mode 提示词 ("> " icon 在 first line, "::: " after).
		PromptNormalIconFocused lipgloss.Style
		PromptNormalIconBlurred lipgloss.Style
		PromptNormalFocused     lipgloss.Style
		PromptNormalBlurred     lipgloss.Style

		// Plan mode 提示词.
		PromptPlanIconFocused lipgloss.Style
		PromptPlanIconBlurred lipgloss.Style
		PromptPlanDotsFocused lipgloss.Style
		PromptPlanDotsBlurred lipgloss.Style

		// YOLO mode 提示词.
		PromptYoloIconFocused lipgloss.Style
		PromptYoloIconBlurred lipgloss.Style
		PromptYoloDotsFocused lipgloss.Style
		PromptYoloDotsBlurred lipgloss.Style

		// Bang mode 提示词 (" ! " icon + ":::" dots, Turtle color).
		PromptBangIconFocused lipgloss.Style
		PromptBangIconBlurred lipgloss.Style
		PromptBangDotsFocused lipgloss.Style
		PromptBangDotsBlurred lipgloss.Style

		// Question mode 提示词 (" ? " icon + ":::" dots).
		PromptQuestionIconFocused lipgloss.Style
		PromptQuestionIconBlurred lipgloss.Style

		// Question choice styling.
		QuestionSelected   lipgloss.Style // Active choice text (Dolly).
		QuestionUnselected lipgloss.Style // Inactive header text (Sash).
		QuestionBody       lipgloss.Style // Description/body text.
		QuestionConfirm    lipgloss.Style // Confirm tab title (primary).
		QuestionNote       lipgloss.Style // Saved note text (dimmer than body).
		QuestionCursorBar  lipgloss.Style // Active cursor indicator bar.
		QuestionRadioOn    lipgloss.Style // Selected single-choice radio.
		QuestionRadioOff   lipgloss.Style // Unselected single-choice radio.
		QuestionCheckOn    lipgloss.Style // Checked multi-choice indicator.
		QuestionCheckOff   lipgloss.Style // Unchecked multi-choice indicator.

		// Sysadmin mode 提示词 (" # " icon + ":::" dots).
		PromptSysadminIconFocused lipgloss.Style
		PromptSysadminIconBlurred lipgloss.Style
		PromptSysadminDotsFocused lipgloss.Style
		PromptSysadminDotsBlurred lipgloss.Style
	}

	// Radio
	Radio struct {
		On    lipgloss.Style
		Off   lipgloss.Style
		Label lipgloss.Style // Text next to a radio button
	}

	// Tabs 为 batch question forms. Uses uv types 为 direct
	// screen rendering without lipgloss.
	Tab struct {
		ActiveBorder          uv.Border
		InactiveBorder        uv.Border
		ActiveBorderBlurred   uv.Border
		InactiveBorderBlurred uv.Border
		ActiveStyle           uv.Style
		InactiveStyle         uv.Style
	}

	// 后台
	Background color.Color

	// Exit banner
	ExitBanner struct {
		Text  lipgloss.Style // Parting message, session title, and resume command
		Label lipgloss.Style // Session and Continue labels
	}

	// Logo
	Logo struct {
		FieldColor         color.Color
		TitleColorA        color.Color
		TitleColorB        color.Color
		CharmColor         color.Color
		VersionColor       color.Color
		SmallCharm         lipgloss.Style // "Charm™" label in SmallRender
		SmallDiagonals     lipgloss.Style // Diagonal line fill in SmallRender
		GradCanvas         lipgloss.Style // Blank canvas for gradient painting
		SmallGradFromColor color.Color    // Small "Crush" wordmark gradient start
		SmallGradToColor   color.Color    // Small "Crush" wordmark gradient end
	}

	// Working indicator gradient (spinners/shimmers 在 助手 "thinking",
	// 工具-调用 pending, CLI generating, startup).
	WorkingGradFromColor color.Color
	WorkingGradToColor   color.Color
	WorkingLabelColor    color.Color // Label text color next to the indicator
	WorkingTimerColor    color.Color // Elapsed timer suffix color
	ThinkingLabelColor   color.Color // Label text color for thinking/summarizing spinners
	CanceledLabelColor   color.Color // Label text color for canceled prompts

	// Section 标题
	Section struct {
		Title lipgloss.Style
		Line  lipgloss.Style
	}

	// Initialize
	Initialize struct {
		Header  lipgloss.Style
		Content lipgloss.Style
		Accent  lipgloss.Style
	}

	// LSP
	LSP struct {
		ErrorDiagnostic   lipgloss.Style
		WarningDiagnostic lipgloss.Style
		HintDiagnostic    lipgloss.Style
		InfoDiagnostic    lipgloss.Style
	}

	// Sidebar
	Sidebar struct {
		SessionTitle lipgloss.Style // Current session title at top of sidebar
		WorkingDir   lipgloss.Style // Working directory path (PrettyPath)
	}

	// ModelInfo (模型 name, 提供方, reasoning, token/cost 摘要)
	ModelInfo struct {
		Icon                 lipgloss.Style // Model icon (◇)
		Name                 lipgloss.Style // Model name text
		Provider             lipgloss.Style // "via <provider>" text
		ProviderFallback     lipgloss.Style // Provider on its own second line
		Reasoning            lipgloss.Style // Reasoning effort text
		TokenCount           lipgloss.Style // "(42K)" token count
		TokenPercentage      lipgloss.Style // "42%" percent of context window
		EstimatedUsagePrefix lipgloss.Style // "~" prefix for estimated usage
		Cost                 lipgloss.Style // "$0.42" cost readout
		HypercreditIcon      lipgloss.Style // Hypercredit icon (◆)
		HypercreditText      lipgloss.Style // Remaining Hypercredits text
	}

	// Resource styles the LSP/MCP/skills sidebar lists: their heading,
	// each 行's status icon, name, status 文本, 和 truncation hints.
	Resource struct {
		Heading         lipgloss.Style // Section header ("LSPs", "MCPs", "Skills")
		Name            lipgloss.Style // Resource name (e.g. "gopls")
		StatusText      lipgloss.Style // Row status description (e.g. "starting...")
		OfflineIcon     lipgloss.Style // Offline/unstarted/stopped status icon
		WorkingIcon     lipgloss.Style // In-progress icon style with no preset glyph
		DisabledIcon    lipgloss.Style // Disabled status icon
		BusyIcon        lipgloss.Style // Busy/starting status icon
		ErrorIcon       lipgloss.Style // Error status icon
		OnlineIcon      lipgloss.Style // Online/ready status icon
		OnlineText      lipgloss.Style // Online/ready status text (e.g. "connected"), no icon
		NeedsAuthIcon   lipgloss.Style // Needs authentication status icon
		AdditionalText  lipgloss.Style // "None" and "…and N more" text
		CapabilityCount lipgloss.Style // "N tools" / "N prompts" / "N resources"
		RowTitleBase    lipgloss.Style // Base style applied over row titles in common.Status
		RowDescBase     lipgloss.Style // Base style applied over row descriptions in common.Status
		DefaultTitleFg  color.Color    // Default title color when opt is zero
		DefaultDescFg   color.Color    // Default description color when opt is zero
	}

	// Files
	Files struct {
		Path           lipgloss.Style
		Additions      lipgloss.Style
		Deletions      lipgloss.Style
		SectionTitle   lipgloss.Style // "Modified Files" heading
		EmptyMessage   lipgloss.Style // "None" placeholder when no files
		TruncationHint lipgloss.Style // "…and N more" message
	}

	// Chat
	// Messages - chat 消息 item styles
	Messages struct {
		UserBlurred      lipgloss.Style
		UserFocused      lipgloss.Style
		AssistantBlurred lipgloss.Style
		AssistantFocused lipgloss.Style
		NoContent        lipgloss.Style
		Thinking         lipgloss.Style
		ErrorTag         lipgloss.Style
		ErrorTitle       lipgloss.Style
		ErrorDetails     lipgloss.Style
		ToolCallFocused  lipgloss.Style
		ToolCallCompact  lipgloss.Style
		ToolCallBlurred  lipgloss.Style

		// Shell (bang mode) item styles.
		ShellBarFocused    lipgloss.Style // Left vertical bar when focused.
		ShellBarBlurred    lipgloss.Style // Left vertical bar when blurred.
		ShellPrompt        lipgloss.Style // "$" prompt symbol (focused).
		ShellPromptBlurred lipgloss.Style // "$" prompt symbol (blurred).
		ShellCommand       lipgloss.Style // Command text (syntax-highlighted).
		ShellOutput        lipgloss.Style // Plain output text.
		ShellExitCode      lipgloss.Style // Non-zero exit code indicator.
		ShellTruncation    lipgloss.Style // "N more lines" hint.

		SectionHeader lipgloss.Style

		// Plan section styles
		PlanBox lipgloss.Style // Border+padding for the final plan message

		// Thinking section styles
		ThinkingBox            lipgloss.Style // Background for thinking content
		ThinkingTruncationHint lipgloss.Style // "… (N lines hidden)" hint
		ThinkingFooterTitle    lipgloss.Style // "Thought for" text
		ThinkingFooterDuration lipgloss.Style // Duration value
		AssistantInfoIcon      lipgloss.Style
		AssistantInfoModel     lipgloss.Style
		AssistantInfoProvider  lipgloss.Style
		AssistantInfoDuration  lipgloss.Style
		SubduedHypercreditIcon lipgloss.Style // Subdued ◆ for hypercredit figures within subdued text
		AssistantCanceled      lipgloss.Style // Italic "Canceled" footer

		// 渠道 消息 metadata line styles.
		ChannelInfoIcon      lipgloss.Style // Chat-bubble glyph prefix
		ChannelInfoSender    lipgloss.Style // Sender name in channel metadata line
		ChannelInfoProvider  lipgloss.Style // "via <channel>" text
		ChannelInfoTimestamp lipgloss.Style // "at <timestamp>" text
	}

	// 工具 - styles 为 工具调用 rendering
	Tool struct {
		// Icon styles 用 工具 status
		IconPending   lipgloss.Style
		IconSuccess   lipgloss.Style
		IconError     lipgloss.Style
		IconCancelled lipgloss.Style

		// 工具 name styles
		NameNormal lipgloss.Style // Top-level tool name
		NameNested lipgloss.Style // Nested child tool name (inside Agent/Agentic Fetch)

		// Parameter list styles
		ParamMain   lipgloss.Style
		ParamKey    lipgloss.Style
		Description lipgloss.Style // A tool's own summary of what it is doing

		// Content rendering styles
		ContentLine           lipgloss.Style // Individual content line with background and width
		ContentTruncation     lipgloss.Style // Truncation message "… (N lines)"
		ContentCodeLine       lipgloss.Style // Code line with background and width
		ContentCodeTruncation lipgloss.Style // Code truncation message with bgBase
		ContentCodeBg         color.Color    // Background color for syntax highlighting
		Body                  lipgloss.Style // Body content padding (PaddingLeft(2))

		// Deprecated - kept 为 backward compatibility
		ContentBg         lipgloss.Style // Content background
		ContentText       lipgloss.Style // Content text
		ContentLineNumber lipgloss.Style // Line numbers in code

		// State 消息 styles
		StateWaiting   lipgloss.Style // "Waiting for tool response..."
		StateCancelled lipgloss.Style // "Canceled."

		// 错误 styles
		ErrorTag     lipgloss.Style // ERROR tag
		ErrorMessage lipgloss.Style // Error message text

		// Warning styles (used 为 权限 denied)
		WarnTag     lipgloss.Style // WARN tag
		WarnMessage lipgloss.Style // Warning message text

		// Diff styles
		DiffTruncation lipgloss.Style // Diff truncation message with padding

		// Multi-edit note styles
		NoteTag     lipgloss.Style // NOTE tag (yellow background)
		NoteMessage lipgloss.Style // Note message text

		// Job header styles (为 bash jobs)
		JobIconPending lipgloss.Style // Pending job icon (green dark)
		JobIconError   lipgloss.Style // Error job icon (red dark)
		JobIconSuccess lipgloss.Style // Success job icon (green)
		JobToolName    lipgloss.Style // Job tool name "Bash" (blue)
		JobAction      lipgloss.Style // Action text (Start, Output, Kill)
		JobPID         lipgloss.Style // PID text
		JobDescription lipgloss.Style // Description text

		// Agent task styles
		AgentTaskTag lipgloss.Style // Agent task tag (blue background, bold)
		AgentPrompt  lipgloss.Style // Agent prompt text
		AgentName    lipgloss.Style // Subagent first name
		AgentLabel   lipgloss.Style // Label a subagent was dispatched with, after its first name
		AgentMessage lipgloss.Style // Message sent to a subagent

		// Agentic fetch styles
		AgenticFetchPromptTag lipgloss.Style // Agentic fetch prompt tag (green background, bold)

		// Todo styles
		TodoRatio          lipgloss.Style // Todo ratio (e.g., "2/5")
		TodoCompletedIcon  lipgloss.Style // Completed todo icon
		TodoInProgressIcon lipgloss.Style // In-progress todo icon
		TodoPendingIcon    lipgloss.Style // Pending todo icon
		TodoStatusNote     lipgloss.Style // " · completed N" / " · starting task" trailing note
		TodoItem           lipgloss.Style // Default body text for todo list items
		TodoJustStarted    lipgloss.Style // Text of the just-started todo in tool-call bodies

		// MCP tools
		MCPName     lipgloss.Style // The mcp name
		MCPToolName lipgloss.Style // The mcp tool name
		MCPArrow    lipgloss.Style // The mcp arrow icon

		// Images 和 external resources
		ResourceLoadedText      lipgloss.Style
		ResourceLoadedIndicator lipgloss.Style
		ResourceName            lipgloss.Style
		ResourceSize            lipgloss.Style
		MediaType               lipgloss.Style

		// Hooks
		HookLabel        lipgloss.Style // "Hook" label
		HookName         lipgloss.Style // Hook command name
		HookMatcher      lipgloss.Style // Matcher regex pattern
		HookArrow        lipgloss.Style // Arrow indicator
		HookDetail       lipgloss.Style // Decision detail text
		HookOK           lipgloss.Style // "OK" status
		HookDenied       lipgloss.Style // "Denied" status
		HookDeniedLabel  lipgloss.Style // "Hook" label when denied
		HookDeniedReason lipgloss.Style // Denied reason text
		HookRewrote      lipgloss.Style // "Rewrote Input" indicator

		// Action verb colors 为 工具-调用 headers.
		ActionCreate  lipgloss.Style // Constructive actions (e.g. "Add", "Create")
		ActionDestroy lipgloss.Style // Destructive actions (e.g. "Remove", "Delete")

		// 工具结果 helpers.
		ResultEmpty      lipgloss.Style // "No results" placeholder
		ResultTruncation lipgloss.Style // "… and N more" truncation line
		ResultItemName   lipgloss.Style // Item name (left column in result lists)
		ResultItemDesc   lipgloss.Style // Item description (right column)
	}

	// Dialog styles
	Dialog struct {
		Title              lipgloss.Style
		TitleText          lipgloss.Style
		TitleError         lipgloss.Style
		TitleAccent        lipgloss.Style
		TitleLineBase      lipgloss.Style // Base for the gradient ╱╱╱ next to dialog titles
		TitleGradFromColor color.Color    // Default dialog title ╱╱╱ gradient start
		TitleGradToColor   color.Color    // Default dialog title ╱╱╱ gradient end
		// View 是 main content area style.
		View          lipgloss.Style
		PrimaryText   lipgloss.Style
		SecondaryText lipgloss.Style
		// HelpView 是 line 那 contains help.
		HelpView lipgloss.Style
		Help     struct {
			Ellipsis       lipgloss.Style
			ShortKey       lipgloss.Style
			ShortDesc      lipgloss.Style
			ShortSeparator lipgloss.Style
			FullKey        lipgloss.Style
			FullDesc       lipgloss.Style
			FullSeparator  lipgloss.Style
		}

		NormalItem   lipgloss.Style
		SelectedItem lipgloss.Style
		InputPrompt  lipgloss.Style

		// PromptMessage 是 friendly description 的 what
		// integrated credential 提示词 (GPG pinentry) 是 asking 为.
		PromptMessage lipgloss.Style
		// PromptKey styles the labeled key rows shown under it.
		PromptKey lipgloss.Style
		// SSHMessage 是 friendly description 的 what integrated
		// SSH askpass 提示词 是 asking 为.
		SSHMessage lipgloss.Style
		// SSHKey styles labeled account/key 行 shown under it.
		SSHKey lipgloss.Style

		List lipgloss.Style

		Spinner lipgloss.Style

		// ContentPanel 是 used 为 content blocks 用 subtle background.
		ContentPanel   lipgloss.Style
		ContentPanelBg color.Color // Background color for ContentPanel syntax highlighting.

		// Scrollbar styles 为 scrollable content.
		ScrollbarThumb lipgloss.Style
		ScrollbarTrack lipgloss.Style

		// Arguments
		Arguments struct {
			Content                  lipgloss.Style
			Description              lipgloss.Style
			InputLabelBlurred        lipgloss.Style
			InputLabelFocused        lipgloss.Style
			InputRequiredMarkBlurred lipgloss.Style
			InputRequiredMarkFocused lipgloss.Style
		}

		// ListItem styles info-文本 rendered alongside list items (commands,
		// models, reasoning options). Sessions have their own overrides below.
		ListItem struct {
			InfoBlurred lipgloss.Style
			InfoFocused lipgloss.Style
		}

		Models struct {
			ConfiguredText lipgloss.Style // "Configured" badge shown on the ModelGroup header
		}

		Permissions struct {
			KeyText   lipgloss.Style // Left key cell of a key/value row
			ValueText lipgloss.Style // Right value cell of a key/value row
			ParamsBg  color.Color    // Background color behind highlighted JSON parameters
		}

		Quit struct {
			Content lipgloss.Style // Wrapper for the quit dialog's inner content
			Hint    lipgloss.Style // Style for quit hint
			Warning lipgloss.Style // Style for the work-in-progress warning
			Frame   lipgloss.Style // Outer rounded border framing the quit dialog
		}

		APIKey struct {
			Spinner lipgloss.Style // Loading spinner while validating the key
		}

		// AuthMethod styles the OAuth-vs-API-key choice dialog.
		AuthMethod struct {
			Prompt      lipgloss.Style // "How would you like to authenticate?" question line
			CardBlurred lipgloss.Style // Unselected choice card frame and label
			CardFocused lipgloss.Style // Selected choice card frame and label
		}

		OAuth struct {
			Spinner      lipgloss.Style // Loading spinner
			Instructions lipgloss.Style // Emphasized instruction text
			UserCode     lipgloss.Style // Prominent user code display
			Success      lipgloss.Style // Positive status text (e.g. "Authentication successful!")
			Link         lipgloss.Style // Underlined verification URL
			Enter        lipgloss.Style // "enter" keyword highlight in instructions
			ErrorText    lipgloss.Style // Error message when authentication fails
			StatusText   lipgloss.Style // Narrative status text ("Initializing...", "Verifying...", etc.)
			UserCodeBg   color.Color    // Background color of the centered user-code box
		}

		ImagePreview lipgloss.Style

		Sessions struct {
			// styles 为 当 we 是 在 删除 mode
			DeletingView                   lipgloss.Style
			DeletingItemFocused            lipgloss.Style
			DeletingItemBlurred            lipgloss.Style
			DeletingTitle                  lipgloss.Style
			DeletingMessage                lipgloss.Style
			DeletingTitleGradientFromColor color.Color
			DeletingTitleGradientToColor   color.Color

			// styles 为 当 we 是 在 更新 mode
			RenamingView                   lipgloss.Style
			RenamingingItemFocused         lipgloss.Style
			RenamingItemBlurred            lipgloss.Style
			RenamingingTitle               lipgloss.Style
			RenamingingMessage             lipgloss.Style
			RenamingTitleGradientFromColor color.Color
			RenamingTitleGradientToColor   color.Color
			RenamingPlaceholder            lipgloss.Style

			InfoBlurred lipgloss.Style // Timestamp text on unfocused session items
			InfoFocused lipgloss.Style // Timestamp text on the focused session item
		}
	}

	// Status bar 和 help
	Status struct {
		Help lipgloss.Style

		// Mode badges shown before the help hints.
		ModeBadgePlan     lipgloss.Style
		ModeBadgeYolo     lipgloss.Style
		ModeBadgeSysadmin lipgloss.Style

		// Full-width banners shown 当 switching modes.
		ModeBannerPlan          lipgloss.Style
		ModeBannerPlanBadge     lipgloss.Style
		ModeBannerYolo          lipgloss.Style
		ModeBannerYoloBadge     lipgloss.Style
		ModeBannerSysadmin      lipgloss.Style
		ModeBannerSysadminBadge lipgloss.Style

		ErrorIndicator   lipgloss.Style
		WarnIndicator    lipgloss.Style
		InfoIndicator    lipgloss.Style
		UpdateIndicator  lipgloss.Style
		SuccessIndicator lipgloss.Style

		ErrorMessage   lipgloss.Style
		WarnMessage    lipgloss.Style
		InfoMessage    lipgloss.Style
		UpdateMessage  lipgloss.Style
		SuccessMessage lipgloss.Style
	}

	// Completions popup styles
	Completions struct {
		Normal  lipgloss.Style
		Focused lipgloss.Style
		Match   lipgloss.Style
	}

	// Attachments styles
	Attachments AttachmentStyles

	// Pills styles 为 todo/队列 pills
	Pills struct {
		Base               lipgloss.Style // Base pill style with padding
		Focused            lipgloss.Style // Pill with visible rounded border
		QueueItemPrefix    lipgloss.Style // Prefix for queue list items
		QueueItemText      lipgloss.Style // Queue list item body text
		QueueLabel         lipgloss.Style // "N Queued" label text
		QueueIconBase      lipgloss.Style // Base style for queue gradient triangles
		QueueGradFromColor color.Color    // Start color for queue indicator gradient
		QueueGradToColor   color.Color    // End color for queue indicator gradient
		TodoLabel          lipgloss.Style // "To-Do" label
		TodoProgress       lipgloss.Style // Todo ratio (e.g. "2/5")
		TodoCurrentTask    lipgloss.Style // Current in-progress task name
		TodoSpinner        lipgloss.Style // Todo spinner style
		HelpKey            lipgloss.Style // Keystroke hint style
		HelpText           lipgloss.Style // Help action text style
		Area               lipgloss.Style // Pills area container
	}
}

// ChromaTheme converts current markdown chroma styles 到 chroma
// StyleEntries map.
func (s *Styles) ChromaTheme() chroma.StyleEntries {
	return ChromaEntries(s.Markdown.CodeBlock.Chroma)
}

// ChromaEntries converts markdown chroma 配置 到 chroma style entries,
// same way glamour does 为 code blocks. 一个 nil 配置 has no entries.
func ChromaEntries(c *ansi.Chroma) chroma.StyleEntries {
	if c == nil {
		return chroma.StyleEntries{}
	}
	return chroma.StyleEntries{
		chroma.Text:                chromaStyle(c.Text),
		chroma.Error:               chromaStyle(c.Error),
		chroma.Comment:             chromaStyle(c.Comment),
		chroma.CommentPreproc:      chromaStyle(c.CommentPreproc),
		chroma.Keyword:             chromaStyle(c.Keyword),
		chroma.KeywordReserved:     chromaStyle(c.KeywordReserved),
		chroma.KeywordNamespace:    chromaStyle(c.KeywordNamespace),
		chroma.KeywordType:         chromaStyle(c.KeywordType),
		chroma.Operator:            chromaStyle(c.Operator),
		chroma.Punctuation:         chromaStyle(c.Punctuation),
		chroma.Name:                chromaStyle(c.Name),
		chroma.NameBuiltin:         chromaStyle(c.NameBuiltin),
		chroma.NameTag:             chromaStyle(c.NameTag),
		chroma.NameAttribute:       chromaStyle(c.NameAttribute),
		chroma.NameClass:           chromaStyle(c.NameClass),
		chroma.NameConstant:        chromaStyle(c.NameConstant),
		chroma.NameDecorator:       chromaStyle(c.NameDecorator),
		chroma.NameException:       chromaStyle(c.NameException),
		chroma.NameFunction:        chromaStyle(c.NameFunction),
		chroma.NameOther:           chromaStyle(c.NameOther),
		chroma.Literal:             chromaStyle(c.Literal),
		chroma.LiteralNumber:       chromaStyle(c.LiteralNumber),
		chroma.LiteralDate:         chromaStyle(c.LiteralDate),
		chroma.LiteralString:       chromaStyle(c.LiteralString),
		chroma.LiteralStringEscape: chromaStyle(c.LiteralStringEscape),
		chroma.GenericDeleted:      chromaStyle(c.GenericDeleted),
		chroma.GenericEmph:         chromaStyle(c.GenericEmph),
		chroma.GenericInserted:     chromaStyle(c.GenericInserted),
		chroma.GenericStrong:       chromaStyle(c.GenericStrong),
		chroma.GenericSubheading:   chromaStyle(c.GenericSubheading),
		chroma.Background:          chromaStyle(c.Background),
	}
}

// AttachmentStyles 是 styles 为 one 附件 chip 行. They 是
// passed 到 attachments renderer 作为 unit 所以 adding style does 不
// mean touching every construction site.
type AttachmentStyles struct {
	Normal   lipgloss.Style
	Image    lipgloss.Style
	Text     lipgloss.Style
	Skill    lipgloss.Style
	Remove   lipgloss.Style
	Deleting lipgloss.Style
	More     lipgloss.Style // "N more…" hint for chips that didn't fit
}

// DialogHelpStyles returns styles 为 dialog help.
func (s *Styles) DialogHelpStyles() help.Styles {
	return help.Styles(s.Dialog.Help)
}

// Clone returns deep copy 的 Styles struct, ensuring 指针 fields
// (particularly within ansi.StyleConfig) 是 不 aliased. 这 是 used 到
// safely snapshot styles before a theme preview.
func (s *Styles) Clone() Styles {
	clone := *s
	clone.Markdown = cloneStyleConfig(s.Markdown)
	clone.QuietMarkdown = cloneStyleConfig(s.QuietMarkdown)
	return clone
}

// cloneStyleConfig deep-copies ansi.StyleConfig 通过 JSON round-tripping.
//
// NOTE: 这 assumes ansi.StyleConfig remains fully JSON-round-trippable.
// If glamour library adds non-serializable fields, 这 approach will
// need 到 是 replaced 用 reflect-based deep copy.
func cloneStyleConfig(src ansi.StyleConfig) ansi.StyleConfig {
	data, err := json.Marshal(src)
	if err != nil {
		slog.Error("Failed to marshal markdown styles for theme preview", "error", err)
		return src
	}
	var dst ansi.StyleConfig
	if err := json.Unmarshal(data, &dst); err != nil {
		slog.Error("Failed to unmarshal markdown styles for theme preview", "error", err)
		return src
	}
	return dst
}

// hex returns 指针 到 "#rrggbb" representation 的 c. 它's used 到
// satisfy glamour's string-指针 API 当 configuring markdown colors
// 从 theme palette.
func hex(c color.Color) *string {
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}

func chromaStyle(style ansi.StylePrimitive) string {
	var s strings.Builder

	if style.Color != nil {
		s.WriteString(*style.Color)
	}
	if style.BackgroundColor != nil {
		if s.Len() > 0 {
			s.WriteString(" ")
		}
		s.WriteString("bg:")
		s.WriteString(*style.BackgroundColor)
	}
	if style.Italic != nil && *style.Italic {
		if s.Len() > 0 {
			s.WriteString(" ")
		}
		s.WriteString("italic")
	}
	if style.Bold != nil && *style.Bold {
		if s.Len() > 0 {
			s.WriteString(" ")
		}
		s.WriteString("bold")
	}
	if style.Underline != nil && *style.Underline {
		if s.Len() > 0 {
			s.WriteString(" ")
		}
		s.WriteString("underline")
	}

	return s.String()
}
