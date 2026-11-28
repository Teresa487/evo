package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// createTestLogFile creates temporary log 文件 用 given entries.
func createTestLogFile(t *testing.T, entries []map[string]any) string {
	t.Helper()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "evo.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)
	defer file.Close()

	for _, entry := range entries {
		line, err := json.Marshal(entry)
		require.NoError(t, err)
		_, err = file.WriteString(string(line) + "\n")
		require.NoError(t, err)
	}

	return logFile
}

// makeLogEntry creates standard log 条目 为 testing.
func makeLogEntry(level, msg, source string, line int, extra map[string]any) map[string]any {
	entry := map[string]any{
		"time":  time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		"level": level,
		"msg":   msg,
		"source": map[string]any{
			"file": source,
			"line": line,
		},
	}
	maps.Copy(entry, extra)
	return entry
}

func TestNewEvoLogsTool(t *testing.T) {
	t.Parallel()
	tool := NewEvoLogsTool("/tmp/test.log")
	require.NotNil(t, tool)
	require.Equal(t, EvoLogsToolName, tool.Info().Name)
}

func TestEvoLogs_HappyPath(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Application started", "app.go", 42, map[string]any{"version": "1.0.0"}),
		makeLogEntry("DEBUG", "Processing request", "handler.go", 100, map[string]any{"request_id": "abc123"}),
		makeLogEntry("ERROR", "Database connection failed", "db.go", 55, map[string]any{"retry_count": 3}),
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 3)

	// Verify format: TIMESTAMP LEVEL SOURCE:LINE 消息
	require.Contains(t, lines[0], "INFO")
	require.Contains(t, lines[0], "app.go:42")
	require.Contains(t, lines[0], "Application started")
	require.Contains(t, lines[0], "version=1.0.0")

	require.Contains(t, lines[1], "DEBUG")
	require.Contains(t, lines[1], "handler.go:100")

	require.Contains(t, lines[2], "ERROR")
	require.Contains(t, lines[2], "db.go:55")
}

func TestEvoLogs_DefaultLines(t *testing.T) {
	t.Parallel()
	// 创建 100 log entries.
	var entries []map[string]any
	for i := range 100 {
		entries = append(entries, makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil))
	}

	logFile := createTestLogFile(t, entries)

	// 调用 用 Lines: 0 should 默认 到 50.
	result := runEvoLogs(logFile, EvoLogsParams{Lines: 0})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 50)

	// Verify we got last 50 entries (条目 50-99).
	require.Contains(t, lines[0], "Entry 50")
	require.Contains(t, lines[49], "Entry 99")
}

func TestEvoLogs_MaxCap(t *testing.T) {
	t.Parallel()
	// 创建 200 log entries.
	var entries []map[string]any
	for i := range 200 {
		entries = append(entries, makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil))
	}

	logFile := createTestLogFile(t, entries)

	// 请求 200 lines, 但 should 只 get 100 (max cap).
	result := runEvoLogs(logFile, EvoLogsParams{Lines: 200})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 100)
}

func TestEvoLogs_MissingFile(t *testing.T) {
	t.Parallel()
	result := runEvoLogs("/nonexistent/path/evo.log", EvoLogsParams{Lines: 50})
	require.Contains(t, result, "No log file found")
}

func TestEvoLogs_EmptyFile(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "evo.log")
	_, err := os.Create(logFile)
	require.NoError(t, err)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 50})
	require.Contains(t, result, "Log file is empty")
}

func TestEvoLogs_MalformedLines(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "evo.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// 写入 some valid 和 some invalid lines.
	validEntry := makeLogEntry("INFO", "Valid entry", "app.go", 1, nil)
	line, _ := json.Marshal(validEntry)
	file.WriteString(string(line) + "\n")
	file.WriteString("this is not json\n")
	file.WriteString(`{"incomplete": "json` + "\n")

	validEntry2 := makeLogEntry("INFO", "Another valid entry", "app.go", 2, nil)
	line2, _ := json.Marshal(validEntry2)
	file.WriteString(string(line2) + "\n")

	file.Close()

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")
	// Only 2 valid lines should 是 returned.
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "Valid entry")
	require.Contains(t, lines[1], "Another valid entry")
}

func TestEvoLogs_ExtraFieldsSorted(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Test message", "app.go", 1, map[string]any{
			"z_field": "last",
			"a_field": "first",
			"m_field": "middle",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// Fields should 是 sorted alphabetically.
	idxA := strings.Index(result, "a_field=first")
	idxM := strings.Index(result, "m_field=middle")
	idxZ := strings.Index(result, "z_field=last")

	require.True(t, idxA < idxM, "a_field should come before m_field")
	require.True(t, idxM < idxZ, "m_field should come before z_field")
}

