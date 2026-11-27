package filepathext

import (
	"path/filepath"
	"runtime"
	"strings"
)

// SmartJoin joins two paths, treating second 路径 作为 absolute 如果 it 是
// absolute 路径.
func SmartJoin(one, two string) string {
	if SmartIsAbs(two) {
		return two
	}
	return filepath.Join(one, two)
}

// SmartIsAbs checks 如果 路径 是 absolute, considering both OS-specific 和
// Unix-style paths.
func SmartIsAbs(path string) bool {
	switch runtime.GOOS {
	case "windows":
		return filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/")
	default:
		return filepath.IsAbs(path)
	}
}

// SplitGlobPrefix splits glob pattern into longest leading 运行 的
// literal 路径 segments 和 remaining pattern. prefix contains no
// glob metacharacters, 所以 callers can safely use it 作为 目录 到 start
// walk 从. For "internal/agent/*.go" it returns ("internal/agent",
// "*.go"); 为 "**/foo.go" it returns ("", "**/foo.go").
func SplitGlobPrefix(pattern string) (prefix, rest string) {
	pattern = filepath.ToSlash(pattern)
	segments := strings.Split(pattern, "/")
	var literal []string
	for i, seg := range segments {
		if strings.ContainsAny(seg, "*?[{\\") {
			rest = strings.Join(segments[i:], "/")
			return strings.Join(literal, "/"), rest
		}
		literal = append(literal, seg)
	}
	// Whole pattern 是 literal ( plain 路径); walk 它的 parent 和 match
	// basename 所以 existing match logic still applies.
	if len(literal) == 0 {
		return "", pattern
	}
	parent := strings.Join(literal[:len(literal)-1], "/")
	return parent, literal[len(literal)-1]
}
