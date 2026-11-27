// question.go —— agent 提问工具的后端支撑：把问题转给客户端等待回答。

package backend

import (
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/question"
)

// AnswerQuestion submits answers 为 question batch 请求
// names. returned bool reports whether 这 调用 resolved
// pending batch (true) 或 found it already resolved 通过 previous
// caller (false).
func (b *Backend) AnswerQuestion(workspaceID string, req proto.QuestionAnswer) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}

	responses := make([]question.Answer, len(req.Responses))
	for i, r := range req.Responses {
		responses[i] = question.Answer{
			QuestionID:  r.QuestionID,
			SelectedIDs: r.SelectedIDs,
			FillInText:  r.FillInText,
			Yes:         r.Yes,
			Notes:       r.Notes,
		}
	}

	return ws.Questions.Answer(req.BatchRequestID, responses), nil
}

// CancelQuestion cancels pending question batch 用 given ID
// 为 工作区. Returns true 如果 question was cancelled, false 如果
// none was pending.
func (b *Backend) CancelQuestion(workspaceID, batchID string) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}
	return ws.Questions.Cancel(batchID), nil
}
