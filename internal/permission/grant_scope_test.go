package permission

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// grantFirst answers first 提示词 service raises, 和 reports
// 请求 it was answering.
func grantFirst(t *testing.T, svc Service, persistent bool) PermissionRequest {
	t.Helper()
	events := svc.Subscribe(t.Context())
	select {
	case ev := <-events:
		if persistent {
			svc.GrantPersistent(ev.Payload)
		} else {
			svc.Grant(ev.Payload)
		}
		return ev.Payload
	case <-time.After(5 * time.Second):
		t.Fatal("no permission request was published")
		return PermissionRequest{}
	}
}

// 一个 会话 grant taken 在 one command must 不 cover different one.
// working 目录 是 same 为 every shell command 在 会话, 所以
// without command 在 key, approving `ls` once would stand 在 为
// approving everything afterwards.
func TestSessionGrantDoesNotCoverADifferentCommand(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService("/work", nil, nil)
	events := svc.Subscribe(t.Context())

	benign := make(chan bool, 1)
	go func() {
		ok, _ := svc.Request(t.Context(), CreatePermissionRequest{
			SessionID: "s", ToolCallID: "c1", ToolName: "bash",
			Action: "execute", Description: "ls", Path: "/work",
			GrantKey: "ls",
		})
		benign <- ok
	}()

	select {
	case ev := <-events:
		require.True(t, svc.GrantPersistent(ev.Payload))
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt for the benign command")
	}
	require.True(t, <-benign)

	// same command again rides grant 和 must 不 提示词.
	repeat, err := svc.Request(t.Context(), CreatePermissionRequest{
		SessionID: "s", ToolCallID: "c2", ToolName: "bash",
		Action: "execute", Description: "ls", Path: "/work",
		GrantKey: "ls",
	})
	require.NoError(t, err)
	require.True(t, repeat, "the approved command should not prompt again")

	// 一个 different command 在 same 会话 和 目录 must 提示词.
	dangerous := make(chan bool, 1)
	go func() {
		ok, _ := svc.Request(t.Context(), CreatePermissionRequest{
			SessionID: "s", ToolCallID: "c3", ToolName: "bash",
			Action: "execute", Description: "sudo rm -rf /", Path: "/work",
			GrantKey: "sudo rm -rf /", Danger: "sudo",
		})
		dangerous <- ok
	}()

	select {
	case ev := <-events:
		require.Equal(t, "sudo", ev.Payload.Danger)
		svc.Deny(ev.Payload)
	case granted := <-dangerous:
		t.Fatalf("a different command rode the earlier grant without prompting (granted=%v)", granted)
	case <-time.After(5 * time.Second):
		t.Fatal("neither prompted nor returned")
	}
}

// 一个 resolution 是 recorded against 请求 service issued, 不
// against whatever answering 客户端 claims it was answering.
func TestGrantIgnoresFieldsSuppliedByTheAnswer(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService("/work", nil, nil)
	events := svc.Subscribe(t.Context())

	done := make(chan bool, 1)
	go func() {
		ok, _ := svc.Request(t.Context(), CreatePermissionRequest{
			SessionID: "s", ToolCallID: "c1", ToolName: "view",
			Action: "read", Description: "read notes.txt", Path: "/work/notes.txt",
		})
		done <- ok
	}()

	var issued PermissionRequest
	select {
	case ev := <-events:
		issued = ev.Payload
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt")
	}

	// Answer 用 right ID 但 wholly different, far broader claim
	// about what was being asked.
	forged := issued
	forged.ToolName = "bash"
	forged.Action = "execute"
	forged.Path = "/work"
	forged.GrantKey = ""
	require.True(t, svc.GrantPersistent(forged))
	require.True(t, <-done)

	// forged shape must 不 have been recorded, 所以 bash command 在
	// 那 目录 still prompts.
	bash := make(chan bool, 1)
	go func() {
		ok, _ := svc.Request(t.Context(), CreatePermissionRequest{
			SessionID: "s", ToolCallID: "c2", ToolName: "bash",
			Action: "execute", Description: "sudo rm -rf /", Path: "/work",
			GrantKey: "sudo rm -rf /",
		})
		bash <- ok
	}()

	select {
	case ev := <-events:
		svc.Deny(ev.Payload)
	case granted := <-bash:
		t.Fatalf("a grant was recorded from client-supplied fields (granted=%v)", granted)
	case <-time.After(5 * time.Second):
		t.Fatal("neither prompted nor returned")
	}
}

// outcome 通知 names 工具调用 service asked about, 所以
// mis-stated answer cannot dismiss different 调用 在 another 客户端's UI.
func TestNotificationUsesTheStoredToolCallID(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService("/work", nil, nil)
	events := svc.Subscribe(t.Context())
	notes := svc.SubscribeNotifications(t.Context())

	go func() {
		svc.Request(t.Context(), CreatePermissionRequest{
			SessionID: "s", ToolCallID: "real-call", ToolName: "view",
			Action: "read", Description: "read x", Path: "/work/x",
		})
	}()

	var issued PermissionRequest
	select {
	case ev := <-events:
		issued = ev.Payload
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt")
	}
	// Drain 请求 通知 published alongside 提示词.
	select {
	case <-notes:
	case <-time.After(5 * time.Second):
		t.Fatal("no request notification")
	}

	forged := issued
	forged.ToolCallID = "someone-elses-call"
	require.True(t, svc.Grant(forged))

	select {
	case n := <-notes:
		require.Equal(t, "real-call", n.Payload.ToolCallID,
			"the notification must name the call the service asked about")
	case <-time.After(5 * time.Second):
		t.Fatal("no resolution notification")
	}
}
