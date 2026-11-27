package common

import (
	"cmp"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/x/nerdfont"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PrettyPath formats 文件 路径 用 home 目录 shortening 和 applies
// muted styling.
func PrettyPath(t *styles.Styles, path string, width int) string {
	formatted := home.Short(path)
	return t.Sidebar.WorkingDir.Width(width).Render(formatted)
}

// PrettyPathWithBranch formats 文件 路径 用 optional git branch prefix.
// When branch 是 present, it 是 displayed 在 它的 own line above 路径.
func PrettyPathWithBranch(t *styles.Styles, path, branch string, width int) string {
	formatted := home.Short(path)
	if branch != "" {
		branchLine := t.Sidebar.WorkingDir.Width(width).Render(branch)
		pathLine := t.Sidebar.WorkingDir.Width(width).Render(formatted)
		return branchLine + "\n" + pathLine
	}
	return t.Sidebar.WorkingDir.Width(width).Render(formatted)
}

// PrettyPathInline formats 文件 路径 用 optional git branch 在 same
// line, truncating 到 fit given width.
func PrettyPathInline(t *styles.Styles, path, branch string, width int) string {
	formatted := home.Short(path)
	if branch != "" {
		combined := branch + " " + formatted
		formatted = ansi.Truncate(combined, width, "…")
	}
	return t.Sidebar.WorkingDir.Width(width).Render(formatted)
}

// FormatReasoningEffort formats reasoning effort level 为 display.
func FormatReasoningEffort(effort string) string {
	if effort == "xhigh" {
		return "X-High"
	}
	return cases.Title(language.English).String(effort)
}

// ModelContextInfo contains token usage 和 cost information 为 模型.
type ModelContextInfo struct {
	ContextUsed    int64
	ModelContext   int64
	Cost           float64
	EstimatedUsage bool
}

// ModelInfo renders 模型 information including name, 提供方, reasoning
// settings, 和 optional 上下文 usage/cost.
func ModelInfo(t *styles.Styles, modelName, providerName, reasoningInfo string, context *ModelContextInfo, width int, hyperCredits *int) string {
	modelIcon := t.ModelInfo.Icon.Render(styles.ModelIcon)
	modelName = t.ModelInfo.Name.Render(modelName)

	// Build first line 用 模型 name 和 optionally 提供方 在 same line
	var firstLine string
	if providerName != "" {
		providerInfo := t.ModelInfo.Provider.Render(fmt.Sprintf("via %s", providerName))
		modelWithProvider := fmt.Sprintf("%s %s %s", modelIcon, modelName, providerInfo)

		// Check 如果 it fits 在 one line
		if lipgloss.Width(modelWithProvider) <= width {
			firstLine = modelWithProvider
		} else {
			// If it doesn't fit, put 提供方 在 next line
			firstLine = fmt.Sprintf("%s %s", modelIcon, modelName)
		}
	} else {
		firstLine = fmt.Sprintf("%s %s", modelIcon, modelName)
	}

	parts := []string{firstLine}

	// If 提供方 didn't fit 在 first line, add it 作为 second line
	if providerName != "" && !strings.Contains(firstLine, "via") {
		providerInfo := fmt.Sprintf("via %s", providerName)
		parts = append(parts, t.ModelInfo.ProviderFallback.Render(providerInfo))
	}

	if reasoningInfo != "" {
		parts = append(parts, t.ModelInfo.Reasoning.Render(reasoningInfo))
	}

	if context != nil {
		formattedInfo := formatTokensAndCost(t, context.ContextUsed, context.ModelContext, context.Cost, context.EstimatedUsage)
		parts = append(parts, lipgloss.NewStyle().PaddingLeft(2).Render(formattedInfo))
	}

	if providerName == hyper.DisplayName && hyperCredits != nil {
		hcInfo := t.ModelInfo.HypercreditIcon.Render(styles.HypercreditIcon)
		hcInfo += " "
		hcInfo += t.ModelInfo.HypercreditText.Render(fmt.Sprintf("%s Hypercredits", FormatCredits(*hyperCredits)))
		parts = append(parts, "", hcInfo)
	}

	return lipgloss.NewStyle().Width(width).Render(
		lipgloss.JoinVertical(lipgloss.Left, parts...),
	)
}

// formatTokensAndCost formats token usage 和 cost 用 appropriate units
// (K/M) 和 percentage 的 上下文 窗口.
func formatTokensAndCost(t *styles.Styles, tokens, contextWindow int64, cost float64, estimated bool) string {
	var formattedTokens string
	switch {
	case tokens >= 1_000_000:
		formattedTokens = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		formattedTokens = fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		formattedTokens = fmt.Sprintf("%d", tokens)
	}

	if strings.HasSuffix(formattedTokens, ".0K") {
		formattedTokens = strings.Replace(formattedTokens, ".0K", "K", 1)
	}
	if strings.HasSuffix(formattedTokens, ".0M") {
		formattedTokens = strings.Replace(formattedTokens, ".0M", "M", 1)
	}

	var percentage float64
	if contextWindow > 0 {
		percentage = (float64(tokens) / float64(contextWindow)) * 100
	}

	formattedCost := t.ModelInfo.Cost.Render(fmt.Sprintf("$%.2f", cost))

	formattedTokens = t.ModelInfo.TokenCount.Render(fmt.Sprintf("(%s)", formattedTokens))
	percentageText := fmt.Sprintf("%d%%", int(percentage))
	if estimated {
		percentageText = "~" + percentageText
	}
	formattedPercentage := t.ModelInfo.TokenPercentage.Render(percentageText)
	formattedTokens = fmt.Sprintf("%s %s", formattedPercentage, formattedTokens)
	if percentage > 80 {
		formattedTokens = fmt.Sprintf("%s %s", nerdfont.Glyph(styles.LSPWarningIcon, "W"), formattedTokens)
	}

	return fmt.Sprintf("%s %s", formattedTokens, formattedCost)
}

// FormatCredits formats integer 用 comma separators 为 thousands.
func FormatCredits(n int) string {
	s := strconv.FormatInt(int64(n), 10)
	if n < 1000 {
		return s
	}
	// Calculate how many digits before the first comma.
	firstGroup := len(s) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && i == firstGroup {
			b = append(b, ',')
			firstGroup += 3
		}
		b = append(b, s[i])
	}
	return string(b)
}

