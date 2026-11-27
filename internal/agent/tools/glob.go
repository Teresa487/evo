// glob.go —— glob 工具：按文件名模式查找文件。

package tools

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
)

const GlobToolName = "glob"

//go:embed glob.md.tpl
var globDescriptionTmpl []byte

var globDescriptionTpl = template.Must(
	template.New("globDescription").
		Parse(string(globDescriptionTmpl)),
)

type globDescriptionData struct {
	MaxResults int
}

func globDescription() string {
	return renderTemplate(globDescriptionTpl, globDescriptionData{
		MaxResults: 100,
	})
}

type GlobParams struct {
	Pattern string `json:"pattern" description:"The glob pattern to match files against"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

func NewGlobTool(workingDir string, cfg config.ToolGlob) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		GlobToolName,
		globDescription(),
		func(ctx context.Context, params GlobParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := cmp.Or(params.Path, workingDir)

			// Bound search 所以 huge 或 symlink-heavy root (e.g. $HOME
			// 或 module 缓存) fails cleanly instead 的 pinning CPU
			// 和 hanging agent.
			searchCtx, cancel := context.WithTimeout(ctx, cfg.GetTimeout())
			defer cancel()

			files, truncated, err := globFiles(searchCtx, params.Pattern, searchPath, 100)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error finding files: %v", err)), nil
			}

			var output string
			if len(files) == 0 {
				output = "No files found"
			} else {
				normalizeFilePaths(files)
				output = strings.Join(files, "\n")
				if truncated {
					output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				GlobResponseMetadata{
					NumberOfFiles: len(files),
					Truncated:     truncated,
				},
			), nil
		},
	)
}

func globFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	// Scope walk 到 pattern's literal 目录 prefix. 一个 pattern
	// like "internal/agent/*.go" 只 needs 到 walk "internal/agent", 所以 we
	// start there instead 的 enumerating entire tree 和 filtering.
	// Patterns 那 begin 用 wildcard (e.g. "**/foo.go") have no prefix
	// 和 still walk 从 searchPath.
	prefix, rest := filepathext.SplitGlobPrefix(pattern)
	walkRoot := searchPath
	walkPattern := pattern
	if prefix != "" {
		walkRoot = filepath.Join(searchPath, prefix)
		walkPattern = rest
	}

	cmdRg := getRgCmd(ctx, walkPattern)
	if cmdRg != nil {
		cmdRg.Dir = walkRoot
		matches, err := runRipgrep(cmdRg, walkRoot, limit)
		if err == nil {
			return matches, len(matches) >= limit && limit > 0, nil
		}
		slog.Warn("Ripgrep execution failed, falling back to doublestar", "error", err)
	}

	return fsext.GlobGitignoreAwareCtx(ctx, walkPattern, walkRoot, limit)
}

func runRipgrep(cmd *exec.Cmd, searchRoot string, limit int) ([]string, error) {
	// 流 ripgrep's stdout instead 的 buffering whole 文件 list.
	// Over huge root (e.g. $HOME) full --files listing can 是
	// hundreds 的 MB; reading it all 在 once 和 then sorting allocated
	// gigabytes. We 读取 incrementally 和 stop once we have bounded
	// pool 的 candidates.
	//
	// We collect more than `limit` 所以 shortest-路径 preference below
	// still has something 到 choose 从, 但 pool 是 capped 所以 memory
	// stays small ( few thousand paths) no matter how large tree 是.
	candidatePool := max(limit*20, 1000)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ripgrep: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ripgrep: %w", err)
	}

	var matches []string
	reader := bufio.NewReader(stdout)
	for {
		path, err := reader.ReadString(0)
		if len(path) > 0 {
			path = strings.TrimRight(path, "\x00")
			if path != "" {
				absPath := filepathext.SmartJoin(searchRoot, path)
				if !fsext.SkipHidden(absPath) {
					matches = append(matches, absPath)
				}
			}
		}
		if err != nil {
			break // EOF or read error; drain handled by Wait below.
		}
		if len(matches) >= candidatePool {
			// Enough candidates; stop reading 和 let process 是
			// killed 通过 command 上下文 / Wait. Draining rest
			// would just 缓冲 paths we 是 going 到 discard.
			break
		}
	}

	// Close our end 所以 ripgrep gets SIGPIPE 和 stops, then reap it.
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if waitErr != nil && len(matches) == 0 {
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // No matches.
		}
		return nil, fmt.Errorf("ripgrep: %w\n%s", waitErr, stderr.String())
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i]) < len(matches[j])
	})

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func normalizeFilePaths(paths []string) {
	for i, p := range paths {
		paths[i] = filepath.ToSlash(p)
	}
}
