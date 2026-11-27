// tools.go —— 工具注册与装配：把各工具挂到 agent 的工具集上。

package tools

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os/exec"
	"testing"

	"charm.land/fantasy"
)

type (
	sessionIDContextKey string
	messageIDContextKey string
	supportsImagesKey   string
	modelNameKey        string
	channelContextKey   string
)

const (
	// SessionIDContextKey 是 key 为 会话 ID 在 上下文.
	SessionIDContextKey sessionIDContextKey = "session_id"
	// MessageIDContextKey 是 key 为 消息 ID 在 上下文.
	MessageIDContextKey messageIDContextKey = "message_id"
	// SupportsImagesContextKey 是 key 为 模型's 图片 support 能力.
	SupportsImagesContextKey supportsImagesKey = "supports_images"
	// ModelNameContextKey 是 key 为 模型 name 在 上下文.
	ModelNameContextKey modelNameKey = "model_name"
	// ChannelContextKey 是 key 为 渠道 那 originated turn.
	ChannelContextKey channelContextKey = "channel"
)

// getContextValue 是 generic helper 那 retrieves typed 值 从 上下文.
// If 值 是 不 found 或 has wrong type, it returns 默认 值.
func getContextValue[T any](ctx context.Context, key any, defaultValue T) T {
	value := ctx.Value(key)
	if value == nil {
		return defaultValue
	}
	if typedValue, ok := value.(T); ok {
		return typedValue
	}
	return defaultValue
}

// GetSessionFromContext retrieves 会话 ID 从 上下文.
func GetSessionFromContext(ctx context.Context) string {
	return getContextValue(ctx, SessionIDContextKey, "")
}

// GetChannelFromContext retrieves 渠道 那 originated current turn.
func GetChannelFromContext(ctx context.Context) string {
	return getContextValue(ctx, ChannelContextKey, "")
}

// GetMessageFromContext retrieves 消息 ID 从 上下文.
func GetMessageFromContext(ctx context.Context) string {
	return getContextValue(ctx, MessageIDContextKey, "")
}

// GetSupportsImagesFromContext retrieves whether 模型 supports images 从 上下文.
func GetSupportsImagesFromContext(ctx context.Context) bool {
	return getContextValue(ctx, SupportsImagesContextKey, false)
}

// GetModelNameFromContext retrieves 模型 name 从 上下文.
func GetModelNameFromContext(ctx context.Context) string {
	return getContextValue(ctx, ModelNameContextKey, "")
}

// UnsupportedMediaResponse tells 模型 it cannot receive 工具's media.
func UnsupportedMediaResponse(modelName string) fantasy.ToolResponse {
	return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName))
}

// NewPermissionDeniedResponse returns 工具 响应 indicating 用户
// denied 权限, 用 StopTurn set 所以 agent loop does 不 重试.
func NewPermissionDeniedResponse() fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse("User denied permission")
	resp.StopTurn = true
	return resp
}

// ghAvailable indicates whether `gh` CLI 是 available 在 路径.
var ghAvailable = func() bool {
	if testing.Testing() {
		return false
	}
	_, err := exec.LookPath("gh")
	return err == nil
}()

// toolDescriptionData 是 common data structure 为 工具 description templates.
type toolDescriptionData struct {
	GhAvailable bool
}

// renderToolDescription renders 工具 description template 用 given data.
func renderToolDescription(tmpl *template.Template) string {
	data := toolDescriptionData{
		GhAvailable: ghAvailable,
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		panic("failed to execute tool description template: " + err.Error())
	}
	return out.String()
}

// renderTemplate renders Go template 用 given data.
func renderTemplate(tmpl *template.Template, data any) string {
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		panic("failed to execute tool description template: " + err.Error())
	}
	return out.String()
}
