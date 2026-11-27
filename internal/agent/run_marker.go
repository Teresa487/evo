package agent

import (
	"context"
	"sync/atomic"
)

// runCompleteMarkerKey 是 unexported 上下文 key carrying
// [runCompleteMarker] 从 派发 boundary (backend.runAgent)
// down into coordinator. 它 lets dispatcher learn whether
// coordinator already published authoritative 终态
// notify.RunComplete 为 运行, 所以 fallback 终态 事件 是 只
// emitted 当 one 是 actually missing (e.g. 错误 returned before
// sessionAgent.运行 ever executed). 它 avoids breaking change 到
// Coordinator interface.
type runCompleteMarkerKey struct{}

// runCompleteMarker records whether 终态 RunComplete has been
// published 为 运行. 它 是 shared 通过 指针 through 上下文 所以
// 发布 deep 在 调用 stack 是 observable 通过 dispatcher
// after 调用 returns.
type runCompleteMarker struct {
	published atomic.Bool
}

// WithRunCompleteMarker returns ctx carrying a fresh marker the
// coordinator 可以 flag via [MarkRunCompletePublished] once it emits
// 运行's 终态 RunComplete. Callers 读取 结果 用
// [RunCompletePublished]. Attaching marker 是 optional: code paths
// without one simply skip the dedup signal.
func WithRunCompleteMarker(ctx context.Context) context.Context {
	return context.WithValue(ctx, runCompleteMarkerKey{}, &runCompleteMarker{})
}

// MarkRunCompletePublished records 那 authoritative 终态
// RunComplete has been published 为 运行 carried 通过 ctx. 它 是
// no-op 当 no marker 是 present (e.g. 在-process/local 运行 路径,
// which 是 不 dispatched through backend.runAgent).
func MarkRunCompletePublished(ctx context.Context) {
	if m, ok := ctx.Value(runCompleteMarkerKey{}).(*runCompleteMarker); ok {
		m.published.Store(true)
	}
}

// RunCompletePublished reports whether [MarkRunCompletePublished] was
// called 在 ctx's marker. 它 returns false 当 no marker 是 present.
func RunCompletePublished(ctx context.Context) bool {
	if m, ok := ctx.Value(runCompleteMarkerKey{}).(*runCompleteMarker); ok {
		return m.published.Load()
	}
	return false
}
