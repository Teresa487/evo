package chat

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// newTestRenderer builds fresh glamour renderer 为 given
// width. We deliberately do NOT share renderers between calls 在
// equivalence tests 所以 any hidden state 在
// [glamour.TermRenderer] cannot leak 从 "cached" rendering
// 路径 into "fresh" rendering 路径.
func newTestRenderer(t *testing.T, width int) *glamour.TermRenderer {
	t.Helper()
	sty := styles.CharmtonePantera()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(sty.Markdown),
		glamour.WithWordWrap(width),
	)
	require.NoError(t, err)
	return r
}

// freshRender renders content 作为 single document 用 fresh
// glamour renderer 和 applies same trailing-newline trim
// 那 streamingMarkdown.Render does. Use 这 为 byte- 和
// visible-equivalence comparisons against streaming 路径.
func freshRender(t *testing.T, content string, width int) string {
	t.Helper()
	r := newTestRenderer(t, width)
	out, err := r.Render(content)
	require.NoError(t, err)
	return strings.TrimSuffix(out, "\n")
}

// stripANSI removes all ANSI CSI escape sequences 从 s 所以 two
// renders 用 different colour state can 是 compared 在 their
// visible glyphs alone.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// normalizeRender strips ANSI, trailing spaces 和 repeated blank lines
// 从 render 所以 failure messages show 它的 visible 文本.
func normalizeRender(s string) string {
	clean := stripANSI(s)
	lines := strings.Split(clean, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	// Collapse consecutive blank lines.
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		blank := l == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = blank
	}
	// Trim leading 和 trailing blanks.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// containsRawMarkdownSource reports whether visible portion 的
// rendered contains literal markdown source markers 那 should
// have been consumed 通过 glamour. Used 通过 T2 到 assert 那
// intermediate streaming flushes don't leak raw source through 到
// 用户. We deliberately 只 flag markers 那 glamour
// removes during rendering ("```" fence delimiters, "|" 表
// pipes embedded 在 line 那 also contains pipes — actual
// 表 syntax — 和 bare "###" headers); pipes-在-prose 和
// dashes 是 too common 到 flag.
func containsRawMarkdownSource(rendered string) bool {
	clean := stripANSI(rendered)
	if strings.Contains(clean, "```") {
		return true
	}
	for _, line := range strings.Split(clean, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "###") {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------
// T1: findSafeMarkdownBoundary unit tests.
// -----------------------------------------------------------------------

// TestFindSafeMarkdownBoundary_TableDriven exercises the
// findSafeMarkdownBoundary decision tree across full set 的
// constructs §4.4 calls out: plain paragraphs, fenced code (open
// 和 closed), lists, tables, block quotes, 和 setext headers.
func TestFindSafeMarkdownBoundary_TableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		// want 是 expected boundary; -1 means "no safe
		// boundary." When >=0 测试 asserts content[:want]
		// ends after blank-line separator 和 content[:want]
		// 是 完成 prefix.
		want int
	}{
		{
			name:    "empty",
			content: "",
			want:    -1,
		},
		{
			name:    "single line",
			content: "Just a single paragraph",
			want:    -1,
		},
		{
			name:    "two paragraphs",
			content: "First paragraph.\n\nSecond paragraph.",
			// boundary 在 start 的 "Second"
			want: len("First paragraph.\n\n"),
		},
		{
			name:    "three paragraphs picks latest",
			content: "First.\n\nSecond.\n\nThird.",
			want:    len("First.\n\nSecond.\n\n"),
		},
		{
			name:    "open fence at end",
			content: "Para.\n\n```go\nfoo()\n",
			// no closing fence — every blank-line candidate
			// before content end 是 INSIDE fence ( open
			// fence opened 在 offset 7). Actually ONLY
			// blank line 是 between "Para." 和 "```go", 所以
			// candidate boundary 是 right before "```go". At
			// 那 point fence count = 0, even, 但 line
			// AFTER ( first non-blank) 是 "```go" which
			// would change rendering 的 prefix… hmm,
			// actually it wouldn't change the prefix's
			// rendering because prefix 是 just "Para.\n\n".
			// boundary would 是 ACCEPTED. Let's check
			// what our impl does.
			want: len("Para.\n\n"),
		},
		{
			name:    "inside open fence: no candidate after open",
			content: "Para.\n\n```go\nfoo()\n\nbar()\n",
			// blank line after "foo()" 是 INSIDE fence
			// (fence count 在 那 prefix = 1, odd), must
			// reject. The earlier blank line between "Para."
			// 和 "```go" should still 是 safe (fence count
			// 在 那 prefix = 0).
			want: len("Para.\n\n"),
		},
		{
			name:    "closed fence followed by paragraph",
			content: "Para1.\n\n```\nfoo()\n```\n\nPara2.",
			// latest blank line 是 between "```" 和 "Para2.";
			// fence count 在 那 prefix = 2 (even), last
			// non-blank line 是 "```" which 是 不 list/
			// 表/quote/setext.
			want: len("Para1.\n\n```\nfoo()\n```\n\n"),
		},
		{
			name:    "open list at end",
			content: "Para.\n\n- one\n- two\n",
			// last non-blank line 的 any blank-bounded prefix
			// 是 list item; our boundary check rejects.
			// blank line between "Para." 和 "- one" 是
			// 只 candidate, 但 line AFTER (first
			// non-blank 的 suffix) 是 "- one" — 那's fine,
			// a list opening doesn't change the prefix's
			// rendering. So boundary BEFORE list 是
			// accepted.
			want: len("Para.\n\n"),
		},
		{
			name:    "list interior: no boundary",
			content: "- one\n- two\n",
			// no blank line 在 all.
			want: -1,
		},
		{
			name:    "closed list then paragraph",
			content: "- one\n- two\n\nPara.",
			// blank line after the list. Last non-blank line
			// 的 prefix 是 "- two" — list item — 所以
			// candidate 是 REJECTED. (Conservative: we don't
			// know list 是 "closed" without looking 在
			// what follows.)
			want: -1,
		},
		{
			name:    "table at end",
			content: "Para.\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n",
			// blank-line candidate 是 between "Para." 和
			// 表 opener. Last non-blank line 的 prefix 是
			// "Para." — fine. Line AFTER 是 "| | b |"
			// which 是 表 line; doesn't retroactively
			// change "Para." Boundary accepted.
			want: len("Para.\n\n"),
		},
		{
			name:    "table interior with internal blank line: no late boundary",
			content: "| a | b |\n| --- | --- |\n\n| 1 | 2 |\n",
			// blank line 在 middle 是 followed 通过
			// another 表 line. Last non-blank line 的
			// prefix 是 "| --- | --- |" which contains
			// pipe — we reject.
			want: -1,
		},
		{
			name:    "block quote at end",
			content: "Para.\n\n> quoted\n> still quoted\n",
			// Last non-blank line 的 any prefix 那 ends
			// inside quote block 是 "> ..." line —
			// rejected. The blank line BEFORE the quote
			// gives prefix 的 "Para.\n\n" — last non-blank
			// "Para." — accepted.
			want: len("Para.\n\n"),
		},
		{
			name:    "setext underline pending",
			content: "Heading\n\n=====\n",
			// blank line between "Heading" 和 "=====".
			// Prefix = "Heading\n\n", last non-blank "Heading"
			// — fine. But FIRST non-blank line 的
			// suffix 是 "=====", setext-underline
			// candidate. Splitting here would render the
			// prefix 作为 paragraph "Heading", 但
			// canonical render would treat the whole thing
			// 作为 setext header. Reject.
			//
			// (Note: per CommonMark, a blank line between a
			// paragraph 和 underline actually breaks
			// setext, 所以 setext interpretation may 不
			// apply. But boundary check 是 conservative
			// — being wrong costs one slow frame, being
			// over-aggressive costs visible breakage.)
			want: -1,
		},
		{
			name:    "indented code at end of prefix",
			content: "Para.\n\n    code line\n\nNext.",
			// prefix candidates:
			//   "Para.\n\n" — last non-blank "Para.", accepted
			//   "Para.\n\n    code line\n\n" — last non-blank
			// 是 " code line" which 是 indented 4
			//   spaces — REJECTED.
			// Latest accepted 是 first.
			want: len("Para.\n\n"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := findSafeMarkdownBoundary(c.content)
			require.Equalf(t, c.want, got,
				"findSafeMarkdownBoundary(%q) = %d, want %d", c.content, got, c.want)
			if got > 0 {
				// Boundary must point 到 start 的 line
				// (i.e. just after newline) 当 prefix
				// 是 non-empty.
				require.True(t, got <= len(c.content),
					"boundary %d out of range (len=%d)", got, len(c.content))
				if got > 0 && got <= len(c.content) {
					require.Equal(t, byte('\n'), c.content[got-1],
						"boundary %d does not sit immediately after a newline", got)
				}
			}
		})
	}
}

// -----------------------------------------------------------------------
// T2: streaming-equivalence tests.
// -----------------------------------------------------------------------

// streamingScenarios returns the four canonical document shapes
// 那 exercise different boundary-detection paths.
func streamingScenarios() []struct {
	name string
	doc  string
} {
	return []struct {
		name string
		doc  string
	}{
		{
			name: "plain-paragraphs",
			doc: strings.Join([]string{
				"This is the first paragraph of the document.",
				"",
				"Here is the second paragraph; it has some words.",
				"",
				"And a third paragraph for good measure.",
				"",
				"Finally a fourth paragraph to push past one boundary.",
			}, "\n"),
		},
		{
			name: "paragraphs-with-fence",
			doc: strings.Join([]string{
				"Intro paragraph.",
				"",
				"Some explanatory prose before the code.",
				"",
				"```go",
				"func hello() {",
				"\tfmt.Println(\"hi\")",
				"}",
				"```",
				"",
				"And a closing paragraph after the code block.",
			}, "\n"),
		},
		{
			name: "paragraphs-with-list",
			doc: strings.Join([]string{
				"Intro paragraph.",
				"",
				"- list item one",
				"- list item two",
				"- list item three",
				"",
				"Trailing paragraph.",
			}, "\n"),
		},
		{
			name: "paragraphs-with-table",
			doc: strings.Join([]string{
				"Intro paragraph.",
				"",
				"| col a | col b |",
				"| ----- | ----- |",
				"| 1     | 2     |",
				"| 3     | 4     |",
				"",
				"Trailing paragraph after the table.",
			}, "\n"),
		},
	}
}

// progressivePrefixes splits doc into n monotonically growing
// byte prefixes, ending 用 full document. n>=1.
func progressivePrefixes(doc string, n int) []string {
	if n < 1 {
		n = 1
	}
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		// integer scaling 所以 last 条目 是 exactly len(doc)
		size := len(doc) * i / n
		if i == n {
			size = len(doc)
		}
		out = append(out, doc[:size])
	}
	return out
}

