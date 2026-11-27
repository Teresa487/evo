package dialog

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// ActionClose 是 消息 到 close current dialog.
type ActionClose struct{}

// ActionQuit 是 消息 到 quit application.
type ActionQuit = tea.QuitMsg

// ActionOpenDialog 是 消息 到 open dialog.
type ActionOpenDialog struct {
	DialogID string
}

// ActionSelectSession 是 消息 indicating 会话 has been selected.
type ActionSelectSession struct {
	Session session.Session
}

// ActionSelectModel 是 消息 indicating 模型 has been selected.
type ActionSelectModel struct {
	Provider       catwalk.Provider
	Model          config.SelectedModel
	ModelType      config.SelectedModelType
	ReAuthenticate bool
}

// Messages 为 commands
type (
	ActionNewSession              struct{}
	ActionToggleHelp              struct{}
	ActionToggleCompactMode       struct{}
	ActionToggleThinking          struct{}
	ActionTogglePills             struct{}
	ActionExternalEditor          struct{}
	ActionToggleYoloMode          struct{}
	ActionToggleSysadminMode      struct{}
	ActionToggleNotifications     struct{}
	ActionSelectNotificationStyle struct {
		Style string
	}
	ActionToggleTransparentBackground struct{}
	ActionToggleMouseSupport          struct{}
	ActionSwitchTheme                 struct {
		Theme string
	}
	ActionPreviewTheme struct {
		Theme string
	}
	ActionRevertThemePreview  struct{}
	ActionPreviewThemePalette struct {
		Base    string
		Palette styles.Palette
	}
	ActionSaveThemePalette struct {
		Name    string
		Base    string
		Palette styles.Palette
	}
	ActionEditTheme struct {
		Name string
	}
	ActionRevertThemePalette    struct{}
	ActionRevertOverriddenTheme struct {
		Name string
	}
	ActionCreateTheme struct {
		Name string
		Base string
	}
	ActionRenameTheme struct {
		OldName string
		NewName string
	}
	ActionDeleteTheme struct {
		Name string
	}
	ActionInitializeProject struct{}
	ActionSummarize         struct {
		SessionID string
	}
	// ActionSelectReasoningEffort 是 消息 indicating reasoning effort
	// has been selected.
	ActionSelectReasoningEffort struct {
		Effort string
	}
	// ActionPermissionResponse carries 用户's decision 在
	// 权限 请求.
	ActionPermissionResponse struct {
		Permission permission.PermissionRequest
		Action     PermissionAction
	}
	// Messages 为 integrated pinentry dialog.
	ActionPinentrySubmit struct {
		RequestID string
		Secret    string
	}
	ActionPinentryCancel struct {
		RequestID string
	}
	// Messages 为 integrated SSH askpass dialog. secret 只
	// ever travels through ActionSSHSubmit 到 工作区, mirroring
	// how 权限 dialog resolves 它的 requests.
	ActionSSHSubmit struct {
		RequestID string
		Secret    string
	}
	ActionSSHCancel struct {
		RequestID string
	}
	// ActionRunCustomCommand 是 消息 到 运行 custom command.
	ActionRunCustomCommand struct {
		Content   string
		Arguments []commands.Argument
		Args      map[string]string // Actual argument values
		Skill     *skills.Skill     // Set when this is a skill command
	}
	// ActionAttachSkill 是 sent 当 skill 是 selected 从 commands
	// dialog 到 是 attached 到 conversation 作为 markdown 附件.
	ActionAttachSkill struct {
		ID   string
		Name string
	}
	// ActionRunMCPPrompt 是 消息 到 运行 custom command.
	ActionRunMCPPrompt struct {
		Title       string
		Description string
		PromptID    string
		ClientID    string
		Arguments   []commands.Argument
		Args        map[string]string // Actual argument values
	}
	// ActionEnableDockerMCP 是 消息 到 enable Docker MCP.
	ActionEnableDockerMCP struct{}
	// ActionDisableDockerMCP 是 消息 到 disable Docker MCP.
	ActionDisableDockerMCP struct{}
)

// Messages 为 MCP OAuth authentication dialog.
type (
	// ActionMCPAuthStarted 是 sent 当 用户 approves authentication
	// 为 MCP 服务端. UI should initiate actual auth flow
	// using provided 上下文, which dialog will 取消 如果
	// 用户 closes it.
	ActionMCPAuthStarted struct {
		Name string
		Ctx  context.Context
	}

	// ActionMCPAuthComplete 是 sent 当 MCP authentication succeeds.
	ActionMCPAuthComplete struct {
		Name string
	}

	// ActionMCPAuthErrored 是 sent 当 MCP authentication fails.
	ActionMCPAuthErrored struct {
		Name  string
		Error error
	}
)

// Messages 为 API key input dialog.
type (
	ActionChangeAPIKeyState struct {
		State APIKeyInputState
	}
)

// Messages 为 OAuth2 device flow dialog.
type (
	// ActionInitiateOAuth 是 sent 当 device auth 是 initiated
	// successfully.
	ActionInitiateOAuth struct {
		DeviceCode      string
		UserCode        string
		ExpiresIn       int
		VerificationURL string
		Interval        int
	}

	// ActionCompleteOAuth 是 sent 当 device flow completes successfully.
	ActionCompleteOAuth struct {
		Token *oauth.Token
	}

	// ActionOAuthErrored 是 sent 当 device flow encounters 错误.
	ActionOAuthErrored struct {
		Error error
	}

	// ActionCloseOAuth closes OAuth dialog 和 runs given 清理
	// command, cancelling any 在-flight authorization. 它 exists 所以
	// dismissed dialog does 不 leave poller 或 loopback listener
	// running 在 background.
	ActionCloseOAuth struct {
		Cmd tea.Cmd
	}

	// ActionSelectAuthMethod 是 sent 当 用户 picks how 到
	// authenticate 提供方 那 supports both OAuth 和 API keys.
	ActionSelectAuthMethod struct {
		Provider  catwalk.Provider
		Model     config.SelectedModel
		ModelType config.SelectedModelType
		UseOAuth  bool
	}
)

// ActionCmd represents action 那 carries [tea.Cmd] 到 是 passed 到
// Bubble Tea program loop.
type ActionCmd struct {
	Cmd tea.Cmd
}

// ActionFilePickerSelected 是 消息 indicating 文件 has been selected 在
// 文件 picker dialog.
type ActionFilePickerSelected struct {
	Path string
}

// Cmd returns command 那 reads 文件 在 路径 和 sends
// [消息.Attachement] 到 program.
func (a ActionFilePickerSelected) Cmd() tea.Cmd {
	path := a.Path
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		isFileLarge, err := common.IsFileTooBig(path, common.MaxAttachmentSize)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
		}
		if isFileLarge {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "file too large, max 5MB",
			}
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
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
