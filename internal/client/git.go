package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// GitBranch returns current Git branch 的 工作区's working
// 目录 作为 reported 通过 服务端. 它 returns empty string 当
// 目录 是 不 Git repository 或 HEAD 是 detached.
func (c *Client) GitBranch(ctx context.Context, id string) (string, error) {
	rsp, err := c.get(ctx, fmt.Sprintf("/workspaces/%s/git/branch", id), nil, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get git branch: %w", err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get git branch: status code %d", rsp.StatusCode)
	}
	var result struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(rsp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode git branch response: %w", err)
	}
	return result.Branch, nil
}
