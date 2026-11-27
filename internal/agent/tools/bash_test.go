package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) InheritAutoApproval(parentSessionID, childSessionID string) {}

func (m *mockBashPermissionService) ActiveRequests() []permission.PermissionRequest {
	return nil
}

func (m *mockBashPermissionService) SetSessionScope(scope permission.SessionScope) {}

func (m *mockBashPermissionService) SetSkipRequests(skip bool) {}

func (m *mockBashPermissionService) SkipRequests() bool {
	return false
}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *mockBashPermissionService) SetPermissionMode(mode permission.PermissionMode) {}

func (m *mockBashPermissionService) PermissionMode() permission.PermissionMode {
	return permission.PermissionModeNormal
}

func (m *mockBashPermissionService) SubscribeModeChanges(ctx context.Context) <-chan pubsub.Event[permission.ModeChangedEvent] {
	return make(<-chan pubsub.Event[permission.ModeChangedEvent])
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to background")

	bgManager := shell.GetBackgroundShellManager()
	require.NoError(t, bgManager.Kill(t.Context(), meta.ShellID))
}

type recordingPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requestCount int
	allow        bool
	mode         permission.PermissionMode
	lastDanger   string
}

func (m *recordingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	m.requestCount++
	m.lastDanger = req.Danger
	return m.allow, nil
}

func (m *recordingPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *recordingPermissionService) AutoApproveSession(sessionID string) {}

func (m *recordingPermissionService) InheritAutoApproval(parentSessionID, childSessionID string) {}

func (m *recordingPermissionService) ActiveRequests() []permission.PermissionRequest {
	return nil
}

func (m *recordingPermissionService) SetSessionScope(scope permission.SessionScope) {}

func (m *recordingPermissionService) SetSkipRequests(skip bool) {}

func (m *recordingPermissionService) SkipRequests() bool {
	return false
}

func (m *recordingPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *recordingPermissionService) SetPermissionMode(mode permission.PermissionMode) { m.mode = mode }

func (m *recordingPermissionService) PermissionMode() permission.PermissionMode { return m.mode }

func (m *recordingPermissionService) SubscribeModeChanges(ctx context.Context) <-chan pubsub.Event[permission.ModeChangedEvent] {
	return make(<-chan pubsub.Event[permission.ModeChangedEvent])
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(permissions, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil), perms
}

func TestBashTool_ChainedCommandsRequirePermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// 一个 chain 是 judged 通过 它的 parts, 所以 joining two 读取-只 commands 是
	// no more 的 提示词 than either one alone would 是.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls",
		Command:     "ls && echo done",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "a chain of read-only commands should not prompt")

	// One 片段 那 writes 是 enough 到 require approval 为 whole chain.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "chained touch",
		Command:     "ls && touch newfile",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "a chain containing a write must prompt")

	// Plain ls should NOT trigger 权限 check.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "plain ls",
		Command:     "ls -la",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "plain ls should not trigger permission request")
}

func TestBashTool_ChainedCommandsDenied(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls denied",
		Command:     "ls && rm -rf /",
	})

	require.Equal(t, 1, perms.requestCount)
	require.Contains(t, resp.Content, "User denied permission")
}

func TestIsDangerousCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		dangerous bool
	}{
		{
			name:      "simple banned command - curl",
			command:   "curl https://example.com",
			dangerous: true,
		},
		{
			name:      "simple banned command - sudo",
			command:   "sudo apt-get update",
			dangerous: true,
		},
		{
			name:      "npm global install with --global",
			command:   "npm install --global typescript",
			dangerous: true,
		},
		{
			name:      "npm global install with -g",
			command:   "npm install -g typescript",
			dangerous: true,
		},
		{
			name:      "npm local install",
			command:   "npm install typescript",
			dangerous: false,
		},
		{
			name:      "go test with -exec",
			command:   "go test -exec ./malicious ./...",
			dangerous: true,
		},
		{
			name:      "go test without -exec",
			command:   "go test ./...",
			dangerous: false,
		},
		{
			name:      "safe command - ls",
			command:   "ls -la",
			dangerous: false,
		},
		{
			name:      "safe command - echo",
			command:   "echo hello",
			dangerous: false,
		},
		{
			name:      "safe command - git",
			command:   "git status",
			dangerous: false,
		},
		{
			name:      "pip install with --user",
			command:   "pip install --user requests",
			dangerous: true,
		},
		{
			name:      "pip install without --user",
			command:   "pip install requests",
			dangerous: false,
		},
		{
			name:      "brew install",
			command:   "brew install wget",
			dangerous: true,
		},
		{
			// Quoting command name must 不 evade detection.
			name:      "quoted dangerous command name",
			command:   `"brew" install wget`,
			dangerous: true,
		},
		{
			// Quoted arguments must still 是 seen 通过 argument blockers.
			name:      "quoted dangerous argument",
			command:   `go test "-exec" ./malicious ./...`,
			dangerous: true,
		},
		{
			// Command substitution can't 是 resolved without executing, 所以
			// it 是 treated conservatively 作为 dangerous.
			name:      "command substitution is conservative",
			command:   "$(echo brew) install wget",
			dangerous: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shell.IsCommandBlocked(tt.command, blockFuncs(defaultBlockedCommands))
			require.Equal(t, tt.dangerous, result, "command: %s", tt.command)
		})
	}
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters 是 2 cells wide; 这 string 是 far wider than
	// MaxOutputLength 所以 TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content, t.TempDir()))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji 用 ZWJ sequences should 不 是 split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestResolveBlockedCommands(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses the defaults", func(t *testing.T) {
		t.Parallel()
		require.ElementsMatch(t, defaultBlockedCommands, resolveBlockedCommands(nil))
	})

	t.Run("blocked_commands adds to the defaults", func(t *testing.T) {
		t.Parallel()
		got := resolveBlockedCommands(&config.Permissions{
			BlockedCommands: []string{"kubectl", "terraform"},
		})
		require.Contains(t, got, "kubectl")
		require.Contains(t, got, "terraform")
		require.Contains(t, got, "curl", "defaults must still apply")
	})

	t.Run("allowed_commands removes defaults", func(t *testing.T) {
		t.Parallel()
		got := resolveBlockedCommands(&config.Permissions{
			AllowedCommands: []string{"curl", "wget"},
		})
		require.NotContains(t, got, "curl")
		require.NotContains(t, got, "wget")
		require.Contains(t, got, "sudo", "unrelated defaults must stay")
	})

	t.Run("allowed_commands wins over blocked_commands", func(t *testing.T) {
		t.Parallel()
		got := resolveBlockedCommands(&config.Permissions{
			BlockedCommands: []string{"kubectl"},
			AllowedCommands: []string{"kubectl"},
		})
		require.NotContains(t, got, "kubectl")
	})

	t.Run("built-in order is preserved and duplicates collapsed", func(t *testing.T) {
		t.Parallel()
		got := resolveBlockedCommands(&config.Permissions{
			BlockedCommands: []string{"curl", "kubectl", "kubectl"},
		})
		// list 是 embedded verbatim 在 工具 description, 所以
		// defaults must keep their hand-grouped order 和 additions go last.
		require.Equal(t, defaultBlockedCommands, got[:len(defaultBlockedCommands)])
		require.Equal(t, []string{"kubectl"}, got[len(defaultBlockedCommands):],
			"additions append once; a duplicate of a default is dropped")
	})

	t.Run("unblocking a command stops it reading as dangerous", func(t *testing.T) {
		t.Parallel()
		blocked := resolveBlockedCommands(&config.Permissions{AllowedCommands: []string{"curl"}})
		require.False(t, shell.IsCommandBlocked("curl https://example.com", blockFuncs(blocked)))
		require.True(t, shell.IsCommandBlocked("sudo rm -rf /", blockFuncs(blocked)))
	})
}

// TestBashTool_ApprovedDangerousCommandRuns pins rule 那 command
// 用户 was warned about 和 approved anyway reaches shell unguarded:
// re-blocking it would override the answer they just gave.
func TestBashTool_ApprovedDangerousCommandRuns(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeNormal,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// `ifconfig` 是 在 默认 dangerous list, 所以 it 是 prompted 为.
	// Approving it must 不 then 是 overridden 通过 exec-time block list.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "approved dangerous command",
		Command:     "ifconfig --help",
	})

	require.Equal(t, 1, perms.requestCount, "a dangerous command must be prompted for")
	require.Equal(t, "ifconfig", perms.lastDanger,
		"the prompt must name what tripped the check")
	require.NotContains(t, resp.Content, "is a dangerous command",
		"an approved command must not be re-blocked at exec time")
}

// TestBashTool_UnflaggedCommandKeepsBlockList 是 other side 的 那 rule.
// static check expands commands 用 globbing off, 所以 glob hides
// dangerous name 从 it 和 yolo mode auto-approves 调用 unseen. block
// list stays 在 在 exec time, where glob has resolved 和 real command
// name 是 finally visible.
func TestBashTool_UnflaggedCommandKeepsBlockList(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "ifconfig"), []byte("#!/bin/sh\n"), 0o755))
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeYolo,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "indirect dangerous command",
		Command:     "ifconf?g --help",
	})

	require.Empty(t, perms.lastDanger, "the static check cannot see through a glob")
	require.Contains(t, resp.Content, "is a dangerous command")
}

// TestBashTool_SysadminModeSkipsBlockList pins sysadmin mode 作为 deliberate
// exception: it asks 为 nothing 和 blocks nothing.
func TestBashTool_SysadminModeSkipsBlockList(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeSysadmin,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "ifconfig"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "indirect dangerous command",
		Command:     "ifconf?g --help",
	})

	require.NotContains(t, resp.Content, "is a dangerous command")
}

