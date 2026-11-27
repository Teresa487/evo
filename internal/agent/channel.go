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
