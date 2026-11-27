package backend_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestBackend_WorkspaceSkillsIsolation verifies 那 skill discovery
// state 和 SSE events 是 per-工作区, 不 process-global. Two
// workspaces 在 same backend process must 不 see each other's
// discoveries (either 在 their initial snapshot 或 在 subsequent
// PublishStates events).
func TestBackend_WorkspaceSkillsIsolation(t *testing.T) {
	// Isolate all 的 配置.Init's filesystem reads 从 host.
	// project-local .agents/skills/<name>/SKILL.md per working dir 是
	// what we actually want each 工作区 到 see; everything else
	// (global skills, XDG dirs, etc.) must 是 empty/deterministic.
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(hostHome, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(hostHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(hostHome, ".cache"))
	t.Setenv("CRUSH_SKILLS_DIR", t.TempDir())

	// Each 工作区 gets 它的 own working 目录 containing
	// distinct project-local skill 所以 discovery output differs.
	wdA := t.TempDir()
	wdB := t.TempDir()
	writeSkill(t, wdA, "wsa-only-skill", "Workspace A only skill.")
	writeSkill(t, wdB, "wsb-only-skill", "Workspace B only skill.")

	srvCfg, err := config.Init(t.Context(), wdA, "", false)
	require.NoError(t, err)
	b := backend.New(t.Context(), srvCfg, nil)

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	wsA, _, err := b.CreateWorkspace(proto.Workspace{
		ClientID: cidA,
		Path:     wdA,
		DataDir:  filepath.Join(wdA, ".crush"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.DeleteWorkspace(wsA.ID, cidA) })

	wsB, _, err := b.CreateWorkspace(proto.Workspace{
		ClientID: cidB,
		Path:     wdB,
		DataDir:  filepath.Join(wdB, ".crush"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.DeleteWorkspace(wsB.ID, cidB) })

	require.NotNil(t, wsA.Skills, "workspace A must have its own skills.Manager")
	require.NotNil(t, wsB.Skills, "workspace B must have its own skills.Manager")
	require.NotSame(t, wsA.Skills, wsB.Skills, "managers must be distinct instances per workspace")

	// Initial snapshots see each 工作区's own filesystem skill, 和
	// neither sees the other's.
	statesA := wsA.Skills.States()
	statesB := wsB.Skills.States()
	require.True(t, containsSkillName(statesA, "wsa-only-skill"),
		"workspace A snapshot missing its own skill")
	require.False(t, containsSkillName(statesA, "wsb-only-skill"),
		"workspace A snapshot leaked workspace B's skill")
	require.True(t, containsSkillName(statesB, "wsb-only-skill"),
		"workspace B snapshot missing its own skill")
	require.False(t, containsSkillName(statesB, "wsa-only-skill"),
		"workspace B snapshot leaked workspace A's skill")

	// Subscribe 到 each 工作区's SSE 事件 流.
	ctxA, cancelA := context.WithCancel(t.Context())
	t.Cleanup(cancelA)
	chA, err := b.SubscribeEvents(ctxA, wsA.ID)
	require.NoError(t, err)

	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)
	chB, err := b.SubscribeEvents(ctxB, wsB.ID)
	require.NoError(t, err)

	// Trigger republish 在 工作区 一个 只. marker name lets us
	// distinguish 这 事件 从 any incidental backend activity.
	const marker = "wsa-republish-marker"
	wsA.Skills.PublishStates([]*skills.SkillState{
		{Name: marker, State: skills.StateNormal},
	})

	// 工作区 一个 must receive 它的 own 事件.
	require.True(t,
		waitForSkillsEvent(t, chA, marker, 2*time.Second),
		"workspace A never received its own skills event")

	// 工作区 B must NOT receive 工作区 一个's 事件.
	require.False(t,
		waitForSkillsEvent(t, chB, marker, 250*time.Millisecond),
		"workspace B leaked workspace A's skills event")

	// And 一个's published states 是 now visible 在 它的 manager's
	// snapshot (verifies PublishStates updates 缓存, 不 just
	// 总线).
	updatedA := wsA.Skills.States()
	require.True(t, containsSkillName(updatedA, marker),
		"PublishStates must update Manager.States()")

	// B's manager snapshot 是 untouched.
	require.False(t, containsSkillName(wsB.Skills.States(), marker),
		"workspace B's Manager.States() leaked workspace A's republish")
}

func writeSkill(t *testing.T, workingDir, name, desc string) {
	t.Helper()
	skillDir := filepath.Join(workingDir, ".agents", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s\n", name, desc, desc)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
}

func containsSkillName(states []*skills.SkillState, name string) bool {
	for _, s := range states {
		if s.Name == name {
			return true
		}
	}
	return false
}

// waitForSkillsEvent drains given 事件 渠道 until either
// pubsub.事件[skills.事件] containing state named marker arrives 或
// 超时 elapses. Non-skills events 在 渠道 是 silently
// skipped — backend fans 在 many 事件 types 和 we 只 care
// about skills here.
func waitForSkillsEvent(t *testing.T, ch <-chan pubsub.Event[tea.Msg], marker string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			se, ok := ev.Payload.(pubsub.Event[skills.Event])
			if !ok {
				continue
			}
			if containsSkillName(se.Payload.States, marker) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
