package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// refusingModel refuses 它的 first `refusals` streams, then answers.
type refusingModel struct {
	fakeLanguageModel
	refusals int
	calls    int
}

func (m *refusingModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	m.calls++
	reason := fantasy.FinishReasonStop
	if m.calls <= m.refusals {
		reason = fantasy.FinishReasonContentFilter
	}
	return func(yield func(fantasy.StreamPart) bool) {
		_ = yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"}) &&
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: "answer"}) &&
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"}) &&
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: reason})
	}, nil
}

// 一个 拒绝 是 retryable 只 while turn has retries left.
func TestRefusalRetryModel_SpendsTheTurnsBudget(t *testing.T) {
	t.Parallel()
	ctx := withRefusalRetries(t.Context(), 1)
	model := newRefusalRetryModel(&refusingModel{refusals: 3})
	retryable := func(ctx context.Context) bool {
		stream, err := model.Stream(ctx, fantasy.Call{})
		require.NoError(t, err)
		var last fantasy.StreamPart
		for part := range stream {
			last = part
		}
		require.Equal(t, fantasy.StreamPartTypeError, last.Type)
		require.ErrorIs(t, last.Error, errRefused)
		var providerErr *fantasy.ProviderError
		require.True(t, errors.As(last.Error, &providerErr))
		return providerErr.IsRetryable()
	}
	require.True(t, retryable(ctx), "the first refusal is retried")
	require.False(t, retryable(ctx), "the budget is spent")
	require.False(t, retryable(t.Context()), "without a budget nothing is retried")
}

func TestRefusalRetryModel_PassesOtherFinishesThrough(t *testing.T) {
	t.Parallel()
	stream, err := newRefusalRetryModel(&refusingModel{}).Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var last fantasy.StreamPart
	for part := range stream {
		last = part
	}
	require.Equal(t, fantasy.StreamPartTypeFinish, last.Type)
	require.Equal(t, fantasy.FinishReasonStop, last.FinishReason)
}

// TestRefusalRetryModel_AgentRetriesRefusal runs real agent loop, 所以 it
// waits out one 重试 backoff.
func TestRefusalRetryModel_AgentRetriesRefusal(t *testing.T) {
	t.Parallel()
	model := &refusingModel{refusals: 1}
	retries := 0
	ctx := withRefusalRetries(t.Context(), refusalRetriesPerTurn)
	result, err := fantasy.NewAgent(newRefusalRetryModel(model)).Stream(ctx, fantasy.AgentStreamCall{
		Prompt:  "hi",
		OnRetry: func(*fantasy.ProviderError, time.Duration) { retries++ },
	})
	require.NoError(t, err)
	require.Equal(t, 1, retries)
	require.Equal(t, 2, model.calls)
	require.Equal(t, "answer", result.Response.Content.Text())
}

// 一个 second 拒绝 在 turn 是 shown rather than retried again.
func TestRefusalRetryModel_AgentStopsAfterOneRetry(t *testing.T) {
	t.Parallel()
	model := &refusingModel{refusals: 5}
	ctx := withRefusalRetries(t.Context(), refusalRetriesPerTurn)
	_, err := fantasy.NewAgent(newRefusalRetryModel(model)).Stream(ctx, fantasy.AgentStreamCall{Prompt: "hi"})
	require.ErrorIs(t, err, errRefused)
	require.Equal(t, 2, model.calls)
}

func TestTurnEndReason_Refused(t *testing.T) {
	t.Parallel()
	require.Equal(t, refusedToolCallResult, turnEndReason(&message.Message{}, refusedError(false)))
}
