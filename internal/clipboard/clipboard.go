// Package clipboard provides cross-platform clipboard access behind build-tag
// guards 所以 那 unsupported platforms (e.g., Android, iOS) compile without
// requiring CGO 或 platform-specific dependencies.
package clipboard

import "errors"

// Format identifies kind 的 data 到 读取 从 clipboard.
type Format int

const (
	// FormatText 是 plain UTF-8 文本.
	FormatText Format = iota
	// FormatImage 是 二进制 图片 data (PNG).
	FormatImage
)

var (
	// ErrUnsupported 是 returned 当 clipboard access 是 不 available 在
	// the current platform.
	ErrUnsupported = errors.New("clipboard operations are not supported on this platform")
	// ErrEmpty 是 returned 当 clipboard holds no data 为
	// requested format.
	ErrEmpty = errors.New("clipboard is empty or holds an unsupported format")
	// ErrWriteFailed 是 returned 当 platform has clipboard 但
	// written 文本 是 不 there afterwards.
	ErrWriteFailed = errors.New("clipboard write did not land")
)

// Init initializes the clipboard subsystem. On unsupported platforms it
// returns ErrUnsupported 但 是 otherwise safe 到 调用.
func Init() error {
	return initClipboard()
}

// WriteText writes plain 文本 到 system clipboard 和 reads it back 到
// confirm 写入 landed. 它 returns ErrUnsupported 在 platforms without
// clipboard 或 当 Init did 不 succeed, 和 ErrWriteFailed 当
// clipboard does 不 hold 文本 afterwards.
func WriteText(text string) error {
	return writeText(text)
}

// 读取 returns clipboard contents 为 given format. 它 returns
// ErrEmpty 当 no matching data 是 present 和 ErrUnsupported 在 platforms
// without clipboard support.
func Read(f Format) ([]byte, error) {
	return read(f)
}
