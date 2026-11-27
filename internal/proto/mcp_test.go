package proto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMCPClientInfo_ChannelRoundTrip verifies 渠道 flag survives
// JSON wire format 所以 客户端/服务端 sessions can show which servers 是 活跃
// channels.
func TestMCPClientInfo_ChannelRoundTrip(t *testing.T) {
	t.Parallel()

	src := MCPClientInfo{
		Name:      "webhook",
		State:     MCPStateConnected,
		ToolCount: 1,
		Channel:   true,
	}

	data, err := json.Marshal(src)
	require.NoError(t, err)

	var got MCPClientInfo
	require.NoError(t, json.Unmarshal(data, &got))
	require.True(t, got.Channel, "channel flag must survive the wire")
	require.Equal(t, "webhook", got.Name)
}

// TestMCPEvent_ChannelMessageRoundTrip verifies rendered <渠道> body
// survives the JSON wire format.
func TestMCPEvent_ChannelMessageRoundTrip(t *testing.T) {
	t.Parallel()

	src := MCPEvent{
		Type:           MCPEventChannelMessage,
		Name:           "webhook",
		ChannelMessage: `<channel source="webhook">hi</channel>`,
	}

	data, err := json.Marshal(src)
	require.NoError(t, err)

	var got MCPEvent
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, MCPEventChannelMessage, got.Type)
	require.Equal(t, `<channel source="webhook">hi</channel>`, got.ChannelMessage)
}
