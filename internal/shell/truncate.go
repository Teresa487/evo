// truncate.go —— 长输出的截断与落盘提示。

package shell

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Limits 在 how much bang-mode output 是 kept 在 会话 和 sent 到
// 模型. Commands like `git log` 或 `cat` 在 large 文件 can emit
// megabytes; keeping all 的 it would both blow up 上下文 窗口 和
// inflate 会话 forever.
const (
	maxPersistedLines = 2000
	maxPersistedBytes = 50 << 10
)

// spillSubdir 是 where oversized command output 是 parked, relative 到
// caller-supplied data 目录.
const spillSubdir = "shell-output"

// spillPattern matches files SpillOutput creates. Pruning 是 limited 到
// 这 shape 所以 misconfigured 目录 cannot lose unrelated files.
const spillPattern = "output-*.log"

// spillRetention 是 how long spill 文件 outlives command 那 wrote
// it. Long enough 到 resume yesterday's 会话 和 still 读取 what it
// referenced, short enough 那 stale output does 不 pile up.
const spillRetention = 7 * 24 * time.Hour

// spillDirLimit caps spill 目录. Age alone does 不 bound disk:
// busy afternoon 的 large 工具 output can fill it well inside
// retention 窗口, 所以 oldest files go early 到 stay under 这.
const spillDirLimit = 256 << 20

// prunedDirs records spill directories 这 process has already swept,
// 所以 sweep costs one ReadDir per 目录 rather than one per command.
var prunedDirs sync.Map

// TruncateForPersist bounds shell output before it 是 存储 在 会话.
// Output inside limits 是 returned unchanged. Anything larger 是 written
// 在 full 到 文件 under spillDir 和 replaced 通过 它的 head 和 tail plus
// 指针 到 那 文件, 所以 agent can 读取 omitted middle 在 demand.
//
// Truncated output 是 stripped 的 ANSI sequences: cut points land 在
// middle 的 流, where unterminated escape would bleed styling into
// rest 的 chat.
func TruncateForPersist(output, spillDir string) string {
	if withinBudget(output) {
		return output
	}

	// Colour codes 是 不 content. Output 那 只 crosses limit
	// because 的 escape sequences 是 kept whole.
	plain := ansi.Strip(output)
	if withinBudget(plain) {
		return output
	}

	head, tail := headAndTail(plain)
	omitted := strings.Count(plain, "\n") -
		strings.Count(head, "\n") - strings.Count(tail, "\n")

	note := "output truncated"
	if omitted > 0 {
		note = fmt.Sprintf("%d lines omitted", omitted)
	}
	if path, err := SpillOutput(plain, spillDir); err == nil {
		note += ", full output: " + path
	} else {
		slog.Debug("Could not spill command output", "dir", spillDir, "error", err)
	}
	return head + "\n\n" + note + "\n\n" + tail
}

// withinBudget reports whether output 是 small enough 到 keep whole.
func withinBudget(output string) bool {
	return len(output) <= maxPersistedBytes &&
		strings.Count(output, "\n")+1 <= maxPersistedLines
}

// headAndTail splits output into leading 和 trailing 片段, each
// bounded 通过 both line 和 byte budget.
//
// Lines 是 claimed 从 front until head budget runs out, then
// 从 back until tail budget does. Spending budgets 从 both
// ends 是 what keeps end 的 output alive 当 it 是 只 few
// lines long 但 very wide: plain lines[:n] / lines[len-n:] split hands
// every line 到 head 和 leaves tail empty.
func headAndTail(output string) (head, tail string) {
	lines := strings.Split(output, "\n")
	halfLines := maxPersistedLines / 2
	halfBytes := maxPersistedBytes / 2

	first, last := 0, len(lines)
	var headBytes, tailBytes int
	for first < last {
		if first < halfLines && headBytes+len(lines[first])+1 <= halfBytes {
			headBytes += len(lines[first]) + 1
			first++
			continue
		}
		if len(lines)-last < halfLines && tailBytes+len(lines[last-1])+1 <= halfBytes {
			tailBytes += len(lines[last-1]) + 1
			last--
			continue
		}
		break
	}

	head = strings.Join(lines[:first], "\n")
	tail = strings.Join(lines[last:], "\n")

	// 一个 line wider than budget fits 在 neither end. Keep 作为 much 的 it
	// 作为 budget allows rather than returning nothing.
	if head == "" {
		head = clipHead(lines[0], halfBytes)
	}
	if tail == "" {
		tail = clipTail(lines[len(lines)-1], halfBytes)
	}
	return head, tail
}

// SpillOutput writes command output 那 was too large 到 keep 在 上下文 到
// 文件 agent can 读取 back, 和 returns 那 文件's 路径.
//
// Files land 在 subdirectory 的 dir, which callers set 到 project data
// 目录 所以 agent's view 工具 can 读取 them without
// outside--working-目录 权限 提示词. An empty dir falls back 到
// system temp 目录.
//
// Spill files outlive command 那 wrote them, since agent may want
// 到 读取 one later 在 会话. They 是 swept 在 retention 窗口
// instead; see [pruneSpills].
func SpillOutput(output, dir string) (string, error) {
	if dir == "" {
		// No data 目录 到 own: fall back 到 system temp
		// 目录 和 leave pruning 到 OS. Sweeping shared
		// 目录 为 name we do 不 exclusively own would risk
		// deleting someone else's files.
		dir = os.TempDir()
	} else {
		dir = filepath.Join(dir, spillSubdir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		pruneSpills(dir)
	}

	f, err := os.CreateTemp(dir, spillPattern)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(output); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// pruneSpills deletes spill files older than [spillRetention], then the
// oldest 的 what remains until 目录 fits [spillDirLimit]. 它 runs
// once per 目录 per process. Failures 是 不 worth surfacing:
// spill 这 sweep precedes 是 still perfectly good.
func pruneSpills(dir string) {
	if _, swept := prunedDirs.LoadOrStore(dir, true); swept {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Debug("Could not read spill directory", "dir", dir, "error", err)
		return
	}

	cutoff := time.Now().Add(-spillRetention)
	var kept []fs.FileInfo
	var keptBytes int64
	for _, entry := range entries {
		if match, _ := filepath.Match(spillPattern, entry.Name()); !match {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			removeSpill(dir, info.Name())
			continue
		}
		kept = append(kept, info)
		keptBytes += info.Size()
	}

	// Still over cap once stale files 是 gone: drop oldest
	// survivors until it fits.
	slices.SortFunc(kept, func(a, b fs.FileInfo) int {
		return a.ModTime().Compare(b.ModTime())
	})
	for _, info := range kept {
		if keptBytes <= spillDirLimit {
			break
		}
		removeSpill(dir, info.Name())
		keptBytes -= info.Size()
	}
}

func removeSpill(dir, name string) {
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		slog.Debug("Could not prune spill file", "name", name, "error", err)
	}
}

// clipHead returns 在 most n bytes 从 start 的 s, cut 在 rune
// boundary.
func clipHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clipTail returns 在 most n bytes 从 end 的 s, cut 在 rune
// boundary.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
