// capped_buffer.go —— 有上限的输出缓冲，避免内存被长输出撑爆。

package shell

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// maxKeptOutput bounds one output 流: plenty 为 spill 文件, 和 it
// caps what each retained finished job holds.
const maxKeptOutput = 1 << 20

// cappedBuffer keeps first 和 last limit/2 bytes 的 流 在 fixed
// memory, 所以 command 那 never stops writing cannot exhaust it. 它 是
// safe 为 concurrent use.
type cappedBuffer struct {
	mu      sync.Mutex
	half    int
	head    []byte
	ring    []byte // The latest bytes past the head, oldest at next once full.
	next    int
	dropped int64
	seq     atomic.Uint64
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{half: limit / 2}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	take := min(b.half-len(b.head), len(p))
	b.head = append(b.head, p[:take]...)
	p = p[take:]
	if fill := min(b.half-len(b.ring), len(p)); fill > 0 {
		b.ring = append(b.ring, p[:fill]...)
		p = p[fill:]
	}
	if len(p) > b.half {
		b.dropped += int64(len(p) - b.half)
		p = p[len(p)-b.half:]
	}
	for len(p) > 0 {
		c := copy(b.ring[b.next:], p)
		b.dropped += int64(c)
		b.next = (b.next + c) % b.half
		p = p[c:]
	}
	b.seq.Add(1)
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	older, newer := b.ring[b.next:], b.ring[:b.next]
	if b.dropped == 0 {
		var sb strings.Builder
		sb.Grow(len(b.head) + len(b.ring))
		sb.Write(b.head)
		sb.Write(older)
		sb.Write(newer)
		return sb.String()
	}

	// Trim partial runes 在 seams, 在 most UTFMax bytes 所以 二进制 survives.
	head := b.head
	start := len(head) - 1
	for start > 0 && len(head)-start < utf8.UTFMax && !utf8.RuneStart(head[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(head[start:]) {
		head = head[:start]
	}
	skip := 0
	for skip < len(b.ring) && skip < utf8.UTFMax-1 && !utf8.RuneStart(ringByte(older, newer, skip)) {
		skip++
	}
	dropped := b.dropped + int64(len(b.head)-len(head)+skip)
	if skip < len(older) {
		older = older[skip:]
	} else {
		newer = newer[skip-len(older):]
		older = nil
	}

	const prefix, suffix = "\n\n... [", " bytes truncated] ...\n\n"
	count := strconv.FormatInt(dropped, 10)
	var sb strings.Builder
	sb.Grow(len(head) + len(prefix) + len(count) + len(suffix) + len(older) + len(newer))
	sb.Write(head)
	sb.WriteString(prefix)
	sb.WriteString(count)
	sb.WriteString(suffix)
	sb.Write(older)
	sb.Write(newer)
	return sb.String()
}

// Tail returns 在 most last maxBytes 的 output without copying whole
// 缓冲. When bytes were cut 从 front, leading partial UTF-8 bytes 是
// skipped 所以 valid UTF-8 stays valid.
func (b *cappedBuffer) Tail(maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	older, newer := b.ring[b.next:], b.ring[:b.next]
	var head []byte
	cut := b.dropped > 0

	if len(b.ring) >= maxBytes {
		skip := len(b.ring) - maxBytes
		if skip > 0 {
			cut = true
		}
		if skip < len(older) {
			older = older[skip:]
		} else {
			newer = newer[skip-len(older):]
			older = nil
		}
	} else if b.dropped == 0 {
		needHead := maxBytes - len(b.ring)
		if needHead < len(b.head) {
			head = b.head[len(b.head)-needHead:]
			cut = true
		} else {
			head = b.head
		}
	}

	total := len(head) + len(older) + len(newer)
	if total == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(total)
	sb.Write(head)
	sb.Write(older)
	sb.Write(newer)
	s := sb.String()
	if cut {
		skip := 0
		for skip < len(s) && skip < utf8.UTFMax-1 && !utf8.RuneStart(s[skip]) {
			skip++
		}
		s = s[skip:]
	}
	return s
}

// ringByte returns byte i 的 tail, which 是 older followed 通过 newer.
func ringByte(older, newer []byte, i int) byte {
	if i < len(older) {
		return older[i]
	}
	return newer[i-len(older)]
}
