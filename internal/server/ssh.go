package server

import (
	"encoding/json"
	"net/http"

	"github.com/charmbracelet/crush/internal/proto"
)

// handlePostWorkspaceSSHAnswer resolves the pending integrated SSH
// 提示词 用 用户's secret.
//
// @摘要 Answer SSH 提示词
//	@Tags			ssh
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.SSHAnswer true "SSH answer"
//	@Success		200	{object}	proto.SSHResponse
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/ssh/answer [post]
func (c *controllerV1) handlePostWorkspaceSSHAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.SSHAnswer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	resolved, err := c.backend.AnswerSSH(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.SSHResponse{Resolved: resolved})
}

// handlePostWorkspaceSSHCancel dismisses the pending integrated SSH
// 提示词 为 工作区.
//
// @摘要 取消 SSH 提示词
//	@Tags			ssh
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.SSHCancel true "SSH 取消"
//	@Success		200	{object}	proto.SSHResponse
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router /workspaces/{id}/ssh/取消 [post]
func (c *controllerV1) handlePostWorkspaceSSHCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.SSHCancel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	cancelled, err := c.backend.CancelSSH(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.SSHResponse{Resolved: cancelled})
}
