package dialog

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
)

// sameFuzzyMatch reports whether two fuzzy.Match values 是
// observably equal. Because Match contains a slice (MatchedIndexes)
// it 是 不 directly comparable 用 ==; we compare scalar
// fields 和 then walk indexes. dialog list items use 这
// 到 skip gratuitous version bumps 当 SetMatch reapplies same
// match.
func sameFuzzyMatch(a, b fuzzy.Match) bool {
	return a.Str == b.Str &&
		a.Index == b.Index &&
		a.Score == b.Score &&
		slices.Equal(a.MatchedIndexes, b.MatchedIndexes)
}

// ListItem represents selectable 和 searchable item 在 dialog list.
type ListItem interface {
	list.FilterableItem
	list.Focusable
	list.MatchSettable

	// ID returns unique identifier 的 item.
	ID() string
}

// SessionItem wraps [会话.会话] 到 implement [ListItem] interface.
type SessionItem struct {
	*list.Versioned
	session.Session
	t                *styles.Styles
	sessionsMode     sessionsMode
	m                fuzzy.Match
	cache            map[int]string
	updateTitleInput textinput.Model
	focused          bool
	hideInfo         bool
}

var _ ListItem = &SessionItem{}

// Filter returns filterable 值 的 会话.
func (s *SessionItem) Filter() string {
	return s.Title
}

// ID returns unique identifier 的 会话.
func (s *SessionItem) ID() string {
	return s.Session.ID
}

// SetMatch sets fuzzy match 为 会话 item.
func (s *SessionItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(s.m, m) {
		return
	}
	s.cache = nil
	s.m = m
	if s.Versioned != nil {
		s.Bump()
	}
}

// InputValue returns updated 标题 值
func (s *SessionItem) InputValue() string {
	return s.updateTitleInput.Value()
}

// HandleInput forwards input 消息 到 更新 标题 input
func (s *SessionItem) HandleInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	s.updateTitleInput, cmd = s.updateTitleInput.Update(msg)
	if s.Versioned != nil {
		s.Bump()
	}
	return cmd
}

// Cursor returns cursor 的 更新 标题 input
func (s *SessionItem) Cursor() *tea.Cursor {
	return s.updateTitleInput.Cursor()
}

// InfoText returns secondary 文本 shown 在 right 的 item.
func (s *SessionItem) InfoText() string {
	return humanize.Time(time.Unix(s.UpdatedAt, 0))
}

// SetHideInfo controls whether timestamp info column 是 shown.
// dialog hides it uniformly 当 it would crowd 标题.
func (s *SessionItem) SetHideInfo(v bool) {
	if s.hideInfo == v {
		return
	}
	s.cache = nil
	s.hideInfo = v
	if s.Versioned != nil {
		s.Bump()
	}
}

// Render returns string representation 的 会话 item.
func (s *SessionItem) Render(width int) string {
	info := s.InfoText()
	if s.hideInfo {
		info = ""
	}
	styles := ListItemStyles{
		ItemBlurred:     s.t.Dialog.NormalItem,
		ItemFocused:     s.t.Dialog.SelectedItem,
		InfoTextBlurred: s.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: s.t.Dialog.Sessions.InfoFocused,
	}

	switch s.sessionsMode {
	case sessionsModeDeleting:
		styles.ItemBlurred = s.t.Dialog.Sessions.DeletingItemBlurred
		styles.ItemFocused = s.t.Dialog.Sessions.DeletingItemFocused
	case sessionsModeUpdating:
		styles.ItemBlurred = s.t.Dialog.Sessions.RenamingItemBlurred
		styles.ItemFocused = s.t.Dialog.Sessions.RenamingingItemFocused
		if s.focused {
			const cursorPadding = 1
			inputWidth := max(0, width-styles.ItemFocused.GetHorizontalFrameSize()-cursorPadding)
			s.updateTitleInput.SetWidth(inputWidth)
			s.updateTitleInput.Placeholder = ansi.Truncate(s.Title, width, "…")
			return styles.ItemFocused.Render(s.updateTitleInput.View())
		}
	}

	return renderItem(styles, s.Title, info, s.focused, width, s.cache, &s.m)
}

type ListItemStyles struct {
	ItemBlurred     lipgloss.Style
	ItemFocused     lipgloss.Style
	InfoTextBlurred lipgloss.Style
	InfoTextFocused lipgloss.Style
}