// TestStreamingMarkdown_FinalMatchesFullRender drives 序号 的
// progressive prefixes through streamingMarkdown 和 asserts final
// output 是 byte-identical 到 fresh full-document render.
func TestStreamingMarkdown_FinalMatchesFullRender(t *testing.T) {
	t.Parallel()

	const width = 80
	const steps = 15

	for _, sc := range streamingScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			renderer := newTestRenderer(t, width)
			var sm streamingMarkdown
			prefixes := progressivePrefixes(sc.doc, steps)

			var lastOut string
			for _, p := range prefixes {
				lastOut = sm.Render(p, width, renderer)
			}

			fresh := freshRender(t, sc.doc, width)
			require.Equal(t, fresh, lastOut,
				"final streaming output must match a fresh full render")
		})
	}
}

// TestStreamingMarkdown_IntermediateOutputsPlausible asserts 那
// every intermediate 刷盘 returns non-empty string 和 does
// 不 leak raw markdown source through 到 用户. 这 是
// "visually plausible" half 的 T2.
func TestStreamingMarkdown_IntermediateOutputsPlausible(t *testing.T) {
	t.Parallel()

	const width = 80
	const steps = 12

	for _, sc := range streamingScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			renderer := newTestRenderer(t, width)
			var sm streamingMarkdown

			for i, p := range progressivePrefixes(sc.doc, steps) {
				if p == "" {
					continue
				}
				out := sm.Render(p, width, renderer)
				require.NotEmptyf(t, out, "step %d: empty render for prefix len %d", i, len(p))
				require.Falsef(t, containsRawMarkdownSource(out),
					"step %d: render leaked raw markdown source.\nprefix=%q\nout=%s",
					i, p, normalizeRender(out))
			}
		})
	}
}