// StatusOpts defines options 为 rendering status line 用 icon, 标题,
// description, 和 optional extra content.
type StatusOpts struct {
	Icon             string // if empty no icon will be shown
	Title            string
	TitleColor       color.Color
	Description      string
	DescriptionColor color.Color
	ExtraContent     string // additional content to append after the description
}

// Status renders status line 用 icon, 标题, description, 和 extra
// content. description 是 truncated 如果 it exceeds available width.
func Status(t *styles.Styles, opts StatusOpts, width int) string {
	icon := opts.Icon
	title := opts.Title
	description := opts.Description

	titleColor := cmp.Or(opts.TitleColor, t.Resource.DefaultTitleFg)
	descriptionColor := cmp.Or(opts.DescriptionColor, t.Resource.DefaultDescFg)

	title = t.Resource.RowTitleBase.Foreground(titleColor).Render(title)

	if description != "" {
		extraContentWidth := lipgloss.Width(opts.ExtraContent)
		if extraContentWidth > 0 {
			extraContentWidth += 1
		}
		description = ansi.Truncate(description, width-lipgloss.Width(icon)-lipgloss.Width(title)-2-extraContentWidth, "…")
		description = t.Resource.RowDescBase.Foreground(descriptionColor).Render(description)
	}

	var content []string
	if icon != "" {
		content = append(content, icon)
	}
	content = append(content, title)
	if description != "" {
		content = append(content, description)
	}
	if opts.ExtraContent != "" {
		content = append(content, opts.ExtraContent)
	}

	return strings.Join(content, " ")
}

// Section renders section header 用 标题 和 horizontal line filling
// the remaining width.
func Section(t *styles.Styles, text string, width int, info ...string) string {
	char := styles.SectionSeparator
	length := lipgloss.Width(text) + 1
	remainingWidth := width - length

	var infoText string
	if len(info) > 0 {
		infoText = strings.Join(info, " ")
		if len(infoText) > 0 {
			infoText = " " + infoText
			remainingWidth -= lipgloss.Width(infoText)
		}
	}

	text = t.Section.Title.Render(text)
	if remainingWidth > 0 {
		text = text + " " + t.Section.Line.Render(strings.Repeat(char, remainingWidth)) + infoText
	}
	return text
}

// DialogTitle renders dialog 标题 用 decorative line filling
// remaining width. When 标题 alone exceeds available width it 是
// truncated 用 ellipsis 所以 it never wraps.
func DialogTitle(t *styles.Styles, title string, width int, fromColor, toColor color.Color) string {
	if width > 0 && lipgloss.Width(title) > width {
		return ansi.Truncate(title, width, "…")
	}
	char := "╱"
	length := lipgloss.Width(title) + 1
	remainingWidth := width - length
	if remainingWidth > 0 {
		lines := strings.Repeat(char, remainingWidth)
		lines = styles.ApplyForegroundGrad(t.Dialog.TitleLineBase, lines, fromColor, toColor)
		title = title + " " + lines
	}
	return title
}
