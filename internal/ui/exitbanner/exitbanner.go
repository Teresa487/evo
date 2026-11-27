// Package exitbanner 渲染 what Crush prints after TUI exits.
package exitbanner

import (
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
)

// FallbackWidth 是 used 当 stdout 是 不 终态, 所以 banner still
// wraps 到 something sane 当 output 是 piped 或 captured.
const FallbackWidth = 80

// Render returns exit banner using 活跃 theme, 或 empty string 当
// there 是 nothing 到 print. 一个 nil 或 untitled 会话 means resume hint 是
// omitted, which 为 compact banner leaves nothing 在 all.
func Render(t *styles.Styles, banner config.ExitBanner, sess *session.Session, width int) string {
	if width <= 0 {
		width = FallbackWidth
	}
	hasSession := sess != nil && sess.ID != ""

	switch banner {
	case config.ExitBannerNone:
		return ""

	case config.ExitBannerCompact:
		if !hasSession {
			return ""
		}
		return sessionResumeLines(t, sess, width)

	default:
		// Unrecognized values render the full banner rather than nothing.
		style := lipgloss.NewStyle().Padding(1, 3)
		contentWidth := width - style.GetHorizontalFrameSize()

		sections := []string{logoSection(t, contentWidth)}
		if hasSession {
			sections = append(sections, sessionResumeLines(t, sess, contentWidth))
		}
		return style.Render(strings.Join(sections, "\n\n"))
	}
}

// logoSection returns ASCII art logo followed 通过 parting 消息.
func logoSection(t *styles.Styles, contentWidth int) string {
	crushLogo := logo.Render(t.Logo.GradCanvas, version.Version, true, logo.Opts{
		FieldColor:   t.Logo.FieldColor,
		TitleColorA:  t.Logo.TitleColorA,
		TitleColorB:  t.Logo.TitleColorB,
		CharmColor:   t.Logo.CharmColor,
		VersionColor: t.Logo.VersionColor,
		Hyper:        false,
	})
	// Wrap greeting 和 消息 together: wrapping 只 消息
	// leaves greeting's own width unaccounted 为 和 overflows frame.
	return crushLogo + "\n" +
		t.ExitBanner.Text.Width(contentWidth).Render("Thanks for using Crush! "+randomExitMessage())
}

// sessionResumeLines returns "Session <标题>\nContinue evo -s <hash>"
// pair used 通过 exit banner.
func sessionResumeLines(t *styles.Styles, sess *session.Session, contentWidth int) string {
	title := strings.ReplaceAll(sess.Title, "\n", " ")

	labelWidth := lipgloss.Width("Session  ")
	titleWidth := contentWidth - labelWidth
	if titleWidth > 0 {
		title = ansi.Truncate(title, titleWidth, "…")
	}

	hash := session.HashID(sess.ID)[:7]
	sessionLine := t.ExitBanner.Label.Render("Session  ") + t.ExitBanner.Text.Render(title)
	continueLine := t.ExitBanner.Label.Render("Continue ") + t.ExitBanner.Text.Render("evo -s "+hash)
	return sessionLine + "\n" + continueLine
}

// randomExitMessage returns random exit 消息.
func randomExitMessage() string {
	messages := []string{
		"",
		"See ya later.",
		"You look great.",
		"Have a gorgeous time.",
		"Get some rest.",
		"Come back soon.",
		"You worked handsomely.",
		"Time for a snack.",
		"Who’s hungry?",
		"That was fun.",
		"See you at breakfast?",
		"Time for a nap.",
		"Who wants some spaghetti?",
		"Take care of yourself.",
		"Remember to hydrate.",
		"Time for a swim?",
		"You’re quite glamorous, you know.",
		"Nice work.",
		"You’re a sensation.",
		"Where’s my eyeliner?",
		"It’s tea time.",
	}
	return messages[rand.IntN(len(messages))]
}
