package fsext

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/home"
	"github.com/stretchr/testify/require"
)

func TestLookupClosest(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	t.Run("target found in starting directory", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 target 文件 在 current 目录
		targetFile := filepath.Join(testDir, "target.txt")
		err := os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)

		foundPath, found := LookupClosest(testDir, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})

	t.Run("target found in parent directory", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 subdirectory
		subDir := filepath.Join(testDir, "subdir")
		err := os.Mkdir(subDir, 0o755)
		require.NoError(t, err)

		// 创建 target 文件 在 parent 目录
		targetFile := filepath.Join(testDir, "target.txt")
		err = os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)

		foundPath, found := LookupClosest(subDir, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})

	t.Run("target found in grandparent directory", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 nested subdirectories
		subDir := filepath.Join(testDir, "subdir")
		err := os.Mkdir(subDir, 0o755)
		require.NoError(t, err)

		subSubDir := filepath.Join(subDir, "subsubdir")
		err = os.Mkdir(subSubDir, 0o755)
		require.NoError(t, err)

		// 创建 target 文件 在 grandparent 目录
		targetFile := filepath.Join(testDir, "target.txt")
		err = os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)

		foundPath, found := LookupClosest(subSubDir, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})

	t.Run("target not found", func(t *testing.T) {
		testDir := t.TempDir()

		foundPath, found := LookupClosest(testDir, "nonexistent.txt")
		require.False(t, found)
		require.Empty(t, foundPath)
	})

	t.Run("target directory found", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 target 目录 在 current 目录
		targetDir := filepath.Join(testDir, "targetdir")
		err := os.Mkdir(targetDir, 0o755)
		require.NoError(t, err)

		foundPath, found := LookupClosest(testDir, "targetdir")
		require.True(t, found)
		require.Equal(t, targetDir, foundPath)
	})

	t.Run("stops at home directory", func(t *testing.T) {
		// 这 测试 是 limited 作为 we can't easily 创建 files above home 目录
		// 但 we can 测试 behavior 通过 searching 从 home 目录 itself
		homeDir := home.Dir()

		// Search 为 文件 那 doesn't exist 从 home 目录
		foundPath, found := LookupClosest(homeDir, "nonexistent_file_12345.txt")
		require.False(t, found)
		require.Empty(t, foundPath)
	})

	t.Run("invalid starting directory", func(t *testing.T) {
		foundPath, found := LookupClosest("/invalid/path/that/does/not/exist", "target.txt")
		require.False(t, found)
		require.Empty(t, foundPath)
	})

	t.Run("relative path handling", func(t *testing.T) {
		// 创建 target 文件 在 current 目录
		require.NoError(t, os.WriteFile("target.txt", []byte("test"), 0o644))

		// Search using relative 路径
		foundPath, found := LookupClosest(".", "target.txt")
		require.True(t, found)

		// Resolve symlinks 到 handle macOS /private/var vs /var discrepancy
		expectedPath, err := filepath.EvalSymlinks(filepath.Join(tempDir, "target.txt"))
		require.NoError(t, err)
		actualPath, err := filepath.EvalSymlinks(foundPath)
		require.NoError(t, err)
		require.Equal(t, expectedPath, actualPath)
	})
}

func TestLookupClosestWithOwnership(t *testing.T) {
	// Note: Testing ownership boundaries 是 difficult 在 cross-platform way
	// without creating complex 目录 structures 用 different owners.
	// 这 测试 focuses 在 basic functionality 当 ownership checks pass.

	tempDir := t.TempDir()
	t.Chdir(tempDir)

	t.Run("search respects same ownership", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 subdirectory structure
		subDir := filepath.Join(testDir, "subdir")
		err := os.Mkdir(subDir, 0o755)
		require.NoError(t, err)

		// 创建 target 文件 在 parent 目录
		targetFile := filepath.Join(testDir, "target.txt")
		err = os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)

		// Search 应当 find target assuming same ownership
		foundPath, found := LookupClosest(subDir, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})
}

