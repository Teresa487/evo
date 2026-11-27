package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// streamingMarkdown caches "stable prefix" glamour render 所以 each
// streaming 刷盘 只 re-renders trailing portion 的
// document. F8 的 docs/备注/2026-05-12-chat-rendering-perf.md.
//
// boundary between "stable" 和 "trailing" 是 detected 通过
// [findSafeMarkdownBoundary]: a position immediately after a blank
// line 在 which we can prove no markdown construct 是 open
// (fenced code block, list, 表, block quote, setext header).
//
// Glamour renders document block 通过 block, 所以 在 safe boundary
// raw render 的 prefix followed 通过 raw render 的 rest 是
// byte-identical 到 render 的 whole, 用 one exception: block
// 那 opens document drops 它的 leading separator. Pieces after
// first 是 therefore rendered behind sentinel block 那 renders 到
// nothing (see [streamingMarkdown.renderPiece]). Exactness rests 在
// findSafeMarkdownBoundary 只 allowing cuts where 这 holds.
//
// Invariants:
//
// - stablePrefix 是 always literal byte prefix 的 most
// recently rendered content. If new content does 不 have
// stablePrefix 作为 它的 prefix 缓存 是 dropped.
// - stablePrefixRender 是 raw glamour render 的 stablePrefix 作为
//     it appears within the whole document.
// - width 是 glamour wrap width 那 produced
// stablePrefixRender. 一个 width change drops 缓存.
type streamingMarkdown struct {
	width              int
	stablePrefix       string
	stablePrefixRender string
	// Cached cumulative state 在 stable prefix boundary.
	// Used 通过 findBoundaryAfter 到 validate new boundary candidates
	// without re-scanning entire prefix 从 start.
	// baseFenceCount 是 always even (safe boundaries require even
	// fence parity), 所以 delta scan always starts outside fence.
	baseFenceCount    int
	baseHasListMarker bool

	// prefixShape describes stablePrefixRender's lines, 和 lastLines
	// 是 line count 的 whatever most recent Render returned.
	// Callers need count 到 size truncation hint; shape keeps
	// 那 O(trail) rather than O(document) scan 在 every 刷盘.
	prefixShape lineShape
	lastLines   int

	// lastContent 和 lastRender memoize most recent render 在 width.
	lastContent string
	lastRender  string
}

// LastLines reports number 的 lines 在 string most recent
// [streamingMarkdown.Render] returned, 用 surrounding whitespace
// trimmed. Equivalent 到 countLines 在 那 trimmed string, without
// rescanning it.
func (s *streamingMarkdown) LastLines() int {
	return s.lastLines
}

// Reset drops every cached 字段. After Reset next Render 调用
// 是 guaranteed 到 是 full render.
func (s *streamingMarkdown) Reset() {
	s.width = 0
	s.stablePrefix = ""
	s.stablePrefixRender = ""
	s.baseFenceCount = 0
	s.baseHasListMarker = false
	s.prefixShape = lineShape{}
	s.lastLines = 0
	s.lastContent = ""
	s.lastRender = ""
}

// Render returns glamour render 的 content 在 given width,
// reusing cached stable-prefix render 当 it 是 safe 到 do 所以.
// On any uncertainty 调用 falls back 到 full render via
// renderer 和 leaves 缓存 untouched (或 drops it).
//
// returned string has 它的 trailing newline trimmed 到 match
// existing renderMarkdown contract 在 AssistantMessageItem.
//
// Concurrency: glamour's Render 是 stateful 和 不 safe 为
// concurrent invocation 在 shared renderer. Crush's TUI 是
// single-threaded 所以 production never contends, 但 parallel
// callers (most notably 测试 suite) must serialize. We hold
// [common.LockMarkdownRenderer] 为 entire prefix +
// trailing render 序号 所以 other goroutines cannot interleave
// their own Render calls 和 corrupt goldmark's BlockStack.
func (s *streamingMarkdown) Render(content string, width int, renderer *glamour.TermRenderer) string {
	if content != "" && content == s.lastContent && width == s.width {
		return s.lastRender
	}
	out := s.render(content, width, renderer)
	s.lastContent, s.lastRender = content, out
	return out
}