func TestEvoLogs_NonStringValues(t *testing.T) {
	t.Parallel()
	entry := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"level":  "INFO",
		"msg":    "Test values",
		"source": map[string]any{"file": "app.go", "line": 1},
		"count":  42,
		"ratio":  3.14,
		"active": true,
		"data":   nil,
		"obj":    map[string]any{"key": "value"},
		"arr":    []any{1, 2, 3},
	}

	logFile := createTestLogFile(t, []map[string]any{entry})

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// Numbers should 是 bare (不 quoted).
	require.Contains(t, result, "count=42")
	require.Contains(t, result, "ratio=3.14")

	// Booleans should 是 bare.
	require.Contains(t, result, "active=true")

	// Null should 是 bare.
	require.Contains(t, result, "data=null")

	// Objects 和 arrays should 是 JSON-encoded 和 quoted.
	require.Contains(t, result, `obj="{`)
	require.Contains(t, result, `arr="[`)
}

func TestEvoLogs_Redaction(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "API call", "api.go", 10, map[string]any{
			"authorization":  "Bearer secret123",
			"api_key":        "my-api-key",
			"api-key":        "my-api-key-2",
			"apikey":         "myapikey",
			"token":          "mytoken",
			"secret":         "mysecret",
			"password":       "mypassword",
			"credential":     "mycred",
			"Authorization":  "Bearer secret456",
			"API_KEY":        "uppercase",
			"my_token_value": "nestedtoken",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// All sensitive fields should 是 redacted.
	require.Contains(t, result, "authorization=[REDACTED]")
	require.Contains(t, result, "api_key=[REDACTED]")
	require.Contains(t, result, "api-key=[REDACTED]")
	require.Contains(t, result, "apikey=[REDACTED]")
	require.Contains(t, result, "token=[REDACTED]")
	require.Contains(t, result, "secret=[REDACTED]")
	require.Contains(t, result, "password=[REDACTED]")
	require.Contains(t, result, "credential=[REDACTED]")
	require.Contains(t, result, "Authorization=[REDACTED]")
	require.Contains(t, result, "API_KEY=[REDACTED]")
	require.Contains(t, result, "my_token_value=[REDACTED]")

	// Original sensitive values should 不 appear.
	require.NotContains(t, result, "secret123")
	require.NotContains(t, result, "my-api-key")
	require.NotContains(t, result, "mytoken")
}

func TestEvoLogs_ReservedFields(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Test",
			"source": map[string]any{"file": "app.go", "line": 1},
			"Time":   "should be reserved",
			"LEVEL":  "should be reserved",
			"Msg":    "should be reserved",
			"SOURCE": "should be reserved",
			"extra":  "should appear",
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// Reserved fields (case-insensitive) should 不 appear 在 extra fields.
	require.NotContains(t, result, "Time=")
	require.NotContains(t, result, "LEVEL=")
	require.NotContains(t, result, "Msg=")
	require.NotContains(t, result, "SOURCE=")
	require.NotContains(t, result, "time=")  // The extra time field
	require.NotContains(t, result, "level=") // The extra level field

	// Non-reserved 字段 should appear (quoted since it has spaces).
	require.Contains(t, result, `extra="should appear"`)
}

func TestEvoLogs_OversizedLines(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "evo.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// 创建 valid 条目 first.
	validEntry := makeLogEntry("INFO", "Valid entry", "app.go", 1, nil)
	line, _ := json.Marshal(validEntry)
	file.WriteString(string(line) + "\n")

	// 创建 oversized line (more than 1 MB).
	bigValue := strings.Repeat("x", maxLogLineSize+1000)
	bigEntry := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"level":  "INFO",
		"msg":    "Big message",
		"source": map[string]any{"file": "big.go", "line": 1},
		"data":   bigValue,
	}
	bigLine, _ := json.Marshal(bigEntry)
	file.WriteString(string(bigLine) + "\n")

	// 创建 another valid 条目.
	validEntry2 := makeLogEntry("INFO", "Second valid entry", "app.go", 2, nil)
	line2, _ := json.Marshal(validEntry2)
	file.WriteString(string(line2) + "\n")

	file.Close()

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")

	// Only 2 valid entries should 是 returned (oversized one skipped).
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "Valid entry")
	require.Contains(t, lines[1], "Second valid entry")
}