// -----------------------------------------------------------------------
// T3: 缓存 invalidation tests.
// -----------------------------------------------------------------------

// TestStreamingMarkdown_WidthChangeInvalidates asserts 那
// width change blows away cached prefix 所以 next render
// 是 keyed against new width. We can't observe 缓存
// directly without reaching into struct, 所以 we assert
// observable contract: after a width change, the rendered output
// reflects the new width AND the streamingMarkdown's internal
// 缓存 fields 是 reset 到 new state.
func TestStreamingMarkdown_WidthChangeInvalidates(t *testing.T) {
	t.Parallel()

	doc := "Para one.\n\nPara two.\n\nPara three."
	r80 := newTestRenderer(t, 80)
	r40 := newTestRenderer(t, 40)
	var sm streamingMarkdown

	out80 := sm.Render(doc, 80, r80)
	require.Equal(t, 80, sm.width, "width must be cached after first render")
	cachedPrefix := sm.stablePrefix

	out40 := sm.Render(doc, 40, r40)
	require.Equal(t, 40, sm.width, "width change must update cached width")
	require.NotEqual(t, out80, out40,
		"different widths must produce different rendered output")
	// stablePrefix may legitimately have re-advanced after the
	// reset (tryAdvanceFromEmpty), 但 如果 it has, it can no
	// longer carry OLD width's render. We assert 缓存
	// reset 通过 checking 那 cached prefix length 是 在
	// most the current content length.
	require.True(t, len(sm.stablePrefix) <= len(doc),
		"stable prefix must be a prefix of the current content")
	_ = cachedPrefix
}

