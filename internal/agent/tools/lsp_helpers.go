package tools

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// resolvedSymbol holds 结果 的 resolving symbol name 到 LSP position.
type resolvedSymbol struct {
	client *lsp.Client
	path   string
	line   int
	char   int
}

// resolveSymbol greps 为 symbol name, triggers lazy LSP startup, 和
// returns first match position 那 running LSP 客户端 confirms
// 是 valid identifier. Matches inside comments 或 strings 是 skipped
// automatically because LSP 将 reject them.
func resolveSymbol(ctx context.Context, lspManager *lsp.Manager, symbol, workingDir string) (*resolvedSymbol, error) {
	results, err := resolveSymbolResults(ctx, lspManager, symbol, workingDir)
	if err != nil {
		return nil, err
	}

	// Try each candidate until the LSP confirms it's a real identifier.
	// 这 filters out grep matches 在 comments, strings, 或 partial
	// identifiers 那 slipped past word-boundary filter.
	for _, r := range results {
		_, err := r.client.Definition(ctx, r.path, r.line, r.char)
		if err == nil || !isNoIdentifierError(err) {
			return r, nil
		}
	}
	// All candidates were rejected 通过 LSP; return first one
	// 所以 caller gets meaningful 错误 从 their own LSP 调用.
	return results[0], nil
}

// resolveSymbolResults greps 为 symbol 和 returns all viable
// {客户端, 路径, position} tuples. Callers 那 need just one match
// (definition, rename, 调用 hierarchy) use resolveSymbol; callers 那
// want 到 iterate all matches (references) use 这 directly.
func resolveSymbolResults(ctx context.Context, lspManager *lsp.Manager, symbol, workingDir string) ([]*resolvedSymbol, error) {
	lspManager.Start(ctx, workingDir)

	// Use word boundaries 到 avoid matching inside larger identifiers
	// (e.g. "Bar" inside "myBar"). symbol 是 already QuoteMeta'd
	// 所以 dots 和 other regex metacharacters 是 escaped.
	pattern := `\b` + regexp.QuoteMeta(symbol) + `\b`
	matches, _, err := searchFiles(ctx, pattern, workingDir, "", 100)
	if err != nil {
		return nil, fmt.Errorf("failed to search for symbol: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("symbol '%s' not found in grep results", symbol)
	}

	var results []*resolvedSymbol
	for _, match := range matches {
		absPath, err := filepath.Abs(match.path)
		if err != nil {
			continue
		}

		client := findLSPClient(lspManager, absPath)
		if client == nil {
			continue
		}

		results = append(results, &resolvedSymbol{
			client: client,
			path:   absPath,
			line:   match.lineNum,
			char:   match.charNum + getSymbolOffset(symbol),
		})
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no LSP client handles any file matching '%s'", symbol)
	}
	return results, nil
}

// findLSPClient returns first LSP 客户端 那 handles given 文件 路径.
func findLSPClient(lspManager *lsp.Manager, filePath string) *lsp.Client {
	if abs, err := filepath.Abs(filePath); err == nil {
		filePath = abs
	}
	for c := range lspManager.Clients().Seq() {
		if c.HandlesFile(filePath) {
			return c
		}
	}
	return nil
}

// collectAffectedFiles extracts all unique 文件 paths 从 WorkspaceEdit.
func collectAffectedFiles(edit *protocol.WorkspaceEdit) []string {
	seen := make(map[string]struct{})
	var files []string

	for uri := range edit.Changes {
		path, err := uri.Path()
		if err != nil {
			continue
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	addURI := func(uri protocol.DocumentURI) {
		path, err := uri.Path()
		if err != nil {
			return
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	for _, change := range edit.DocumentChanges {
		switch {
		case change.TextDocumentEdit != nil:
			addURI(change.TextDocumentEdit.TextDocument.URI)
		case change.CreateFile != nil:
			addURI(change.CreateFile.URI)
		case change.RenameFile != nil:
			addURI(change.RenameFile.OldURI)
			addURI(change.RenameFile.NewURI)
		case change.DeleteFile != nil:
			addURI(change.DeleteFile.URI)
		}
	}

	return files
}

// isNoIdentifierError checks 如果 错误 indicates grep match was 不
// actually identifier (e.g., matched inside comment 或 string).
func isNoIdentifierError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no identifier found")
}

// getSymbolOffset returns character offset 到 actual symbol name
// 在 qualified symbol (e.g., "Bar" 在 "foo.Bar" 或 "method" 在 "Class::method").
func getSymbolOffset(symbol string) int {
	if idx := strings.LastIndex(symbol, "::"); idx != -1 {
		return idx + 2
	}
	if idx := strings.LastIndex(symbol, "."); idx != -1 {
		return idx + 1
	}
	if idx := strings.LastIndex(symbol, "\\"); idx != -1 {
		return idx + 1
	}
	return 0
}

// cleanupLocations deduplicates 和 sorts slice 的 LSP locations.
func cleanupLocations(locations []protocol.Location) []protocol.Location {
	slices.SortFunc(locations, func(a, b protocol.Location) int {
		if a.URI != b.URI {
			return strings.Compare(string(a.URI), string(b.URI))
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return cmp.Compare(a.Range.Start.Line, b.Range.Start.Line)
		}
		return cmp.Compare(a.Range.Start.Character, b.Range.Start.Character)
	})
	return slices.CompactFunc(locations, func(a, b protocol.Location) bool {
		return a.URI == b.URI &&
			a.Range.Start.Line == b.Range.Start.Line &&
			a.Range.Start.Character == b.Range.Start.Character
	})
}
