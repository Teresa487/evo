package common

import (
	"fmt"
	"image/color"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/xchroma"
)

const formatterName = "evo"

// Code block colors.
//
// Glamour turns style 配置's code-block chroma settings into chroma
// style registered globally under one fixed name, 和 只 first time:
// renderers built after theme switch kept highlighting fenced code 用
// colors 的 first theme ever rendered. Registering again 是 no fix,
// because chroma's registry 是 plain map 那 every highlight reads
// without 锁, 所以 writing it while renderer runs 在 another goroutine
// 是 data race.
//
// So registry 是 只 written here, 在 init: it gets fixed set 的
// empty placeholder styles. Each renderer's 配置 names one placeholder
// instead 的 carrying chroma settings ([codeBlockConfig]), placeholder
// stands 为 one distinct set 的 settings, 和 "crush" formatter every
// renderer uses swaps placeholder 为 real style while it formats.
// Placeholders 是 reused round-robin once all 是 taken; renderers 是
// rebuilt 在 every theme change, 所以 live ones always share current
// settings.
const codeBlockSlots = 32

var (
	// codeBlockSlotOf maps each placeholder style 到 它的 slot. 它 是
	// written 只 在 init.
	codeBlockSlotOf = map[*chroma.Style]int{}

	// codeBlockMu guards the slot assignments below.
	codeBlockMu     sync.Mutex
	codeBlockByKey  = map[string]int{}
	codeBlockKeys   [codeBlockSlots]string
	codeBlockStyles [codeBlockSlots]*chroma.Style
	codeBlockNext   int
)

func init() {
	// NOTE: Glamour does 不 offer us option 到 pass formatter
	// implementation directly. We need 到 register 和 use 通过 name.
	var zero color.Color
	inner := xchroma.Formatter(zero, nil)
	formatters.Register(formatterName, chroma.FormatterFunc(
		func(w io.Writer, style *chroma.Style, it chroma.Iterator) error {
			return inner.Format(w, codeBlockStyle(style), it)
		},
	))
	for i := range codeBlockSlots {
		placeholder := chromastyles.Register(chroma.MustNewStyle(codeBlockSlotName(i), chroma.StyleEntries{}))
		codeBlockSlotOf[placeholder] = i
	}
}

// codeBlockSlotName 是 registry name 的 code block placeholder.
func codeBlockSlotName(slot int) string {
	return fmt.Sprintf("crush-code-block-%d", slot)
}

// codeBlockConfig returns cfg ready 到 hand 到 glamour: chroma settings 在
// 它的 code block 是 replaced 通过 name 的 placeholder 那 stands
// 为 them. See comment 在 [codeBlockSlots].
func codeBlockConfig(cfg ansi.StyleConfig) ansi.StyleConfig {
	if cfg.CodeBlock.Chroma == nil {
		return cfg
	}
	cfg.CodeBlock.Theme = codeBlockSlotName(codeBlockSlot(cfg.CodeBlock.Chroma))
	cfg.CodeBlock.Chroma = nil
	return cfg
}

// codeBlockSlot returns placeholder slot 为 given chroma settings,
// assigning one (和 building 它的 style) first time they 是 seen.
func codeBlockSlot(c *ansi.Chroma) int {
	entries := styles.ChromaEntries(c)
	var key strings.Builder
	for _, t := range slices.Sorted(maps.Keys(entries)) {
		fmt.Fprintf(&key, "%d=%s;", t, entries[t])
	}

	codeBlockMu.Lock()
	defer codeBlockMu.Unlock()
	if slot, ok := codeBlockByKey[key.String()]; ok {
		return slot
	}
	slot := codeBlockNext
	codeBlockNext = (codeBlockNext + 1) % codeBlockSlots
	if old := codeBlockKeys[slot]; old != "" {
		delete(codeBlockByKey, old)
	}
	codeBlockKeys[slot] = key.String()
	codeBlockStyles[slot] = chroma.MustNewStyle("evo", entries)
	codeBlockByKey[key.String()] = slot
	return slot
}

