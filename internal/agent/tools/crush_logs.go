// crush_logs.go —— crush_logs 工具：读取 Crush 自身日志。

package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
)

const CrushLogsToolName = "crush_logs"

//go:embed crush_logs.md.tpl
var crushLogsDescriptionTmpl []byte

var crushLogsDescriptionTpl = template.Must(
	template.New("crushLogsDescription").
		Parse(string(crushLogsDescriptionTmpl)),
)

type crushLogsDescriptionData struct {
	DefaultLines int
	MaxLines     int
}

func crushLogsDescription() string {
	return renderTemplate(crushLogsDescriptionTpl, crushLogsDescriptionData{
		DefaultLines: defaultLogLines,
		MaxLines:     maxLogLines,
	})
}

// Max line size 到 prevent memory issues 用 very long log lines (1 MB).
const maxLogLineSize = 1024 * 1024

// 默认 和 max line limits.
const (
	defaultLogLines = 50
	maxLogLines     = 100
)

// Reserved fields 那 should 不 appear 作为 extra key=值 pairs.
// Case-insensitive matching 是 used.
var reservedFields = map[string]bool{
	"time":   true,
	"level":  true,
	"source": true,
	"msg":    true,
}

// Sensitive 字段 keys 那 should 是 redacted (matched case-insensitively).
var sensitiveKeys = []string{
	"authorization",
	"api-key",
	"api_key",
	"apikey",
	"token",
	"secret",
	"password",
	"credential",
}

type CrushLogsParams struct {
	Lines int `json:"lines,omitempty" description:"Number of recent log entries to return (default 50, max 100)"`
}

func NewCrushLogsTool(logFile string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		CrushLogsToolName,
		crushLogsDescription(),
		func(ctx context.Context, params CrushLogsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			result := runCrushLogs(logFile, params)
			return fantasy.NewTextResponse(result), nil
		},
	)
}

// runCrushLogs reads 和 formats last N log entries 从 given 文件.
func runCrushLogs(logFile string, params CrushLogsParams) string {
	// Validate 和 clamp lines parameter.
	lines := params.Lines
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	// Check 如果 文件 exists.
	info, err := os.Stat(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "No log file found"
		}
		return fmt.Sprintf("Error accessing log file: %v", err)
	}

	if info.Size() == 0 {
		return "Log file is empty"
	}

	// 读取 last N lines 从 log 文件.
	logEntries, err := readLastLines(logFile, lines)
	if err != nil {
		return fmt.Sprintf("Error reading log file: %v", err)
	}

	if len(logEntries) == 0 {
		return "Log file is empty"
	}

	// Format 和 return entries.
	formatted := formatLogEntries(logEntries)
	return strings.Join(formatted, "\n")
}

// readLastLines reads last n lines 从 文件 通过 seeking 到 end 和
// scanning backwards. Lines exceeding maxLogLineSize 是 skipped.
func readLastLines(filePath string, n int) ([]map[string]any, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Size() == 0 {
		return nil, nil
	}

	// Seek 到 end 和 读取 chunks backwards.
	var entries []map[string]any
	const chunkSize = 8192 // 8KB chunks

	pos := stat.Size()
	var remainder []byte

	for pos > 0 && len(entries) < n {
		chunkStart := max(pos-chunkSize, 0)

		chunkLen := int(pos - chunkStart)
		if chunkLen == 0 {
			break
		}

		_, err := file.Seek(chunkStart, 0)
		if err != nil {
			return nil, err
		}

		chunk := make([]byte, chunkLen)
		_, err = io.ReadFull(file, chunk)
		if err != nil {
			return nil, err
		}

		// Combine 用 remainder 从 previous (earlier) chunk.
		data := append(chunk, remainder...)

		// Split into lines (without final incomplete line 如果 any).
		lines := splitLines(data)

		// Keep incomplete line 为 next iteration.
		if len(data) > 0 && data[len(data)-1] != '\n' {
			remainder = lines[len(lines)-1]
			lines = lines[:len(lines)-1]
		} else {
			remainder = nil
		}

		// Parse lines 从 end 到 start 到 get most recent first.
		for i := len(lines) - 1; i >= 0; i-- {
			if len(lines[i]) > maxLogLineSize {
				// Skip oversized lines silently.
				continue
			}

			// Try 到 parse 作为 JSON.
			var entry map[string]any
			if err := json.Unmarshal(lines[i], &entry); err != nil {
				// Skip malformed lines silently.
				continue
			}

			entries = append(entries, entry)
			if len(entries) >= n {
				break
			}
		}

		pos = chunkStart
	}

	// Handle final remainder.
	if len(remainder) > 0 && len(remainder) <= maxLogLineSize {
		var entry map[string]any
		if err := json.Unmarshal(remainder, &entry); err == nil {
			if len(entries) < n {
				entries = append(entries, entry)
			}
		}
	}

	// Reverse 到 get chronological order (oldest first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

// splitLines splits data into lines without allocating strings.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := range len(data) {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// formatLogEntries formats log entries into compact 文本 format.
func formatLogEntries(entries []map[string]any) []string {
	var result []string
	for _, entry := range entries {
		result = append(result, formatLogEntry(entry))
	}
	return result
}

// formatLogEntry formats single log 条目 into compact 文本 format:
// TIMESTAMP LEVEL SOURCE:LINE 消息 key=值...
func formatLogEntry(entry map[string]any) string {
	var parts []string

	// Extract 和 format timestamp (time-只, no date).
	timeStr := extractTime(entry)
	parts = append(parts, timeStr)

	// Extract level.
	level := extractLevel(entry)
	parts = append(parts, level)

	// Extract source.
	source := extractSource(entry)
	parts = append(parts, source)

	// Extract 消息.
	msg := extractMessage(entry)

	// Collect extra fields (excluding reserved fields).
	extraFields := extractExtraFields(entry)

	// Build the output.
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(part)
	}
	b.WriteByte(' ')
	b.WriteString(msg)

	// Append sorted key=值 pairs.
	if len(extraFields) > 0 {
		keys := make([]string, 0, len(extraFields))
		for k := range extraFields {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			b.WriteByte(' ')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(formatValue(extraFields[k], k))
		}
	}

	return b.String()
}

