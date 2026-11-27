package fsext

import (
	"cmp"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// fastIgnoreDirs 是 set 的 目录 names 那 是 always ignored.
// 这 provides O(1) lookup 为 common cases 到 avoid expensive pattern matching.
var fastIgnoreDirs = map[string]bool{
	".git":            true,
	".svn":            true,
	".hg":             true,
	".bzr":            true,
	".vscode":         true,
	".idea":           true,
	"node_modules":    true,
	"__pycache__":     true,
	".pytest_cache":   true,
	".cache":          true,
	".tmp":            true,
	".Trash":          true,
	".Spotlight-V100": true,
	".fseventsd":      true,
	".crush":          true,
	"OrbStack":        true,
	".local":          true,
	".share":          true,
}

// commonIgnorePatterns contains commonly ignored files 和 directories.
// Note: Exact 目录 names 那 是 在 fastIgnoreDirs 是 handled there 为 O(1) lookup.
// 这 list contains wildcard patterns 和 文件-specific patterns.
var commonIgnorePatterns = sync.OnceValue(func() []gitignore.Pattern {
	patterns := []string{
		// IDE 和 editor files (wildcards)
		"*.swp",
		"*.swo",
		"*~",
		".DS_Store",
		"Thumbs.db",

		// Build artifacts (non-fastIgnoreDirs)
		"target",
		"build",
		"dist",
		"out",
		"bin",
		"obj",
		"*.o",
		"*.so",
		"*.dylib",
		"*.dll",
		"*.exe",

		// Logs 和 temporary files (wildcards)
		"*.log",
		"*.tmp",
		"*.temp",

		// Language-specific (wildcards 和 non-fastIgnoreDirs)
		"*.pyc",
		"*.pyo",
		"vendor",
		"Cargo.lock",
		"package-lock.json",
		"yarn.lock",
		"pnpm-lock.yaml",
	}
	return parsePatterns(patterns, nil)
})

// gitGlobalIgnorePatterns returns patterns 从 git's global excludes 文件
// (core.excludesFile), following git's 配置 resolution order.
var gitGlobalIgnorePatterns = sync.OnceValue(func() []gitignore.Pattern {
	cfg, err := gitconfig.LoadConfig(gitconfig.GlobalScope)
	if err != nil {
		slog.Debug("Failed to load global git config", "error", err)
		return nil
	}

	excludesFilePath := cmp.Or(
		cfg.Raw.Section("core").Options.Get("excludesfile"),
		filepath.Join(home.Config(), "git", "ignore"),
	)
	excludesFilePath = home.Long(excludesFilePath)

	bts, err := os.ReadFile(excludesFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("Failed to read git global excludes file", "path", excludesFilePath, "error", err)
		}
		return nil
	}

	return parsePatterns(strings.Split(string(bts), "\n"), nil)
})

// crushGlobalIgnorePatterns returns patterns 从 用户's
// ~/.配置/crush/ignore 文件.
var crushGlobalIgnorePatterns = sync.OnceValue(func() []gitignore.Pattern {
	name := filepath.Join(home.Config(), "crush", "ignore")
	bts, err := os.ReadFile(name)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("Failed to read crush global ignore file", "path", name, "error", err)
		}
		return nil
	}
	lines := strings.Split(string(bts), "\n")
	return parsePatterns(lines, nil)
})

// parsePatterns parses gitignore pattern strings into Pattern objects.
// domain 是 路径 components where patterns 是 defined (nil 为 global).
func parsePatterns(lines []string, domain []string) []gitignore.Pattern {
	var patterns []gitignore.Pattern
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns
}

type directoryLister struct {
	// dirPatterns caches parsed patterns 从 .gitignore/.crushignore 为 each 目录.
	// 这 avoids re-reading files 当 building combined matchers.
	dirPatterns *csync.Map[string, []gitignore.Pattern]
	// combinedMatchers caches combined matcher 为 each 目录 那 includes
	// all ancestor patterns. 这 allows O(1) matching per 文件.
	combinedMatchers *csync.Map[string, gitignore.Matcher]
	rootPath         string
}

func NewDirectoryLister(rootPath string) *directoryLister {
	return &directoryLister{
		rootPath:         rootPath,
		dirPatterns:      csync.NewMap[string, []gitignore.Pattern](),
		combinedMatchers: csync.NewMap[string, gitignore.Matcher](),
	}
}

// pathToComponents splits 路径 into 它的 components 为 gitignore matching.
func pathToComponents(path string) []string {
	path = filepath.ToSlash(path)
	if path == "" || path == "." {
		return nil
	}
	return strings.Split(path, "/")
}