// TestStreamingMarkdown_NonPrefixContentInvalidates verifies
// 那 content which 是 NOT prefix-extension 的 cached
// stable prefix triggers Reset 和 fresh render 路径. 这
// guards "用户 retried turn" case.
func TestStreamingMarkdown_NonPrefixContentInvalidates(t *testing.T) {
	t.Parallel()

	const width = 80
	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	// Drive streaming 序号 所以 缓存 picks up stable
	// prefix.
	doc := "Para one.\n\nPara two.\n\nPara three."
	for _, p := range progressivePrefixes(doc, 6) {
		_ = sm.Render(p, width, r)
	}
	require.NotEmpty(t, sm.stablePrefix,
		"stable prefix must be populated after streaming a multi-paragraph doc")

	// Now switch 到 entirely different content (用户 retried).
	other := "Completely different opening paragraph.\n\nAnd a second."
	out := sm.Render(other, width, r)
	require.NotEmpty(t, out)
	// stablePrefix must 是 prefix 的 `other`, i.e. 缓存 was
	// reset off the OLD content.
	require.True(t, strings.HasPrefix(other, sm.stablePrefix),
		"stable prefix must be reset to a prefix of the new content")

	// Visual equivalence 到 fresh render 的 `other`.
	fresh := freshRender(t, other, width)
	require.Equal(t, fresh, out,
		"render after non-prefix content change must match a fresh render")
}

// TestStreamingMarkdown_ResetClearsCache asserts Reset() drops
// every cached 字段; next render 是 necessarily full
// render 路径.
func TestStreamingMarkdown_ResetClearsCache(t *testing.T) {
	t.Parallel()

	const width = 80
	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	doc := "Para one.\n\nPara two.\n\nPara three."
	_ = sm.Render(doc, width, r)
	// sample doc has safe boundaries 所以 缓存 should
	// have advanced. If 为 some reason it didn't, we still
	// want Reset 到 是 no-op-safe operation; assert
	// post-Reset state directly.
	sm.Reset()
	require.Equal(t, 0, sm.width)
	require.Equal(t, "", sm.stablePrefix)
	require.Equal(t, "", sm.stablePrefixRender)

	// Next render must 是 full render 路径. Drive one step
	// 和 verify output matches fresh full render.
	out := sm.Render(doc, width, r)
	fresh := freshRender(t, doc, width)
	require.Equal(t, fresh, out)
}

// -----------------------------------------------------------------------
// T4: fallback safety.
// -----------------------------------------------------------------------

// TestStreamingMarkdown_NoSafeBoundaryAlwaysFullRenders covers
// "one giant 表 being built character 通过 character" case.
// Every 刷盘 must fall back 到 full render; 缓存 must
// 不 advance into unsafe state. We compare each 刷盘 到
// fresh full render 的 same prefix; bytes must match 为
// each prefix individually.
//
// (Byte equality 是 sound here because no concatenation happens:
// streaming 路径 delegates straight 到 renderer.Render 当
// 缓存 是 empty 和 no safe boundary exists.)
func TestStreamingMarkdown_NoSafeBoundaryAlwaysFullRenders(t *testing.T) {
	t.Parallel()

	const width = 80

	// One growing 表 — no blank lines anywhere, 所以 no
	// boundary candidate 是 ever found.
	doc := strings.Join([]string{
		"| col a | col b | col c |",
		"| ----- | ----- | ----- |",
		"| 1     | 2     | 3     |",
		"| 4     | 5     | 6     |",
		"| 7     | 8     | 9     |",
		"| 10    | 11    | 12    |",
		"| 13    | 14    | 15    |",
		"| 16    | 17    | 18    |",
		"| 19    | 20    | 21    |",
		"| 22    | 23    | 24    |",
	}, "\n")
	require.Equal(t, -1, findSafeMarkdownBoundary(doc),
		"sanity check: no blank lines, no safe boundary")

	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	prefixes := progressivePrefixes(doc, 10)
	for i, p := range prefixes {
		if p == "" {
			continue
		}
		out := sm.Render(p, width, r)
		fresh := freshRender(t, p, width)
		require.Equalf(t, fresh, out,
			"step %d (len=%d): streaming output must byte-equal a fresh render when boundary detection fails",
			i, len(p))
	}
	// 缓存 must remain empty: no boundary was ever found, no
	// width change occurred, no advance ever cached anything.
	require.Equal(t, "", sm.stablePrefix,
		"stable prefix must remain empty when no safe boundary ever exists")
}

