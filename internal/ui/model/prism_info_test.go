package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// prismWorkspace 是 minimal [工作区.工作区] stub providing
// methods 助手-info rendering 路径 needs.
type prismWorkspace struct {
	workspace.Workspace
}

func (w *prismWorkspace) Config() *config.Config { return &config.Config{} }
func (w *prismWorkspace) WorkingDir() string     { return "/" }

func newPrismTestUI() *UI {
	com := common.DefaultCommon(&prismWorkspace{})
	return &UI{
		com:     com,
		status:  NewStatus(com, nil),
		chat:    NewChat(com, config.ScrollbarDefault),
		state:   uiChat,
		focus:   uiFocusEditor,
		width:   140,
		height:  45,
		session: &session.Session{ID: "s1"},
		keyMap:  DefaultKeyMap(),
		dialog:  dialog.NewOverlay(),
	}
}

func prismToolTurn(finished bool, prismName string) message.Message {
	parts := []message.ContentPart{
		message.ToolCall{ID: "tc1", Name: "bash", Input: "{}", Finished: true},
	}
	if finished {
		parts = append(parts, message.Finish{Reason: message.FinishReasonToolUse, Time: 1735689600})
	}
	return message.Message{
		ID:             "a1",
		SessionID:      "s1",
		Role:           message.Assistant,
		Model:          "prism-model",
		Provider:       "hyper",
		Parts:          parts,
		PrismModelName: prismName,
	}
}

// TestUpdateSessionMessage_PrismInfoBetweenTurns reproduces the between-turn
// flow: 助手消息 streams without finish 片段, then turn
// finishes 作为 工具-use turn 用 Prism-routed 模型 name. info item
// must appear once routed name 是 available.
func TestUpdateSessionMessage_PrismInfoBetweenTurns(t *testing.T) {
	m := newPrismTestUI()

	// Turn streams: no finish 片段, no prism info item yet.
	_ = m.appendSessionMessage(prismToolTurn(false, ""))
	require.Nil(t, m.chat.MessageItem(chat.AssistantInfoID("a1")))

	// An 更新 mid-流 changes nothing.
	_ = m.updateSessionMessage(prismToolTurn(false, ""))
	require.Nil(t, m.chat.MessageItem(chat.AssistantInfoID("a1")))

	// Turn finishes 作为 工具-use turn 用 routed 模型 name:
	// info item must 是 appended.
	_ = m.updateSessionMessage(prismToolTurn(true, "GLM 5.3"))
	require.NotNil(t, m.chat.MessageItem(chat.AssistantInfoID("a1")), "expected assistant info item between turns")

	// 一个 later 更新 为 same turn must 不 remove it.
	_ = m.updateSessionMessage(prismToolTurn(true, "GLM 5.3"))
	require.NotNil(t, m.chat.MessageItem(chat.AssistantInfoID("a1")))
}

// TestUpdateSessionMessage_NoPrismInfoWithoutName ensures intermediate turns
// without Prism-routed 模型 name do 不 get info item.
func TestUpdateSessionMessage_NoPrismInfoWithoutName(t *testing.T) {
	m := newPrismTestUI()

	_ = m.appendSessionMessage(prismToolTurn(false, ""))
	_ = m.updateSessionMessage(prismToolTurn(true, ""))
	require.Nil(t, m.chat.MessageItem(chat.AssistantInfoID("a1")))
}
