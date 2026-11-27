package common

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// RemapANSI16 replaces basic ANSI 16-color SGR codes 用 24-bit
// truecolor 从 palette. Programs emit \x1b[31m etc. 和 trust
// 终态 到 pick color; inside Crush's TUI those defaults 是
// often illegible 在 our dark background. Rewriting them 到 explicit
// RGB keeps output readable regardless 的 终态 configuration.
//
// Uses [ansi.DecodeSequence] 为 parsing (same approach 作为
// [colorprofile.Writer]) since there 是 no upstream palette-remap API.
func RemapANSI16(s string, palette [16]color.Color) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}

	var buf strings.Builder
	buf.Grow(len(s))

	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var state byte
	for len(s) > 0 {
		parser.Reset()
		seq, _, n, newState := ansi.DecodeSequence(s, state, parser)

		if ansi.HasCsiPrefix(seq) && parser.Command() == 'm' {
			remapSGR(parser.Params(), palette, &buf)
		} else {
			buf.WriteString(seq)
		}

		s = s[n:]
		state = newState
	}

	return buf.String()
}

// remapSGR rewrites one SGR 序号, replacing 16-color params 用
// truecolor 从 palette. Extended colors (38/48/58 用 ;5;n 或
// ;2;r;g;b sub-params) pass through unchanged. Non-color attributes
// (bold, italic, etc.) 和 默认 color resets (39/49/59) also pass
// through.
func remapSGR(params ansi.Params, palette [16]color.Color, buf *strings.Builder) {
	buf.WriteString("\x1b[")

	first := true
	for i := 0; i < len(params); i++ {
		p := params[i].Param(0)

		if !first {
			buf.WriteByte(';')
		}
		first = false

		switch {
		// Extended color introducers consume subsequent params 作为
		// arguments. Skip them whole 所以 they aren't misread.
		case p == 38 || p == 48 || p == 58:
			buf.WriteString(strconv.Itoa(p))
			if i+1 < len(params) {
				sub := params[i+1].Param(0)
				switch sub {
				case 5: // 256-color: 38;5;n
					buf.WriteByte(';')
					buf.WriteString(strconv.Itoa(sub))
					if i+2 < len(params) {
						buf.WriteByte(';')
						buf.WriteString(strconv.Itoa(params[i+2].Param(0)))
						i += 2
					} else {
						i++
					}
				case 2: // truecolor: 38;2;r;g;b
					buf.WriteByte(';')
					buf.WriteString(strconv.Itoa(sub))
					for j := 2; j <= 4 && i+j < len(params); j++ {
						buf.WriteByte(';')
						buf.WriteString(strconv.Itoa(params[i+j].Param(0)))
					}
					i += min(4, len(params)-i-1)
				default:
					i++
				}
			}

		case p >= 30 && p <= 37:
			writeTruecolor(buf, 38, palette[p-30])
		case p >= 90 && p <= 97:
			writeTruecolor(buf, 38, palette[8+p-90])
		case p >= 40 && p <= 47:
			writeTruecolor(buf, 48, palette[p-40])
		case p >= 100 && p <= 107:
			writeTruecolor(buf, 48, palette[8+p-100])

		default:
			buf.WriteString(strconv.Itoa(p))
		}
	}

	buf.WriteByte('m')
}

// StripCursorControl removes ANSI escape sequences 那 move cursor,
// erase regions 的 screen, 或 change 终态 modes. These sequences
// 是 emitted 通过 programs like git push, cargo build, 和 npm install 到
// animate progress bars 和 status lines. When captured 作为 raw 文本 和
// replayed inside Crush's TUI viewport they corrupt the render state.
//
// Preserved: SGR (color/style) sequences, OSC hyperlinks, printable 文本.
// Stripped: CSI cursor movement (一个-H, f), erase (J, K), scroll (S, T),
// save/restore cursor (s, u), DEC private modes (?h, ?l), 和 ESC
// save/restore cursor sequences (ESC 7, ESC 8). Bare carriage returns
// (\r) 是 also handled 通过 simulating line-overwrite behavior: within
// each line, 文本 after last \r wins, matching what real 终态
// would display.
func StripCursorControl(s string) string {
	if !strings.ContainsRune(s, 0x1b) && !strings.ContainsRune(s, '\r') {
		return s
	}

	var buf strings.Builder
	buf.Grow(len(s))

	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var state byte
	for len(s) > 0 {
		parser.Reset()
		seq, _, n, newState := ansi.DecodeSequence(s, state, parser)

		if ansi.HasCsiPrefix(seq) {
			cmd := parser.Command()
			final := cmd & 0xff
			prefix := (cmd >> 8) & 0xff

			switch final {
			case 'm':
				// SGR: keep (colors/styles).
				buf.WriteString(seq)
			case 'h', 'l':
				// DEC private mode set/reset (?h, ?l): strip.
				// Regular h/l without ? prefix 是 also non-rendering.
				_ = prefix
			case 'A', 'B', 'C', 'D', // cursor up/down/forward/back
				'E', 'F', // cursor next/prev line
				'G',      // cursor horizontal absolute
				'H', 'f', // cursor position
				'J',      // erase display
				'K',      // erase line
				'S', 'T', // scroll up/down
				's', 'u': // save/restore cursor
				// Strip all cursor/screen control.
			default:
				// Unknown CSI: pass through 到 avoid data loss.
				buf.WriteString(seq)
			}
		} else if ansi.HasEscPrefix(seq) && len(seq) == 2 {
			// ESC followed 通过 single byte: check 为 DEC save/restore.
			switch seq[1] {
			case '7', '8':
				// DEC save/restore cursor: strip.
			default:
				buf.WriteString(seq)
			}
		} else {
			buf.WriteString(seq)
		}

		s = s[n:]
		state = newState
	}

	result := buf.String()

	// Handle bare \r 通过 simulating line-overwrite. Split 在 newlines
	// first 所以 we 只 process \r within individual lines.
	if strings.ContainsRune(result, '\r') {
		result = simulateCarriageReturns(result)
	}

	return result
}

// simulateCarriageReturns processes bare \r characters within each line,
// keeping 只 文本 after last \r. 这 matches 终态 behavior
// where \r moves cursor 到 column 0 和 subsequent 文本 overwrites
// what was there before. Progress bars use 这 pattern extensively.
func simulateCarriageReturns(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// writeTruecolor appends "introducer;2;r;g;b" 到 buf. Nil color emits
// bare introducer 所以 终态 默认 applies.
func writeTruecolor(buf *strings.Builder, introducer int, c color.Color) {
	if c == nil {
		buf.WriteString(strconv.Itoa(introducer))
		return
	}
	r, g, b, _ := c.RGBA()
	buf.WriteString(strconv.Itoa(introducer))
	buf.WriteString(";2;")
	buf.WriteString(strconv.Itoa(int(r >> 8)))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(int(g >> 8)))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(int(b >> 8)))
}
