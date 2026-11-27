// server.go —— 协议里的服务端信息与握手类型。

package proto

// 服务端 control commands accepted 通过 /control endpoint.
const (
	// ServerControlShutdown 是 original, unconditional spelling.
	// Servers still 受理 it, 但 now apply same idleness check 作为
	// ServerControlShutdownIfIdle 所以 那 clients predating check
	// cannot take live sessions down.
	ServerControlShutdown = "shutdown"
	// ServerControlShutdownIfIdle asks 服务端 到 shut down 只 如果 it
	// 是 空闲. 它 exists 所以 客户端 can tell whether 服务端 checks
	// 那 condition 在 all: servers predating check reject
	// unknown command instead 的 shutting down 和 taking other sessions
	// 用 them.
	ServerControlShutdownIfIdle = "shutdown_if_idle"
)

// ServerControl represents 服务端 control 请求.
type ServerControl struct {
	Command string `json:"command"`
}
