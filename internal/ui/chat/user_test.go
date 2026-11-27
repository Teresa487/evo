package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// newTestChannelUserItem builds UserMessageItem carrying 文本 和 creation
// timestamp, 用 real attachments renderer, 为 渠道 render tests.
func newTestChannelUserItem(text string, createdAt int64) *UserMessageItem {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:        "test-id",
		Role:      message.User,
		CreatedAt: createdAt,
		Parts:     []message.ContentPart{message.TextContent{Text: text}},
	}
	r := attachments.NewRenderer(sty.Attachments)
	return NewUserMessageItem(&sty, msg, r).(*UserMessageItem)
}

func newTestChannelInfoItem(text string, createdAt int64) *ChannelInfoItem {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:        "test-id",
		Role:      message.User,
		CreatedAt: createdAt,
		Parts:     []message.ContentPart{message.TextContent{Text: text}},
	}
	return NewChannelInfoItem(&sty, msg).(*ChannelInfoItem)
}

// --- UserMessageItem body tests (body 只, no metadata) ---

// TestRawRender_ChannelMessageBody verifies 那 UserMessageItem 为
// 渠道 消息 renders 只 body content, 不 metadata line.
func TestRawRender_ChannelMessageBody(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="+15551234567" sender_name="Alice" time="14:30">Hello from Signal!</channel>`

	item := newTestChannelUserItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	// Body should 是 rendered 作为 markdown, 不 raw XML.
	require.Contains(t, out, "Hello from Signal!")

	// Raw XML tags must 不 是 visible.
	require.NotContains(t, out, "<channel")
	require.NotContains(t, out, "</channel>")

	// Metadata must NOT appear 在 body — it 是 rendered 通过 ChannelInfoItem.
	require.NotContains(t, out, "via signal")
	require.NotContains(t, out, "at 14:30")
}

// TestRawRender_ChannelMessageSourceOnlyBody verifies 那 消息 用
// 只 source renders just body 在 UserMessageItem.
func TestRawRender_ChannelMessageSourceOnlyBody(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal">Just the source, no sender or time.</channel>`

	item := newTestChannelUserItem(text, 1752456000)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "Just the source")
	require.NotContains(t, out, "<channel")
	require.NotContains(t, out, "</channel>")
	require.NotContains(t, out, "via signal")
}

// TestRawRender_ChannelMessageMalformed verifies 那 truncated/invalid XML
// does 不 panic 和 falls back 到 plain markdown rendering.
func TestRawRender_ChannelMessageMalformed(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="broken`

	item := newTestChannelUserItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.NotEmpty(t, out)
	require.Contains(t, out, "<channel")
}

// TestRawRender_NormalMessage verifies 那 non-渠道 messages 是 不
// affected 通过 渠道 rendering logic.
func TestRawRender_NormalMessage(t *testing.T) {
	t.Parallel()

	text := `This is a normal user message.`

	item := newTestChannelUserItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "This is a normal user message.")
	require.NotContains(t, out, "signal")
	require.NotContains(t, out, "via")
	require.NotContains(t, out, "<channel")
}

// TestRawRender_ChannelMessageEmptyBody verifies 那 渠道 消息 用
// no body renders empty string 在 UserMessageItem.
func TestRawRender_ChannelMessageEmptyBody(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="+15551234567" sender_name="Bob" time="09:15"></channel>`

	item := newTestChannelUserItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.NotContains(t, out, "<channel")
	require.NotContains(t, out, "</channel>")
	require.NotContains(t, out, "via signal")
}

// TestRawRender_ChannelMessageSenderFallbackBody verifies 那 body
// renders correctly 当 sender 是 provided 但 sender_name 是 不.
func TestRawRender_ChannelMessageSenderFallbackBody(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="+15559876543">Fallback sender</channel>`

	item := newTestChannelUserItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "Fallback sender")
	require.NotContains(t, out, "<channel")
}

// --- ChannelInfoItem metadata tests ---

// TestChannelInfo_Full verifies 那 ChannelInfoItem renders full
// metadata line: "[icon] [sender] via [渠道] 在 [timestamp]".
func TestChannelInfo_Full(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="+15551234567" sender_name="Alice" time="14:30">Hello!</channel>`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	// Must contain sender, 渠道 source, 和 timestamp.
	require.Contains(t, out, "Alice")
	require.Contains(t, out, "via signal")
	require.Contains(t, out, "at 14:30")

	// 必须 contain chat-bubble glyph icon.
	require.Contains(t, out, styles.ChannelIcon)

	// Must NOT contain body content — 那's 在 UserMessageItem.
	require.NotContains(t, out, "Hello!")

	// 必须 NOT contain raw XML.
	require.NotContains(t, out, "<channel")
	require.NotContains(t, out, "</channel>")
}

// TestChannelInfo_SenderFallback verifies 那 当 sender 是 provided 但
// sender_name 是 不, raw sender 值 是 shown.
func TestChannelInfo_SenderFallback(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="+15559876543">Body text</channel>`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "+15559876543")
	require.Contains(t, out, "via signal")
}

