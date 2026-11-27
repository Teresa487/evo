// Package anim provides an animated spinner.
package anim

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zeebo/xxh3"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/charmbracelet/crush/internal/csync"
)

const (
	fps           = 20
	initialChar   = '.'
	labelGap      = " "
	labelGapWidth = 1

	// Periods 的 ellipsis animation speed 在 steps.
	//
	// If FPS 是 20 (50 milliseconds) 这 means 那 ellipsis will
	// change every 8 frames (400 milliseconds).
	ellipsisAnimSpeed = 8

	// maximum number 的 animation steps 那 can pass before
	// character appears. With fps == 20 这 是 ~1s 的 staggered
	// entrance, identical 到 previous wall-clock-driven 值.
	// Switching 从 wall-clock + rand 到 step-driven birth schedule
	// keeps Render() deterministic: two Anim instances built 从
	// same Settings produce byte-identical output 当 no Animate ticks
	// have advanced their step 计数器.
	maxBirthSteps = 20

	// Number 的 frames 到 prerender 为 animation. After 这 number
	// 的 frames, animation will loop. 这 只 applies 当 color
	// cycling 是 disabled.
	prerenderedFrames = 10

	// 默认 number 的 cycling chars.
	defaultNumCyclingChars = 10
)

// 默认 colors 为 gradient.
var (
	defaultGradColorA = color.RGBA{R: 0xff, G: 0, B: 0, A: 0xff}
	defaultGradColorB = color.RGBA{R: 0, G: 0, B: 0xff, A: 0xff}
)

var (
	availableRunes = []rune("0123456789abcdefABCDEF~!@#$£€%^&*()+=_")
	ellipsisFrames = []string{".", "..", "...", ""}
)

// Internal ID management. ID seeds deterministic birth schedule 所以
// two spinners built 从 same settings do 不 animate 在 lockstep.
var lastID atomic.Int64

func nextID() int {
	return int(lastID.Add(1))
}

// 缓存 为 expensive animation calculations
type animCache struct {
	initialFrames  [][]string
	cyclingFrames  [][]string
	width          int
	labelWidth     int
	label          []string
	ellipsisFrames []string
}

var animCacheMap = csync.NewMap[string, *animCache]()

