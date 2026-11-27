// history.go —— 协议里的历史/文件追踪类型。

package proto

// 文件 represents 文件 tracked 在 会话 history.
type File struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Version   int64  `json:"version"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}
