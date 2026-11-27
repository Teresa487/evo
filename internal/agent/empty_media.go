package agent

import (
	"charm.land/fantasy"
)

// emptyMediaNote replaces 工具's media 当 it carries no bytes, since
// providers reject empty media block 通过 failing whole 请求.
const emptyMediaNote = "The tool returned empty media data, so there is nothing to show here."

// emptyMediaText 是 文本 工具结果 carries once 它的 empty media 是
// dropped, keeping whatever 工具 said alongside it.
func emptyMediaText(text string) string {
	if text == "" {
		return emptyMediaNote
	}
	return text + "\n" + emptyMediaNote
}

// dropEmptyToolMedia replaces every 工具结果 whose media has no data 用
// 文本 note, 所以 no 工具 can put empty media block 在 wire.
func dropEmptyToolMedia(messages []fantasy.Message) []fantasy.Message {
	for i, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		var content []fantasy.MessagePart
		for j, part := range msg.Content {
			result, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				continue
			}
			media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Output)
			if !ok || media.Data != "" {
				continue
			}
			if content == nil {
				content = append([]fantasy.MessagePart(nil), msg.Content...)
			}
			result.Output = fantasy.ToolResultOutputContentText{Text: emptyMediaText(media.Text)}
			content[j] = result
		}
		if content != nil {
			messages[i].Content = content
		}
	}
	return messages
}