func TestEvoLogs_PartialTrailingLine(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "evo.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// 创建 valid entries.
	for i := range 5 {
		entry := makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil)
		line, _ := json.Marshal(entry)
		file.WriteString(string(line) + "\n")
	}

	// 写入 partial/truncated line (no closing brace 或 newline).
	file.WriteString(`{"time": "2024-01-15T10:00:00Z", "level": "INFO", "msg": "Truncated`)
	file.Close()

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")

	// Should get 5 valid entries, truncated line 是 skipped.
	require.Len(t, lines, 5)
	for i, line := range lines {
		require.Contains(t, line, fmt.Sprintf("Entry %d", i))
	}
}

func TestEvoLogs_ValueQuoting(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Test", "app.go", 1, map[string]any{
			"empty":          "",
			"with_spaces":    "hello world",
			"with_equals":    "a=b",
			"with_newline":   "line1\nline2",
			"with_quote":     `say "hello"`,
			"with_backslash": "path\\to\\file",
			"normal":         "simplevalue",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// Empty strings should 是 quoted.
	require.Contains(t, result, `empty=""`)

	// Strings 用 spaces should 是 quoted.
	require.Contains(t, result, `with_spaces="hello world"`)

	// Strings 用 = should 是 quoted.
	require.Contains(t, result, `with_equals="a=b"`)

	// Strings 用 newlines should escape them.
	require.Contains(t, result, `with_newline="line1\nline2"`)

	// Strings 用 quotes should escape them.
	require.Contains(t, result, `with_quote="say \"hello\""`)

	// Strings 用 backslashes should escape them.
	require.Contains(t, result, `with_backslash="path\\to\\file"`)

	// Normal strings without special chars should 是 bare.
	require.Contains(t, result, "normal=simplevalue")
}

func TestEvoLogs_ChronologicalOrder(t *testing.T) {
	t.Parallel()
	// 创建 entries 用 different timestamps.
	baseTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	entries := []map[string]any{
		{
			"time":   baseTime.Add(0 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "First",
			"source": map[string]any{"file": "app.go", "line": 1},
		},
		{
			"time":   baseTime.Add(1 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Second",
			"source": map[string]any{"file": "app.go", "line": 2},
		},
		{
			"time":   baseTime.Add(2 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Third",
			"source": map[string]any{"file": "app.go", "line": 3},
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")

	// Verify chronological order (oldest first).
	require.Len(t, lines, 3)
	require.Contains(t, lines[0], "First")
	require.Contains(t, lines[1], "Second")
	require.Contains(t, lines[2], "Third")
}

func TestEvoLogs_TimeOnlyFormat(t *testing.T) {
	t.Parallel()
	entry := map[string]any{
		"time":   "2024-01-15T15:04:05Z",
		"level":  "INFO",
		"msg":    "Test",
		"source": map[string]any{"file": "app.go", "line": 1},
	}

	logFile := createTestLogFile(t, []map[string]any{entry})

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 1})

	// Should show time-只 format.
	require.True(t, strings.HasPrefix(result, "15:04:05"), "Expected time-only format, got: %s", result)
}

func TestEvoLogs_LevelVariations(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("DEBUG", "Debug message", "app.go", 1, nil),
		makeLogEntry("INFO", "Info message", "app.go", 2, nil),
		makeLogEntry("WARN", "Warn message", "app.go", 3, nil),
		makeLogEntry("WARNING", "Warning message", "app.go", 4, nil),
		makeLogEntry("ERROR", "Error message", "app.go", 5, nil),
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 5})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 5)

	// Check level normalization.
	require.Contains(t, lines[0], "DEBUG")
	require.Contains(t, lines[1], "INFO")
	require.Contains(t, lines[2], "WARN")
	require.Contains(t, lines[3], "WARN") // WARNING -> WARN
	require.Contains(t, lines[4], "ERROR")
}

func TestEvoLogs_SourceVariations(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		// Source 作为 object 用 文件 和 line.
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Object source",
			"source": map[string]any{"file": "/path/to/app.go", "line": 42},
		},
		// Source 作为 string.
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "String source",
			"source": "/path/to/handler.go:100",
		},
		// Missing source.
		{
			"time":  time.Now().Format(time.RFC3339),
			"level": "INFO",
			"msg":   "No source",
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runEvoLogs(logFile, EvoLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 3)

	// Check source formatting (should use basename 只).
	require.Contains(t, lines[0], "app.go:42")
	require.Contains(t, lines[1], "handler.go") // String source gets basename too
	require.Contains(t, lines[2], "unknown:0")  // Missing source
}