// extractTime extracts 和 formats timestamp 从 log 条目.
// Returns time-只 format (15:04:05).
func extractTime(entry map[string]any) string {
	timeVal, ok := entry["time"]
	if !ok {
		return "--:--:--"
	}

	timeStr, ok := timeVal.(string)
	if !ok {
		return "--:--:--"
	}

	// Parse RFC3339 format.
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		// Try other common formats.
		t, err = time.Parse("2006-01-02T15:04:05", timeStr)
		if err != nil {
			return "--:--:--"
		}
	}

	return t.Format("15:04:05")
}

// extractLevel extracts 和 normalizes log level.
func extractLevel(entry map[string]any) string {
	levelVal, ok := entry["level"]
	if !ok {
		return "INFO"
	}

	levelStr, ok := levelVal.(string)
	if !ok {
		return "INFO"
	}

	switch strings.ToUpper(levelStr) {
	case "DEBUG":
		return "DEBUG"
	case "INFO":
		return "INFO"
	case "WARN", "WARNING":
		return "WARN"
	case "ERROR":
		return "ERROR"
	default:
		return "INFO"
	}
}

// extractSource extracts source 文件 和 line 从 log 条目.
func extractSource(entry map[string]any) string {
	sourceVal, ok := entry["source"]
	if !ok {
		return "unknown:0"
	}

	// Source can 是 string 或 object 用 "文件" 和 "line".
	switch s := sourceVal.(type) {
	case string:
		return filepath.Base(s)
	case map[string]any:
		fileVal, ok := s["file"].(string)
		if !ok {
			return "unknown:0"
		}
		fileVal = filepath.Base(fileVal)

		lineNum := 0
		if lineVal, ok := s["line"]; ok {
			switch l := lineVal.(type) {
			case float64:
				lineNum = int(l)
			case int:
				lineNum = l
			case json.Number:
				if n, err := l.Int64(); err == nil {
					lineNum = int(n)
				}
			}
		}
		return fmt.Sprintf("%s:%d", fileVal, lineNum)
	default:
		return "unknown:0"
	}
}

// extractMessage extracts log 消息.
func extractMessage(entry map[string]any) string {
	msgVal, ok := entry["msg"]
	if !ok {
		return ""
	}

	if msgStr, ok := msgVal.(string); ok {
		return msgStr
	}

	return fmt.Sprintf("%v", msgVal)
}

// extractExtraFields extracts all non-reserved fields 从 log 条目.
func extractExtraFields(entry map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range entry {
		// Skip reserved fields (case-insensitive).
		if isReservedField(k) {
			continue
		}
		// Redact sensitive values.
		if isSensitiveKey(k) {
			result[k] = "[REDACTED]"
		} else {
			result[k] = v
		}
	}
	return result
}

// isReservedField checks 如果 字段 name 是 reserved (case-insensitive).
func isReservedField(name string) bool {
	lowerName := strings.ToLower(name)
	return reservedFields[lowerName]
}

// isSensitiveKey checks 如果 key contains sensitive information (case-insensitive).
func isSensitiveKey(name string) bool {
	lowerName := strings.ToLower(name)
	for _, sensitive := range sensitiveKeys {
		if strings.Contains(lowerName, sensitive) {
			return true
		}
	}
	return false
}

// formatValue formats 值 according 到 quoting rules.
func formatValue(value any, key string) string {
	// Redact sensitive values (second check 为 safety).
	if isSensitiveKey(key) {
		return "[REDACTED]"
	}

	switch v := value.(type) {
	case string:
		return formatStringValue(v)
	case float64:
		// Check 如果 it's actually integer.
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	case map[string]any, []any:
		// Objects 和 arrays 是 JSON-encoded 和 quoted.
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return quoteString(fmt.Sprintf("%v", v))
		}
		return quoteString(string(jsonBytes))
	default:
		return quoteString(fmt.Sprintf("%v", v))
	}
}

// formatStringValue formats string 值 用 quoting 如果 needed.
func formatStringValue(s string) string {
	// Quote 如果 empty, contains spaces, =, newlines, 或 special chars.
	needsQuote := len(s) == 0 ||
		strings.ContainsAny(s, " =\n\r\t\"") ||
		strings.Contains(s, "\\")

	if !needsQuote {
		return s
	}

	return quoteString(s)
}

// quoteString quotes string 用 double quotes 和 escapes special characters.
func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
