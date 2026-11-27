// agentic_fetch_tool.go —— agentic_fetch 工具：让子 agent 抓取并总结URL内容。
//
// 从产物反推：模型调用该工具 → 校验参数 → 起一个子 agent 执行抓取总结。

package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed templates/agentic_fetch.md
var agenticFetchToolDescription string

// agenticFetchValidationResult holds validated parameters 从 工具调用 上下文.
type agenticFetchValidationResult struct {
	SessionID      string
	AgentMessageID string
}

// validateAgenticFetchParams validates 工具调用 parameters 和 extracts required 上下文 values.
func validateAgenticFetchParams(ctx context.Context, params tools.AgenticFetchParams) (agenticFetchValidationResult, error) {
	if params.Prompt == "" {
		return agenticFetchValidationResult{}, errors.New("prompt is required")
	}

	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return agenticFetchValidationResult{}, errors.New("session id missing from context")
	}

	agentMessageID := tools.GetMessageFromContext(ctx)
	if agentMessageID == "" {
		return agenticFetchValidationResult{}, errors.New("agent message id missing from context")
	}

	return agenticFetchValidationResult{
		SessionID:      sessionID,
		AgentMessageID: agentMessageID,
	}, nil
}

//go:embed templates/agentic_fetch_prompt.md.tpl
var agenticFetchPromptTmpl []byte

func (c *coordinator) agenticFetchTool(_ context.Context, client *http.Client) (fantasy.AgentTool, error) {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		tools.AgenticFetchToolName,
		agenticFetchToolDescription,
		func(ctx context.Context, params tools.AgenticFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			validationResult, err := validateAgenticFetchParams(ctx, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Determine description based 在 mode.
			var description string
			if params.URL != "" {
				description = fmt.Sprintf("Fetch and analyze content from URL: %s", params.URL)
			} else {
				description = "Search the web and analyze results"
			}

			p, err := c.permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   validationResult.SessionID,
					Path:        c.cfg.WorkingDir(),
					ToolCallID:  call.ID,
					ToolName:    tools.AgenticFetchToolName,
					Action:      "fetch",
					Description: description,
					Params:      tools.AgenticFetchPermissionsParams(params),
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return tools.NewPermissionDeniedResponse(), nil
			}

			tmpDir, err := os.MkdirTemp(c.cfg.Config().Options.DataDirectory, "crush-fetch-*")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary directory: %s", err)), nil
			}
			defer os.RemoveAll(tmpDir)

			var fullPrompt string

			if params.URL != "" {
				// URL mode: fetch the URL content first.
				content, err := tools.FetchURLAndConvert(ctx, client, params.URL)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to fetch URL: %s", err)), nil
				}

				hasLargeContent := len(content) > tools.LargeContentThreshold

				if hasLargeContent {
					tempFile, err := os.CreateTemp(tmpDir, "page-*.md")
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary file: %s", err)), nil
					}
					tempFilePath := tempFile.Name()

					if _, err := tempFile.WriteString(content); err != nil {
						tempFile.Close()
						return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to write content to file: %s", err)), nil
					}
					tempFile.Close()

					fullPrompt = fmt.Sprintf("%s\n\nThe web page from %s has been saved to: %s\n\nUse the view and grep tools to analyze this file and extract the requested information.", params.Prompt, params.URL, tempFilePath)
				} else {
					fullPrompt = fmt.Sprintf("%s\n\nWeb page URL: %s\n\n<webpage_content>\n%s\n</webpage_content>", params.Prompt, params.URL, content)
				}
			} else {
				// Search mode: let 子代理 search 和 fetch 作为 needed.
				fullPrompt = fmt.Sprintf("%s\n\nUse the web_search tool to find relevant information. Break down the question into smaller, focused searches if needed. After searching, use web_fetch to get detailed content from the most relevant results.", params.Prompt)
			}

			promptOpts := []prompt.Option{
				prompt.WithWorkingDir(tmpDir),
			}

			promptTemplate, err := prompt.NewPrompt("agentic_fetch", string(agenticFetchPromptTmpl), promptOpts...)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating prompt: %s", err)
			}

			small := c.smallOnlyModels().Small

			systemPrompt, err := promptTemplate.Build(ctx, small.Model.Provider(), small.Model.Model(), c.cfg)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error building system prompt: %s", err)
			}

			smallProviderCfg, ok := c.cfg.Config().Providers.Get(small.ModelCfg.Provider)
			if !ok {
				return fantasy.ToolResponse{}, errors.New("small model provider not configured")
			}

			webFetchTool := tools.NewWebFetchTool(tmpDir, client)
			webSearchTool := tools.NewWebSearchTool(client)
			fetchTools := []fantasy.AgentTool{
				webFetchTool,
				webSearchTool,
				tools.NewGlobTool(tmpDir, c.cfg.Config().Tools.Glob),
				tools.NewGrepTool(tmpDir, c.cfg.Config().Tools.Grep),
				tools.NewSourcegraphTool(client),
				tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, nil, tmpDir),
			}

			// 子代理 tools 运行 without 钩子 interception. top-level
			// `agentic_fetch` 调用 itself 是 already wrapped 从 coder's
			// side; firing hooks again 为 every inner 工具调用 would 运行
			// 用户's hooks N times per delegated turn.

			agent := NewSessionAgent(SessionAgentOptions{
				Models:               c.smallOnlyModels,
				SystemPromptPrefix:   smallProviderCfg.SystemPromptPrefix,
				SystemPrompt:         systemPrompt,
				Cfg:                  c.cfg,
				Prompts:              c.promptTexts,
				DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
				Sessions:             c.sessions,
				Messages:             c.messages,
				Tools:                fetchTools,
				titles:               &c.titles,
			})

			return c.runSubAgentMaybeBackground(ctx, tools.AgenticFetchToolName, accessRead, subAgentParams{
				Agent:          agent,
				SessionID:      validationResult.SessionID,
				AgentMessageID: validationResult.AgentMessageID,
				ToolCallID:     call.ID,
				Prompt:         fullPrompt,
				SessionTitle:   "Fetch Analysis",
				SessionSetup: func(sessionID string) {
					c.permissions.AutoApproveSession(sessionID)
				},
			})
		},
	), nil
}
