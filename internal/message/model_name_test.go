package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestModelNamePersists covers the model_name column: the display name
// set 在 creation survives 读取, 和 更新 那 fills it 在 later
// persists too, 所以 UI can keep showing friendly name after
// 模型 是 renamed 或 removed 从 配置.
func TestModelNamePersists(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:      Assistant,
		Model:     "model-id",
		ModelName: "Friendly Name",
		Provider:  "test-provider",
	})
	require.NoError(t, err)

	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "Friendly Name", got.ModelName)
	require.Equal(t, "model-id", got.Model)
	require.Equal(t, "test-provider", got.Provider)

	msg.ModelName = "Renamed Name"
	require.NoError(t, svc.Update(t.Context(), msg))
	require.NoError(t, svc.Flush(t.Context(), msg.ID))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed Name", got.ModelName)

	// Legacy messages created without name 读取 back empty, 不
	// garbage, 所以 UI falls back 到 模型 ID.
	legacy, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:     Assistant,
		Model:    "legacy-model",
		Provider: "test-provider",
	})
	require.NoError(t, err)
	got, err = svc.Get(t.Context(), legacy.ID)
	require.NoError(t, err)
	require.Empty(t, got.ModelName)
	require.Equal(t, "legacy-model", got.Model)
}
