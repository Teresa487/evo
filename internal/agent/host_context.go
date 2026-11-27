// host_context.go —— 把宿主应用状态和用户 notes 注入本轮 prompt。
//
// hostContextPrompt 生成 <host_context>，notesPrompt 生成 <user_notes>；
// 它们只随当前提问发送，不写进消息历史。

package agent

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/proto"
)

// notesPrompt tells 模型 what 用户 did since 它的 last turn, ahead
// 的 提示词. 它 是 "" without 备注.
func notesPrompt(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "<user_notes>\nThings the user did since your last message. Take them into account.\n" +
		strings.Join(notes, "\n\n") + "\n</user_notes>\n\n"
}

// hostContextPrompt tells 模型 what 用户 has 在 view 在 host
// application, ahead 的 提示词 it 是 about. 它 是 "" without host.
func hostContextPrompt(hc proto.HostContext) string {
	if hc.ActiveFile == "" && len(hc.OpenFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<host_context>\nThe user is working in an editor next to you. Use this to resolve what they mean by \"here\", \"this file\" or \"this line\".\n")
	if hc.ActiveFile != "" {
		fmt.Fprintf(&b, "Current file: %s", hc.ActiveFile)
		if hc.Line > 0 {
			fmt.Fprintf(&b, ", cursor on line %d", hc.Line)
		}
		b.WriteString("\n")
	}
	if len(hc.OpenFiles) > 0 {
		b.WriteString("Open files:\n")
		for _, f := range hc.OpenFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	b.WriteString("</host_context>\n\n")
	return b.String()
}
