// auth_method.go —— 选择 provider 认证方式（API key / OAuth）。

package dialog

import (
	"cmp"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// AuthMethodID 是 identifier 为 auth method selection dialog.
const AuthMethodID = "auth_method"

const (
	defaultAuthMethodDialogMaxWidth = 72
	authMethodCardGap               = 1
	// authMethodCardMargin 是 blank space between dialog frame 和
	// pair 的 cards 在 each side.
	authMethodCardMargin = 1
	// authMethodCardHeight 是 total card height, border included.
	// odd content height lets the one-line "API Key" label center exactly;
	// two-line OAuth label lands within half 行 的 center.
	authMethodCardHeight = 13
	// authMethodMinCardWidth 是 smallest card width 那 keeps
	// two-line OAuth label legible; below it the cards stack vertically.
	authMethodMinCardWidth = 20
	// authMethodDoubleClickThreshold bounds delay between two clicks 在
	// card 为 second one 到 count 作为 confirmation.
	authMethodDoubleClickThreshold = 400 * time.Millisecond
)

// AuthMethod asks how 到 authenticate 提供方 那 supports both
// OAuth 和 API key, before starting either flow.
type AuthMethod struct {
	com          *common.Common
	isOnboarding bool
	provider     catwalk.Provider
	model        config.SelectedModel
	modelType    config.SelectedModelType

	selected       int
	oauthCardArea  image.Rectangle
	apiKeyCardArea image.Rectangle
	lastClickCard  int
	lastClickTime  time.Time
	help           help.Model
	keyMap         struct {
		Choose key.Binding
		Select key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*AuthMethod)(nil)

// NewAuthMethod creates dialog 那 chooses between OAuth 和 API key
// authentication 为 given 提供方.
func NewAuthMethod(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
) *AuthMethod {
	m := &AuthMethod{
		com:           com,
		isOnboarding:  isOnboarding,
		provider:      provider,
		model:         model,
		modelType:     modelType,
		lastClickCard: -1,
	}

	m.help = help.New()
	m.help.Styles = com.Styles.DialogHelpStyles()

	// Vim-style h/l work too, 但 stay out 的 help bar.
	m.keyMap.Choose = key.NewBinding(
		key.WithKeys("left", "right", "up", "down", "tab", "shift+tab", "h", "l"),
		key.WithHelp("←/→", "choose"),
	)
	m.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "accept"),
	)
	m.keyMap.Close = key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
		key.WithHelp("esc", "back"),
	)

	return m
}

// ID implements Dialog.
func (m *AuthMethod) ID() string {
	return AuthMethodID
}

// HandleMsg implements Dialog.
func (m *AuthMethod) HandleMsg(msg tea.Msg) Action {
	if click, ok := msg.(tea.MouseClickMsg); ok {
		return m.handleMouseClick(click)
	}

	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}

	switch {
	case key.Matches(keyMsg, m.keyMap.Close):
		return ActionClose{}
	case key.Matches(keyMsg, m.keyMap.Choose):
		m.selected = 1 - m.selected
		return nil
	case key.Matches(keyMsg, m.keyMap.Select):
		return ActionSelectAuthMethod{
			Provider:  m.provider,
			Model:     m.model,
			ModelType: m.modelType,
			UseOAuth:  m.selected == 0,
		}
	}
	return nil
}

// handleMouseClick focuses clicked card; second click 在 same
// card within the double-click threshold confirms the choice.
func (m *AuthMethod) handleMouseClick(msg tea.MouseClickMsg) Action {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	point := image.Pt(msg.X, msg.Y)
	card := -1
	switch {
	case point.In(m.oauthCardArea):
		card = 0
	case point.In(m.apiKeyCardArea):
		card = 1
	}
	if card < 0 {
		m.lastClickCard = -1
		return nil
	}
	now := time.Now()
	confirm := m.lastClickCard == card && now.Sub(m.lastClickTime) <= authMethodDoubleClickThreshold
	m.lastClickCard = card
	m.lastClickTime = now
	m.selected = card
	if !confirm {
		return nil
	}
	return ActionSelectAuthMethod{
		Provider:  m.provider,
		Model:     m.model,
		ModelType: m.modelType,
		UseOAuth:  m.selected == 0,
	}
}