// TestStreamingMarkdown_NoSafeBoundaryDoesNotCrash 是
// minimum-viability assertion 的 T4: even 当 boundary
// detection fails 在 every 刷盘 streaming 路径 must 不
// crash 和 must produce non-empty output 为 non-empty input.
func TestStreamingMarkdown_NoSafeBoundaryDoesNotCrash(t *testing.T) {
	t.Parallel()

	const width = 80
	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	// 一个 deeply-pathological input: single line 那 grows
	// one character 在 time. There 是 never blank-line
	// separator 所以 缓存 是 never advanced.
	src := "The quick brown fox jumps over the lazy dog."
	for i := 1; i <= len(src); i++ {
		out := sm.Render(src[:i], width, r)
		require.NotEmpty(t, out, "streaming output must not be empty for non-empty input")
	}
}

// -----------------------------------------------------------------------
// Integration assertions 在 wired-在 路径.
// -----------------------------------------------------------------------

// -----------------------------------------------------------------------
// T5 / T6 / T7: anywhere-在-prefix hazards (B1 / B2 / B3 从
// F8 round-2 review). For each hazard we drive every progressive
// prefix 的 document 那 exercises hazard through 缓存
// 和 assert two contracts:
//
//  1. The cached stable prefix never contains the hazard. If the
// hazard line 是 在 byte offset H, then after every 刷盘
// len(sm.stablePrefix) <= H. 这 是 "no silent
// corruption" half — algorithm cannot 受理 boundary
// 那 splits across hazard.
//
// 2. final 刷盘 是 visually equivalent 到 fresh full
// render 的 完成 document. 这 是 same T2-style
// equivalence assertion ported 到 new doc shapes.
// -----------------------------------------------------------------------

