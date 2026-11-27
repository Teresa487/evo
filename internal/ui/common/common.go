// common.go —— UI 各面板共享的上下文对象 Common，持有 Workspace 与样式。
//
// 前端对后端的所有访问都通过 Common.Workspace（workspace.Workspace 接口）：
// 本地模式是 AppWorkspace，远程模式是 ClientWorkspace。
// 本文件还提供 plan marker、图片/音频路径判断等 UI 公共工具。
package common

import (
	"errors"
	"fmt"
	"image"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/clipboard"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
)

// MaxAttachmentSize defines maximum allowed size 为 文件 attachments (5 MB).
const MaxAttachmentSize = int64(5 * 1024 * 1024)

// PlanStartMarker 是 sentinel plan agent emits 在 它的 own line 在
// very start 的 它的 final 响应, 所以 UI can tell plan document
// apart 从 agent's intermediate exploratory replies while it streams.
const PlanStartMarker = "<!-- CRUSH_PLAN_START -->"

// PlanReadyMarker 是 sentinel plan agent emits 在 它的 own line 在
// end 的 它的 final 响应 到 signal 那 plan 是 ready 为 execution.
const PlanReadyMarker = "<!-- CRUSH_PLAN_READY -->"

// PlanStartMarkerPresent reports whether 文本 contains plan-start
// sentinel 在 line 通过 itself. See [PlanReadyMarkerPresent] 为 why
// check 是 line-scoped.
func PlanStartMarkerPresent(text string) bool {
	return planMarkerPresent(text, PlanStartMarker)
}

// PlanReadyMarkerPresent reports whether 文本 contains plan-ready sentinel
// 在 line 通过 itself. An own-line check (rather than substring match) avoids
// false positive 当 agent merely mentions marker inside explanatory
// prose, while still tolerating trailing whitespace 或 备注 after it. Inline
// code backticks around marker count 作为 marker line too: some models wrap
// marker despite 提示词 asking 为 plain 文本.
func PlanReadyMarkerPresent(text string) bool {
	return planMarkerPresent(text, PlanReadyMarker)
}

func planMarkerPresent(text, marker string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if planMarkerLine(line, marker) {
			return true
		}
	}
	return false
}

// planMarkerLine reports whether line consists solely 的 given plan
// sentinel, optionally wrapped 在 inline-code backticks 和 whitespace.
func planMarkerLine(line, marker string) bool {
	trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`"))
	return trimmed == marker
}

// StripPlanReadyMarker removes lines 那 consist solely 的 plan-ready
// sentinel (matching detection rule 的 [PlanReadyMarkerPresent]) 所以
// marker never shows up 在 rendered chat output. Mentions 的 marker
// inside prose 是 left untouched. When marker was wrapped 在 它的 own
// code fence — ``` markers 在 lines directly before 和 after —
// fence pair 是 removed 用 it 所以 empty code block 是 不 left behind.
func StripPlanReadyMarker(text string) string {
	return stripPlanMarker(text, PlanReadyMarker)
}

// StripPlanMarkers removes both plan-start 和 plan-ready sentinel lines.
// Use it wherever rendered 文本 could contain either marker, such 作为 plan
// card (which strips trailing ready marker 和 leading start marker)
// 或 interrupted plan whose start marker never got 它的 ready companion.
func StripPlanMarkers(text string) string {
	return stripPlanMarker(stripPlanMarker(text, PlanStartMarker), PlanReadyMarker)
}

func stripPlanMarker(text, marker string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	inFence := false
	for i := 0; i < len(lines); i++ {
		if planMarkerLine(lines[i], marker) {
			// When marker sits 在 block fenced 在 lines directly
			// before 和 after, drop whole block 所以 empty code
			// block 是 不 left behind. inFence guards against eating
			// closing fence 的 real code block above marker.
			if inFence && len(kept) > 0 && isCodeFenceLine(kept[len(kept)-1]) &&
				i+1 < len(lines) && isCodeFenceLine(lines[i+1]) {
				kept = kept[:len(kept)-1]
				i++
				inFence = false
			}
			continue
		}
		if isCodeFenceLine(lines[i]) {
			inFence = !inFence
		}
		kept = append(kept, lines[i])
	}
	return strings.Join(kept, "\n")
}

// isCodeFenceLine reports whether line opens 或 closes fenced code
// block, e.g. ``` 或 ```go.
func isCodeFenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return false
	}
	info := strings.TrimLeft(trimmed[3:], "`")
	return !strings.ContainsAny(info, "` ")
}

// AllowedImageTypes defines permitted 图片 文件 types.
var AllowedImageTypes = []string{".jpg", ".jpeg", ".png"}

// IsImagePath reports whether given 路径 has one 的 allowed 图片
// 文件 extensions.
func IsImagePath(path string) bool {
	lowerPath := strings.ToLower(path)
	return slices.ContainsFunc(AllowedImageTypes, func(ext string) bool {
		return strings.HasSuffix(lowerPath, ext)
	})
}

// AllowedAudioTypes defines permitted 音频 文件 types.
var AllowedAudioTypes = []string{".mp3", ".wav", ".m4a", ".aac", ".ogg", ".flac"}

// IsAudioPath reports whether given 路径 has one 的 allowed 音频
// 文件 extensions.
func IsAudioPath(path string) bool {
	lowerPath := strings.ToLower(path)
	return slices.ContainsFunc(AllowedAudioTypes, func(ext string) bool {
		return strings.HasSuffix(lowerPath, ext)
	})
}

