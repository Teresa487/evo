package tools

import (
	"fmt"
	"slices"
	"strings"
)

// whitespaceCorrectedNote tells caller 那 old_string did 不 match
// byte-为-byte 和 那 edit was applied 到 whitespace-equivalent 文本
// found 在 文件, 所以 it can verify outcome.
const whitespaceCorrectedNote = "Note: old_string did not match exactly. The edit was applied to whitespace-equivalent text in the file and new_string was re-indented to match the file's style. Verify the result."

// normMatch 是 line range 在 original (un-normalized) content 那
// matches the search pattern after whitespace normalization.
type normMatch struct{ startLine, endLine int }

// findNormalizedMatches searches content 为 old after collapsing each line's
// whitespace runs 到 single spaces. 它 returns line ranges 的 all
// non-overlapping matches, mapped back 到 original content's line numbers.
// Only matches 那 span whole lines 是 reported: replacements happen 在 line
// granularity, 所以 accepting partial-line match would discard rest 的
// line.
func findNormalizedMatches(content, old string) []normMatch {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(old, "\n")

	normOld := joinNormalized(oldLines)
	if strings.TrimSpace(normOld) == "" {
		return nil
	}

	normContentLines := make([]string, len(contentLines))
	for i, l := range contentLines {
		normContentLines[i] = normalizeWS(l)
	}
	normContent := strings.Join(normContentLines, "\n")

	var matches []normMatch
	searchFrom := 0
	for searchFrom <= len(normContent) {
		idx := strings.Index(normContent[searchFrom:], normOld)
		if idx == -1 {
			break
		}
		absIdx := searchFrom + idx
		end := absIdx + len(normOld)
		atLineStart := absIdx == 0 || normContent[absIdx-1] == '\n'
		atLineEnd := end == len(normContent) || normContent[end] == '\n'
		if !atLineStart || !atLineEnd {
			searchFrom = absIdx + 1
			continue
		}
		startLine := lineAtOffset(normContentLines, absIdx)
		endLine := startLine + len(oldLines) - 1
		if endLine >= len(contentLines) {
			break
		}
		matches = append(matches, normMatch{startLine, endLine})
		// Whole-line matches consume every line they cover, 所以 resuming after
		// match 保持 reported ranges disjoint.
		searchFrom = end + 1
	}
	return matches
}

// normalizedReplace attempts whitespace-normalized find-和-replace 当
// exact match fails. If unique match 是 found (或 replaceAll 是 set), it
// extracts actual 文本 从 文件, adapts new's indentation 到 match
// 文件's style, 和 performs replacement. Returns new content 和
// true 在 success, 或 ("", false) 如果 no safe match was found.
func normalizedReplace(content, old, new string, replaceAll bool) (string, bool) {
	matches := findNormalizedMatches(content, old)
	if len(matches) == 0 {
		return "", false
	}
	if !replaceAll && len(matches) > 1 {
		return "", false // Ambiguous; let the model disambiguate.
	}

	contentLines := strings.Split(content, "\n")
	fileUnit := detectIndentUnit(contentLines)

	// Replace 在 reverse order 到 preserve line indices.
	result := slices.Clone(contentLines)
	for _, m := range slices.Backward(matches) {
		actual := strings.Join(contentLines[m.startLine:m.endLine+1], "\n")
		adapted := adaptIndentation(actual, old, new, fileUnit)
		adaptedLines := strings.Split(adapted, "\n")
		result = slices.Concat(result[:m.startLine], adaptedLines, result[m.endLine+1:])
	}

	return strings.Join(result, "\n"), true
}

// joinNormalized normalizes each line 和 joins 用 newlines.
func joinNormalized(lines []string) string {
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = normalizeWS(l)
	}
	return strings.Join(norm, "\n")
}