// TestBashTool_DeniedDangerousCommandDoesNotRun 是 other half: denial 在
// 权限 layer stops command, no block list required.
func TestBashTool_DeniedDangerousCommandDoesNotRun(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  false,
		mode:   permission.PermissionModeNormal,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "denied dangerous command",
		Command:     "ifconfig --help",
	})

	require.Equal(t, 1, perms.requestCount)
	require.True(t, resp.IsError)
}

// TestBashTool_SafeReadOnlyPathStillEnforcesBlockList pins one branch 那
// never asks: safe-prefix command skips 权限 请求 entirely, 所以
// block list stays 在 there 作为 backstop.
func TestBashTool_SafeReadOnlyPathStillEnforcesBlockList(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeNormal,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "safe command",
		Command:     "pwd",
	})

	require.Zero(t, perms.requestCount, "safe read-only commands must not prompt")
	require.False(t, resp.IsError)
}

// newModeBashTool builds bash 工具 在 real 权限 service 在 given mode.
func newModeBashTool(t *testing.T, mode permission.PermissionMode) (fantasy.AgentTool, permission.Service, context.Context, string) {
	t.Helper()
	workingDir := t.TempDir()
	perms := permission.NewPermissionService(workingDir, nil, nil)
	perms.SetPermissionMode(mode)
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	return tool, perms, ctx, workingDir
}

func TestBashTool_YoloModeBlocksDangerousCommandsWithoutPrompting(t *testing.T) {
	tool, perms, ctx, workingDir := newModeBashTool(t, permission.PermissionModeYolo)
	prompts := perms.Subscribe(t.Context())

	// Off 测试 goroutine, 所以 reinstated 提示词 fails fast instead 的 hanging.
	type result struct {
		content string
		isError bool
	}
	done := make(chan result, 1)
	go func() {
		resp := runBashTool(t, tool, ctx, BashParams{
			Description: "dangerous command in yolo mode",
			Command:     "touch ran && ifconfig --help",
		})
		done <- result{resp.Content, resp.IsError}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("yolo mode blocked waiting for a permission prompt that should never have been raised")
	}

	require.True(t, got.isError)
	require.Contains(t, got.content, "ifconfig is a dangerous command")
	require.Contains(t, got.content, "sysadmin mode", "the refusal must name the way out")
	require.NoFileExists(t, filepath.Join(workingDir, "ran"), "nothing on a refused line may run")

	select {
	case p := <-prompts:
		t.Fatalf("yolo mode must not prompt, but got a request for %q", p.Payload.Description)
	default:
	}
}

func TestBashTool_YoloModeRunsOrdinaryCommands(t *testing.T) {
	tool, _, ctx, workingDir := newModeBashTool(t, permission.PermissionModeYolo)

	resp := runBashTool(t, tool, ctx, BashParams{Description: "ordinary command", Command: "touch ran"})

	require.False(t, resp.IsError, resp.Content)
	require.FileExists(t, filepath.Join(workingDir, "ran"))
}

func TestBashTool_SysadminModeRunsDangerousCommands(t *testing.T) {
	tool, _, ctx, workingDir := newModeBashTool(t, permission.PermissionModeSysadmin)
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "ifconfig"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	resp := runBashTool(t, tool, ctx, BashParams{Description: "dangerous command", Command: "./ifconfig --help"})

	require.NotContains(t, resp.Content, "is a dangerous command")
}

// TestBashTool_NormalModeStillPromptsForDangerousCommands 是 other half 的
// rule: manual mode 保持 asking.
func TestBashTool_NormalModeStillPromptsForDangerousCommands(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeNormal,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")

	runBashTool(t, tool, ctx, BashParams{
		Description: "dangerous command in normal mode",
		Command:     "ifconfig --help",
	})

	require.Equal(t, 1, perms.requestCount, "normal mode must still ask")
	require.Equal(t, "ifconfig", perms.lastDanger,
		"the prompt must name what tripped the check")
}

// TestBlockedCommandErrorNamesTheCommand checks 那 command stopped 在
// exec time names command 那 was recognised. static check 和
// 运行-time check have 到 agree about 那, 或 same command 是 described
// two different ways depending 在 which one caught it.
func TestBlockedCommandErrorNamesTheCommand(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "ifconfig"), []byte("#!/bin/sh\n"), 0o755))
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
		mode:   permission.PermissionModeYolo,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, shell.CoreutilsTurtle, nil, nil, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "indirect dangerous command",
		Command:     "ifconf?g --help",
	})

	require.Contains(t, resp.Content, "ifconfig is a dangerous command",
		"the error must name the command, not the spelling that reached it")
}
