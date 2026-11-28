// git.go —— 服务端的 Git 相关端点（状态、分支信息）。

package server

import (
	"net/http"

	"github.com/charmbracelet/crush/internal/proto"
)

// handleGetWorkspaceGitBranch returns current Git branch 的
// 工作区's working 目录.
//
// @摘要 Get current Git branch
// @Tags			git
// @Param id 路径 string true "工作区 ID"
// @Success		200	{object}	proto.GitBranchResponse
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router			/workspaces/{id}/git/branch [get]
func (c *controllerV1) handleGetWorkspaceGitBranch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	branch, err := c.backend.GitBranch(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.GitBranchResponse{Branch: branch})
}
