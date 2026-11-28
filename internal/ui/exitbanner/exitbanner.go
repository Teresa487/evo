// Package exitbanner 渲染应用退出后的 banner。
package exitbanner

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
)

// FallbackWidth 是 stdout 不是终端时使用的宽度，
// 保证输出被管道或捕获时 banner 仍能合理换行。
const FallbackWidth = 80

// Render 使用当前主题渲染退出 banner；没有内容可打印时返回空字符串。
// 会话为 nil 或没有标题时会省略继续会话提示；compact 模式下若没有会话，
// 就没有剩余内容可打印。
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
		// 无法识别的值按完整 banner 渲染，而不是什么都不显示。
		style := lipgloss.NewStyle().Padding(1, 3)
		contentWidth := width - style.GetHorizontalFrameSize()

		sections := []string{logoSection(t, contentWidth)}
		if hasSession {
			sections = append(sections, sessionResumeLines(t, sess, contentWidth))
		}
		return style.Render(strings.Join(sections, "\n\n"))
	}
}

// logoSection 返回 ASCII logo。
func logoSection(t *styles.Styles, contentWidth int) string {
	return logo.Render(t.Logo.GradCanvas, version.Version, true, logo.Opts{
		FieldColor:   t.Logo.FieldColor,
		TitleColorA:  t.Logo.TitleColorA,
		TitleColorB:  t.Logo.TitleColorB,
		CharmColor:   t.Logo.CharmColor,
		VersionColor: t.Logo.VersionColor,
		Hyper:        false,
	})
}

// sessionResumeLines 返回退出 banner 中使用的
// "Session <标题>\nContinue evo -s <hash>" 两行。
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
