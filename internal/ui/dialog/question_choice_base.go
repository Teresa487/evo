package dialog

import (
	"fmt"
	"image"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// choiceListMaxWidth 是 maximum content width 为 choice
// question components.
const choiceListMaxWidth = 120

// questionIconPrompt returns themed question icon based 在
// focus state. Shared 通过 all question component types.
func questionIconPrompt(sty *styles.Styles, focused bool) string {
	if focused {
		return sty.Editor.PromptQuestionIconFocused.Render()
	}
	return sty.Editor.PromptQuestionIconBlurred.Render()
}

// choiceList 是 shared base 为 single-choice 和 multi-choice
// question components. 它 embeds questionEditor 为 fill-在, 备注,
// 和 editor handling. Concrete types embed it 和 只 implement
// selection semantics.
// fillInTop 是 first content-line index 的 fill-在 行,
// computed during buildLines. -1 当 no fill-在 rows exist.
// Used 通过 clampBounds 到 keep fill-在 visible during wheel
// scrolling without scanning lines 在 every 事件.
type choiceList struct {
	questionEditor
	Request question.Question

	cursorIdx        int
	scrollOffset     int // lines scrolled past the top of the viewport
	focused          bool
	lastWidth        int
	choiceCompositor *lipgloss.Compositor
	suppressScroll   bool // skip scroll clamping after mouse click
	wheelActive      bool // wheel-scroll mode: skip cursor snap until next keyboard nav
	hoverX, hoverY   int  // current mouse position for hover highlight
	hoveredChoice    int  // choice index under mouse, or -1
	mouseActive      bool // true when last interaction was mouse (hover mode)

	// Cached layout 为 wheel-scroll bounds checking.
	lastLines    []contentLine // last rendered lines from drawContent
	lastViewport int           // last viewport height from drawContent
	fillInTop    int           // first fill-in row index, or -1
	fillInBottom int           // last fill-in row index, or -1

	keyUp    key.Binding
	keyDown  key.Binding
	keyClose key.Binding
}

// numberKeyIndex returns zero-based choice index 为 number
// key press (1-9), 或 -1 如果 key 是 不 valid shortcut 为
// the current choices.
func (c *choiceList) numberKeyIndex(msg tea.KeyPressMsg) int {
	if len(msg.Text) != 1 {
		return -1
	}
	n, err := strconv.Atoi(msg.Text)
	if err != nil || n < 1 || n > len(c.Request.Choices) {
		return -1
	}
	return n - 1
}

// newChoiceList creates choiceList 用 configured fill-在
// textarea 和 navigation bindings.
func newChoiceList(sty *styles.Styles, req question.Question) choiceList {
	return choiceList{
		questionEditor: newQuestionEditor(sty),
		Request:        req,
		hoveredChoice:  -1,
		hoverX:         -1,
		hoverY:         -1,
		fillInTop:      -1,
		fillInBottom:   -1,
		keyUp:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
		keyDown:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		keyClose:       CloseKey,
	}
}

func (c *choiceList) itemCount() int {
	return len(c.Request.Choices) + 1 // +1 for fill-in
}

func (c *choiceList) isFillIn() bool {
	return c.cursorIdx == len(c.Request.Choices)
}

// moveUp moves cursor up, wrapping around. Closes any 活跃
// note editor since note 上下文 changes 用 cursor.
func (c *choiceList) moveUp() {
	c.wheelActive = false
	if c.mouseActive {
		// Adopt hovered item 作为 anchor 所以 first arrow
		// press moves one step above it (hoveredChoice-1). When no
		// choice 是 hovered, hoveredChoice 是 -1, 所以 anchor 在 0 和
		// wrap 到 last item.
		c.cursorIdx = max(c.hoveredChoice, 0)
	}
	c.mouseActive = false
	c.fillIn.Blur()
	if c.activeNoteKey != "" {
		c.closeNote(c.noteKey())
	}
	c.cursorIdx--
	if c.cursorIdx < 0 {
		c.cursorIdx = c.itemCount() - 1
	}
}

// moveDown moves the cursor down, wrapping around. Closes any
// 活跃 note editor since note 上下文 changes 用 cursor.
func (c *choiceList) moveDown() {
	c.wheelActive = false
	if c.mouseActive {
		// Adopt hovered item 作为 anchor 所以 first arrow
		// press moves one step below it (hoveredChoice+1). When no
		// choice 是 hovered, hoveredChoice 是 -1, which advances 到 0.
		c.cursorIdx = c.hoveredChoice
	}
	c.mouseActive = false
	c.fillIn.Blur()
	if c.activeNoteKey != "" {
		c.closeNote(c.noteKey())
	}
	c.cursorIdx++
	if c.cursorIdx >= c.itemCount() {
		c.cursorIdx = 0
	}
}

// adoptHover moves cursor onto hovered item 和 leaves
// hover mode. 调用 it before non-directional keyboard action 那
// operates 在 current cursor (select, toggle, note) 所以 key
// acts 在 item under mouse rather than stale cursor.
// Arrow keys handle hover 移交 themselves via moveUp/moveDown.
func (c *choiceList) adoptHover() {
	if c.mouseActive && c.hoveredChoice >= 0 {
		c.cursorIdx = c.hoveredChoice
	}
	c.mouseActive = false
}

// handleFillInKey processes keys 当 fill-在 textarea 是
// focused. Returns (cmd, handled). When handled 是 true
// caller should 不 process key further.
func (c *choiceList) handleFillInKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, c.keyClose):
		c.fillIn.Blur()
		return nil, true
	case key.Matches(msg, c.navUp):
		// Arrows move relative 到 fill-在 用户 是 editing,
		// 不 choice mouse happens 到 hover, 所以 drop hover mode
		// before navigating.
		c.mouseActive = false
		c.moveUp()
		if c.isFillIn() {
			c.fillIn.Focus()
			return c.fillIn.Focus(), true
		}
		return nil, true
	case key.Matches(msg, c.navDown):
		c.mouseActive = false
		c.moveDown()
		if c.isFillIn() {
			c.fillIn.Focus()
			return c.fillIn.Focus(), true
		}
		return nil, true
	default:
		// Typing 是 keyboard input, 所以 leave hover mode: fill-在
		// regains 它的 gutter bar 和 any hovered choice releases it.
		c.wheelActive = false
		c.mouseActive = false
		var cmd tea.Cmd
		c.fillIn, cmd = c.fillIn.Update(msg)
		return cmd, true
	}
}

