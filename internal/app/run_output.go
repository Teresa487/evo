// run_output.go —— 非交互运行时的输出与 spinner 处理。

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// messageLister lists 存储 messages 的 会话.
type messageLister interface {
	List(ctx context.Context, sessionID string) ([]message.Message, error)
}

// answerPrinter writes 助手's replies 在 one 会话 到
// output 的 non-interactive 运行. As reply grows, 只 它的 new 文本 是
// written.
type answerPrinter struct {
	out       io.Writer
	sessionID string
	// replies holds what has been seen 的 each reply, 通过 消息 ID.
	replies map[string]replyState
	// printed 是 set once any 文本 has been written.
	printed bool
}

// replyState 是 what has been seen 的 one reply.
type replyState struct {
	// content 是 reply's 文本 作为 it was last seen.
	content string
	// shown 是 set once some 的 content has been written, 或 当
	// reply was 存储 before 运行 started.
	shown bool
}

// newAnswerPrinter returns printer 为 运行 在 given 会话. In
// continued 会话, replies already 存储 count 作为 printed, 所以 they
// 是 不 repeated. 一个 new 会话 has no 存储 replies, 所以 存储 是
// 不 读取.
func newAnswerPrinter(ctx context.Context, out io.Writer, sessionID string, messages messageLister, continued bool) (*answerPrinter, error) {
	p := &answerPrinter{out: out, sessionID: sessionID, replies: make(map[string]replyState)}
	if !continued {
		return p, nil
	}
	existing, err := messages.List(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list session messages: %w", err)
	}
	for _, msg := range existing {
		if p.isReply(msg) {
			p.replies[msg.ID] = replyState{content: msg.Content().String(), shown: true}
		}
	}
	return p, nil
}

// isReply reports whether msg 是 助手消息 在 printer's
// 会话 那 has 在 least one 片段 和 是 不 压缩 摘要. Its
// 文本 may still 是 empty, 作为 it 是 为 消息 那 只 calls tools.
func (p *answerPrinter) isReply(msg message.Message) bool {
	return msg.SessionID == p.sessionID &&
		msg.Role == message.Assistant &&
		!msg.IsSummaryMessage &&
		len(msg.Parts) > 0
}

// 写入 prints 文本 那 msg adds 到 what has been seen 的 reply.
// 一个 reply's 文本 normally 只 grows. 文本 那 是 prefix 的 what was
// seen 是 treated 作为 stale 更新 和 adds nothing. 文本 那 no longer
// starts 用 what was seen has been replaced, which happens 当
// 提供方 请求 是 retried after 片段 的 reply has streamed.
// new 文本 是 printed 在 full, 在 new line 如果 old 文本 was shown.
func (p *answerPrinter) write(msg message.Message) {
	if !p.isReply(msg) {
		return
	}
	content := msg.Content().String()
	st := p.replies[msg.ID]

	part, ok := strings.CutPrefix(content, st.content)
	switch {
	case ok:
	case strings.HasPrefix(st.content, content):
		return
	default:
		if st.shown {
			fmt.Fprintln(p.out)
		}
		part, st = content, replyState{}
	}

	// Trim leading whitespace. Sometimes the LLM includes leading
	// formatting 和 indentation, which we don't want here. Until
	// something has been printed, leading newlines 是 trimmed too.
	switch {
	case !p.printed:
		part = strings.TrimLeft(part, " \t\r\n")
	case !st.shown:
		part = strings.TrimLeft(part, " \t")
	}
	// Ignore initial whitespace-只 messages.
	if p.printed || strings.TrimSpace(part) != "" {
		p.printed = true
		st.shown = st.shown || part != ""
		fmt.Fprint(p.out, part)
	}
	st.content = content
	p.replies[msg.ID] = st
}

// 刷盘 prints whatever 消息 events have 不 delivered. agent
// stores every 消息 before it reports 那 it 是 done, 但 events
// carrying last updates may still 是 queued 在 那 point, 所以
// rest 是 读取 从 存储.
func (p *answerPrinter) flush(ctx context.Context, messages messageLister) {
	msgs, err := messages.List(ctx, p.sessionID)
	if err != nil {
		if ctx.Err() != nil {
			slog.Debug("Non-interactive: run ended before its output was finished", "session_id", p.sessionID, "error", err)
		} else {
			slog.Warn("Non-interactive: could not read the session's messages; the output may be incomplete", "session_id", p.sessionID, "error", err)
		}
		return
	}
	for _, msg := range msgs {
		p.write(msg)
	}
}

// printUntilDone prints 会话's replies 作为 their events arrive,
// until agent sends 它的 结果 在 done. 它 then prints 从 存储
// whatever events did 不 投递. beforeWait runs before each wait,
// 和 stopSpinner runs 当 reply arrives 或 运行 ends.
func (p *answerPrinter) printUntilDone(
	ctx context.Context,
	events <-chan pubsub.Event[message.Message],
	done <-chan error,
	messages messageLister,
	beforeWait, stopSpinner func(),
) error {
	for {
		beforeWait()
		select {
		case err := <-done:
			stopSpinner()
			p.flush(ctx, messages)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrRequestCancelled) {
					slog.Debug("Non-interactive: agent processing cancelled", "session_id", p.sessionID)
					return nil
				}
				return fmt.Errorf("agent processing failed: %w", err)
			}
			return nil

		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if p.isReply(event.Payload) {
				stopSpinner()
			}
			p.write(event.Payload)

		case <-ctx.Done():
			stopSpinner()
			return ctx.Err()
		}
	}
}