func renderItem(t ListItemStyles, title string, info string, focused bool, width int, cache map[int]string, m *fuzzy.Match) string {
	if cache == nil {
		cache = make(map[int]string)
	}

	cached, ok := cache[width]
	if ok {
		return cached
	}

	style := t.ItemBlurred
	if focused {
		style = t.ItemFocused
	}

	// Build content 到 width left after item style's own padding,
	// 所以 final rendered line 是 exactly `width` wide. Otherwise
	// padding pushes line past list width 和 it wraps.
	lineWidth := max(0, width-style.GetHorizontalFrameSize())

	var infoText string
	var infoWidth int
	if len(info) > 0 {
		// Cap info column 所以 long 值 (e.g. 提供方 name) can
		// truncate instead 的 overflowing 行 或 squeezing 标题
		// 到 nothing; 标题 keeps 在 least half width.
		if maxInfo := lineWidth / 2; lipgloss.Width(info)+2 > maxInfo {
			info = ansi.Truncate(info, max(0, maxInfo-2), "…")
		}
		infoText = fmt.Sprintf(" %s ", info)
		if focused {
			infoText = t.InfoTextFocused.Render(infoText)
		} else {
			infoText = t.InfoTextBlurred.Render(infoText)
		}

		infoWidth = lipgloss.Width(infoText)
	}

	title = ansi.Truncate(title, max(0, lineWidth-infoWidth), "…")
	titleWidth := lipgloss.Width(title)
	gap := strings.Repeat(" ", max(0, lineWidth-titleWidth-infoWidth))
	content := title
	if m != nil && len(m.MatchedIndexes) > 0 {
		var lastPos int
		parts := make([]string, 0)
		ranges := matchedRanges(m.MatchedIndexes)
		for _, rng := range ranges {
			start, stop := bytePosToVisibleCharPos(title, rng)
			if start > lastPos {
				parts = append(parts, ansi.Cut(title, lastPos, start))
			}
			// NOTE: We're using [ansi.Style] here instead 的 [lipglosStyle]
			// because we can control underline start 和 stop more
			// precisely via [ansi.AttrUnderline] 和 [ansi.AttrNoUnderline]
			// which 只 affect underline attribute without interfering
			// 用 other style attributes.
			parts = append(
				parts,
				ansi.NewStyle().Underline(true).String(),
				ansi.Cut(title, start, stop+1),
				ansi.NewStyle().Underline(false).String(),
			)
			lastPos = stop + 1
		}
		if lastPos < ansi.StringWidth(title) {
			parts = append(parts, ansi.Cut(title, lastPos, ansi.StringWidth(title)))
		}

		content = strings.Join(parts, "")
	}

	content = style.Render(content + gap + infoText)
	cache[width] = content
	return content
}

// SetFocused sets focus state 的 会话 item.
func (s *SessionItem) SetFocused(focused bool) {
	if s.focused == focused {
		return
	}
	s.cache = nil
	s.focused = focused
	if s.Versioned != nil {
		s.Bump()
	}
}

// sessionItems takes slice 的 [会话.会话]s 和 convert them 到 slice
// 的 [ListItem]s.
func sessionItems(t *styles.Styles, mode sessionsMode, sessions ...session.Session) []list.FilterableItem {
	items := make([]list.FilterableItem, len(sessions))
	for i, s := range sessions {
		item := &SessionItem{Versioned: list.NewVersioned(), Session: s, t: t, sessionsMode: mode}
		if mode == sessionsModeUpdating {
			item.updateTitleInput = textinput.New()
			item.updateTitleInput.SetVirtualCursor(false)
			item.updateTitleInput.Prompt = ""
			item.updateTitleInput.SetStyles(sessionRenameInputStyles(t))
			item.updateTitleInput.SetValue(s.Title)
			item.updateTitleInput.Focus()
		}
		items[i] = item
	}
	return items
}

// sessionRenameInputStyles returns styles 的 input 那 renames
// 会话.
func sessionRenameInputStyles(t *styles.Styles) textinput.Styles {
	inputStyle := t.TextInput
	inputStyle.Focused.Placeholder = t.Dialog.Sessions.RenamingPlaceholder
	return inputStyle
}

func matchedRanges(in []int) [][2]int {
	if len(in) == 0 {
		return [][2]int{}
	}
	current := [2]int{in[0], in[0]}
	if len(in) == 1 {
		return [][2]int{current}
	}
	var out [][2]int
	for i := 1; i < len(in); i++ {
		if in[i] == current[1]+1 {
			current[1] = in[i]
		} else {
			out = append(out, current)
			current = [2]int{in[i], in[i]}
		}
	}
	out = append(out, current)
	return out
}

func bytePosToVisibleCharPos(str string, rng [2]int) (int, int) {
	bytePos, byteStart, byteStop := 0, rng[0], rng[1]
	pos, start, stop := 0, 0, 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start = pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	stop = pos
	return start, stop
}
