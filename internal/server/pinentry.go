// pinentry.go —— 凭据输入请求在服务端与客户端之间的转发。

package server

import (
	"encoding/json"
	"net/http"

	"github.com/charmbracelet/crush/internal/proto"
)

// handlePostWorkspacePinentryAnswer resolves the pending integrated
// pinentry 提示词 用 用户's secret.
//
// @摘要 Answer pinentry 提示词
// @Tags			pinentry
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.PinentryAnswer true "Pinentry answer"
// @Success		200	{object}	proto.PinentryResponse
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router			/workspaces/{id}/pinentry/answer [post]
func (c *controllerV1) handlePostWorkspacePinentryAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.PinentryAnswer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	resolved, err := c.backend.AnswerPinentry(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.PinentryResponse{Resolved: resolved})
}

// handlePostWorkspacePinentryCancel dismisses the pending integrated
// pinentry 提示词 为 工作区.
//
// @摘要 取消 pinentry 提示词
// @Tags			pinentry
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.PinentryCancel true "Pinentry 取消"
// @Success		200	{object}	proto.PinentryResponse
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router /workspaces/{id}/pinentry/取消 [post]
func (c *controllerV1) handlePostWorkspacePinentryCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.PinentryCancel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	cancelled, err := c.backend.CancelPinentry(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.PinentryResponse{Resolved: cancelled})
}
