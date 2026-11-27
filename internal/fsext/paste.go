package fsext

import (
	"os"
	"strings"
)

func ParsePastedFiles(s string) []string {
	s = strings.TrimSpace(s)

	// NOTE: Rio 在 Windows adds NULL chars 为 some reason.
	s = strings.ReplaceAll(s, "\x00", "")

	switch {
	case attemptStat(s):
		return strings.Split(s, "\n")
	case os.Getenv("WT_SESSION") != "":
		return windowsTerminalParsePastedFiles(s)
	default:
		return unixParsePastedFiles(s)
	}
}

func attemptStat(s string) bool {
	for path := range strings.SplitSeq(s, "\n") {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func windowsTerminalParsePastedFiles(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}

	var (
		paths    []string
		current  strings.Builder
		inQuotes = false
	)
	for i := range len(s) {
		ch := s[i]

		switch {
		case ch == '"':
			if inQuotes {
				// End 的 quoted section
				if current.Len() > 0 {
					paths = append(paths, current.String())
					current.Reset()
				}
				inQuotes = false
			} else {
				// Start 的 quoted section
				inQuotes = true
			}
		case inQuotes:
			current.WriteByte(ch)
		case ch != ' ':
			// 文本 outside quotes 是 不 allowed
			return nil
		}
	}

	// Add any remaining content 如果 quotes were properly closed
	if current.Len() > 0 && !inQuotes {
		paths = append(paths, current.String())
	}

	// If quotes were 不 closed, return empty (malformed input)
	if inQuotes {
		return nil
	}

	return paths
}

func unixParsePastedFiles(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}

	var (
		paths   []string
		current strings.Builder
		escaped = false
	)
	for i := range len(s) {
		ch := s[i]

		switch {
		case escaped:
			// After backslash, add character 作为-是 (including space)
			current.WriteByte(ch)
			escaped = false
		case ch == '\\':
			// Check 如果 这 backslash 是 在 end 的 string
			if i == len(s)-1 {
				// Trailing backslash, treat 作为 literal
				current.WriteByte(ch)
			} else {
				// Start 的 escape 序号
				escaped = true
			}
		case ch == ' ':
			// Space separates paths (unless escaped)
			if current.Len() > 0 {
				paths = append(paths, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}

	// Handle trailing backslash 如果 present
	if escaped {
		current.WriteByte('\\')
	}

	// Add last 路径 如果 any
	if current.Len() > 0 {
		paths = append(paths, current.String())
	}

	return paths
}