// adaptIndentation rewrites newStr's leading whitespace 到 match 文件's
// indentation style (fileUnit, detected 从 whole 文件). 它 converts
// newStr's indentation 从 style caller wrote it 在 到 文件's
// style, applying depth offset 所以 newStr lands 在 same nesting level 作为
// actual matched 文本 从 文件. offset 是 applied even 当 both
// styles agree, because oldStr's nesting level may still differ 从
// matched 文本's.
func adaptIndentation(actualStr, oldStr, newStr, fileUnit string) string {
	if fileUnit == "" {
		return newStr
	}

	actualLines := strings.Split(actualStr, "\n")
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	// unit caller indented 用. oldStr 是 best signal, 但 it may
	// have no indentation 在 all, 在 which case newStr's own indentation (和
	// finally 文件's) 是 next best guess.
	sourceUnit := detectIndentUnit(oldLines)
	if sourceUnit == "" {
		sourceUnit = detectIndentUnit(newLines)
	}
	if sourceUnit == "" {
		sourceUnit = fileUnit
	}

	// Compute base indent depth 的 actual match 和 old string
	// (从 their first non-empty lines). difference 是 applied 作为
	// offset 所以 那 newStr lands 在 correct nesting level.
	baseDepth := firstIndentDepth(actualLines, fileUnit)
	oldBaseDepth := firstIndentDepth(oldLines, sourceUnit)
	depthOffset := baseDepth - oldBaseDepth
	if sourceUnit == fileUnit && depthOffset == 0 {
		return newStr
	}

	result := make([]string, len(newLines))
	for i, line := range newLines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			result[i] = line
			continue
		}
		leading := line[:len(line)-len(trimmed)]
		depth := max(measureDepth(leading, sourceUnit)+depthOffset, 0)
		result[i] = strings.Repeat(fileUnit, depth) + trimmed
	}
	return strings.Join(result, "\n")
}

// firstIndentDepth returns indent depth 的 first non-empty line.
func firstIndentDepth(lines []string, unit string) int {
	for _, l := range lines {
		trimmed := strings.TrimLeft(l, " \t")
		if trimmed == "" {
			continue
		}
		leading := l[:len(l)-len(trimmed)]
		return measureDepth(leading, unit)
	}
	return 0
}

// detectIndentUnit returns indentation unit used 通过 given lines:
// "\t" 为 tab-indented files, 或 string 的 N spaces 为 space-indented
// files. Returns "" 如果 indentation cannot 是 determined.
func detectIndentUnit(lines []string) string {
	minSpaces := 0
	hasTabs := false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		leading := line[:len(line)-len(trimmed)]
		if leading == "" {
			continue
		}
		if strings.Contains(leading, "\t") {
			hasTabs = true
			break
		}
		n := len(leading)
		if n > 0 && (minSpaces == 0 || n < minSpaces) {
			minSpaces = n
		}
	}
	if hasTabs {
		return "\t"
	}
	if minSpaces > 0 {
		return strings.Repeat(" ", minSpaces)
	}
	return ""
}

// measureDepth returns how many indent units deep the leading whitespace
// represents.
func measureDepth(leading, unit string) int {
	if unit == "" {
		return 0
	}
	if unit == "\t" {
		return strings.Count(leading, "\t")
	}
	spaces := strings.Count(leading, " ")
	return spaces / len(unit)
}

// diagnoseMismatch produces diagnostic hint 当 old_string 是 不 found
// 在 content 和 normalized matching also failed. 它 helps models
// self-correct 通过 showing what 文件 actually contains near best
// match.
func diagnoseMismatch(content, old string) string {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(old, "\n")

	if len(oldLines) == 0 {
		return ""
	}

	// Strategy 1: whitespace-normalized search.
	if hint := diagnoseWhitespaceMismatch(contentLines, oldLines); hint != "" {
		return hint
	}

	// Strategy 2: line-similarity search.
	if hint := diagnoseBestLineMatch(contentLines, oldLines); hint != "" {
		return hint
	}

	return ""
}

