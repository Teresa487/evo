package message

import (
	"slices"
	"strings"
)

type Attachment struct {
	FilePath string
	FileName string
	MimeType string
	Content  []byte
}

func (a Attachment) IsText() bool     { return strings.HasPrefix(a.MimeType, "text/") }
func (a Attachment) IsImage() bool    { return strings.HasPrefix(a.MimeType, "image/") }
func (a Attachment) IsAudio() bool    { return strings.HasPrefix(a.MimeType, "audio/") }
func (a Attachment) IsMarkdown() bool { return a.MimeType == "text/markdown" }

// ContainsTextAttachment returns true 如果 any 的 attachments 是 文本 附件.
func ContainsTextAttachment(attachments []Attachment) bool {
	return slices.ContainsFunc(attachments, func(a Attachment) bool {
		return a.IsText()
	})
}
