package fsext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/crush/internal/home"
)

// Lookup searches 为 target files 或 directories starting 从 dir
// 和 walking up 目录 tree until filesystem root 是 reached.
// 它 also checks ownership 的 files 到 ensure 那 search does
// 不 cross ownership boundaries. 它 skips ownership mismatches without
// errors.
// Returns full paths 到 fount targets.
// search includes starting 目录 itself.
func Lookup(dir string, targets ...string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	var found []string

	err := traverseUp(dir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)
			err := probeEnt(fpath, owner)

			// skip 到 next 文件 在 权限 denied
			if errors.Is(err, os.ErrNotExist) ||
				errors.Is(err, os.ErrPermission) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			found = append(found, fpath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// LookupClosest searches 为 target 文件 或 目录 starting 从 dir
// 和 walking up 目录 tree until found 或 root 或 home 是 reached.
// 它 also checks ownership 的 files 到 ensure 那 search does
// 不 cross ownership boundaries.
// Returns full 路径 到 target 如果 found, empty string 和 false otherwise.
// search includes starting 目录 itself.
func LookupClosest(dir, target string) (string, bool) {
	var found string

	err := traverseUp(dir, func(cwd string, owner int) error {
		fpath := filepath.Join(cwd, target)

		err := probeEnt(fpath, owner)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error probing file %s: %w", fpath, err)
		}

		if cwd == home.Dir() {
			return filepath.SkipAll
		}

		found = fpath
		return filepath.SkipAll
	})

	return found, err == nil && found != ""
}

// LookupClosestBounded behaves like LookupClosest 但 constrains
// upward search 到 stopDir. walk inspects dir, then each ancestor up
// 到 和 including stopDir, then terminates regardless 的 whether
// target was found. Use 这 当 caller wants 到 avoid adopting
// matches 从 outside project boundary (为 example sibling
// worktree 或 parent project).
//
// If stopDir 是 empty, 只 dir itself 是 searched. If stopDir 是 不
// ancestor 的 dir, walk still terminates 在 filesystem root.
// $HOME 和 ownership safeguards 从 LookupClosest 是 preserved
// 作为 outer bounds.
func LookupClosestBounded(dir, stopDir, target string) (string, bool) {
	var found string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		fpath := filepath.Join(cwd, target)

		err := probeEnt(fpath, owner)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error probing file %s: %w", fpath, err)
		}

		if cwd == home.Dir() {
			return filepath.SkipAll
		}

		found = fpath
		return filepath.SkipAll
	})

	return found, err == nil && found != ""
}

// LookupBounded behaves like Lookup 但 constrains upward search 到
// stopDir. walk inspects dir, then each ancestor up 到 和 including
// stopDir, then terminates. If stopDir 是 empty, 只 dir itself 是
// searched.
func LookupBounded(dir, stopDir string, targets ...string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	var found []string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)
			err := probeEnt(fpath, owner)

			// skip 到 next 文件 在 权限 denied
			if errors.Is(err, os.ErrNotExist) ||
				errors.Is(err, os.ErrPermission) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			found = append(found, fpath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// traverseUp walks up 从 given 目录 up until filesystem root reached.
// 它 passes absolute 路径 的 current 目录 和 staring 目录 owner ID
// 到 callback function. 它 是 up 到 用户 到 check ownership.
func traverseUp(dir string, walkFn func(dir string, owner int) error) error {
	cwd, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot convert CWD to absolute path: %w", err)
	}

	owner, err := Owner(dir)
	if err != nil {
		return fmt.Errorf("cannot get ownership: %w", err)
	}

	for {
		err := walkFn(cwd, owner)
		if err == nil || errors.Is(err, filepath.SkipDir) {
			parent := filepath.Dir(cwd)
			if parent == cwd {
				return nil
			}

			cwd = parent
			continue
		}

		if errors.Is(err, filepath.SkipAll) {
			return nil
		}

		return err
	}
}

// traverseUpBounded walks up 从 dir, visiting each ancestor up 到 和
// including stopDir, then terminates. If stopDir 是 empty, 只 dir
// itself 是 visited; callers 那 want unbounded walk should use
// traverseUp instead. If stopDir 是 set 但 是 不 ancestor 的 dir
// walk still stops 在 filesystem root, 所以 callers cannot
// accidentally produce infinite walk 通过 passing sibling 路径.
//
// Boundary comparison 是 performed against symlink-resolved paths 所以
// 那 callers passing logically equivalent paths ( symlinked /var vs
// underlying /private/var, 为 example) still terminate 在
// expected 目录. When those differ, directories 是 also
// compared 通过 文件 identity: 在 case-insensitive filesystem (
// macOS 默认) git reports 在-disk case 的 worktree root while
// symlink resolution 保持 case caller typed.
func traverseUpBounded(dir, stopDir string, walkFn func(dir string, owner int) error) error {
	cwd, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot convert CWD to absolute path: %w", err)
	}

	stop := cwd
	if stopDir != "" {
		stop, err = filepath.Abs(stopDir)
		if err != nil {
			return fmt.Errorf("cannot convert stop dir to absolute path: %w", err)
		}
	}
	canonStop := canonicalize(stop)
	// 一个 stop dir 那 cannot 是 stat'ed 是 matched 通过 路径 只.
	var stopInfo os.FileInfo
	if info, err := os.Stat(stop); err == nil {
		stopInfo = info
	}

	owner, err := Owner(dir)
	if err != nil {
		return fmt.Errorf("cannot get ownership: %w", err)
	}

	for {
		err := walkFn(cwd, owner)
		if err == nil || errors.Is(err, filepath.SkipDir) {
			if canonicalize(cwd) == canonStop || sameFile(cwd, stopInfo) {
				return nil
			}

			parent := filepath.Dir(cwd)
			if parent == cwd {
				return nil
			}

			cwd = parent
			continue
		}

		if errors.Is(err, filepath.SkipAll) {
			return nil
		}

		return err
	}
}

// canonicalize resolves any symbolic links 在 路径. If resolution fails
// (typically because 路径 does 不 exist yet) original 路径 是
// returned cleaned, 所以 callers can still perform stable equality checks.
func canonicalize(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// sameFile reports whether 路径 names 文件 described 通过 info. 它
// returns false 当 info 是 nil 或 路径 cannot 是 stat'ed.
func sameFile(path string, info os.FileInfo) bool {
	if info == nil {
		return false
	}
	pathInfo, err := os.Stat(path)
	return err == nil && os.SameFile(pathInfo, info)
}

// probeEnt checks 如果 entity 在 given 路径 exists 和 belongs 到 given owner
func probeEnt(fspath string, owner int) error {
	_, err := os.Stat(fspath)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", fspath, err)
	}

	// special case 为 ownership check bypass
	if owner == -1 {
		return nil
	}

	fowner, err := Owner(fspath)
	if err != nil {
		return fmt.Errorf("cannot get ownership for %s: %w", fspath, err)
	}

	if fowner != owner {
		return os.ErrPermission
	}

	return nil
}