func (s *streamingMarkdown) render(content string, width int, renderer *glamour.TermRenderer) string {
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	defer mu.Unlock()
	full := func() string {
		out, err := renderer.Render(content)
		if err != nil {
			out = content
		} else {
			out = strings.TrimSuffix(out, "\n")
		}
		// Counted against trimmed form: 那 是 what
		// windowing callers measure, 和 glue paths below
		// already return trimmed output.
		s.lastLines = countLines(trimGlamourMargins(out))
		return out
	}

	// Width change OR content 不 prefix-extension: drop 缓存 和
	// render 在 full. prefix 是 seeded 只 once content grows,
	// 所以 content 那 never grows renders exactly once.
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.Reset()
		s.width = width
		return full()
	}

	// Incremental boundary search: 只 scan delta after
	// stable prefix. The cached cumulative state (baseFenceCount,
	// baseHasListMarker) lets us validate candidates 在 O(delta)
	// instead 的 re-scanning entire prefix. See CHARM-1785.
	boundary := s.findBoundaryAfter(content)
	if boundary < 0 {
		// No safe boundary anywhere yet. Full render; do 不
		// modify 缓存 ( future 刷盘 may find one).
		return full()
	}

	if boundary > len(s.stablePrefix) {
		// 一个 new chunk 的 safe content: render it 和 promote
		// boundary.
		newChunk := content[len(s.stablePrefix):boundary]
		chunkRender, ok := s.renderPiece(newChunk, renderer)
		if !ok {
			s.Reset()
			s.width = width
			return full()
		}
		s.prefixShape = s.prefixShape.join(shapeOf(chunkRender))
		s.stablePrefixRender += chunkRender
		s.stablePrefix = content[:boundary]
		// 更新 cumulative state 为 new stable prefix.
		s.baseFenceCount += countFenceLines(newChunk)
		s.baseHasListMarker = s.baseHasListMarker || chunkHasListMarker(newChunk)
	}

	trail, ok := s.renderPiece(content[len(s.stablePrefix):], renderer)
	if !ok {
		return full()
	}
	s.lastLines = s.prefixShape.join(shapeOf(trail)).lines()
	return strings.TrimSuffix(s.stablePrefixRender+trail, "\n")
}

// findBoundaryAfter searches 为 latest safe boundary 在 content
// 那 是 strictly after stable prefix. 它 uses cached
// cumulative state (baseFenceCount, baseHasListMarker) 到 validate
// candidates without re-scanning the entire prefix, making the search
// O(delta) instead 的 O(n) per tick. See CHARM-1785.
//
// Returns -1 当 no safe boundary exists after stable prefix.
func (s *streamingMarkdown) findBoundaryAfter(content string) int {
	// When there 是 no stable prefix, fall back 到 full scan.
	if len(s.stablePrefix) == 0 {
		if b := findSafeMarkdownBoundary(content); b > 0 {
			return b
		}
		return s.relaxedBoundary(content, 0)
	}

	// Scan blank-line candidates 从 latest 到 earliest, 但 只
	// those strictly after stable prefix. Candidates 在 或
	// before stable prefix 是 already covered 通过 缓存.
	for p := blankLineBefore(content, len(content)); p > len(s.stablePrefix); p = blankLineBefore(content, p-1) {
		if s.isSafeBoundaryIncremental(content, p) {
			return p
		}
	}
	if b := s.relaxedBoundary(content, len(s.stablePrefix)); b > len(s.stablePrefix) {
		return b
	}
	// No new boundary found after the stable prefix. Return the
	// stable prefix length 所以 caller takes "boundary <=
	// len(stablePrefix)" 路径 (render trailing fresh, keep 缓存).
	return len(s.stablePrefix)
}

