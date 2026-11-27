package proto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMessageUnmarshalSkipsUnknownPartType guards 那 片段 type 从
// newer 服务端 是 dropped 在 它的 own instead 的 failing whole decode.
func TestMessageUnmarshalSkipsUnknownPartType(t *testing.T) {
	t.Parallel()

	const data = `{"id":"m-1","role":"assistant","parts":[` +
		`{"type":"text","data":{"text":"hello"}},` +
		`{"type":"future_widget","data":{"blob":"x"}}]}`

	var msg Message
	require.NoError(t, json.Unmarshal([]byte(data), &msg))
	require.Equal(t, []ContentPart{TextContent{Text: "hello"}}, msg.Parts)
}