// getDirPatterns returns parsed patterns 为 specific 目录's
// .gitignore 和 .crushignore files. Results 是 cached.
func (dl *directoryLister) getDirPatterns(dir string) []gitignore.Pattern {
	return dl.dirPatterns.GetOrSet(dir, func() []gitignore.Pattern {
		var allPatterns []gitignore.Pattern

		relPath, _ := filepath.Rel(dl.rootPath, dir)
		var domain []string
		if relPath != "" && relPath != "." {
			domain = pathToComponents(relPath)
		}

		for _, ignoreFile := range []string{".gitignore", ".crushignore"} {
			ignPath := filepath.Join(dir, ignoreFile)
			if content, err := os.ReadFile(ignPath); err == nil {
				lines := strings.Split(string(content), "\n")
				allPatterns = append(allPatterns, parsePatterns(lines, domain)...)
			}
		}
		return allPatterns
	})
}

// getCombinedMatcher returns matcher 那 combines all gitignore patterns
// 从 root 到 given 目录, plus common patterns 和 home patterns.
// Results 是 cached per 目录, 和 we reuse parent 目录 matchers.
func (dl *directoryLister) getCombinedMatcher(dir string) gitignore.Matcher {
	return dl.combinedMatchers.GetOrSet(dir, func() gitignore.Matcher {
		var allPatterns []gitignore.Pattern

		// Add common patterns first (lowest priority).
		allPatterns = append(allPatterns, commonIgnorePatterns()...)

		// Add global ignore patterns (git core.excludesFile + crush global ignore).
		allPatterns = append(allPatterns, gitGlobalIgnorePatterns()...)
		allPatterns = append(allPatterns, crushGlobalIgnorePatterns()...)

		// Collect patterns 从 root 到 这 目录.
		relDir, _ := filepath.Rel(dl.rootPath, dir)
		var pathParts []string
		if relDir != "" && relDir != "." {
			pathParts = pathToComponents(relDir)
		}

		// Add patterns 从 each 目录 从 root 到 current.
		currentPath := dl.rootPath
		allPatterns = append(allPatterns, dl.getDirPatterns(currentPath)...)

		for _, part := range pathParts {
			currentPath = filepath.Join(currentPath, part)
			allPatterns = append(allPatterns, dl.getDirPatterns(currentPath)...)
		}

		return gitignore.NewMatcher(allPatterns)
	})
}

// shouldIgnore checks 如果 路径 should 是 ignored based 在 gitignore rules.
// 这 uses combined matcher 那 includes all ancestor patterns 为 O(1) matching.
func (dl *directoryLister) shouldIgnore(path string, ignorePatterns []string, isDir bool) bool {
	base := filepath.Base(path)

	// Fast 路径: O(1) lookup 为 commonly ignored directories.
	if isDir && fastIgnoreDirs[base] {
		return true
	}

	// Check explicit ignore patterns.
	if len(ignorePatterns) > 0 {
		for _, pattern := range ignorePatterns {
			if matched, err := filepath.Match(pattern, base); err == nil && matched {
				return true
			}
		}
	}

	// Don't apply gitignore rules 到 root 目录 itself.
	if path == dl.rootPath {
		return false
	}

	relPath, err := filepath.Rel(dl.rootPath, path)
	if err != nil {
		relPath = path
	}

	pathComponents := pathToComponents(relPath)
	if len(pathComponents) == 0 {
		return false
	}

	// Get combined matcher 为 parent 目录.
	parentDir := filepath.Dir(path)
	matcher := dl.getCombinedMatcher(parentDir)

	if matcher.Match(pathComponents, isDir) {
		slog.Debug("Ignoring path", "path", relPath)
		return true
	}

	return false
}

// ListDirectory lists files 和 directories 在 specified 路径.
func ListDirectory(initialPath string, ignorePatterns []string, depth, limit int) ([]string, bool, error) {
	found := csync.NewSlice[string]()
	dl := NewDirectoryLister(initialPath)

	slog.Debug("Listing directory", "path", initialPath, "depth", depth, "limit", limit, "ignorePatterns", ignorePatterns)

	conf := fastwalk.Config{
		Follow:   true,
		ToSlash:  fastwalk.DefaultToSlash(),
		Sort:     fastwalk.SortDirsFirst,
		MaxDepth: depth,
	}

	err := fastwalk.Walk(&conf, initialPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip files we don't have permission to access
		}

		isDir := d.IsDir()
		if dl.shouldIgnore(path, ignorePatterns, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		if path != initialPath {
			if isDir {
				path = path + string(filepath.Separator)
			}
			found.Append(path)
		}

		if limit > 0 && found.Len() >= limit {
			return filepath.SkipAll
		}

		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, false, err
	}

	matches, truncated := truncate(slices.Collect(found.Seq()), limit)
	return matches, truncated || errors.Is(err, filepath.SkipAll), nil
}