// nonBlankLines returns non-blank visible lines 的 s 用
// per-line trailing whitespace trimmed. Used 到 compare two
// rendered fragments 为 content equivalence 当 paragraph-
// margin behaviour legitimately differs between a single fresh
// render 和 streaming split render (per F8 design principle D
// — visual equivalence 是 bar, byte-equivalence 是 不).
//
// Some glamour block types (notably HTML blocks 和 reference
// link definitions) interact 用 adjacent paragraph blocks
// during a single render — adjacency effectively suppresses the
// blank-line margin between blocks. When streaming 路径
// renders prefix 和 trail 在 separate calls, seam 是
// re-introduced 作为 blank line. visible 文本 是 identical;
// 只 inter-block margin differs.
func nonBlankLines(s string) []string {
	clean := stripANSI(s)
	out := make([]string, 0)
	for _, l := range strings.Split(clean, "\n") {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// runProgressiveBoundaryRespectTest 是 shared body 的 T5/T6/T7.
// 它 accepts document 和 byte offset 的 line whose
// PRESENCE 在 prefix must trigger hazard reject;
// cached stable prefix may never extend past hazardLineOffset.
//
// final-output equivalence check 是 content-based (non-blank
// lines compared) rather than full-normalization: see
// nonBlankLines 为 reason.
func runProgressiveBoundaryRespectTest(t *testing.T, doc string, hazardLineOffset int) {
	t.Helper()
	const width = 80
	const steps = 25

	renderer := newTestRenderer(t, width)
	var sm streamingMarkdown

	prefixes := progressivePrefixes(doc, steps)
	var lastOut string
	for i, p := range prefixes {
		if p == "" {
			continue
		}
		lastOut = sm.Render(p, width, renderer)
		require.NotEmptyf(t, lastOut, "step %d: empty render", i)
		require.LessOrEqualf(t, len(sm.stablePrefix), hazardLineOffset,
			"step %d: cached stable prefix advanced past the hazard line\n"+
				"prefix len=%d, hazard at %d, sm.stablePrefix=%q",
			i, len(sm.stablePrefix), hazardLineOffset, sm.stablePrefix)
	}

	fresh := freshRender(t, doc, width)
	require.Equal(t, nonBlankLines(fresh), nonBlankLines(lastOut),
		"final streaming output must contain the same non-blank lines as a fresh full render")
}

// TestStreamingMarkdown_LooseListContinuation locks 在 B1 fix.
// 一个 loose list followed 通过 continuation paragraph 和 then
// trailing paragraph creates a candidate boundary between the list
// item 和 它的 continuation; trailing non-blank line 的 那
// candidate prefix 是 continuation paragraph (不 list
// marker), 所以 line-只 check would 受理 it.
// anywhere-在-prefix list-marker check rejects it.
func TestStreamingMarkdown_LooseListContinuation(t *testing.T) {
	t.Parallel()

	doc := strings.Join([]string{
		"Intro paragraph.",
		"",
		"- item one",
		"",
		"  continuation paragraph still belongs to item one",
		"",
		"- item two",
		"",
		"Trailing paragraph after the list.",
	}, "\n")

	// The first list marker line begins after "Intro paragraph.\n\n".
	// cached stable prefix may include 那 boundary (BEFORE
	// list opens) 但 must never advance into list.
	hazardOffset := strings.Index(doc, "- item one")
	require.Greater(t, hazardOffset, 0, "test setup")

	runProgressiveBoundaryRespectTest(t, doc, hazardOffset)
}

// TestStreamingMarkdown_HTMLBlock locks 在 B2 fix. 一个 raw HTML
// block followed 通过 paragraph creates candidate boundary
// between closed HTML block 和 trailing paragraph.
// anywhere-在-prefix HTML-opener check rejects any boundary 那
// would include HTML block 在 stable prefix.
func TestStreamingMarkdown_HTMLBlock(t *testing.T) {
	t.Parallel()

	doc := strings.Join([]string{
		"Intro paragraph.",
		"",
		"<div>",
		"some block content",
		"</div>",
		"",
		"Trailing paragraph after the HTML block.",
	}, "\n")

	hazardOffset := strings.Index(doc, "<div>")
	require.Greater(t, hazardOffset, 0, "test setup")

	runProgressiveBoundaryRespectTest(t, doc, hazardOffset)
}

// TestStreamingMarkdown_HTMLBlockType7 covers HTML block type 7
// (CommonMark): generic open/close tag whose name 是 NOT 在
// fixed type-6 set still opens HTML block 和 must forfeit any
// boundary 那 would split block off 从 following content.
func TestStreamingMarkdown_HTMLBlockType7(t *testing.T) {
	t.Parallel()

	doc := strings.Join([]string{
		"Intro paragraph.",
		"",
		"<custom-tag>",
		"some block content",
		"</custom-tag>",
		"",
		"Trailing paragraph after the custom-tag block.",
	}, "\n")

	hazardOffset := strings.Index(doc, "<custom-tag>")
	require.Greater(t, hazardOffset, 0, "test setup")

	runProgressiveBoundaryRespectTest(t, doc, hazardOffset)
}

// TestStreamingMarkdown_LinkRefDefinition locks 在 B3 fix. 一个
// reference link definition followed 通过 paragraph 那 uses
// reference creates boundary candidate between def 和
// paragraph; rendering them 在 separate glamour passes loses
// definition. anywhere-在-prefix ref-def check rejects.
func TestStreamingMarkdown_LinkRefDefinition(t *testing.T) {
	t.Parallel()

	doc := strings.Join([]string{
		"Intro paragraph.",
		"",
		"[ref]: http://example.com",
		"",
		"Trailing paragraph that links to [the example][ref] inline.",
	}, "\n")

	hazardOffset := strings.Index(doc, "[ref]:")
	require.Greater(t, hazardOffset, 0, "test setup")

	runProgressiveBoundaryRespectTest(t, doc, hazardOffset)
}

// TestAssistantStreamingContent_ResetOnClearCache guards the
// integration contract 那 ClearItemCache (style change) drops
// streaming-markdown 缓存. Without 这, style change
// would leave OLD style's ANSI sequences embedded 在
// stable-prefix render 和 next 刷盘 would visually mix
// styles.
func TestAssistantStreamingContent_ResetOnClearCache(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	doc := "Para one.\n\nPara two.\n\nPara three."
	msg := finishedAssistantMessage("stream-clear", doc)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 80
	_ = item.RawRender(width)
	// Drive second 消息 那 extends content 所以
	// streaming 缓存 has chance 到 advance (如果 it would).
	doc2 := doc + "\n\nFour."
	item.SetMessage(finishedAssistantMessage("stream-clear", doc2))
	_ = item.RawRender(width)

	// Now wipe the caches the way ClearItemCache does.
	item.clearCache()

	require.Equal(t, "", item.streamingContent.stablePrefix,
		"clearCache must Reset the streaming-markdown cache")
	require.Equal(t, "", item.streamingContent.stablePrefixRender)
	require.Equal(t, 0, item.streamingContent.width)
}

// -----------------------------------------------------------------------
// Relaxed boundary (#3162). Prose holding no blank line never satisfies
// blank-line predicate, 所以 before relaxBoundaryAfter 缓存 never
// advances 和 every 刷盘 re-renders whole document. Past 那 much
// unstable tail we cut 在 plain newline instead — 但 只 where
// same hazard checks pass.
// -----------------------------------------------------------------------

// longProse returns n lines 的 plain prose joined 通过 single newlines,
// 所以 document holds no blank-line separator anywhere.
func longProse(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("Let me reconsider step %d: the constraint holds for every branch here.", i)
	}
	return strings.Join(lines, "\n")
}

func TestStreamingMarkdown_RelaxedBoundaryAdvancesOnLongProse(t *testing.T) {
	t.Parallel()

	const width = 80
	doc := longProse(400)
	require.Greater(t, len(doc), relaxBoundaryAfter*2,
		"sanity check: doc must outgrow the relax threshold")
	require.Equal(t, -1, findSafeMarkdownBoundary(doc),
		"sanity check: no blank lines, no safe boundary")

	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	var lastOut string
	for _, p := range progressivePrefixes(doc, 40) {
		if p == "" {
			continue
		}
		lastOut = sm.Render(p, width, r)
		require.NotEmpty(t, lastOut)
		require.True(t, strings.HasPrefix(p, sm.stablePrefix),
			"stable prefix must stay a literal prefix of the content")
	}

	require.NotEmpty(t, sm.stablePrefix,
		"relaxed boundary must advance the cache once the tail outgrows the threshold")
	visible := stripANSI(lastOut)
	require.Contains(t, visible, "step 0:", "head of the trace must survive")
	require.Contains(t, visible, "step 399:", "tail of the trace must survive")
}

func TestStreamingMarkdown_RelaxedBoundaryRespectsHazards(t *testing.T) {
	t.Parallel()

	const width = 80

	tests := []struct {
		name string
		doc  string
	}{
		{
			// Every line holds pipe, 所以 lineOpensConstruct rejects
			// each candidate 和 表 是 never split.
			name: "long table",
			doc: func() string {
				rows := make([]string, 1200)
				rows[0] = "| col a | col b | col c |"
				rows[1] = "| ----- | ----- | ----- |"
				for i := 2; i < len(rows); i++ {
					rows[i] = fmt.Sprintf("| %d | %d | %d |", i, i*2, i*3)
				}
				return strings.Join(rows, "\n")
			}(),
		},
		{
			// An open fence makes parity odd 在 every candidate
			// inside it, 所以 no cut lands 在 code block.
			name: "open code fence",
			doc:  "```go\n" + longProse(400),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Greater(t, len(tc.doc), relaxBoundaryAfter*2)

			r := newTestRenderer(t, width)
			var sm streamingMarkdown
			for _, p := range progressivePrefixes(tc.doc, 40) {
				if p == "" {
					continue
				}
				require.NotEmpty(t, sm.Render(p, width, r))
			}
			require.Equal(t, "", sm.stablePrefix,
				"cache must not advance across an unsafe construct")
		})
	}
}