// relaxBoundaryAfter bounds how much unstable tail 刷盘 will
// re-render. Prose holding no blank line never satisfies the
// blank-line predicate, 所以 缓存 never advances 和 every 刷盘
// re-renders whole document: O(n) per tick, O(n^2) over turn. 一个
// reasoning trace 那 reaches few hundred KB burns tens 的 GB 的
// churn 那 way, which 是 enough 到 outrun GC (#3162).
// Sized just above prose paragraph 所以 structured 文本 never reaches
// it: blank-line boundary fires first 和 tail stays small.
// Only prose 用 no paragraph breaks 在 all gets cut here, 和 it has
// no reflow left 到 protect.
const relaxBoundaryAfter = 2 << 10

// relaxedBoundaryCandidates caps how many newline candidates one
// 刷盘 will validate, keeping search cost flat 当 none 的 them
// pass.
const relaxedBoundaryCandidates = 8

// relaxedBoundary looks 为 cut 在 plain newline once tail
// after `after` has outgrown [relaxBoundaryAfter]. Candidates 是
// still validated 通过 same predicate blank-line search uses,
// 所以 every construct check survives; 只 conservatism given up
// 是 paragraph wrapping across cut, which costs one re-wrap 在
// point the reader has usually already scrolled past.
//
// Returns -1 while tail 是 still small, 和 当 no candidate
// validates — caller then behaves exactly 作为 it did before.
// Content 用 no newline 在 all has nothing 到 cut 在 和 stays 在
// full-render 路径.
func (s *streamingMarkdown) relaxedBoundary(content string, after int) int {
	if len(content)-after <= relaxBoundaryAfter {
		return -1
	}
	tried := 0
	for p := newlineBefore(content, len(content)); p > after; p = newlineBefore(content, p-1) {
		if s.isSafeBoundaryIncremental(content, p) {
			return p
		}
		if tried++; tried >= relaxedBoundaryCandidates {
			break
		}
	}
	return -1
}

// newlineBefore returns byte offset 的 first character AFTER
// latest newline 那 ends strictly before `until`, 或 -1 当
// there 是 none.
func newlineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	nl := strings.LastIndexByte(content[:until], '\n')
	if nl < 0 {
		return -1
	}
	return nl + 1
}

// isSafeBoundaryIncremental validates boundary candidate 在
// position p using the cached cumulative state plus a delta scan
// 的 content[len(stablePrefix):p]. 这 avoids O(n) re-scan
// 那 isSafeBoundaryAt performs 在 every candidate.
func (s *streamingMarkdown) isSafeBoundaryIncremental(content string, p int) bool {
	delta := content[len(s.stablePrefix):p]

	// (2) Fence parity: base count + delta count must 是 even.
	if (s.baseFenceCount+countFenceLines(delta))%2 != 0 {
		return false
	}

	// (2b) HTML 和 link-ref hazards 在 delta.
	if deltaHasHTMLorRef(delta) {
		return false
	}

	// (2b) List hazard: 如果 list marker exists anywhere 在
	// full prefix (base OR delta), the last non-blank line before
	// boundary must 不 是 indented continuation paragraph.
	hasListMarker := s.baseHasListMarker || chunkHasListMarker(delta)
	if hasListMarker {
		lastLine := lastNonBlankLine(content[:p])
		if lastLine != "" && !isListItemMarker(strings.TrimLeft(lastLine, " \t")) {
			if len(lastLine) > 0 && (lastLine[0] == ' ' || lastLine[0] == '\t') {
				return false
			}
		}
	}

	// (3) Last non-blank line must 不 open construct.
	lastLine := lastNonBlankLine(content[:p])
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// (4) Setext underline check.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// deltaHasHTMLorRef reports whether delta (文本 between
// stable prefix 和 boundary candidate) contains HTML block
// opener 或 link reference definition.
func deltaHasHTMLorRef(delta string) bool {
	inFence := false
	for line := range splitLines(delta) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isHTMLBlockOpener(line) || isLinkRefDefinition(line) {
			return true
		}
	}
	return false
}

