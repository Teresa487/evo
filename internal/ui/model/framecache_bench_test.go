package model

import "testing"

// clamped overscroll 在 bottom: case PR targets.
func BenchmarkView_ClampedOverscroll_Cached(b *testing.B) {
	u := newFrameTestUI(b)
	u.chat.ScrollToBottom()
	u.View()
	b.ResetTimer()
	for b.Loop() {
		u.beginFrameUpdate()
		u.markScrollOnly()
		u.chat.ScrollBy(5)
		u.View()
	}
	b.ReportMetric(float64(u.frames.hits), "hits")
	b.ReportMetric(float64(u.frames.misses), "misses")
}

func BenchmarkView_ClampedOverscroll_Uncached(b *testing.B) {
	u := newFrameTestUI(b)
	u.frames = nil
	u.chat.ScrollToBottom()
	u.View()
	b.ResetTimer()
	for b.Loop() {
		u.beginFrameUpdate()
		u.chat.ScrollBy(5)
		u.View()
	}
}

// moving scroll: every frame 是 new position, 所以 缓存 只 costs.
func BenchmarkView_MovingScroll_Cached(b *testing.B) {
	u := newFrameTestUI(b)
	u.chat.ScrollToTop()
	u.View()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		u.beginFrameUpdate()
		u.markScrollOnly()
		if i%150 == 149 {
			u.chat.ScrollToTop()
		} else {
			u.chat.ScrollBy(1)
		}
		i++
		u.View()
	}
	b.ReportMetric(float64(u.frames.hits), "hits")
	b.ReportMetric(float64(u.frames.misses), "misses")
}

func BenchmarkView_MovingScroll_Uncached(b *testing.B) {
	u := newFrameTestUI(b)
	u.frames = nil
	u.chat.ScrollToTop()
	u.View()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		u.beginFrameUpdate()
		if i%150 == 149 {
			u.chat.ScrollToTop()
		} else {
			u.chat.ScrollBy(1)
		}
		i++
		u.View()
	}
}

// how much does 32-条目 LRU buy over single-frame 缓存?
func benchOscillate(b *testing.B, maxEntries int) {
	u := newFrameTestUI(b)
	u.frames = newFrameCache(frameCacheTTL, maxEntries)
	u.chat.ScrollToTop()
	u.View()
	// wander within small 窗口, best case 为 LRU
	deltas := []int{1, 1, -1, 2, -2, 1, -1, -1}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		u.beginFrameUpdate()
		u.markScrollOnly()
		u.chat.ScrollBy(deltas[i%len(deltas)])
		u.View()
	}
	b.ReportMetric(float64(u.frames.hits), "hits")
	b.ReportMetric(float64(u.frames.misses), "misses")
}

func BenchmarkView_Oscillate_LRU1(b *testing.B)  { benchOscillate(b, 1) }
func BenchmarkView_Oscillate_LRU32(b *testing.B) { benchOscillate(b, 32) }
