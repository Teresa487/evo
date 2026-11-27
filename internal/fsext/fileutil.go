package fsext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/charlievieth/fastwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/x/ansi"
)

type FileInfo struct {
	Path    string
	ModTime time.Time
}

func SkipHidden(path string) bool {
	// Check 为 hidden files (starting 用 dot)
	base := filepath.Base(path)
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}

	commonIgnoredDirs := map[string]bool{
		".crush":           true,
		"node_modules":     true,
		"vendor":           true,
		"dist":             true,
		"build":            true,
		"target":           true,
		".git":             true,
		".idea":            true,
		".vscode":          true,
		"__pycache__":      true,
		"bin":              true,
		"obj":              true,
		"out":              true,
		"coverage":         true,
		"logs":             true,
		"generated":        true,
		"bower_components": true,
		"jspm_packages":    true,
	}

	parts := strings.SplitSeq(path, string(os.PathSeparator))
	for part := range parts {
		if commonIgnoredDirs[part] {
			return true
		}
	}
	return false
}

// FastGlobWalker provides gitignore-aware 文件 walking 用 fastwalk
// 它 uses hierarchical ignore checking like git does, checking .gitignore/.crushignore
// files 在 each 目录 从 root 到 target 路径.
type FastGlobWalker struct {
	directoryLister *directoryLister
}

func NewFastGlobWalker(searchPath string) *FastGlobWalker {
	return &FastGlobWalker{
		directoryLister: NewDirectoryLister(searchPath),
	}
}

// ShouldSkip checks 如果 文件 路径 should 是 skipped based 在 hierarchical gitignore,
// crushignore, 和 hidden 文件 rules.
func (w *FastGlobWalker) ShouldSkip(path string) bool {
	return w.directoryLister.shouldIgnore(path, nil, false)
}

// ShouldSkipDir checks 如果 目录 路径 should 是 skipped based 在 hierarchical
// gitignore, crushignore, 和 hidden 文件 rules.
func (w *FastGlobWalker) ShouldSkipDir(path string) bool {
	return w.directoryLister.shouldIgnore(path, nil, true)
}

// Glob globs files.
//
// Does 不 respect gitignore.
func Glob(pattern string, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(context.Background(), pattern, cwd, limit, false)
}

// GlobGitignoreAware globs files respecting gitignore.
func GlobGitignoreAware(pattern string, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(context.Background(), pattern, cwd, limit, true)
}

// GlobGitignoreAwareCtx 是 like [GlobGitignoreAware] 但 stops early 当 ctx
// 是 cancelled (e.g. 在 超时), returning whatever was found 所以 far.
func GlobGitignoreAwareCtx(ctx context.Context, pattern, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(ctx, pattern, cwd, limit, true)
}

func globWithDoubleStar(ctx context.Context, pattern, searchPath string, limit int, gitignore bool) ([]string, bool, error) {
	// Normalize pattern 到 forward slashes 在 Windows 所以 their 配置 can use
	// backslashes
	pattern = filepath.ToSlash(pattern)

	walker := NewFastGlobWalker(searchPath)
	found := csync.NewSlice[FileInfo]()
	conf := fastwalk.Config{
		// Do 不 follow symlinks: following them lets walk escape
		// search root (into module caches, nix 存储, $HOME, etc.) 和
		// chase cycles, which 是 slow 和 can hang. Mirrors rg 路径,
		// which no longer passes -L.
		Follow:  false,
		ToSlash: fastwalk.DefaultToSlash(),
		Sort:    fastwalk.SortFilesFirst,
	}
	err := fastwalk.Walk(&conf, searchPath, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll // Timed out or cancelled; stop walking.
		}
		if err != nil {
			return nil // Skip files we can't access
		}

		isDir := d.IsDir()
		if isDir {
			if gitignore && walker.ShouldSkipDir(path) {
				return filepath.SkipDir
			}
		} else {
			if gitignore && walker.ShouldSkip(path) {
				return nil
			}
		}

		relPath, err := filepath.Rel(searchPath, path)
		if err != nil {
			relPath = path
		}

		// Normalize separators 到 forward slashes
		relPath = filepath.ToSlash(relPath)

		// Check 如果 路径 matches pattern
		matched, err := doublestar.Match(pattern, relPath)
		if err != nil || !matched {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		found.Append(FileInfo{Path: path, ModTime: info.ModTime()})
		if limit > 0 && found.Len() >= limit*2 { // NOTE: why x2?
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, false, fmt.Errorf("fastwalk error: %w", err)
	}

	matches := slices.SortedFunc(found.Seq(), func(a, b FileInfo) int {
		return b.ModTime.Compare(a.ModTime)
	})
	matches, truncated := truncate(matches, limit)

	results := make([]string, len(matches))
	for i, m := range matches {
		results[i] = m.Path
	}
	return results, truncated || errors.Is(err, filepath.SkipAll), nil
}

// ShouldExcludeFile checks 如果 文件 should 是 excluded 从 processing
// based 在 common patterns 和 ignore rules.
func ShouldExcludeFile(rootPath, filePath string) bool {
	info, err := os.Stat(filePath)
	isDir := err == nil && info.IsDir()
	return NewDirectoryLister(rootPath).
		shouldIgnore(filePath, nil, isDir)
}

func PrettyPath(path string) string {
	return home.Short(path)
}

func DirTrim(pwd string, lim int) string {
	var (
		out string
		sep = string(filepath.Separator)
	)
	dirs := strings.Split(pwd, sep)
	if lim > len(dirs)-1 || lim <= 0 {
		return pwd
	}
	for i := len(dirs) - 1; i > 0; i-- {
		out = sep + out
		if i == len(dirs)-1 {
			out = dirs[i]
		} else if i >= len(dirs)-lim {
			// Keep first grapheme cluster, 不 first byte: CJK,
			// combining marks, 和 emoji can span multiple bytes 和 runes,
			// 所以 byte 或 single rune would render wrong character.
			first, _ := ansi.FirstGraphemeCluster(dirs[i], ansi.GraphemeWidth)
			out = first + out
		} else {
			out = "..." + out
			break
		}
	}
	out = filepath.Join("~", out)
	return out
}

// PathOrPrefix returns prefix 如果 路径 starts 用 it, 或 falls back 到
// 路径 otherwise.
func PathOrPrefix(path, prefix string) string {
	if HasPrefix(path, prefix) {
		return prefix
	}
	return path
}

// HasPrefix checks 如果 given 路径 starts 用 specified prefix.
// Uses filepath.Rel 到 determine 如果 路径 是 within prefix.
func HasPrefix(path, prefix string) bool {
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	// If 路径 是 within prefix, Rel will 不 return 路径 starting 用 ".."
	return !strings.HasPrefix(rel, "..")
}

// ToUnixLineEndings converts Windows line endings (CRLF) 到 Unix line endings (LF).
func ToUnixLineEndings(content string) (string, bool) {
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\r\n", "\n"), true
	}
	return content, false
}

// ToWindowsLineEndings converts Unix line endings (LF) 到 Windows line endings (CRLF).
func ToWindowsLineEndings(content string) (string, bool) {
	if !strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\n", "\r\n"), true
	}
	return content, false
}

func truncate[T any](input []T, limit int) ([]T, bool) {
	if limit > 0 && len(input) > limit {
		return input[:limit], true
	}
	return input, false
}
