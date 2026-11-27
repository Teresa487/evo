package image

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"io"
	"log/slog"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/disintegration/imaging"
	paintbrush "github.com/jordanella/go-ansi-paintbrush"
)

// TransmittedMsg 是 消息 indicating 那 图片 has been transmitted 到
// 终态.
type TransmittedMsg struct {
	ID string
}

// Encoding represents encoding format 的 图片.
type Encoding byte

// 图片 encodings.
const (
	EncodingBlocks Encoding = iota
	EncodingKitty
)

type imageKey struct {
	id   string
	cols int
	rows int
}

// Hash returns hash 值 为 图片 key.
// 这 uses FNV-32a 为 simplicity 和 speed.
func (k imageKey) Hash() uint32 {
	h := fnv.New32a()
	_, _ = io.WriteString(h, k.ID())
	return h.Sum32()
}

// ID returns unique string representation 的 图片 key.
func (k imageKey) ID() string {
	return fmt.Sprintf("%s-%dx%d", k.id, k.cols, k.rows)
}

// CellSize represents size 的 single 终态 cell 在 pixels.
type CellSize struct {
	Width, Height int
}

type cachedImage struct {
	img        image.Image
	cols, rows int
}

var (
	cachedImages = map[imageKey]cachedImage{}
	cachedMutex  sync.RWMutex
)

// ResetCache clears 图片 缓存, freeing all cached decoded images.
func ResetCache() {
	cachedMutex.Lock()
	clear(cachedImages)
	cachedMutex.Unlock()
}

// fitImage resizes 图片 到 fit within specified dimensions 在
// 终态 cells, maintaining aspect ratio.
func fitImage(id string, img image.Image, cs CellSize, cols, rows int) image.Image {
	if img == nil {
		return nil
	}

	key := imageKey{id: id, cols: cols, rows: rows}

	cachedMutex.RLock()
	cached, ok := cachedImages[key]
	cachedMutex.RUnlock()
	if ok {
		return cached.img
	}

	if cs.Width == 0 || cs.Height == 0 {
		return img
	}

	maxWidth := cols * cs.Width
	maxHeight := rows * cs.Height

	img = imaging.Fit(img, maxWidth, maxHeight, imaging.Lanczos)

	cachedMutex.Lock()
	cachedImages[key] = cachedImage{
		img:  img,
		cols: cols,
		rows: rows,
	}
	cachedMutex.Unlock()

	return img
}

// HasTransmitted checks 如果 图片 用 given ID has already been
// transmitted 到 终态.
func HasTransmitted(id string, cols, rows int) bool {
	key := imageKey{id: id, cols: cols, rows: rows}

	cachedMutex.RLock()
	_, ok := cachedImages[key]
	cachedMutex.RUnlock()
	return ok
}

// Transmit transmits 图片 data 到 终态 如果 needed. 这 是 used 到
// 缓存 图片 在 终态 为 later rendering.
func (e Encoding) Transmit(id string, img image.Image, cs CellSize, cols, rows int, tmux bool) tea.Cmd {
	if img == nil {
		return nil
	}

	key := imageKey{id: id, cols: cols, rows: rows}

	cachedMutex.RLock()
	_, ok := cachedImages[key]
	cachedMutex.RUnlock()
	if ok {
		return nil
	}

	cmd := func() tea.Msg {
		if e != EncodingKitty {
			cachedMutex.Lock()
			cachedImages[key] = cachedImage{
				img:  img,
				cols: cols,
				rows: rows,
			}
			cachedMutex.Unlock()
			return TransmittedMsg{ID: key.ID()}
		}

		var buf bytes.Buffer
		img := fitImage(id, img, cs, cols, rows)
		bounds := img.Bounds()
		imgWidth := bounds.Dx()
		imgHeight := bounds.Dy()
		imgID := int(key.Hash())
		if err := kitty.EncodeGraphics(&buf, img, &kitty.Options{
			ID:               imgID,
			Action:           kitty.TransmitAndPut,
			Transmission:     kitty.Direct,
			Format:           kitty.RGBA,
			ImageWidth:       imgWidth,
			ImageHeight:      imgHeight,
			Columns:          cols,
			Rows:             rows,
			VirtualPlacement: true,
			Quiet:            1,
			Chunk:            true,
			ChunkFormatter: func(chunk string) string {
				if tmux {
					return ansi.TmuxPassthrough(chunk)
				}
				return chunk
			},
		}); err != nil {
			slog.Error("Failed to encode image for kitty graphics", "err", err)
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "failed to encode image",
			}
		}

		return tea.RawMsg{Msg: buf.String()}
	}

	return cmd
}

// Render renders given 图片 within specified dimensions using
// specified encoding.
func (e Encoding) Render(id string, cols, rows int) string {
	key := imageKey{id: id, cols: cols, rows: rows}
	cachedMutex.RLock()
	cached, ok := cachedImages[key]
	cachedMutex.RUnlock()
	if !ok {
		return ""
	}

	img := cached.img

	switch e {
	case EncodingBlocks:
		canvas := paintbrush.New()
		canvas.SetImage(img)
		canvas.SetWidth(cols)
		canvas.SetHeight(rows)
		canvas.Weights = map[rune]float64{
			'': .95,
			'': .95,
			'▁': .9,
			'▂': .9,
			'▃': .9,
			'▄': .9,
			'▅': .9,
			'▆': .85,
			'█': .85,
			'▊': .95,
			'▋': .95,
			'▌': .95,
			'▍': .95,
			'▎': .95,
			'▏': .95,
			'●': .95,
			'◀': .95,
			'▲': .95,
			'▶': .95,
			'▼': .9,
			'○': .8,
			'◉': .95,
			'◧': .9,
			'◨': .9,
			'◩': .9,
			'◪': .9,
		}
		canvas.Paint()
		return strings.TrimSpace(canvas.GetResult())
	case EncodingKitty:
		// Build Kitty graphics unicode place holders
		var fg color.Color
		var extra int
		var r, g, b int
		hashedID := key.Hash()
		id := int(hashedID)
		extra, r, g, b = id>>24&0xff, id>>16&0xff, id>>8&0xff, id&0xff

		if id <= 255 {
			fg = ansi.IndexedColor(b)
		} else {
			fg = color.RGBA{
				R: uint8(r), //nolint:gosec
				G: uint8(g), //nolint:gosec
				B: uint8(b), //nolint:gosec
				A: 0xff,
			}
		}

		fgStyle := ansi.NewStyle().ForegroundColor(fg).String()

		var buf bytes.Buffer
		for y := range rows {
			// As optimization, we 只 写入 fg color 序号 id, 和
			// column-行 data once 在 first cell. 终态 will handle
			// the rest.
			buf.WriteString(fgStyle)
			buf.WriteRune(kitty.Placeholder)
			buf.WriteRune(kitty.Diacritic(y))
			buf.WriteRune(kitty.Diacritic(0))
			if extra > 0 {
				buf.WriteRune(kitty.Diacritic(extra))
			}
			for x := 1; x < cols; x++ {
				buf.WriteString(fgStyle)
				buf.WriteRune(kitty.Placeholder)
			}
			if y < rows-1 {
				buf.WriteByte('\n')
			}
		}

		return buf.String()

	default:
		return ""
	}
}
