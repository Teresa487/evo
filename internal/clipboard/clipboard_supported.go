//go:build (darwin || linux || windows || freebsd || openbsd || netbsd) && !ios && !android

package clipboard

import (
	"bytes"
	"context"
	"time"

	"golang.design/x/clipboard"
)

// 一个 clipboard 是 shared 用 every other program 在 machine, 和 写入
// 是 不 one atomic step: backend clears clipboard 和 then sets
// new contents. Another writer landing 在 那 窗口 makes perfectly good
// 写入 look lost, 所以 try few times before believing it.
const (
	writeAttempts = 3
	retryDelay    = 5 * time.Millisecond
)

// ready reports whether native clipboard 是 usable. Touching clipboard
// after failed initialization may panic, 和 golang.design's Init 是
// idempotent 和 cheap once it has 运行, 所以 every 条目 point asks it again
// rather than tracking initialization state 的 our own.
func ready() bool {
	return clipboard.Init() == nil
}

func initClipboard() error {
	return clipboard.Init()
}

func writeText(text string) error {
	if !ready() {
		return ErrUnsupported
	}
	var err error
	for attempt := range writeAttempts {
		if attempt > 0 {
			time.Sleep(retryDelay)
		}
		if err = attemptWriteText(text); err == nil {
			return nil
		}
	}
	return err
}

func attemptWriteText(text string) error {
	// 一个 写入 错误 means backend never took clipboard; reading back
	// catches rest, where 写入 是 accepted 但 文本 是 不 served
	// afterwards. Neither check subsumes other: failed 写入 leaves
	// earlier identical copy 在 place, which reads back 作为 success.
	if _, err := clipboard.Write(context.Background(), clipboard.FmtText, []byte(text)); err != nil {
		return ErrWriteFailed
	}
	// Writing nothing empties clipboard, 所以 there 是 nothing 到 读取 back.
	if text == "" {
		return nil
	}
	data, err := clipboard.Read(context.Background(), clipboard.FmtText)
	if err != nil || !bytes.Equal(data, []byte(text)) {
		return ErrWriteFailed
	}
	return nil
}

func read(f Format) ([]byte, error) {
	if !ready() {
		return nil, ErrUnsupported
	}
	var format clipboard.Format
	switch f {
	case FormatText:
		format = clipboard.FmtText
	case FormatImage:
		format = clipboard.FmtImage
	default:
		return nil, ErrEmpty
	}
	data, err := clipboard.Read(context.Background(), format)
	if err != nil || data == nil {
		return nil, ErrEmpty
	}
	return data, nil
}
