// runid.go —— 在 context 里传递 RunID，串起一次请求的终态事件。

package agent

import "context"

// runIDContextKey 是 unexported 上下文 key used 到 carry
// caller-supplied RunID 从 工作区 HTTP boundary
// (backend.SendMessage) down into coordinator.运行 without forcing
// breaking change 到 Coordinator.运行 signature. 值 是
// then copied onto SessionAgentCall.RunID 通过 coordinator 所以
// agent's 终态 RunComplete 事件 can echo it back 到
// originating caller.
type runIDContextKey struct{}

// WithRunID returns ctx tagged 用 per-请求 RunID. 它 是
// boundary helper 为 callers 那 need their SendMessage→运行
// 终态 事件 到 是 uniquely correlatable (e.g. `crush 运行`
// against 会话 那 may 是 busy). Empty runIDs 是 存储
// 作为-是; downstream code treats empty RunID 作为 "caller did 不
// supply one" 和 falls back 到 SessionID-只 correlation.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDContextKey{}, runID)
}

// RunIDFromContext returns RunID set 通过 [WithRunID], 或 "" 如果
// none was set 或 值 是 不 string. Exported because
// coordinator 和 tests 在 other packages need 到 读取 it; safe 到
// 调用 在 any 上下文.
func RunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(runIDContextKey{}).(string); ok {
		return v
	}
	return ""
}
