package proto

// 会话 represents 会话 在 proto layer.
//
// IsBusy 是 computed 在 读取 (it 是 不 persisted 用 会话) 和
// reflects whether agent 运行 是 currently 在 flight 为 这 会话.
// 它 是 populated 通过 REST handlers 在 internal/服务端/proto.go 从
// 工作区's AgentCoordinator. 会话 SSE 事件 路径 does 不 set
// it, since SSE consumers can compute presence 从 other agent signals.
//
// AttachedClients counts number 的 clients currently viewing 这
// 会话 — i.e. entries 在 工作区's clients map whose
// currentSessionID equals 这 会话's ID 和 which have 在 least one
// live SSE 流. Hold-只 clients (streams == 0) do 不 contribute.
// Like IsBusy, it 是 computed 在 读取 通过 REST handlers.
type Session struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parent_session_id"`
	Title            string  `json:"title"`
	MessageCount     int64   `json:"message_count"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	EstimatedUsage   bool    `json:"estimated_usage,omitempty"`
	SummaryMessageID string  `json:"summary_message_id"`
	Cost             float64 `json:"cost"`
	Todos            []Todo  `json:"todos,omitempty"`
	Channel          string  `json:"channel,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	IsBusy           bool    `json:"is_busy"`
	AttachedClients  int     `json:"attached_clients"`
}

// Todo represents single todo 条目 在 会话 在 proto layer.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form"`
}