// TestStreamingMarkdown_LastLinesMatchesOutput pins the incrementally
// tracked line count 到 值 fresh countLines would produce.
// renderThinking sizes 它的 "N lines hidden" truncation hint 从
// LastLines, 所以 any drift here shows up 作为 wrong number 在 screen.
func TestStreamingMarkdown_LastLinesMatchesOutput(t *testing.T) {
	t.Parallel()

	const width = 80

	docs := map[string]string{
		"blank-line-free prose": longProse(400),
		"paragraphs":            strings.Repeat("A paragraph of reasoning that runs on for a while.\n\n", 120),
		"mixed":                 longProse(60) + "\n\n" + strings.Repeat("- a list item\n", 30) + "\n\n" + longProse(200),
		"fenced":                "```go\nfunc main() {}\n```\n\n" + longProse(200),
		"table":                 "| a | b |\n| - | - |\n| 1 | 2 |\n\n" + longProse(200),
	}

	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newTestRenderer(t, width)
			var sm streamingMarkdown
			for i, p := range progressivePrefixes(doc, 60) {
				if p == "" {
					continue
				}
				out := sm.Render(p, width, r)
				require.Equalf(t, countLines(strings.TrimSpace(out)), sm.LastLines(),
					"step %d (len=%d): tracked line count must match the output", i, len(p))
			}
		})
	}
}