// chunkHasListMarker reports whether any line 在 chunk 是
// list-item marker (outside fenced code blocks).
func chunkHasListMarker(chunk string) bool {
	inFence := false
	for line := range splitLines(chunk) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isListItemMarker(strings.TrimLeft(line, " \t")) {
			return true
		}
	}
	return false
}

// markdownSentinel 是 empty HTML comment rendered ahead 的 piece 所以
// 它的 first block keeps separator it has mid-document. 它 renders 到
// nothing, 所以 output needs no slicing.
const markdownSentinel = "<!---->\n\n"

// renderPiece returns raw render 的 文本 作为 it appears right after
// stablePrefix 在 whole document. 它 reports false 在 render 错误,
// 和 caller must then render whole document instead.
func (s *streamingMarkdown) renderPiece(text string, renderer *glamour.TermRenderer) (string, bool) {
	if text == "" {
		return "", true
	}
	if s.stablePrefix != "" {
		text = markdownSentinel + text
	}
	out, err := renderer.Render(text)
	return out, err == nil
}

// lineShape summarizes string's newlines 所以 line count 的
// trimGlamourMargins over concatenation follows 从 它的 parts.
type lineShape struct {
	newlines int
	// lead 和 trail count newlines 在 leading 和 trailing
	// whitespace runs; both cover everything 当 there 是 no content.
	lead, trail int
	content     bool
}

func shapeOf(text string) lineShape {
	var shape lineShape
	for idx := 0; idx < len(text); idx++ {
		switch text[idx] {
		case '\n':
			shape.newlines++
			if !shape.content {
				shape.lead++
			}
			shape.trail++
		case ' ', '\t':
		default:
			shape.content = true
			shape.trail = 0
		}
	}
	return shape
}

// join returns shape 的 followed 通过 next.
func (a lineShape) join(next lineShape) lineShape {
	out := lineShape{newlines: a.newlines + next.newlines, content: a.content || next.content}
	out.lead = a.lead
	if !a.content {
		out.lead += next.lead
	}
	out.trail = next.trail
	if !next.content {
		out.trail += a.trail
	}
	return out
}

// lines 是 countLines(trimGlamourMargins(文本)) 为 shaped 文本.
func (a lineShape) lines() int {
	if !a.content {
		return 1
	}
	return a.newlines - a.lead - a.trail + 1
}

// trimGlamourMargins strips leading 和 trailing whitespace
// (including newlines) 从 glamour-rendered fragment.
// Glamour adds leading blank line 为 documents 那 open 用
// heading 或 paragraph, plus trailing newline; both must 是
// removed before concatenation.
func trimGlamourMargins(s string) string {
	return strings.Trim(s, " \t\n")
}

// findSafeMarkdownBoundary returns byte offset 的 END 的
// latest safe boundary 在 content, i.e. offset such 那
// content[:boundary] 是 valid stable-prefix candidate.
// returned offset always points immediately after a blank-line
// separator, 所以 concatenating fresh render 的 content[boundary:]
// 到 cached render 的 content[:boundary] does 不 require glamour
// 到 share state across cut.
//
// Returns -1 当 no safe boundary exists. SAFETY FIRST: any time
// we have slightest doubt we return -1 和 let caller fall
// back 到 full render.
//
// Decision tree, 在 order 的 preference (latest boundary wins):
//
// 1. Walk backward through every "blank line" position p such 那
// content[:p] ends 用 "\n\n" (或 "\n[ \t]*\n").
// 2. For each candidate, check 那 content[:p] has even
// number 的 triple-backtick fence lines (no open fenced
// block). Any odd count means we'd 是 cutting inside fence
// 和 mis-syntax-highlighting trailing partial.
// 2b. Reject 如果 any line 在 content[:p] (outside fenced blocks)
// 是 list-marker line, HTML-block opener, 或 link
// reference definition. See [prefixHasOpenHazard] 为
// reasoning behind these "anywhere 在 prefix" rejects.
// 3. Reject 如果 last non-blank line 的 content[:p] 是:
//     - a list item marker line ("^\s*([-*+]|\d+\.)\s")
// - 表 line (contains "|")
//     - a block quote ("^\s*>")
// - setext header underline ("^=+\s*$" 或 "^-+\s*$")
// - indented code line (4+ leading spaces 或 tab)
// 4. Reject 如果 line immediately AFTER boundary (skipping
//     leading blank lines) looks like a setext underline (a line
// 的 '=' 或 '-' 只). Rendering prefix 作为 paragraph
// would change once underline arrived; 那's exactly
//     "splitting changes the prefix render" hazard §4.4 calls out.
//
// Returns byte offset 的 first character AFTER blank
// line, i.e. start 的 trailing segment.
func findSafeMarkdownBoundary(content string) int {
	if len(content) == 0 {
		return -1
	}

	// Iterate every blank-line position 从 latest 到 earliest.
	for p := blankLineBefore(content, len(content)); p > 0; p = blankLineBefore(content, p-1) {
		if !isSafeBoundaryAt(content, p) {
			continue
		}
		return p
	}
	return -1
}