// TestChannelInfo_CreatedAtFallback verifies 那 当 no time attribute 是
// present, 消息's CreatedAt timestamp 是 used.
func TestChannelInfo_CreatedAtFallback(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal">Uses CreatedAt</channel>`

	item := newTestChannelInfoItem(text, 1752456000)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "via signal")
	require.Contains(t, out, "at ")
}

// TestChannelInfo_NoSource verifies 那 当 no source attribute 是 present,
// "via" clause 是 omitted 但 metadata still renders without panicking.
func TestChannelInfo_NoSource(t *testing.T) {
	t.Parallel()

	text := `<channel sender="+1234" sender_name="Eve" time="08:00">No source</channel>`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	require.Contains(t, out, "Eve")
	require.Contains(t, out, "at 08:00")
	require.NotContains(t, out, "via")
}

// TestChannelInfo_NoMetadata verifies 那 当 只 body 是 present 用
// no metadata attributes, ChannelInfoItem 渲染 empty string.
func TestChannelInfo_NoMetadata(t *testing.T) {
	t.Parallel()

	text := `<channel>Just a body, nothing else.</channel>`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	// With no metadata, info item should 是 empty.
	require.Empty(t, out)
}

// TestChannelInfo_MalformedXML verifies 那 malformed XML does 不 panic.
func TestChannelInfo_MalformedXML(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender="broken`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.RawRender(80))

	// Should 不 panic 和 should 是 empty (XML parse fails).
	require.Empty(t, out)
}

// TestChannelInfo_RenderHasSectionHeader verifies 那 Render method
// applies the SectionHeader padding ( paddingLeft(2) ), confirming the
// metadata appears 作为 separate item outside 消息 body.
func TestChannelInfo_RenderHasSectionHeader(t *testing.T) {
	t.Parallel()

	text := `<channel source="signal" sender_name="Alice" time="14:30">Hi</channel>`

	item := newTestChannelInfoItem(text, 0)
	out := ansi.Strip(item.Render(80))

	// rendered output should start 用 在 least 2 spaces (paddingLeft(2)).
	require.True(t, strings.HasPrefix(out, "  "), "ChannelInfoItem.Render must apply SectionHeader padding")
}

// TestExtractMessageItems_ChannelInfoOnlyWithMetadata verifies the info line
// 是 added below 渠道 消息 只 当 there 是 metadata 到 show, 所以
// malformed 或 bare element does 不 leave blank 行 在 chat.
func TestExtractMessageItems_ChannelInfoOnlyWithMetadata(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	extract := func(text string, createdAt int64) []MessageItem {
		msg := &message.Message{
			ID:        "test-id",
			Role:      message.User,
			CreatedAt: createdAt,
			Parts:     []message.ContentPart{message.TextContent{Text: text}},
		}
		return ExtractMessageItems(&sty, msg, nil, "")
	}

	require.Len(t, extract(`<channel source="signal" sender_name="Alice">hi</channel>`, 0), 2)
	// No attributes 和 no CreatedAt: nothing 到 show.
	require.Len(t, extract(`<channel>hi</channel>`, 0), 1)
	// Malformed element: no info line either.
	require.Len(t, extract(`<channel source="signal">unterminated`, 1752456000), 1)
	// CreatedAt alone 是 enough 为 "在 HH:MM:SS" line.
	require.Len(t, extract(`<channel>hi</channel>`, 1752456000), 2)
}

func TestFormatChannelTime(t *testing.T) {
	t.Parallel()

	const epoch = int64(1752456000)
	want := time.Unix(epoch, 0).Format(time.TimeOnly)
	rfc := time.Unix(epoch, 0).UTC().Format(time.RFC3339)

	require.Equal(t, want, formatChannelTime(rfc, 0), "RFC 3339 is reformatted to local HH:MM:SS")
	require.Equal(t, want, formatChannelTime("1752456000", 0), "epoch seconds")
	require.Equal(t, want, formatChannelTime("1752456000000", 0), "epoch milliseconds")
	require.Equal(t, want, formatChannelTime("", epoch), "empty falls back to CreatedAt")
	require.Empty(t, formatChannelTime("", 0))
	// Values 那 是 不 timestamps 是 shown 作为 服务端 sent them.
	require.Equal(t, "14:30", formatChannelTime("14:30", epoch))
	require.Equal(t, "1430", formatChannelTime("1430", 0), "short numbers are not treated as epochs")
	require.Equal(t, "yesterday", formatChannelTime(" yesterday ", 0))
}
