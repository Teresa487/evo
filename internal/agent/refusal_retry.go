// refusal_retry.go —— provider 安全分类器拒绝响应时的一次性重试。
//
// 把拒绝变成可重试的 ProviderError，让 fantasy 重发该步。

package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

	"charm.land/fantasy"
)

// refusalMessage 是 what 模型 和 用户 see once 拒绝 是 final.
const refusalMessage = "The provider's safety classifier refused this response. Rephrase the request or try a different model."

// refusalRetriesPerTurn 是 how many refusals turn retries before showing
// one. 一个 misfire usually clears 在 first 重试; more mostly waits.
const refusalRetriesPerTurn = 1

type refusalBudgetKey struct{}

// withRefusalRetries gives turn 在 ctx budget 的 拒绝 retries.
func withRefusalRetries(ctx context.Context, n int32) context.Context {
	budget := new(atomic.Int32)
	budget.Store(n)
	return context.WithValue(ctx, refusalBudgetKey{}, budget)
}

// takeRefusalRetry spends one 重试 从 ctx's budget; without budget
// there 是 none 到 spend.
func takeRefusalRetry(ctx context.Context) bool {
	budget, ok := ctx.Value(refusalBudgetKey{}).(*atomic.Int32)
	return ok && budget.Add(-1) >= 0
}

// errRefused marks 响应 提供方's safety classifier stopped.
var errRefused = errors.New("response refused by the provider's safety classifier")

// refusalRetryModel turns refused 响应 into 错误, retryable while
// turn's 拒绝 budget lasts, 所以 fantasy re-requests step 和 resets
// what the refused attempt streamed.
type refusalRetryModel struct {
	fantasy.LanguageModel
}

func newRefusalRetryModel(m fantasy.LanguageModel) fantasy.LanguageModel {
	if m == nil {
		return nil
	}
	return refusalRetryModel{LanguageModel: m}
}

// 流 implements [fantasy.LanguageModel].
func (m refusalRetryModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		inner(func(part fantasy.StreamPart) bool {
			if part.Type == fantasy.StreamPartTypeFinish && part.FinishReason == fantasy.FinishReasonContentFilter {
				retry := takeRefusalRetry(ctx)
				slog.Warn("Provider safety classifier refused the response", "retrying", retry)
				return yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: refusedError(retry)})
			}
			return yield(part)
		})
	}, nil
}

func refusedError(retryable bool) *fantasy.ProviderError {
	return &fantasy.ProviderError{
		Title:          "Refused",
		Message:        refusalMessage,
		Cause:          errRefused,
		TransientError: retryable,
	}
}
