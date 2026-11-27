// channel.go —— 在 context 里传递当前渠道名。
//
// Run 把 call.Channel 放进 ctx，工具/回调通过 ChannelFromContext 读取，
// 用于渠道权限和回复路由。

package agent

import "context"

type channelContextKey struct{}

// WithChannel returns ctx tagged 用 渠道 那 originated turn.
func WithChannel(ctx context.Context, channel string) context.Context {
	return context.WithValue(ctx, channelContextKey{}, channel)
}

// ChannelFromContext returns originating 渠道, 或 empty string 为 local turns.
func ChannelFromContext(ctx context.Context) string {
	if channel, ok := ctx.Value(channelContextKey{}).(string); ok {
		return channel
	}
	return ""
}