// Draw implements Dialog.
func (m *AuthMethod) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	width := max(0, min(defaultAuthMethodDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	rc := NewRenderContext(t, width)
	rc.Title = "Let’s Auth " + cmp.Or(m.provider.Name, string(m.provider.ID))

	prompt := t.Dialog.AuthMethod.Prompt.Width(innerWidth).Render("How would you like to authenticate?")
	if !m.isOnboarding {
		// Keep single blank line under 标题.
		prompt = "\n" + prompt
	}
	prompt += "\n"
	rc.AddPart(prompt)

	cardWidth := max(0, (innerWidth-authMethodCardGap-2*authMethodCardMargin)/2)
	sideBySide := cardWidth >= authMethodMinCardWidth
	if !sideBySide {
		cardWidth = innerWidth
	}
	// OAuth card names account sign-在 uses, which 是
	// 提供方-specific: ChatGPT 为 OpenAI, Grok 为 xAI.
	oauthCardLabel := "ChatGPT Account\nwith Subscription"
	if m.provider.ID == catwalk.InferenceProviderXAI {
		oauthCardLabel = "Grok Account\nwith Subscription"
	}
	oauthCard := m.renderCard(oauthCardLabel, m.selected == 0, cardWidth)
	apiKeyCard := m.renderCard("API Key", m.selected == 1, cardWidth)

	var cards string
	if sideBySide {
		row := lipgloss.JoinHorizontal(lipgloss.Top, oauthCard, strings.Repeat(" ", authMethodCardGap), apiKeyCard)
		cards = lipgloss.PlaceHorizontal(innerWidth, lipgloss.Center, row)
	} else {
		cards = lipgloss.JoinVertical(lipgloss.Left, oauthCard, "", apiKeyCard)
	}
	rc.AddPart(cards)

	rc.Help = "\n" + renderDialogHelp(t, &m.help, m, innerWidth)

	view := rc.Render()
	if m.isOnboarding {
		rc.Title = ""
		rc.IsOnboarding = true
		view = rc.Render()
	}
	m.updateCardAreas(area, view, sideBySide, cardWidth, innerWidth)
	if m.isOnboarding {
		DrawOnboardingCursor(scr, area, view, nil)
	} else {
		DrawCenter(scr, area, view)
	}
	return nil
}

// updateCardAreas records where each card lands 在 screen 所以 mouse clicks
// can 是 mapped back 到 choice.
func (m *AuthMethod) updateCardAreas(area uv.Rectangle, view string, sideBySide bool, cardWidth, innerWidth int) {
	m.oauthCardArea = image.Rectangle{}
	m.apiKeyCardArea = image.Rectangle{}

	viewWidth, viewHeight := lipgloss.Size(view)
	viewWidth = min(viewWidth, area.Dx())
	viewHeight = min(viewHeight, area.Dy())
	var frame lipgloss.Style
	var origin image.Point
	if m.isOnboarding {
		origin = common.BottomLeftRect(area, viewWidth, viewHeight).Min
	} else {
		frame = m.com.Styles.Dialog.View
		origin = common.CenterRect(area, viewWidth, viewHeight).Min
	}
	contentLeft := origin.X + frame.GetMarginLeft() + frame.GetBorderLeftSize() + frame.GetPaddingLeft()
	contentTop := origin.Y + frame.GetMarginTop() + frame.GetBorderTopSize() + frame.GetPaddingTop()

	// Lines above cards: 提示词, plus 标题 和 它的 blank line
	// outside onboarding.
	cardsTop := contentTop + 1
	if !m.isOnboarding {
		cardsTop += 2
	}

	groupWidth := cardWidth
	if sideBySide {
		groupWidth = 2*cardWidth + authMethodCardGap
	}
	cardsLeft := contentLeft + max(0, (innerWidth-groupWidth)/2)

	m.oauthCardArea = image.Rect(cardsLeft, cardsTop, cardsLeft+cardWidth, cardsTop+authMethodCardHeight)
	if sideBySide {
		left := cardsLeft + cardWidth + authMethodCardGap
		m.apiKeyCardArea = image.Rect(left, cardsTop, left+cardWidth, cardsTop+authMethodCardHeight)
	} else {
		top := cardsTop + authMethodCardHeight + 1
		m.apiKeyCardArea = image.Rect(cardsLeft, top, cardsLeft+cardWidth, top+authMethodCardHeight)
	}
}

// renderCard renders one auth option 作为 bordered card 用 它的 label
// centered both ways. The selected card gets the focused frame.
func (m *AuthMethod) renderCard(label string, focused bool, width int) string {
	t := m.com.Styles
	style := t.Dialog.AuthMethod.CardBlurred
	if focused {
		style = t.Dialog.AuthMethod.CardFocused
	}
	return style.
		Width(width).
		Height(authMethodCardHeight).
		Align(lipgloss.Center, lipgloss.Center).
		Render(label)
}

// FullHelp implements help.KeyMap.
func (m *AuthMethod) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp implements help.KeyMap.
func (m *AuthMethod) ShortHelp() []key.Binding {
	return []key.Binding{
		m.keyMap.Choose,
		m.keyMap.Select,
		m.keyMap.Close,
	}
}
