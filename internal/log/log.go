package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/x/term"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	initOnce    sync.Once
	initialized atomic.Bool

	// 文件 是 log 文件 Setup writes 到. SetFile moves it.
	file switchableFile
)

func Setup(logFile string, debug bool, ws ...io.Writer) {
	initOnce.Do(func() {
		_ = file.set(logFile)

		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}

		opts := &slog.HandlerOptions{
			Level:     level,
			AddSource: debug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if debug && len(groups) == 0 && a.Key == slog.SourceKey {
					if src, ok := a.Value.Any().(*slog.Source); ok {
						src.File = trimSourcePath(src.File)
					}
				}
				return a
			},
		}

		var handlers []slog.Handler
		handlers = append(handlers, slog.NewJSONHandler(&file, opts))

		for _, w := range ws {
			if w == nil {
				continue
			}
			if f, ok := w.(term.File); ok && term.IsTerminal(f.Fd()) {
				handlers = append(handlers, slog.NewTextHandler(w, opts))
			} else {
				handlers = append(handlers, slog.NewJSONHandler(w, opts))
			}
		}

		slog.SetDefault(slog.New(slog.NewMultiHandler(handlers...)))
		initialized.Store(true)
	})
}

func Initialized() bool {
	return initialized.Load()
}

// SetFile moves 文件 logging 到 logFile. 它 closes 文件 written 所以 far,
// 所以 这 process no longer holds it open. 它 does nothing before Setup.
func SetFile(logFile string) error {
	if !Initialized() {
		return nil
	}
	return file.set(logFile)
}

// switchableFile 是 rotating log 文件 那 can 是 moved 到 another 路径
// while it 是 在 use.
type switchableFile struct {
	mu sync.Mutex
	w  *lumberjack.Logger
}

func (f *switchableFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w.Write(p)
}

// set points f 在 路径 和 closes 文件 it wrote 到 before, 如果 any.
func (f *switchableFile) set(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.w
	f.w = &lumberjack.Logger{
		Filename:   path,
		MaxSize:    10,    // Max size in MB
		MaxBackups: 0,     // Number of backups
		MaxAge:     30,    // Days
		Compress:   false, // Enable compression
	}
	if prev == nil {
		return nil
	}
	return prev.Close()
}

// Close closes 文件 written 所以 far, 所以 这 process no longer holds it
// open. 一个 later 写入 reopens 文件.
func (f *switchableFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.w == nil {
		return nil
	}
	return f.w.Close()
}

// modulePath 是 路径 prefix source paths 是 trimmed 到.
const modulePath = "github.com/charmbracelet/crush/"

// trimSourcePath shortens absolute source 路径 所以 build-machine
// directories never appear 在 logs. 它 trims 到 module 路径,
// internal/ tree, 或 package 缓存, 和 falls back 到 文件 name.
func trimSourcePath(path string) string {
	switch {
	case strings.Contains(path, modulePath):
		return path[strings.Index(path, modulePath):]
	case strings.Contains(path, "pkg/mod/"):
		return path[strings.LastIndex(path, "pkg/mod/")+len("pkg/mod/"):]
	case strings.Contains(path, "internal/"):
		return path[strings.Index(path, "internal/"):]
	default:
		return path[strings.LastIndex(path, "/")+1:]
	}
}

func RecoverPanic(name string, cleanup func()) {
	if r := recover(); r != nil {
		event.Error(r, "panic", true, "name", name)

		// 创建 timestamped panic log 文件
		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("crush-panic-%s-%s.log", name, timestamp)

		file, err := os.Create(filename)
		if err == nil {
			defer file.Close()

			// 写入 panic information 和 stack trace
			fmt.Fprintf(file, "Panic in %s: %v\n\n", name, r)
			fmt.Fprintf(file, "Time: %s\n\n", time.Now().Format(time.RFC3339))
			fmt.Fprintf(file, "Stack Trace:\n%s\n", debug.Stack())

			// Execute 清理 function 如果 provided
			if cleanup != nil {
				cleanup()
			}
		}
	}
}