// codeBlockStyle returns real style 为 code block placeholder, 或
// style itself 当 it 是 不 one.
func codeBlockStyle(style *chroma.Style) *chroma.Style {
	slot, ok := codeBlockSlotOf[style]
	if !ok {
		return style
	}
	codeBlockMu.Lock()
	defer codeBlockMu.Unlock()
	if s := codeBlockStyles[slot]; s != nil {
		return s
	}
	return style
}

// mdCacheMu guards mdCache, quietMDCache, userMDCache, 和 planMDCache.
//
// 锁 ordering: 当 both mdCacheMu 和 rendererLocksMu 是
// needed (只 在 InvalidateMarkdownRendererCache), acquire
// mdCacheMu FIRST, then rendererLocksMu. No other 调用 site may
// hold rendererLocksMu while acquiring mdCacheMu.
var (
	mdCacheMu    sync.Mutex
	mdCache      = map[int]*glamour.TermRenderer{}
	quietMDCache = map[int]*glamour.TermRenderer{}
	userMDCache  = map[int]*glamour.TermRenderer{}
	planMDCache  = map[int]*glamour.TermRenderer{}
)

// MarkdownRenderer returns glamour [glamour.TermRenderer] configured 用
// given styles 和 width. Renderers 是 memoized per width 和 shared
// across callers; 调用 InvalidateMarkdownRendererCache 当 活跃
// styles change.
//
// returned renderer 是 NOT safe 为 concurrent Render calls
// (goldmark's BlockStack carries state across the public Render
// API). Crush's TUI 是 single-threaded 所以 production never
// contends, 但 parallel callers (most notably parallel tests)
// 必须 serialize via [LockMarkdownRenderer]. Treat renderer
// 作为 effectively pinned 到 one goroutine 在 time.
func MarkdownRenderer(sty *styles.Styles, width int) *glamour.TermRenderer {
	mdCacheMu.Lock()
	defer mdCacheMu.Unlock()
	if r, ok := mdCache[width]; ok {
		return r
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockConfig(sty.Markdown)),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter(formatterName),
	)
	mdCache[width] = r
	return r
}

// UserMarkdownRenderer returns a glamour [glamour.TermRenderer] configured like
// [MarkdownRenderer] 但 用 single line breaks preserved. Renderers 是
// memoized per width 和 shared across callers; 调用
// InvalidateMarkdownRendererCache 当 活跃 styles change. Same
// concurrency contract 作为 [MarkdownRenderer]: serialize via
// [LockMarkdownRenderer].
//
// 用户 input 是 authored 在 plain textarea, 不 written 作为 Markdown source,
// 所以 lone newline 是 line 用户 deliberately typed. Standard Markdown
// treats it 作为 soft break 和 joins lines 当 rendering, which makes
// submitted 消息 display differently 从 what was typed (see
// charmbracelet/crush#3502). Preserving newlines 保持 display faithful.
//
// 这 是 deliberately NOT applied 到 [MarkdownRenderer]: 助手 output 和
// dialog copy 是 genuine Markdown, where soft-wrapping paragraph across
// source lines 是 normal 和 collapsing it 是 correct rendering.
func UserMarkdownRenderer(sty *styles.Styles, width int) *glamour.TermRenderer {
	mdCacheMu.Lock()
	defer mdCacheMu.Unlock()
	if r, ok := userMDCache[width]; ok {
		return r
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockConfig(sty.Markdown)),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter(formatterName),
		glamour.WithPreservedNewLines(),
	)
	userMDCache[width] = r
	return r
}

// QuietMarkdownRenderer returns glamour [glamour.TermRenderer] 用 no colors
// (plain 文本 用 structure) 和 given width. Renderers 是 memoized per
// width 和 shared across callers. Same concurrency contract 作为
// [MarkdownRenderer]: serialize via [LockMarkdownRenderer].
func QuietMarkdownRenderer(sty *styles.Styles, width int) *glamour.TermRenderer {
	mdCacheMu.Lock()
	defer mdCacheMu.Unlock()
	if r, ok := quietMDCache[width]; ok {
		return r
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockConfig(sty.QuietMarkdown)),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter(formatterName),
	)
	quietMDCache[width] = r
	return r
}