func TestLookup(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	t.Run("no targets returns empty slice", func(t *testing.T) {
		testDir := t.TempDir()

		found, err := Lookup(testDir)
		require.NoError(t, err)
		require.Empty(t, found)
	})

	t.Run("single target found in starting directory", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 target 文件 在 current 目录
		targetFile := filepath.Join(testDir, "target.txt")
		err := os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)

		found, err := Lookup(testDir, "target.txt")
		require.NoError(t, err)
		require.Len(t, found, 1)
		require.Equal(t, targetFile, found[0])
	})

	t.Run("multiple targets found in starting directory", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 multiple target files 在 current 目录
		targetFile1 := filepath.Join(testDir, "target1.txt")
		targetFile2 := filepath.Join(testDir, "target2.txt")
		targetFile3 := filepath.Join(testDir, "target3.txt")

		err := os.WriteFile(targetFile1, []byte("test1"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile2, []byte("test2"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile3, []byte("test3"), 0o644)
		require.NoError(t, err)

		found, err := Lookup(testDir, "target1.txt", "target2.txt", "target3.txt")
		require.NoError(t, err)
		require.Len(t, found, 3)
		require.Contains(t, found, targetFile1)
		require.Contains(t, found, targetFile2)
		require.Contains(t, found, targetFile3)
	})

	t.Run("targets found in parent directories", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 subdirectory
		subDir := filepath.Join(testDir, "subdir")
		err := os.Mkdir(subDir, 0o755)
		require.NoError(t, err)

		// 创建 target files 在 parent 目录
		targetFile1 := filepath.Join(testDir, "target1.txt")
		targetFile2 := filepath.Join(testDir, "target2.txt")
		err = os.WriteFile(targetFile1, []byte("test1"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile2, []byte("test2"), 0o644)
		require.NoError(t, err)

		found, err := Lookup(subDir, "target1.txt", "target2.txt")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Contains(t, found, targetFile1)
		require.Contains(t, found, targetFile2)
	})

	t.Run("targets found across multiple directory levels", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 nested subdirectories
		subDir := filepath.Join(testDir, "subdir")
		err := os.Mkdir(subDir, 0o755)
		require.NoError(t, err)

		subSubDir := filepath.Join(subDir, "subsubdir")
		err = os.Mkdir(subSubDir, 0o755)
		require.NoError(t, err)

		// 创建 target files 在 different levels
		targetFile1 := filepath.Join(testDir, "target1.txt")
		targetFile2 := filepath.Join(subDir, "target2.txt")
		targetFile3 := filepath.Join(subSubDir, "target3.txt")

		err = os.WriteFile(targetFile1, []byte("test1"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile2, []byte("test2"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile3, []byte("test3"), 0o644)
		require.NoError(t, err)

		found, err := Lookup(subSubDir, "target1.txt", "target2.txt", "target3.txt")
		require.NoError(t, err)
		require.Len(t, found, 3)
		require.Contains(t, found, targetFile1)
		require.Contains(t, found, targetFile2)
		require.Contains(t, found, targetFile3)
	})

	t.Run("some targets not found", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 只 some target files
		targetFile1 := filepath.Join(testDir, "target1.txt")
		targetFile2 := filepath.Join(testDir, "target2.txt")

		err := os.WriteFile(targetFile1, []byte("test1"), 0o644)
		require.NoError(t, err)
		err = os.WriteFile(targetFile2, []byte("test2"), 0o644)
		require.NoError(t, err)

		// Search 为 existing 和 non-existing targets
		found, err := Lookup(testDir, "target1.txt", "nonexistent.txt", "target2.txt", "another_nonexistent.txt")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Contains(t, found, targetFile1)
		require.Contains(t, found, targetFile2)
	})

	t.Run("no targets found", func(t *testing.T) {
		testDir := t.TempDir()

		found, err := Lookup(testDir, "nonexistent1.txt", "nonexistent2.txt", "nonexistent3.txt")
		require.NoError(t, err)
		require.Empty(t, found)
	})

	t.Run("target directories found", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 target directories
		targetDir1 := filepath.Join(testDir, "targetdir1")
		targetDir2 := filepath.Join(testDir, "targetdir2")
		err := os.Mkdir(targetDir1, 0o755)
		require.NoError(t, err)
		err = os.Mkdir(targetDir2, 0o755)
		require.NoError(t, err)

		found, err := Lookup(testDir, "targetdir1", "targetdir2")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Contains(t, found, targetDir1)
		require.Contains(t, found, targetDir2)
	})

	t.Run("mixed files and directories", func(t *testing.T) {
		testDir := t.TempDir()

		// 创建 target files 和 directories
		targetFile := filepath.Join(testDir, "target.txt")
		targetDir := filepath.Join(testDir, "targetdir")
		err := os.WriteFile(targetFile, []byte("test"), 0o644)
		require.NoError(t, err)
		err = os.Mkdir(targetDir, 0o755)
		require.NoError(t, err)

		found, err := Lookup(testDir, "target.txt", "targetdir")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Contains(t, found, targetFile)
		require.Contains(t, found, targetDir)
	})

	t.Run("invalid starting directory", func(t *testing.T) {
		found, err := Lookup("/invalid/path/that/does/not/exist", "target.txt")
		require.Error(t, err)
		require.Empty(t, found)
	})

	t.Run("relative path handling", func(t *testing.T) {
		// 创建 target files 在 current 目录
		require.NoError(t, os.WriteFile("target1.txt", []byte("test1"), 0o644))
		require.NoError(t, os.WriteFile("target2.txt", []byte("test2"), 0o644))

		// Search using relative 路径
		found, err := Lookup(".", "target1.txt", "target2.txt")
		require.NoError(t, err)
		require.Len(t, found, 2)

		// Resolve symlinks 到 handle macOS /private/var vs /var discrepancy
		expectedPath1, err := filepath.EvalSymlinks(filepath.Join(tempDir, "target1.txt"))
		require.NoError(t, err)
		expectedPath2, err := filepath.EvalSymlinks(filepath.Join(tempDir, "target2.txt"))
		require.NoError(t, err)

		// Check 那 found paths match expected paths (order may vary)
		foundEvalSymlinks := make([]string, len(found))
		for i, path := range found {
			evalPath, err := filepath.EvalSymlinks(path)
			require.NoError(t, err)
			foundEvalSymlinks[i] = evalPath
		}

		require.Contains(t, foundEvalSymlinks, expectedPath1)
		require.Contains(t, foundEvalSymlinks, expectedPath2)
	})
}