// handleNavKey processes up/down navigation keys 当
// fill-在 是 NOT focused. Returns true 如果 key was consumed.
func (c *choiceList) handleNavKey(msg tea.KeyPressMsg) bool {
	switch {
	case key.Matches(msg, c.keyUp):
		c.moveUp()
		if c.isFillIn() {
			c.fillIn.Focus()
		}
		return true
	case key.Matches(msg, c.keyDown):
		c.moveDown()
		if c.isFillIn() {
			c.fillIn.Focus()
		}
		return true
	}
	return false
}

// noteKey returns map key 为 currently focused item's
// note. Choices use their ID; the question itself uses [question.QuestionNoteKey].
func (c *choiceList) noteKey() string {
	if c.isFillIn() || c.cursorIdx >= len(c.Request.Choices) {
		return question.QuestionNoteKey
	}
	return c.Request.Choices[c.cursorIdx].ID
}

// contentLine 是 one visual 行 的 choice list. 文本 是
// pre-rendered, pre-styled string 为 行. fillInRow marks
// first 行 的 focused fill-在 textarea, where hardware
// cursor 是 placed. noteRow marks first 行 的 focused
// note editor.
type contentLine struct {
	text       string
	fillInRow  bool
	noteRow    bool
	cursorItem bool // belongs to the currently selected item
	choiceIdx  int  // zero-based choice index, or -1 if not a choice row
}

// newContentLine creates contentLine 用 choiceIdx initialized
// 到 -1 所以 non-choice rows don't accidentally match choice_0.
func newContentLine(text string) contentLine {
	return contentLine{text: text, choiceIdx: -1}
}

// sectionHeight returns visual line count 的 文本 block
// wrapped 在 width.
func sectionHeight(text string, width int) int {
	if text == "" {
		return 0
	}
	return strings.Count(ansi.Wrap(text, width, ""), "\n") + 1
}

