package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// maxHostThemeBody bounds body 的 host theme 请求. 一个 完成
// theme (every palette 和 syntax key, generous names) 是 few KiB.
const maxHostThemeBody = 64 << 10

// handleGetWorkspaceTheme returns 工作区's host theme.
func (c *controllerV1) handleGetWorkspaceTheme(w http.ResponseWriter, r *http.Request) {
	st, err := c.backend.GetHostTheme(r.PathValue("id"))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, st)
}

// handlePostWorkspaceTheme sets 或, 用 null theme, clears
// 工作区's host theme 和 responds 用 resulting state. 一个 body
// without "theme" key 是 rejected rather than 读取 作为 clear, 所以
// theme sent without 它的 wrapper cannot wipe current one.
func (c *controllerV1) handlePostWorkspaceTheme(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxHostThemeBody)
	// theme stays raw 在 first 所以 missing key can 是 told apart
	// 从 explicit null. wire shape 是 [proto.HostThemeRequest].
	var req struct {
		Theme json.RawMessage `json:"theme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			jsonError(w, http.StatusRequestEntityTooLarge, "theme request is too large")
			return
		}
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if len(req.Theme) == 0 {
		jsonError(w, http.StatusBadRequest, `missing theme; send {"theme": null} to clear it`)
		return
	}
	var theme *proto.HostTheme
	if err := json.Unmarshal(req.Theme, &theme); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	st, err := c.backend.SetHostTheme(id, theme)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, st)
}

// replayHostTheme writes 工作区's current host theme, 如果 one 是
// set, 到 客户端 那 just attached 它的 事件 流. 一个 客户端 那
// starts 或 reconnects after host set 它的 theme would otherwise keep
// 它的 own colors until host changes theme again. 它 runs after
// 流 subscribed, 所以 更新 racing attach 是 delivered again 通过
// 事件 loop rather than lost; clients drop duplicate.
func (c *controllerV1) replayHostTheme(w http.ResponseWriter, r *http.Request, flusher *http.ResponseController, workspaceID string) {
	st, err := c.backend.GetHostTheme(workspaceID)
	if err != nil || st.Theme == nil {
		return
	}
	c.writeEvent(w, r, flusher, pubsub.Event[proto.HostThemeState]{
		Type:    pubsub.UpdatedEvent,
		Payload: st,
	})
}

// replayClientSessions writes 会话 each 客户端 的 工作区
// views 到 客户端 那 just attached 它的 事件 流, 所以 那 it knows
// sessions 的 clients 那 were already there. 它 runs after
// 流 subscribed, like replayHostTheme.
func (c *controllerV1) replayClientSessions(w http.ResponseWriter, r *http.Request, flusher *http.ResponseController, workspaceID string) {
	sessions, err := c.backend.ClientSessions(workspaceID)
	if err != nil {
		return
	}
	for _, s := range sessions {
		c.writeEvent(w, r, flusher, pubsub.Event[proto.ClientSession]{
			Type:    pubsub.UpdatedEvent,
			Payload: s,
		})
	}
}