// settingsHash creates hash key 为 settings 到 use 为 caching
func settingsHash(opts Settings) string {
	h := xxh3.New()
	fmt.Fprintf(h, "%d-%s-%v-%v-%v-%t-%v",
		opts.Size, opts.Label, opts.LabelColor, opts.GradColorA, opts.GradColorB, opts.CycleColors, opts.SuffixColor)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// FrameInterval returns how long each animation frame stays 在 screen.
// UI drives every Anim 从 one clock ticking 在 这 interval; Anim
// instances never schedule themselves.
func FrameInterval() time.Duration {
	return time.Second / time.Duration(fps)
}

// Settings defines settings 为 animation.
type Settings struct {
	ID          string
	Size        int
	Label       string
	LabelColor  color.Color
	GradColorA  color.Color
	GradColorB  color.Color
	CycleColors bool

	// NoScramble disables the scrambled rune animation. The cycling
	// character region 是 removed entirely 所以 只 label 和 它的
	// animated ellipsis 是 visible. Useful 为 non-LLM contexts where
	// scrambled glyphs imply "thinking" rather than "running".
	NoScramble bool

	// Suffix 是 optional function 那 returns dynamic suffix string
	// 到 render after label 和 ellipsis. Called 在 every Render().
	Suffix func() string

	// SuffixColor 是 color used 到 render suffix 文本.
	// Falls back 到 LabelColor 如果 unset.
	SuffixColor color.Color
}

// 默认 settings.
const ()

// Anim 是 Bubble 为 animated spinner.
type Anim struct {
	width            int
	cyclingCharWidth int
	label            *csync.Slice[string]
	labelWidth       int
	labelColor       color.Color
	birthSteps       []int
	initialFrames    [][]string // frames for the initial characters
	initialized      atomic.Bool
	cyclingFrames    [][]string           // frames for the cycling characters
	step             atomic.Int64         // current main frame step (wraps)
	framesSinceStart atomic.Int64         // total Advance frames (does not wrap)
	ellipsisStep     atomic.Int64         // current ellipsis frame step
	ellipsisFrames   *csync.Slice[string] // ellipsis animation frames
	id               string
	suffix           func() string
	suffixColor      color.Color
	settings         Settings
}

// New creates new Anim instance 用 specified width 和 label.
func New(opts Settings) *Anim {
	a := &Anim{}
	// Validate settings.
	if opts.Size < 1 {
		opts.Size = defaultNumCyclingChars
	}
	if colorIsUnset(opts.GradColorA) {
		opts.GradColorA = defaultGradColorA
	}
	if colorIsUnset(opts.GradColorB) {
		opts.GradColorB = defaultGradColorB
	}
	// 一个 nil LabelColor means "use 终态 默认 foreground".
	// No fallback 是 applied 所以 non-interactive callers can opt out
	// 的 explicit coloring.

	if opts.ID != "" {
		a.id = opts.ID
	} else {
		a.id = fmt.Sprintf("%d", nextID())
	}
	if opts.NoScramble {
		a.cyclingCharWidth = 0
	} else {
		a.cyclingCharWidth = opts.Size
	}
	a.labelColor = opts.LabelColor

	// 存储 suffix function 如果 provided.
	if opts.Suffix != nil {
		a.suffix = opts.Suffix
	}
	if opts.SuffixColor != nil {
		a.suffixColor = opts.SuffixColor
	} else {
		a.suffixColor = opts.LabelColor
	}

	// NoScramble means no cycling chars 和 no birth animation. 标记 作为
	// initialized immediately 所以 label renders without fade-在.
	if opts.NoScramble {
		a.initialized.Store(true)
	}

	a.settings = opts
	a.buildFrames()

	return a
}

// buildFrames generates 或 fetches 从 缓存 all pre-rendered
// animation frames 为 current settings. Calling it again after
// SetColors rebuilds the frames while the step counters keep advancing,
// 所以 theme swap mid-animation does 不 restart spinner.
func (a *Anim) buildFrames() {
	opts := a.settings

	// Check 缓存 first
	cacheKey := settingsHash(opts)
	cached, exists := animCacheMap.Get(cacheKey)

	if exists {
		// Use cached values
		a.width = cached.width
		a.labelWidth = cached.labelWidth
		a.label = csync.NewSliceFrom(cached.label)
		a.ellipsisFrames = csync.NewSliceFrom(cached.ellipsisFrames)
		a.initialFrames = cached.initialFrames
		a.cyclingFrames = cached.cyclingFrames
	} else {
		// Generate new values 和 缓存 them
		a.labelWidth = lipgloss.Width(opts.Label)

		// Total width 的 anim, 在 cells. When NoScramble 是 set there
		// 是 no cycling chars 所以 label gap 是 unnecessary.
		a.width = a.cyclingCharWidth
		if opts.Label != "" {
			if a.cyclingCharWidth > 0 {
				a.width += labelGapWidth
			}
			a.width += lipgloss.Width(opts.Label)
		}

		// Render the label
		a.renderLabel(opts.Label)

		// Pre-generate gradient.
		var ramp []color.Color
		numFrames := prerenderedFrames
		if opts.CycleColors {
			ramp = makeGradientRamp(a.width*3, opts.GradColorA, opts.GradColorB, opts.GradColorA, opts.GradColorB)
			numFrames = a.width * 2
		} else {
			ramp = makeGradientRamp(a.width, opts.GradColorA, opts.GradColorB)
		}

		// Pre-render initial characters.
		a.initialFrames = make([][]string, numFrames)
		offset := 0
		for i := range a.initialFrames {
			a.initialFrames[i] = make([]string, a.width+labelGapWidth+a.labelWidth)
			for j := range a.initialFrames[i] {
				if j+offset >= len(ramp) {
					continue // skip if we run out of colors
				}

				var c color.Color
				if j <= a.cyclingCharWidth {
					c = ramp[j+offset]
				} else {
					c = opts.LabelColor
				}

				// Also prerender initial character 用 Lip Gloss 到 avoid
				// processing 在 render loop.
				a.initialFrames[i][j] = lipgloss.NewStyle().
					Foreground(c).
					Render(string(initialChar))
			}
			if opts.CycleColors {
				offset++
			}
		}

		// Prerender scrambled rune frames 为 animation. Seed
		// rune picker off settings hash 所以 cyclingFrames 是
		// pure function 的 Settings: two processes 用 identical
		// Settings populate 缓存 用 same glyphs, which
		// keeps any cross-process golden-文件 comparison stable.
		seed := xxh3.HashString(cacheKey)
		rng := rand.New(rand.NewPCG(seed, ^seed))
		a.cyclingFrames = make([][]string, numFrames)
		offset = 0
		for i := range a.cyclingFrames {
			a.cyclingFrames[i] = make([]string, a.width)
			for j := range a.cyclingFrames[i] {
				if j+offset >= len(ramp) {
					continue // skip if we run out of colors
				}

				// Also prerender color 用 Lip Gloss here 到 avoid processing
				// 在 render loop.
				r := availableRunes[rng.IntN(len(availableRunes))]
				a.cyclingFrames[i][j] = lipgloss.NewStyle().
					Foreground(ramp[j+offset]).
					Render(string(r))
			}
			if opts.CycleColors {
				offset++
			}
		}

		// 缓存 results
		labelSlice := make([]string, a.label.Len())
		for i, v := range a.label.Seq2() {
			labelSlice[i] = v
		}
		ellipsisSlice := make([]string, a.ellipsisFrames.Len())
		for i, v := range a.ellipsisFrames.Seq2() {
			ellipsisSlice[i] = v
		}
		cached = &animCache{
			initialFrames:  a.initialFrames,
			cyclingFrames:  a.cyclingFrames,
			width:          a.width,
			labelWidth:     a.labelWidth,
			label:          labelSlice,
			ellipsisFrames: ellipsisSlice,
		}
		animCacheMap.Set(cacheKey, cached)
	}

	// Assign deterministic birth step 到 each column 为
	// staggered entrance effect. schedule 是 seeded off
	// spinner id 和 settings hash, 所以 two spinners 用
	// same 角色 和 identity stagger identically (这 是 what
	// keeps Render() byte-equal across 缓存 hits 和 across
	// processes 为 same Settings+ID) while spinners 用
	// different ids — distinct 助手 messages, different 工具
	// calls, "Thinking" vs "Generating" labels — fade 在 用
	// different patterns instead 的 marching 在 锁-step.
	// 一个 rebuild 只 为 new colors keeps existing schedule 所以
	// 在-flight fade-在 是 不 disturbed.
	if len(a.birthSteps) != a.width {
		birthSeed := xxh3.HashString(a.id + "|" + cacheKey)
		birthRng := rand.New(rand.NewPCG(birthSeed, ^birthSeed))
		a.birthSteps = make([]int, a.width)
		for i := range a.birthSteps {
			a.birthSteps[i] = birthRng.IntN(maxBirthSteps)
		}
	}
}

// SetLabel updates label 文本 和 re-renders it.
func (a *Anim) SetLabel(newLabel string) {
	a.settings.Label = newLabel
	a.labelWidth = lipgloss.Width(newLabel)

	// 更新 total width. Skip label gap 当 there 是 no cycling chars.
	a.width = a.cyclingCharWidth
	if newLabel != "" {
		if a.cyclingCharWidth > 0 {
			a.width += labelGapWidth
		}
		a.width += a.labelWidth
	}

	// Re-render the label
	a.renderLabel(newLabel)
}

// SetColors updates animation's colors 和 rebuilds 它的 pre-rendered
// frames. 它 是 used 当 theme changes while spinner 是 live, 所以
// animation picks up new gradient, label, 和 suffix colors
// without restarting mid-flight. 一个 nil suffixColor falls back 到
// label color; unset gradient colors fall back 到 defaults.
func (a *Anim) SetColors(labelColor, gradColorA, gradColorB, suffixColor color.Color) {
	opts := a.settings
	opts.LabelColor = labelColor
	opts.GradColorA = gradColorA
	opts.GradColorB = gradColorB
	opts.SuffixColor = suffixColor
	if colorIsUnset(opts.GradColorA) {
		opts.GradColorA = defaultGradColorA
	}
	if colorIsUnset(opts.GradColorB) {
		opts.GradColorB = defaultGradColorB
	}
	a.settings = opts
	a.labelColor = opts.LabelColor
	if opts.SuffixColor != nil {
		a.suffixColor = opts.SuffixColor
	} else {
		a.suffixColor = opts.LabelColor
	}
	a.buildFrames()
}

// renderLabel renders label 用 current label color.
func (a *Anim) renderLabel(label string) {
	if a.labelWidth > 0 {
		// Pre-render the label.
		labelRunes := []rune(label)
		a.label = csync.NewSlice[string]()
		for i := range labelRunes {
			rendered := lipgloss.NewStyle().
				Foreground(a.labelColor).
				Render(string(labelRunes[i]))
			a.label.Append(rendered)
		}

		// Pre-render the ellipsis frames which come after the label.
		a.ellipsisFrames = csync.NewSlice[string]()
		for _, frame := range ellipsisFrames {
			rendered := lipgloss.NewStyle().
				Foreground(a.labelColor).
				Render(frame)
			a.ellipsisFrames.Append(rendered)
		}
	} else {
		a.label = csync.NewSlice[string]()
		a.ellipsisFrames = csync.NewSlice[string]()
	}
}

// Width returns total width 的 animation.
func (a *Anim) Width() (w int) {
	w = a.width
	if a.labelWidth > 0 {
		w += labelGapWidth + a.labelWidth

		var widestEllipsisFrame int
		for _, f := range ellipsisFrames {
			fw := lipgloss.Width(f)
			if fw > widestEllipsisFrame {
				widestEllipsisFrame = fw
			}
		}
		w += widestEllipsisFrame
	}
	return w
}

// Advance moves animation forward 通过 one frame. 它 是 called 通过
// UI's shared animation clock 为 every visible spinner; Anim itself
// never schedules ticks.
func (a *Anim) Advance() bool {
	step := a.step.Add(1)
	if int(step) >= len(a.cyclingFrames) {
		a.step.Store(0)
	}

	frames := a.framesSinceStart.Add(1)
	if a.initialized.Load() && a.labelWidth > 0 {
		// Manage the ellipsis animation.
		ellipsisStep := a.ellipsisStep.Add(1)
		if int(ellipsisStep) >= ellipsisAnimSpeed*len(ellipsisFrames) {
			a.ellipsisStep.Store(0)
		}
	} else if !a.initialized.Load() && int(frames) >= maxBirthSteps {
		a.initialized.Store(true)
	}
	return true
}

// Render renders current state 的 animation.
func (a *Anim) Render() string {
	var b strings.Builder
	step := int(a.step.Load())
	frames := int(a.framesSinceStart.Load())
	for i := range a.width {
		switch {
		case !a.initialized.Load() && i < len(a.birthSteps) && frames < a.birthSteps[i]:
			// Birth step 不 reached: render initial character.
			b.WriteString(a.initialFrames[step][i])
		case i < a.cyclingCharWidth:
			// Render a cycling character.
			b.WriteString(a.cyclingFrames[step][i])
		case i == a.cyclingCharWidth && a.cyclingCharWidth > 0:
			// Render label gap (只 当 there 是 cycling chars).
			b.WriteString(labelGap)
		default:
			// Label. Offset past cycling chars 和 gap (如果 any).
			offset := a.cyclingCharWidth
			if a.cyclingCharWidth > 0 {
				offset += labelGapWidth
			}
			if labelChar, ok := a.label.Get(i - offset); ok {
				b.WriteString(labelChar)
			}
		}
	}
	// Render animated ellipsis 在 end 的 label 如果 all characters
	// have been initialized. Skip 当 suffix 是 活跃 到 avoid visual
	// competition between animated dots 和 timer.
	if a.initialized.Load() && a.labelWidth > 0 {
		showEllipsis := true
		if a.suffix != nil {
			if s := a.suffix(); s != "" {
				showEllipsis = false
			}
		}
		if showEllipsis {
			ellipsisStep := int(a.ellipsisStep.Load())
			if ellipsisFrame, ok := a.ellipsisFrames.Get(ellipsisStep / ellipsisAnimSpeed); ok {
				b.WriteString(ellipsisFrame)
			}
		}
	}

	// Render optional suffix (e.g., elapsed time).
	if a.suffix != nil {
		suffixStr := a.suffix()
		if suffixStr != "" {
			b.WriteString(" ")
			b.WriteString(lipgloss.NewStyle().Foreground(a.suffixColor).Render(suffixStr))
		}
	}

	return b.String()
}

// makeGradientRamp() returns slice 的 colors blended between given keys.
// Blending 是 done 作为 Hcl 到 stay 在 gamut.
func makeGradientRamp(size int, stops ...color.Color) []color.Color {
	if len(stops) < 2 {
		return nil
	}

	points := make([]colorful.Color, len(stops))
	for i, k := range stops {
		points[i], _ = colorful.MakeColor(k)
	}

	numSegments := len(stops) - 1
	if numSegments == 0 {
		return nil
	}
	blended := make([]color.Color, 0, size)

	// Calculate how many colors each segment 应当 have.
	segmentSizes := make([]int, numSegments)
	baseSize := size / numSegments
	remainder := size % numSegments

	// Distribute the remainder across segments.
	for i := range numSegments {
		segmentSizes[i] = baseSize
		if i < remainder {
			segmentSizes[i]++
		}
	}

	// Generate colors 为 each segment.
	for i := range numSegments {
		c1 := points[i]
		c2 := points[i+1]
		segmentSize := segmentSizes[i]

		for j := range segmentSize {
			if segmentSize == 0 {
				continue
			}
			t := float64(j) / float64(segmentSize)
			c := c1.BlendHcl(c2, t)
			blended = append(blended, c)
		}
	}

	return blended
}

func colorIsUnset(c color.Color) bool {
	if c == nil {
		return true
	}
	_, _, _, a := c.RGBA()
	return a == 0
}