// PlanMarkdownRenderer returns glamour [glamour.TermRenderer] 那 keeps
// rich markdown colors 但 paints plan-card background under every
// primitive, 为 final plan 消息 card. Renderers 是 memoized per width
// 和 shared across callers. Same concurrency contract 作为 [MarkdownRenderer]:
// serialize via [LockMarkdownRenderer].
func PlanMarkdownRenderer(sty *styles.Styles, width int) *glamour.TermRenderer {
	mdCacheMu.Lock()
	defer mdCacheMu.Unlock()
	if r, ok := planMDCache[width]; ok {
		return r
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockConfig(sty.PlanMarkdown)),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter(formatterName),
	)
	planMDCache[width] = r
	return r
}

// InvalidateMarkdownRendererCache drops every cached renderer
// AND every per-renderer 互斥锁 在 single atomic critical
// section 所以 two maps cannot disagree mid-toggle. 调用 这
// whenever 活跃 styles change 所以 subsequent renderers
// pick up the new ansi.StyleConfig.
//
// Existing holders 的 old 互斥锁 (mid-Render goroutines) keep
// their reference safely; new renderers minted after the
// invalidation get freshly minted mutexes.
//
// 锁 ordering: mdCacheMu 是 acquired first, then
// rendererLocksMu — see comments 在 each 互斥锁.
func InvalidateMarkdownRendererCache() {
	mdCacheMu.Lock()
	defer mdCacheMu.Unlock()
	rendererLocksMu.Lock()
	defer rendererLocksMu.Unlock()

	mdCache = map[int]*glamour.TermRenderer{}
	quietMDCache = map[int]*glamour.TermRenderer{}
	userMDCache = map[int]*glamour.TermRenderer{}
	planMDCache = map[int]*glamour.TermRenderer{}
	rendererLocks = map[*glamour.TermRenderer]*sync.Mutex{}
}

// rendererLocksMu guards rendererLocks. We key per-renderer
// mutexes 通过 指针 所以 锁 granularity matches
// renderer 缓存 granularity (one 互斥锁 per (width, palette)
// renderer instance, 不 one 互斥锁 为 entire 缓存).
//
// 锁 ordering: 当 both mdCacheMu 和 rendererLocksMu 是
// needed (只 在 InvalidateMarkdownRendererCache), acquire
// mdCacheMu FIRST, then rendererLocksMu.
var (
	rendererLocksMu sync.Mutex
	rendererLocks   = map[*glamour.TermRenderer]*sync.Mutex{}
)

// LockMarkdownRenderer returns per-renderer 互斥锁 used 到
// serialize concurrent Render calls 在 shared
// [glamour.TermRenderer] instance. returned [*sync.互斥锁] 是
// stable 为 lifetime 的 renderer (i.e. until
// [InvalidateMarkdownRendererCache] 是 called).
//
// Callers 那 issue more than one Render 调用 在 same
// logical operation should hold 互斥锁 为 entire
// 序号 所以 other goroutines do 不 interleave their own
// Render calls 和 corrupt renderer state. F8's
// streamingMarkdown 是 immediate consumer; other 调用
// sites 那 today issue exactly one Render 调用 per item
// render 是 safe without locking under single-threaded
// TUI 更新 loop, 但 should adopt 这 锁 如果 they ever 运行
// 在 parallel (e.g. background prerender workers).
func LockMarkdownRenderer(r *glamour.TermRenderer) *sync.Mutex {
	rendererLocksMu.Lock()
	defer rendererLocksMu.Unlock()
	if mu, ok := rendererLocks[r]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	rendererLocks[r] = mu
	return mu
}