func TestLookupClosestBounded(t *testing.T) {
	t.Run("found in starting directory", func(t *testing.T) {
		testDir := t.TempDir()

		targetFile := filepath.Join(testDir, "target.txt")
		require.NoError(t, os.WriteFile(targetFile, []byte("test"), 0o644))

		foundPath, found := LookupClosestBounded(testDir, testDir, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})

	t.Run("found at boundary directory", func(t *testing.T) {
		boundary := t.TempDir()

		subDir := filepath.Join(boundary, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		targetFile := filepath.Join(boundary, "target.txt")
		require.NoError(t, os.WriteFile(targetFile, []byte("test"), 0o644))

		foundPath, found := LookupClosestBounded(subDir, boundary, "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})

	t.Run("does not climb past boundary", func(t *testing.T) {
		parent := t.TempDir()

		// Target lives above the boundary.
		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("test"), 0o644))

		boundary := filepath.Join(parent, "project")
		require.NoError(t, os.Mkdir(boundary, 0o755))

		subDir := filepath.Join(boundary, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		foundPath, found := LookupClosestBounded(subDir, boundary, "target.txt")
		require.False(t, found)
		require.Empty(t, foundPath)
	})

	t.Run("does not climb past boundary spelled in different case", func(t *testing.T) {
		parent := t.TempDir()

		// Target lives above the boundary.
		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("test"), 0o644))

		boundary := filepath.Join(parent, "Project")
		require.NoError(t, os.MkdirAll(filepath.Join(boundary, "subdir"), 0o755))

		// On case-insensitive filesystem ( macOS 默认) 这
		// spelling names 目录 inside boundary.
		subDir := filepath.Join(parent, "project", "subdir")
		if _, err := os.Stat(subDir); err != nil {
			t.Skip("filesystem is case-sensitive")
		}

		foundPath, found := LookupClosestBounded(subDir, boundary, "target.txt")
		require.False(t, found, "walk climbed past the boundary to %s", foundPath)
		require.Empty(t, foundPath)
	})

	t.Run("empty boundary searches only starting directory", func(t *testing.T) {
		parent := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("test"), 0o644))

		subDir := filepath.Join(parent, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		foundPath, found := LookupClosestBounded(subDir, "", "target.txt")
		require.False(t, found)
		require.Empty(t, foundPath)
	})

	t.Run("empty boundary still finds in starting directory", func(t *testing.T) {
		testDir := t.TempDir()

		targetFile := filepath.Join(testDir, "target.txt")
		require.NoError(t, os.WriteFile(targetFile, []byte("test"), 0o644))

		foundPath, found := LookupClosestBounded(testDir, "", "target.txt")
		require.True(t, found)
		require.Equal(t, targetFile, foundPath)
	})
}

func TestLookupBounded(t *testing.T) {
	t.Run("returns matches at and below boundary", func(t *testing.T) {
		boundary := t.TempDir()

		subDir := filepath.Join(boundary, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		atBoundary := filepath.Join(boundary, "target.txt")
		atSub := filepath.Join(subDir, "target.txt")
		require.NoError(t, os.WriteFile(atBoundary, []byte("a"), 0o644))
		require.NoError(t, os.WriteFile(atSub, []byte("b"), 0o644))

		found, err := LookupBounded(subDir, boundary, "target.txt")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Contains(t, found, atBoundary)
		require.Contains(t, found, atSub)
	})

	t.Run("ignores matches above boundary", func(t *testing.T) {
		parent := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("nope"), 0o644))

		boundary := filepath.Join(parent, "project")
		require.NoError(t, os.Mkdir(boundary, 0o755))

		subDir := filepath.Join(boundary, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		// Target lives 只 above boundary.
		found, err := LookupBounded(subDir, boundary, "target.txt")
		require.NoError(t, err)
		require.Empty(t, found)
	})

	t.Run("ignores matches above boundary spelled in different case", func(t *testing.T) {
		parent := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("nope"), 0o644))

		boundary := filepath.Join(parent, "Project")
		require.NoError(t, os.MkdirAll(filepath.Join(boundary, "subdir"), 0o755))

		// On case-insensitive filesystem ( macOS 默认) 这
		// spelling names 目录 inside boundary.
		subDir := filepath.Join(parent, "project", "subdir")
		if _, err := os.Stat(subDir); err != nil {
			t.Skip("filesystem is case-sensitive")
		}

		found, err := LookupBounded(subDir, boundary, "target.txt")
		require.NoError(t, err)
		require.Empty(t, found)
	})

	t.Run("empty boundary searches only starting directory", func(t *testing.T) {
		parent := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(parent, "target.txt"), []byte("nope"), 0o644))

		subDir := filepath.Join(parent, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0o755))

		found, err := LookupBounded(subDir, "", "target.txt")
		require.NoError(t, err)
		require.Empty(t, found)
	})

	t.Run("no targets returns nil", func(t *testing.T) {
		dir := t.TempDir()
		found, err := LookupBounded(dir, dir)
		require.NoError(t, err)
		require.Empty(t, found)
	})
}

