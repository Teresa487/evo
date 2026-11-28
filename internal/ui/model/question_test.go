package model

import (
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/notification"
	"github.com/stretchr/testify/require"
)

// questionWorkspace 是 countingWorkspace 那 records answers
// question form submits.
type questionWorkspace struct {
	*countingWorkspace
	answers [][]question.Answer
}

func (w *questionWorkspace) QuestionAnswer(_ string, responses []question.Answer) bool {
	w.answers = append(w.answers, responses)
	return true
}

// recordingNotifier 是 通知 backend 那 records what it 是
// asked 到 send.
type recordingNotifier struct {
	sent []notification.Notification
}

func (r *recordingNotifier) Send(n notification.Notification) tea.Cmd {
	r.sent = append(r.sent, n)
	return nil
}

// newQuestionUI builds UI 那 runs question events through 更新 用
// EVO_QUESTIONS_PIPED set 到 piped, 所以 值 inherited 从
// environment cannot leak 在. chat starts scrolled 到 top 和
// 窗口 unfocused, 所以 scroll 和 通知 question triggers
// can 是 observed.
func newQuestionUI(t *testing.T, piped string) (*UI, *questionWorkspace, *recordingNotifier) {
	t.Helper()
	t.Setenv("EVO_QUESTIONS_PIPED", piped)

	u := newProgramStatusUI(t)
	ws := &questionWorkspace{countingWorkspace: u.com.Workspace.(*countingWorkspace)}
	u.com.Workspace = ws
	notifier := &recordingNotifier{}
	u.notifyBackend = notifier
	u.caps.ReportFocusEvents = true
	u.notifyWindowFocused = false

	msgs := make([]chat.MessageItem, 0, 60)
	for i := range 60 {
		msgs = append(msgs, testMessageItem{
			id:   "m-" + strconv.Itoa(i),
			text: "message " + strconv.Itoa(i),
		})
	}
	u.chat.SetMessages(msgs...)
	u.updateLayoutAndSize()
	u.chat.ScrollToTop()
	require.False(t, u.chat.AtBottom())
	return u, ws, notifier
}

// questionEvent 是 事件 工作区 publishes 当 agent asks
// batch 用 given ID, 和 again whenever batch 是 replayed.
func questionEvent(id string) pubsub.Event[question.Request] {
	return pubsub.Event[question.Request]{
		Type: pubsub.CreatedEvent,
		Payload: question.Request{
			ID:        id,
			SessionID: "s1",
			Questions: []question.Question{{
				ID:          id + "-q1",
				Type:        question.TypeFreeText,
				Text:        "Anything else?",
				Description: "Free-form notes.",
			}},
		},
	}
}

func TestQuestionRequestOpensForm(t *testing.T) {
	u, _, notifier := newQuestionUI(t, "")

	_, _ = u.Update(questionEvent("b1"))

	require.IsType(t, &dialog.QuestionForm{}, u.activeInline)
	require.True(t, u.hasPendingQuestion())
	require.True(t, u.chat.AtBottom())
	require.Len(t, notifier.sent, 1)
}

func TestQuestionRequestPiped(t *testing.T) {
	u, _, notifier := newQuestionUI(t, "1")

	_, _ = u.Update(questionEvent("b1"))

	require.Nil(t, u.activeInline)
	require.False(t, u.hasPendingQuestion())
	require.False(t, u.chat.AtBottom())
	require.Empty(t, notifier.sent)
}

func TestQuestionReplayKeepsForm(t *testing.T) {
	u, ws, notifier := newQuestionUI(t, "")

	_, _ = u.Update(questionEvent("b1"))
	form := u.activeInline
	_, _ = u.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	u.chat.ScrollToTop()

	// 服务端 re-sends pending batch whenever 客户端 reports 它的
	// 会话; 重放 must 不 wipe what was typed, nor scroll chat
	// 或 notify again.
	_, _ = u.Update(questionEvent("b1"))
	require.Same(t, form, u.activeInline)
	require.False(t, u.chat.AtBottom())
	require.Len(t, notifier.sent, 1)

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, u.activeInline)
	require.Equal(t, [][]question.Answer{{{QuestionID: "b1-q1", FillInText: "x"}}}, ws.answers)
}

func TestQuestionNewBatchReplacesForm(t *testing.T) {
	u, _, _ := newQuestionUI(t, "")

	_, _ = u.Update(questionEvent("b1"))
	form := u.activeInline

	_, _ = u.Update(questionEvent("b2"))
	require.NotSame(t, form, u.activeInline)
	require.Equal(t, "b2", u.activeInline.(*dialog.QuestionForm).BatchID)
}