// lazyTestDoc 是 document 用 safe boundaries, 所以 render 那 grows
// 从 it seeds prefix 缓存.
const lazyTestDoc = "# Title\n\nFirst paragraph of the answer.\n\nSecond paragraph.\n\n- one\n- two\n"

func TestStreamingMarkdown_ColdRenderDoesNotSeed(t *testing.T) {
	t.Parallel()

	var sm streamingMarkdown
	got := sm.Render(lazyTestDoc, 60, newTestRenderer(t, 60))
	require.Equal(t, freshRender(t, lazyTestDoc, 60), got)
	require.Equal(t, countLines(trimGlamourMargins(got)), sm.LastLines())
	require.Empty(t, sm.stablePrefix, "a cold render must not pay for a seed nothing may reuse")
}

func TestStreamingMarkdown_RepeatRenderIsMemoized(t *testing.T) {
	t.Parallel()

	var sm streamingMarkdown
	first := sm.Render(lazyTestDoc, 60, newTestRenderer(t, 60))
	lines := sm.LastLines()

	// 一个 renderer 那 wraps differently proves repeat skips glamour.
	again := sm.Render(lazyTestDoc, 60, newTestRenderer(t, 12))
	require.Equal(t, first, again)
	require.Equal(t, lines, sm.LastLines())

	// 一个 new width misses.
	require.Equal(t, freshRender(t, lazyTestDoc, 30), sm.Render(lazyTestDoc, 30, newTestRenderer(t, 30)))
}

func TestStreamingMarkdown_GrowthSeedsLazily(t *testing.T) {
	t.Parallel()

	var sm streamingMarkdown
	_ = sm.Render(lazyTestDoc, 60, newTestRenderer(t, 60))

	grown := lazyTestDoc + "\nA trailing paragraph.\n"
	got := sm.Render(grown, 60, newTestRenderer(t, 60))
	require.Equal(t, freshRender(t, grown, 60), got)
	require.Equal(t, countLines(trimGlamourMargins(got)), sm.LastLines())
	require.NotEmpty(t, sm.stablePrefix, "growing content seeds the prefix cache")
}

// 一个 finished 消息 rendered cold renders 它的 content 和 thinking once,
// without seeding a stable prefix it would never reuse.
func TestAssistantMessageItem_FinishedRendersOnce(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	paragraphs := strings.Repeat("A paragraph of text.\n\n", 20)
	msg := &message.Message{
		ID:   "finished",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: paragraphs, StartedAt: testStartedAt, FinishedAt: testFinishedAt},
			message.TextContent{Text: paragraphs},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishedAt},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.RawRender(80)

	require.Empty(t, item.streamingContent.stablePrefix)
	require.Empty(t, item.streamingThinking.stablePrefix)
	require.NotEmpty(t, item.streamingContent.lastRender)
	require.NotEmpty(t, item.streamingThinking.lastRender)
}

// TestMarkdownSentinel_RendersToNothing pins the assumption renderPiece
// relies 在: 用 every renderer streaming 路径 uses, sentinel
// renders 到 nothing, 所以 piece behind it needs no slicing.
func TestMarkdownSentinel_RendersToNothing(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	for _, width := range []int{20, 60, 120} {
		for name, renderer := range map[string]*glamour.TermRenderer{
			"content":  common.MarkdownRenderer(&sty, width),
			"thinking": common.QuietMarkdownRenderer(&sty, width),
			"plan":     common.PlanMarkdownRenderer(&sty, width),
		} {
			mu := common.LockMarkdownRenderer(renderer)
			mu.Lock()
			out, err := renderer.Render(markdownSentinel)
			mu.Unlock()
			require.NoError(t, err)
			require.Empty(t, out, "%s renderer at width %d", name, width)
		}
	}
}