// normalizeWS collapses all whitespace runs 到 single space.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// diagnoseWhitespaceMismatch checks whether old matches content after
// whitespace normalization. If 所以, it reports actual lines.
func diagnoseWhitespaceMismatch(contentLines, oldLines []string) string {
	matches := findNormalizedMatches(strings.Join(contentLines, "\n"), strings.Join(oldLines, "\n"))
	if len(matches) == 0 {
		return ""
	}
	return formatWhitespaceHint(contentLines, matches[0].startLine, matches[0].endLine)
}

// lineAtOffset returns line index 为 character offset 在
// newline-joined lines.
func lineAtOffset(lines []string, offset int) int {
	pos := 0
	for i, line := range lines {
		if pos+len(line) >= offset {
			return i
		}
		pos += len(line) + 1 // +1 for the "\n" join separator.
	}
	return len(lines) - 1
}

func formatWhitespaceHint(contentLines []string, startLine, endLine int) string {
	var b strings.Builder
	b.WriteString("A whitespace-normalized match was found, so the text exists but with different whitespace (tabs vs spaces, or different indentation).\n")
	fmt.Fprintf(&b, "Actual file content (lines %d-%d):\n", startLine+1, endLine+1)
	for i := startLine; i <= endLine && i < len(contentLines); i++ {
		fmt.Fprintf(&b, "%6d|%s\n", i+1, visualizeWS(contentLines[i]))
	}
	b.WriteString("Use the exact whitespace shown above (→ = tab, · = space).")
	return b.String()
}

// diagnoseBestLineMatch finds 窗口 的 lines 在 contentLines 那 best
// matches oldLines (compared after trimming leading/trailing whitespace).
func diagnoseBestLineMatch(contentLines, oldLines []string) string {
	if len(oldLines) == 0 || len(contentLines) == 0 {
		return ""
	}

	// Trimmed old lines 为 comparison.
	trimmedOld := make([]string, len(oldLines))
	for i, l := range oldLines {
		trimmedOld[i] = strings.TrimSpace(l)
	}

	// Remove leading/trailing empty lines 从 search pattern.
	for len(trimmedOld) > 0 && trimmedOld[0] == "" {
		trimmedOld = trimmedOld[1:]
	}
	for len(trimmedOld) > 0 && trimmedOld[len(trimmedOld)-1] == "" {
		trimmedOld = trimmedOld[:len(trimmedOld)-1]
	}
	if len(trimmedOld) == 0 {
		return ""
	}

	bestScore := 0
	bestStart := -1
	window := len(trimmedOld)

	for start := range len(contentLines) - window + 1 {
		score := 0
		for j := range window {
			candidate := strings.TrimSpace(contentLines[start+j])
			if candidate == trimmedOld[j] {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestStart = start
		}
	}

	// Require 在 least half lines 到 match 为 useful hint.
	if bestStart == -1 || bestScore < (window+1)/2 {
		return ""
	}

	endLine := bestStart + window - 1

	// Show bit 的 extra 上下文 (1 line before 和 after).
	ctxStart := max(0, bestStart-1)
	ctxEnd := min(len(contentLines)-1, endLine+1)

	var b strings.Builder
	fmt.Fprintf(&b, "No exact match found. Closest match at lines %d-%d (%d/%d lines match after trimming whitespace):\n",
		bestStart+1, endLine+1, bestScore, window)
	for i := ctxStart; i <= ctxEnd; i++ {
		fmt.Fprintf(&b, "%6d|%s\n", i+1, visualizeWS(contentLines[i]))
	}
	b.WriteString("Use the exact text shown above (→ = tab, · = space).")
	return b.String()
}

// visualizeWS replaces tabs 和 leading spaces 用 visible markers 所以
// models can distinguish them. Interior spaces 是 left 作为-是 到 keep
// the output readable.
func visualizeWS(s string) string {
	s = strings.ReplaceAll(s, "\t", "→")
	trimmed := strings.TrimLeft(s, " ")
	leading := len(s) - len(trimmed)
	if leading > 0 {
		s = strings.Repeat("·", leading) + trimmed
	}
	return s
}
