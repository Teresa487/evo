package stringext

import (
	"encoding/base64"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func Capitalize(text string) string {
	return cases.Title(language.English, cases.Compact).String(text)
}

// NormalizeSpace normalizes whitespace 在 given content string.
// 它 replaces Windows-style line endings 用 Unix-style line endings,
// converts tabs 到 four spaces, 和 trims leading 和 trailing newlines.
// Per-line indentation 是 preserved: trimming spaces would eat first
// line's leading whitespace 和 corrupt indentation 在 code previews.
func NormalizeSpace(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\t", "    ")
	content = strings.Trim(content, "\n")
	return content
}

// IsValidBase64 reports whether s 是 canonical base64 under standard
// encoding (RFC 4648). 它 requires 那 s round-trips through
// decode/encode unchanged — rejecting whitespace, missing padding,
// 和 other leniencies 那 DecodeString alone would 受理.
func IsValidBase64(s string) bool {
	if s == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	// Round-trip check rejects whitespace, missing padding, 和 other
	// leniencies 那 DecodeString silently accepts.
	return base64.StdEncoding.EncodeToString(decoded) == s
}