// wrapIndent wraps 文本 在 width 和 prefixes every continuation
// line 用 indent 所以 multi-line content aligns under first
// line's content rather than 刷盘 left.
func wrapIndent(text string, width int, indent string) string {
	wrapped := ansi.Wrap(text, width, "")
	lines := strings.Split(wrapped, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// drawStyledText blits ANSI-styled string into area 和 returns
// number 的 visual lines it occupies.
func drawStyledText(scr uv.Screen, area uv.Rectangle, text string) int {
	if text == "" {
		return 0
	}
	uv.NewStyledString(text).Draw(scr, area)
	return strings.Count(text, "\n") + 1
}

// buildLines renders entire choice list into flat slice 的
// rows. 这 是 single source 的 truth: height 是 len(lines),
// scrolling 是 index math over slice, 和 drawing blits
// 窗口 的 it. itemFn renders choice's label 行(s) 作为 string.
//
// final 行 是 always blank line, giving list one line 的
// bottom padding 作为 real content rather than phantom offset.
func (c *choiceList) buildLines(innerWidth int, fillInPrefix string, itemFn choiceItemRenderer) []contentLine {
	bodyStyle := c.Styles.Editor.QuestionBody
	barActive := c.Styles.Editor.QuestionCursorBar.Render("┃ ")
	const barInactive = "  "

	var lines []contentLine
	// push appends one 行 per line 的 文本, 刷盘 against gutter:
	// question header, 它的 description, 和 blank separators.
	push := func(text string) {
		for ln := range strings.SplitSeq(text, "\n") {
			lines = append(lines, newContentLine(ln))
		}
	}

	// Question 请求头 + blank separator.
	icon := c.iconPrompt()
	iconWidth := lipgloss.Width(icon)
	qIndent := strings.Repeat(" ", iconWidth)
	push(icon + c.Styles.Editor.QuestionUnselected.Render(wrapIndent(c.Request.Text, innerWidth-iconWidth, qIndent)))
	push("")

	// Optional markdown description + blank separator.
	if c.Request.Description != "" {
		push(c.renderDescription(innerWidth))
		push("")
	}

	// Choices: label 行(s), optional wrapped description, note, blank.
	for i, ch := range c.Request.Choices {
		active := i == c.cursorIdx && !c.mouseActive
		hovered := i == c.hoveredChoice && c.mouseActive
		bar := barInactive
		if active || hovered {
			bar = barActive
		}
		// Hang rendered block 在 这 choice's gutter, one 行 per
		// line. bar goes 在 every 行, 所以 block 那 wraps keeps
		// unbroken gutter rather than marking 只 它的 first line.
		hang := func(block string) {
			for ln := range strings.SplitSeq(block, "\n") {
				lines = append(lines, contentLine{text: bar + ln, cursorItem: active, choiceIdx: i})
			}
		}
		hang(itemFn(i, ch, active, innerWidth))
		if ch.Description != "" {
			hang(bodyStyle.Render(wrapIndent(ch.Description, innerWidth-lipgloss.Width(bar), "")))
		}

		// Inline note editor 或 saved note 为 这 choice.
		c.drawNote(&lines, innerWidth, bar, ch.ID, active)

		// Blank separator — tag 用 current choice index 所以 it's
		// 片段 的 clickable/hoverable zone.
		lines = append(lines, contentLine{text: "", choiceIdx: i})
	}

	// Fill-在: bar 和 提示词 mirror choice 行. 提示词 style
	// 是 supplied 通过 component via fillInPrefix (pink 当
	// fill-在 是 selected item). bar follows 活跃/hover
	// state, like choices, rather than staying lit whenever the
	// fill-在 merely holds 文本.
	fillInIdx := len(c.Request.Choices)
	fillActive := c.isFillIn() && !c.mouseActive
	fillHovered := c.mouseActive && c.hoveredChoice == fillInIdx
	fillBar := barInactive
	if fillActive || fillHovered {
		fillBar = barActive
	}
	linesBeforeFillIn := len(lines)
	c.drawFillIn(&lines, innerWidth, fillBar, fillInPrefix, c.isFillIn(), false)

	// Record fill-在 行 range 为 wheel-scroll bounds checking.
	c.fillInTop = -1
	c.fillInBottom = -1
	for i := linesBeforeFillIn; i < len(lines); i++ {
		if c.fillInTop < 0 {
			c.fillInTop = i
		}
		c.fillInBottom = i
	}

	// Tag fill-在 rows 用 fill-在 item index 所以 clicks can
	// navigate 到 it.
	for i := linesBeforeFillIn; i < len(lines); i++ {
		lines[i].choiceIdx = fillInIdx
	}

	// Trailing blank line 为 bottom padding.
	push("")

	return lines
}

// renderDescription renders markdown description 在 width.
func (c *choiceList) renderDescription(width int) string {
	r := common.MarkdownRenderer(c.Styles, width)
	mu := common.LockMarkdownRenderer(r)
	mu.Lock()
	out, err := r.Render(c.Request.Description)
	mu.Unlock()
	if err != nil {
		return c.Request.Description
	}
	return strings.TrimSuffix(out, "\n")
}

// choiceItemRenderer renders choice's label content 作为 string.
// bar prefix 是 applied 通过 buildLines 所以 那 continuation
// lines also receive it. innerWidth 是 available content
// width 为 这 particular render pass (may differ between
// overflow-测试 和 final render).
type choiceItemRenderer func(index int, choice question.Choice, active bool, innerWidth int) string

// height returns total visual height 在 given width. 它 是
// len(buildLines), single source 的 truth 为 layout, 和
// pure function 的 width passed 在 所以 it always agrees 用
// width drawContent will use 这 frame.
func (c *choiceList) height(width int) int {
	if width <= 0 {
		width = c.lastWidth
	}
	innerWidth := min(width-4, choiceListMaxWidth)
	return len(c.buildLines(innerWidth, "> ", func(int, question.Choice, bool, int) string {
		return "x" // single-line placeholder; only count matters
	}))
}

func (c *choiceList) heightChanged() bool {
	return false // height is deterministic
}

func (c *choiceList) setFocused(focused bool) {
	c.focused = focused
}

// setHover updates hover position 和 resolves which choice
// 是 under cursor using compositor. Hover feedback stays
// live even while textarea (fill-在 或 note) 是 focused 所以
// 用户 can still see what mouse 是 pointing 在; effects
// 那 would disrupt editing 是 suppressed elsewhere (keyboard
// nav ignores hover while editing, 和 committed selection 是
// 不 re-styled).
func (c *choiceList) setHover(x, y int) {
	c.hoverX = x
	c.hoverY = y
	c.mouseActive = true
	c.hoveredChoice = -1
	if c.choiceCompositor == nil {
		return
	}
	hit := c.choiceCompositor.Hit(x, y)
	if !hit.Empty() {
		var idx int
		if _, err := fmt.Sscanf(hit.ID(), "choice_%d", &idx); err == nil {
			c.hoveredChoice = idx
		}
	}
}

// iconPrompt returns themed question icon based 在 focus.
func (c *choiceList) iconPrompt() string {
	return questionIconPrompt(c.Styles, c.focused)
}

// drawContent renders choice list 用 scroll support. 它
// builds full line list, clamps scroll offset 到 keep
// cursor visible, then blits visible 窗口. Returns
// hardware cursor position, 或 nil.
func (c *choiceList) drawContent(scr uv.Screen, area uv.Rectangle, fillInPrefix string, itemFn choiceItemRenderer) *tea.Cursor {
	c.lastWidth = area.Dx()
	viewport := area.Dy()

	// Build lines 在 wide width first (matching height(),
	// which form uses 到 size our viewport). Only reserve
	// scrollbar column 和 rebuild narrow 如果 wide layout
	// actually overflows. Testing narrow first would over-报告
	// overflow 通过 one line 在 boundary widths, causing
	// scrollbar 到 flicker during horizontal resize.
	contentWidth := area.Dx()
	innerNarrow := min(contentWidth-1-4, choiceListMaxWidth)
	innerWide := min(contentWidth-4, choiceListMaxWidth)

	lines := c.buildLines(innerWide, fillInPrefix, itemFn)
	overflow := viewport > 0 && len(lines) > viewport
	if overflow && innerNarrow != innerWide {
		lines = c.buildLines(innerNarrow, fillInPrefix, itemFn)
	}

	if overflow {
		contentWidth--
	}
	c.lastLines = lines
	c.lastViewport = viewport
	c.clampScroll(lines, viewport)

	// Blit visible 窗口.
	var cur *tea.Cursor
	for screenRow := range viewport {
		idx := c.scrollOffset + screenRow
		if idx >= len(lines) {
			break
		}
		ln := lines[idx]
		y := area.Min.Y + screenRow
		if ln.text != "" {
			uv.NewStyledString(ln.text).Draw(scr, image.Rect(area.Min.X, y, area.Min.X+contentWidth, y+1))
		}
		if ln.fillInRow {
			fillPrefix := c.Styles.Editor.QuestionBody.Render("> ")
			if tc := c.fillInCursor(screenRow, area.Min.X, lipgloss.Width(fillPrefix)); tc != nil {
				cur = tc
			}
		}
		if ln.noteRow {
			const notePrefix = "> "
			if tc := c.noteCursor(screenRow, area.Min.X, lipgloss.Width(notePrefix)); tc != nil {
				cur = tc
			}
		}
	}

	// Clamp cursor 到 visible area 到 prevent overflow.
	if cur != nil {
		if cur.Y < 0 {
			cur.Y = 0
		} else if cur.Y >= viewport {
			cur.Y = viewport - 1
		}
		if cur.X < 0 {
			cur.X = 0
		} else if cur.X >= area.Dx() {
			cur.X = area.Dx() - 1
		}
	}

	// Scrollbar.
	if overflow {
		sb := common.Scrollbar(c.Styles, viewport, len(lines), viewport, c.scrollOffset)
		if sb != "" {
			x := area.Max.X - 1
			uv.NewStyledString(sb).Draw(scr, image.Rect(x, area.Min.Y, x+1, area.Min.Y+viewport))
		}
	}

	// Build hit layers 为 choice rows.
	c.buildChoiceCompositor(lines, area, contentWidth)

	return cur
}

// buildChoiceCompositor creates hit layers 为 each visible choice
// 行 所以 那 mouse clicks can select choices directly. Each choice
// gets single layer spanning all 它的 visible rows.
func (c *choiceList) buildChoiceCompositor(lines []contentLine, area uv.Rectangle, contentWidth int) {
	// Collect screen-行 range 为 each choice index.
	type rowRange struct{ min, max int }
	ranges := make(map[int]*rowRange)
	for screenRow := range area.Dy() {
		idx := c.scrollOffset + screenRow
		if idx >= len(lines) {
			break
		}
		ln := lines[idx]
		if ln.choiceIdx < 0 {
			continue
		}
		r, ok := ranges[ln.choiceIdx]
		if !ok {
			r = &rowRange{min: screenRow, max: screenRow}
			ranges[ln.choiceIdx] = r
		} else {
			if screenRow < r.min {
				r.min = screenRow
			}
			if screenRow > r.max {
				r.max = screenRow
			}
		}
	}

	var layers []*lipgloss.Layer
	for choiceIdx, r := range ranges {
		height := r.max - r.min + 1
		hitStr := strings.Repeat(strings.Repeat(" ", contentWidth)+"\n", height-1) + strings.Repeat(" ", contentWidth)
		y := area.Min.Y + r.min
		layers = append(layers, lipgloss.NewLayer(hitStr).X(area.Min.X).Y(y).ID(fmt.Sprintf("choice_%d", choiceIdx)))
	}
	if len(layers) > 0 {
		c.choiceCompositor = lipgloss.NewCompositor(layers...)
	} else {
		c.choiceCompositor = nil
	}
}

// clampScroll 保持 cursor item visible using sliding
// 窗口: cursor moves freely within visible region 和
// 只 pushes 窗口 当 it reaches edge. Going down pushes
// bottom; going up pushes top until start (header 和
// HandleWheel scrolls the choice list vertically. Satisfies
// WheelScrollableEditor 所以 form can delegate wheel events.
func (c *choiceList) HandleWheel(deltaX, deltaY float64) {
	if deltaY == 0 {
		return
	}
	c.scrollOffset += int(deltaY)
	c.wheelActive = true
	c.clampToBounds(c.lastLines, c.lastViewport)
}

// clampToBounds enforces scroll bounds [0, max] 和 keeps
// fill-在 在 least partially visible 当 活跃. 它 does NOT
// snap 到 cursor, making it safe 为 wheel-driven scrolling.
func (c *choiceList) clampToBounds(lines []contentLine, viewport int) {
	limit := max(0, len(lines)-viewport)
	c.scrollOffset = min(max(0, c.scrollOffset), limit)
	// When fill-在 是 活跃, ensure 在 least one fill-在 line
	// remains visible 在 viewport.
	if c.isFillIn() && c.fillInTop >= 0 {
		fillInBottom := c.fillInBottom
		if fillInBottom < 0 {
			fillInBottom = c.fillInTop
		}
		// If scrolled past fill-在 entirely, pull back 所以
		// last fill-在 line 是 在 bottom 的 viewport.
		if c.scrollOffset > fillInBottom {
			c.scrollOffset = max(0, fillInBottom-viewport+1)
		}
		// If scrolled above fill-在 start while fill-在 是
		// taller than viewport, pin 到 fill-在 start.
		if c.scrollOffset+viewport <= c.fillInTop && fillInBottom-c.fillInTop+1 >= viewport {
			c.scrollOffset = c.fillInTop
		}
	}
}

// clampScroll 保持 cursor item visible using sliding
// 窗口: cursor moves freely within visible region 和
// 只 pushes 窗口 当 it reaches edge. Going down pushes
// bottom; going up pushes top until start (header 和
// description) comes back into view.
func (c *choiceList) clampScroll(lines []contentLine, viewport int) {
	if c.suppressScroll {
		// Skip snap 到 cursor, 但 不 bounds: click
		// 那 suppressed it may have shrunk content ( blurred
		// fill-在 collapses 到 one 行).
		c.suppressScroll = false
		c.clampToBounds(lines, viewport)
		return
	}
	// After wheel scroll, 只 enforce bounds instead 的 snapping
	// 到 cursor. Wheel mode persists until next keyboard nav.
	if c.wheelActive {
		c.clampToBounds(lines, viewport)
		return
	}
	limit := max(0, len(lines)-viewport)
	if limit == 0 {
		c.scrollOffset = 0
		return
	}

	// 行 range 的 cursor item.
	cursorTop, cursorBottom := -1, -1
	for i, ln := range lines {
		if ln.cursorItem {
			if cursorTop < 0 {
				cursorTop = i
			}
			cursorBottom = i
		}
	}
	// When fill-在 是 focused, narrow cursor target 到
	// specific textarea cursor line 所以 typing auto-scrolls.
	if c.isFillIn() && c.fillIn.Focused() && cursorTop >= 0 {
		if tc := c.fillIn.Cursor(); tc != nil {
			targetLine := cursorTop + tc.Y
			if targetLine >= cursorTop && targetLine <= cursorBottom {
				cursorTop = targetLine
				cursorBottom = targetLine
			}
		}
	}
	if cursorTop < 0 {
		c.scrollOffset = min(max(0, c.scrollOffset), limit)
		return
	}

	// Keep one line below cursor visible (trailing pad 在
	// last item, separator otherwise) 所以 selection 是 never
	// 刷盘 against bottom edge.
	below := min(cursorBottom+1, len(lines)-1)

	// On first selectable item, prefer top 所以 header
	// 和 description (nothing selectable sits above them) come
	// into view.
	if c.cursorIdx == 0 {
		c.scrollOffset = 0
	}
	// Push 窗口 down 如果 cursor's bottom fell below it.
	if below >= c.scrollOffset+viewport {
		c.scrollOffset = below - viewport + 1
	}
	// Push 窗口 up 如果 cursor's top rose above it.
	if cursorTop < c.scrollOffset {
		c.scrollOffset = cursorTop
	}

	c.scrollOffset = min(max(0, c.scrollOffset), limit)
}

// handleFillInFocused processes keys 当 fill-在 textarea 是
// focused. onClose 是 called 为 close key, onDone 为
// done key. Returns (done, cmd, handled). When handled 是 false
// caller 应当 process key itself.
func (c *choiceList) handleFillInFocused(
	msg tea.KeyPressMsg,
	doneKey key.Binding,
	onClose func() (bool, tea.Cmd),
	onDone func() (bool, tea.Cmd),
) (bool, tea.Cmd, bool) {
	if !c.isFillIn() || !c.fillIn.Focused() {
		return false, nil, false
	}
	if key.Matches(msg, c.keyClose) {
		done, cmd := onClose()
		return done, cmd, true
	}
	if key.Matches(msg, doneKey) {
		done, cmd := onDone()
		return done, cmd, true
	}
	cmd, handled := c.handleFillInKey(msg)
	return false, cmd, handled
}