// blankLineBefore returns byte offset 的 first character
// AFTER latest blank-line separator 那 ends strictly before
// `until`. 一个 blank-line separator 是 序号 "\n([ \t]*\n)+"
// — one newline, then one 或 more lines containing 只 spaces 或
// tabs 和 terminated 通过 another newline. returned offset 是
// start 的 first non-blank line 那 follows
// separator (或 position immediately after final newline,
// 如果 no further content remains).
//
// Returns -1 当 no blank-line separator exists before `until`.
func blankLineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	// Walk backward looking 为 newline followed (after optional
	// blank-line content) 通过 another newline. We track latest
	// newline we've seen; 如果 next earlier newline has 只
	// blank chars between them, we have a blank-line separator
	// 和 boundary sits immediately after latest newline.
	end := until
	for end > 0 {
		nl := strings.LastIndexByte(content[:end], '\n')
		if nl < 0 {
			return -1
		}
		// Look 为 earlier newline whose gap 到 nl 是 empty
		// 或 whitespace 只.
		prev := strings.LastIndexByte(content[:nl], '\n')
		for prev >= 0 {
			gap := content[prev+1 : nl]
			if isBlankOrSpaces(gap) {
				return nl + 1
			}
			// Gap had non-whitespace; nl 是 不 blank-line
			// separator. Move up: try 用 earlier newline 作为
			// the new "nl" candidate.
			break
		}
		end = nl
	}
	return -1
}