func TestProbeEnt(t *testing.T) {
	t.Run("existing file with correct owner", func(t *testing.T) {
		tempDir := t.TempDir()

		// 创建 测试 文件
		testFile := filepath.Join(tempDir, "test.txt")
		err := os.WriteFile(testFile, []byte("test"), 0o644)
		require.NoError(t, err)

		// Get owner 的 temp 目录
		owner, err := Owner(tempDir)
		require.NoError(t, err)

		// 测试 probeEnt 用 correct owner
		err = probeEnt(testFile, owner)
		require.NoError(t, err)
	})

	t.Run("existing directory with correct owner", func(t *testing.T) {
		tempDir := t.TempDir()

		// 创建 测试 目录
		testDir := filepath.Join(tempDir, "testdir")
		err := os.Mkdir(testDir, 0o755)
		require.NoError(t, err)

		// Get owner 的 temp 目录
		owner, err := Owner(tempDir)
		require.NoError(t, err)

		// 测试 probeEnt 用 correct owner
		err = probeEnt(testDir, owner)
		require.NoError(t, err)
	})

	t.Run("nonexistent file", func(t *testing.T) {
		tempDir := t.TempDir()

		nonexistentFile := filepath.Join(tempDir, "nonexistent.txt")
		owner, err := Owner(tempDir)
		require.NoError(t, err)

		err = probeEnt(nonexistentFile, owner)
		require.Error(t, err)
		require.True(t, errors.Is(err, os.ErrNotExist))
	})

	t.Run("nonexistent file in nonexistent directory", func(t *testing.T) {
		nonexistentFile := "/this/directory/does/not/exists/nonexistent.txt"

		err := probeEnt(nonexistentFile, -1)
		require.Error(t, err)
		require.True(t, errors.Is(err, os.ErrNotExist))
	})

	t.Run("ownership bypass with -1", func(t *testing.T) {
		tempDir := t.TempDir()

		// 创建 测试 文件
		testFile := filepath.Join(tempDir, "test.txt")
		err := os.WriteFile(testFile, []byte("test"), 0o644)
		require.NoError(t, err)

		// 测试 probeEnt 用 -1 (bypass ownership check)
		err = probeEnt(testFile, -1)
		require.NoError(t, err)
	})

	t.Run("ownership mismatch returns permission error", func(t *testing.T) {
		tempDir := t.TempDir()

		// 创建 测试 文件
		testFile := filepath.Join(tempDir, "test.txt")
		err := os.WriteFile(testFile, []byte("test"), 0o644)
		require.NoError(t, err)

		// 测试 probeEnt 用 different owner (use 9999 which 是 unlikely 到 是 actual owner)
		err = probeEnt(testFile, 9999)
		require.Error(t, err)
		require.True(t, errors.Is(err, os.ErrPermission))
	})
}
