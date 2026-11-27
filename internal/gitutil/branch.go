// Package gitutil provides utility functions 为 interacting 用 Git
// repositories.
package gitutil

import (
	"os"
	"path/filepath"
	"strings"
)

// CurrentBranch returns current Git branch name 为 given 目录.
// Returns empty string 如果 目录 是 不 在 Git repository,
// repository 是 在 detached HEAD state, 或 any 错误 occurs.
//
// 这 是 目录 walk 和 one small 文件 读取, 所以 it 是 不 cached.
// Callers 那 would otherwise ask once per rendered frame poll it instead
// 和 render 从 their own state.
func CurrentBranch(dir string) string {
	return readBranch(dir)
}

// readBranch reads current branch name straight 从 repository's
// HEAD 文件. Going through filesystem 是 both cheaper 和 more tolerant
// than opening repository 用 Git library, which can reject repos 用
// unusual configuration.
func readBranch(dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}

	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}

	// HEAD 是 either symbolic ref ("ref: refs/heads/<branch>") 或, 当
	// detached, a raw commit hash. Only the former names a branch.
	name, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return name
}

// findGitDir walks up 从 dir looking 为 .git 目录. 它 also resolves
// "gitdir:" pointers, which .git files contain 在 worktrees 和 submodules.
// Returns empty string 当 dir 是 不 inside Git repository.
func findGitDir(dir string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		info, statErr := os.Stat(gitPath)
		if statErr == nil {
			if info.IsDir() {
				return gitPath
			}
			content, readErr := os.ReadFile(gitPath)
			if readErr != nil {
				return ""
			}
			gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
			if !ok {
				return ""
			}
			gitDir = strings.TrimSpace(gitDir)
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
			return gitDir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