// isBlankOrSpaces reports whether s consists entirely 的 spaces
// 和 tabs (或 是 empty).
func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// isSafeBoundaryAt reports whether content[:p] 是 safe stable
// prefix. p must 是 blank-line boundary (start 的 line, 用
// blank line immediately preceding).
//
// Beyond last-line checks, three "anywhere 在 prefix"
// hazards force reject because they cannot 是 reliably reasoned
// about 通过 inspecting trailing line alone. For each 的 these
// the simplest, safest rule was chosen — see prefixHasOpenHazard.
func isSafeBoundaryAt(content string, p int) bool {
	prefix := content[:p]

	// (2) Even number 的 triple-backtick fence lines.
	if countFenceLines(prefix)%2 != 0 {
		return false
	}

	// (2b) Anywhere-在-prefix hazards: open list (B1), HTML block
	// opener (B2), reference link definition (B3). Any 的 these
	// anywhere 在 prefix forces fallback.
	if prefixHasOpenHazard(prefix) {
		return false
	}

	// (3) Inspect last non-blank line 的 prefix.
	lastLine := lastNonBlankLine(prefix)
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// (4) If anything follows, make sure it doesn't look like a
	// setext underline 那 would retroactively turn last
	// paragraph 的 prefix into header.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// prefixHasOpenHazard reports whether prefix contains any 的 three
// constructs 那 cannot 是 safely cut 在 blank-line boundary
// even 当 immediately preceding line looks fine. Each check
// uses the SIMPLEST viable conservative rule per the F8 round-2
// review:
//
// B1 (loose lists). 一个 loose list has blank line between item
// 和 continuation paragraph 那 begins 用 indentation
// 但 no list marker. If candidate boundary lands 在 那
// blank line, prefix's trailing non-blank line 是
// continuation paragraph, NOT list marker, 所以 last-line
// check would 受理 it even though list 是 still open.
//
// Rule chosen: any list-marker line ANYWHERE 在 prefix
// forces -1. 这 是 overly conservative — it forfeits
// boundary advancement past closed list — 但 it eliminates
// entire bug class 用 zero parsing 的 CommonMark's
//	   loose-list closure semantics. We retain the most useful
// boundary 在 practice: one BEFORE list opens (no
// marker has appeared 在 prefix yet).
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener
//	   patterns (script/pre/style/textarea, comments, processing
//	   instructions, CDATA, declarations, recognised tag names).
// If prefix opens HTML block 那 suffix closes,
// splitting renders prefix 作为 raw HTML 和 suffix 作为
//	   prose.
//
// Rule chosen: any HTML-block opener anywhere 在 prefix
// forces -1. Same trade-off 作为 B1 — typical 助手
// output contains no raw HTML, 所以 perf cost 是 zero 在
//	   the common case.
//
// B3 (reference link definitions). 一个 line 的 form
// "[label]: <url>" defines link reference 那 suffix
// may later use 作为 "[文本][label]". Splitting document
// loses definition because each half 是 rendered 作为
//	   independent glamour document.
//
// Rule chosen: any reference link definition line anywhere 在
// prefix forces -1. Suffix-side reference detection 是
// fragile (three syntaxes: [文本][label], [label][], [label]),
// 所以 prefix-side check 是 simpler safe choice.
//
// All three rules 受理 perf hit 的 "no boundary after
// list / HTML block / link def" 在 exchange 为 guaranteed
// soundness. If profiling shows 这 kills F8 win 在 real
// streaming traces, next iteration can promote each rule 到
// 它的 less-conservative variant (closure-aware list tracking,
// per-tag HTML close detection, suffix-aware ref tracking).
// prefixHasOpenHazard reports whether prefix contains any 的 three
// constructs 那 cannot 是 safely cut 在 blank-line boundary
// even 当 immediately preceding line looks fine.
//
// B1 (loose lists, refined). 一个 loose list has blank line between
// item 和 continuation paragraph 那 begins 用
// indentation 但 no list marker. If candidate boundary lands
// 在 那 blank line, prefix's trailing non-blank line 是
// continuation paragraph, NOT list marker, 所以 last-line
// check 在 lineOpensConstruct would 受理 it even though
// list 是 still open.
//
// Rule chosen: track whether any list-marker line appears 在
// prefix. If one does AND last non-blank line 是 indented
// (但 是 不 itself list marker), list 是 potentially
// open 和 we reject. 这 catches loose-list continuation
// paragraphs (indented, no marker) 那 lineOpensConstruct
// misses (it 只 flags 4+ spaces), without forfeiting every
// boundary after CLOSED list. 一个 list followed 通过 blank line
// 和 then non-indented paragraph 是 closed; boundary
// after 那 paragraph 是 safe.
//
// previous rule rejected 在 any list marker anywhere 在
// prefix, which killed streaming 缓存 为 every document
// 那 ever contained list — dominant case 为 LLM
//	   thinking blocks. See CHARM-1785.
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener
//	   patterns (script/pre/style/textarea, comments, processing
//	   instructions, CDATA, declarations, recognised tag names).
// If prefix opens HTML block 那 suffix closes,
// splitting renders prefix 作为 raw HTML 和 suffix 作为
//	   prose.
//
// Rule chosen: any HTML-block opener anywhere 在 prefix
// forces -1. Same trade-off 作为 B1 — typical 助手
// output contains no raw HTML, 所以 perf cost 是 zero 在
//	   the common case.
//
// B3 (reference link definitions). 一个 line 的 form
// "[label]: <url>" defines link reference 那 suffix
// may later use 作为 "[文本][label]". Splitting document
// loses definition because each half 是 rendered 作为
//	   independent glamour document.
//
// Rule chosen: any reference link definition line anywhere 在
// prefix forces -1. Suffix-side reference detection 是
// fragile (three syntaxes: [文本][label], [label][], [label]),
// 所以 prefix-side check 是 simpler safe choice.
func prefixHasOpenHazard(prefix string) bool {
	inFence := false
	hasListMarker := false
	var lastNonBlankTrimmed string
	var lastNonBlankRaw string
	for line := range splitLines(prefix) {
		// Track fenced state 所以 list/html/ref patterns inside
		// fenced code block do 不 falsely trigger hazards.
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		lastNonBlankTrimmed = trimmed
		lastNonBlankRaw = line
		// B1: track list markers.
		if isListItemMarker(trimmed) {
			hasListMarker = true
		}
		// B2: HTML block opener.
		if isHTMLBlockOpener(line) {
			return true
		}
		// B3: link reference definition.
		if isLinkRefDefinition(line) {
			return true
		}
	}
	// B1 (refined): list 是 potentially open 只 当 marker
	// appeared earlier AND last non-blank line 是 indented 但
	// 是 不 itself list marker (i.e. it 是 continuation
	// paragraph 那 lineOpensConstruct would miss because it 只
	// catches 4+ leading spaces). 一个 non-indented last line means
	// list was closed 通过 blank line before it.
	if hasListMarker && lastNonBlankTrimmed != "" && !isListItemMarker(lastNonBlankTrimmed) {
		if len(lastNonBlankRaw) > 0 && (lastNonBlankRaw[0] == ' ' || lastNonBlankRaw[0] == '\t') {
			return true
		}
	}
	return false
}

// countFenceLines counts lines 那 begin fenced code block 在
// CommonMark sense: line whose first non-whitespace 运行 是
// 在 least three consecutive backticks (或 tildes). Each such
// line toggles fenced state, 所以 even count means every
// opened fence has been closed.
//
// We 受理 up 到 three leading spaces 的 indentation (CommonMark
// rule) 和 require fence characters 到 是 FIRST
// non-whitespace content 的 line. We deliberately do NOT
// attempt 到 parse info-strings 或 differentiate opener 从
// closer beyond toggling — closing fence 是 just any line
// whose first non-whitespace 运行 是 >=3 的 same fence char.
func countFenceLines(s string) int {
	n := 0
	for line := range splitLines(s) {
		if isFenceLine(line) {
			n++
		}
	}
	return n
}

// isFenceLine reports whether line opens 或 closes fenced code
// block.
func isFenceLine(line string) bool {
	// Strip up 到 3 spaces 的 indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

// lastNonBlankLine returns last non-blank line 的 s, 或 ""
// 当 every line 是 blank.
func lastNonBlankLine(s string) string {
	last := ""
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

// firstNonBlankLine returns first non-blank line 的 s, 或 ""
// 当 every line 是 blank.
func firstNonBlankLine(s string) string {
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// splitLines yields lines 的 s without their terminators.
// final segment 是 yielded even 如果 不 newline-terminated.
func splitLines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start <= len(s)-1 {
			yield(s[start:])
		}
	}
}

// lineOpensConstruct reports whether line 保持 markdown
// construct open across the boundary. We err conservatively —
// any case 那 smells like list/表/quote/setext/indented-code
// returns true.
func lineOpensConstruct(line string) bool {
	// Indented code: tab, 或 4+ leading spaces.
	if len(line) > 0 && line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "    ") {
		return true
	}

	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}

	// Block quote.
	if trimmed[0] == '>' {
		return true
	}

	// List item: "- " "* " "+ " 或 "<digits>. " 或 "<digits>) ".
	if isListItemMarker(trimmed) {
		return true
	}

	// 表: any pipe character anywhere 在 line. Conservative:
	// pipe-在-prose 是 rare 和 cost 的 bailing 是 one slow
	// frame.
	if strings.ContainsRune(line, '|') {
		return true
	}

	// Setext underline candidate 作为 LAST line 的 prefix:
	// 这 would 是 setext header 为 even-earlier paragraph.
	// Refuse 到 split 在 all 在 这 case — boundary 是 right
	// 在 middle 的 header.
	if isSetextUnderlineCandidate(trimmed) {
		return true
	}

	return false
}