// Common defines common UI options 和 configurations.
type Common struct {
	Workspace workspace.Workspace
	Styles    *styles.Styles
}

// 配置 returns pure-data configuration associated 用 这 [Common] instance.
func (c *Common) Config() *config.Config {
	return c.Workspace.Config()
}

// DefaultCommon returns 默认 common UI configurations using
// theme 从 配置 (或 默认 Charmtone theme 如果 unset).
func DefaultCommon(ws workspace.Workspace) *Common {
	var s styles.Styles
	if ws != nil {
		s = ThemeStylesFromConfig(ws.Config())
	} else {
		s = LoadThemeStyles("")
	}
	return &Common{
		Workspace: ws,
		Styles:    &s,
	}
}

// largeModelProviderID returns 提供方 ID 的 currently selected
// large 模型, 或 empty string 如果 none 是 set 或 工作区 是 nil.
func largeModelProviderID(ws workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	cfg := ws.Config()
	if cfg == nil {
		return ""
	}
	return cfg.Models[config.SelectedModelTypeLarge].Provider
}

// NewCommon returns common UI configurations using the given theme.
func NewCommon(ws workspace.Workspace, themeName string) *Common {
	s := LoadThemeStyles(themeName)
	return &Common{
		Workspace: ws,
		Styles:    &s,
	}
}

// ThemeNameFromConfig extracts theme name 从 配置, returning ""
// (which LoadTheme treats 作为 默认) 当 配置 是 nil 或 unset.
func ThemeNameFromConfig(cfg *config.Config) string {
	if cfg == nil || cfg.Options == nil || cfg.Options.TUI == nil {
		return ""
	}
	return cfg.Options.TUI.ActiveTheme
}

// LoadThemeStyles resolves theme name 到 Styles, falling back 到
// CharmtonePantera 在 错误 或 empty name.
func LoadThemeStyles(name string) styles.Styles {
	return styles.ThemeFromConfig(name)
}

// ThemeStylesFromConfig resolves configured theme 到 Styles.
// active_theme 字段 selects either built-在 theme 或 global 用户 theme
// 文件.
func ThemeStylesFromConfig(cfg *config.Config) styles.Styles {
	return LoadThemeStyles(ThemeNameFromConfig(cfg))
}

// IsHyper reports whether currently selected large 模型 是 provided
// 通过 Hyper.
func (c *Common) IsHyper() bool {
	return largeModelProviderID(c.Workspace) == "hyper"
}

// CenterRect returns new [Rectangle] centered within given area 用
// specified width 和 height.
func CenterRect(area uv.Rectangle, width, height int) uv.Rectangle {
	centerX := area.Min.X + area.Dx()/2
	centerY := area.Min.Y + area.Dy()/2
	minX := centerX - width/2
	minY := centerY - height/2
	maxX := minX + width
	maxY := minY + height
	return image.Rect(minX, minY, maxX, maxY)
}

// BottomLeftRect returns new [Rectangle] positioned 在 bottom-left within given area 用
// specified width 和 height.
func BottomLeftRect(area uv.Rectangle, width, height int) uv.Rectangle {
	minX := area.Min.X
	maxX := minX + width
	maxY := area.Max.Y
	minY := maxY - height
	return image.Rect(minX, minY, maxX, maxY)
}

// IsFileTooBig checks 如果 文件 在 given 路径 exceeds specified size
// limit.
func IsFileTooBig(filePath string, sizeLimit int64) (bool, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return false, fmt.Errorf("error getting file info: %w", err)
	}

	if fileInfo.Size() > sizeLimit {
		return true, nil
	}

	return false, nil
}

// CopyToClipboard copies given 文本 到 clipboard using both OSC 52
// (终态 escape 序号) 和 native clipboard 为 maximum compatibility.
// Returns command 那 reports outcome 到 用户, using given
// 消息 在 success.
func CopyToClipboard(text, successMessage string) tea.Cmd {
	return CopyToClipboardWithCallback(text, successMessage, nil)
}

// CopyToClipboardWithCallback copies 文本 到 clipboard 和 executes callback
// before showing success 消息.
// 这 是 useful 当 you need 到 perform additional actions like clearing UI state.
//
// callback 和 success 消息 只 运行 当 copy 是 believed 到
// have worked, 所以 callers can safely use callback 到 discard copied
// state ( selection, say) without losing it 在 failed copy.
func CopyToClipboardWithCallback(text, successMessage string, callback tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		// native 写入 goes first 和 是 verified before OSC 52 goes out,
		// because 终态 handles OSC 52 通过 writing very same
		// clipboard 在 它的 own schedule. Verifying afterwards means reading
		// back 在 middle 的 somebody else's 写入, which reports good
		// copy 作为 lost.
		err := clipboard.WriteText(text)
		// OSC 52 是 fire 和 forget: 终态 never answers, 所以 platform
		// without native clipboard ( SSH 会话, say) gets benefit 的
		// doubt. Only native clipboard 那 accepted 写入 和 then
		// does 不 hold 文本 是 real failure.
		osc52 := tea.SetClipboard(text)
		if errors.Is(err, clipboard.ErrWriteFailed) {
			return tea.Sequence(osc52, util.ReportWarn("Failed to copy to clipboard"))()
		}
		return tea.Sequence(osc52, callback, util.ReportInfo(successMessage))()
	}
}
