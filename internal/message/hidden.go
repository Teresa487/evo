package message

import "context"

type hiddenUserMessageKey struct{}

// WithHiddenUserMessage marks generated continuation 为 模型 history 只.
func WithHiddenUserMessage(ctx context.Context) context.Context {
	return context.WithValue(ctx, hiddenUserMessageKey{}, true)
}

// HiddenUserMessage reports whether the caller requested a hidden continuation.
func HiddenUserMessage(ctx context.Context) bool {
	hidden, _ := ctx.Value(hiddenUserMessageKey{}).(bool)
	return hidden
}