// isListItemMarker reports whether line (already left-trimmed)
// starts 用 CommonMark list-item marker followed 通过 space
// 或 tab.
func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		if len(line) >= 2 && (line[1] == ' ' || line[1] == '\t') {
			return true
		}
		return false
	}
	// Ordered list: digits followed 通过 '.' 或 ')' 和 space.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return false
	}
	if i >= len(line) {
		return false
	}
	if line[i] != '.' && line[i] != ')' {
		return false
	}
	if i+1 >= len(line) {
		return false
	}
	return line[i+1] == ' ' || line[i+1] == '\t'
}

// isSetextUnderlineCandidate reports whether line (用 optional
// leading whitespace) consists entirely 的 '=' 或 entirely 的 '-'
// characters 用 optional trailing whitespace. CommonMark
// requires no leading whitespace 在 underline; we 受理 up
// 到 three spaces 为 safety 所以 indented underline still
// blocks a split.
func isSetextUnderlineCandidate(line string) bool {
	// Strip leading whitespace.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	// Allow trailing whitespace.
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	// Need 在 least one underline character. "-" alone 是 also
	// list marker without a trailing space; the listItem check
	// covers the marker case before we get here.
	return j-i >= 1
}

// isHTMLBlockOpener reports whether line begins one 的 seven
// CommonMark HTML block patterns. We 受理 up 到 three spaces 的
// leading indentation (CommonMark rule). Matching 是 intentionally
// loose — we 只 need 到 know line "looks like HTML
// block start", 不 parse contained markup.
func isHTMLBlockOpener(line string) bool {
	// Strip up 到 3 spaces 的 indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}

	// Type 2: HTML comment "<!--".
	if strings.HasPrefix(rest, "<!--") {
		return true
	}
	// Type 3: processing instruction "<?".
	if strings.HasPrefix(rest, "<?") {
		return true
	}
	// Type 5: CDATA "<![CDATA[".
	if strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	// Type 4: declaration "<!" followed 通过 ASCII letter.
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}

	// Type 1: <script | <pre | <style | <textarea (case-insensitive)
	// followed 通过 whitespace, '>', end-的-line, 或 other non-name
	// terminators. Use permissive HasPrefix check 在 lowercase.
	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}

	// Types 6 & 7: open 或 close 的 block-level tag.
	//
	// Type 6 matches a fixed CommonMark tag set; type 7 matches any
	// otherwise-valid open/close tag whose name 是 不 在
	// script/pre/style/textarea family. We collapse both into a
	// single check: line must start 用 '<' 或 '</' followed
	// 通过 ASCII letter. 这 deliberately mirrors other
	// hazards — 当 在 doubt, forfeit boundary. Lines like
	// "<3", "<-", "<<", 或 mid-line "<foo>" do NOT trigger because
	// we require line 到 *start* (after up 到 3 spaces) 用
	// '<letter' 或 '</letter'.
	j := 1 // past '<'
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

// isASCIILetter reports whether b 是 ASCII letter.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLinkRefDefinition reports whether line matches a CommonMark
// link reference definition opener. The conservative pattern:
//
//	^[ ]{0,3}\[[^\]]+\]:\s*\S+
//
// i.e. up 到 3 spaces, then bracketed label (no nested ']'),
// then colon, then whitespace, then 在 least one non-whitespace
// character 的 destination. We do 不 validate destination —
// presence 的 ref-def opener anywhere 在 prefix 是 enough
// 到 forfeit boundary.
func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		// No closing bracket, 或 empty label.
		return false
	}
	// i points 在 ']'.
	i++
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	// Skip required whitespace.
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	// At least one non-whitespace character 的 destination.
	return i < len(line)
}
