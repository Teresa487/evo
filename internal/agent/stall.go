// stall.go —— 识别模型「偷懒收尾」，并把它推回去继续。
//
// 模型有时会在任务没做完时输出一句「要我继续吗？」或「下一步我将…」就停住。
// 这一轮本该继续，只是它没有发出工具调用。这里的判定只覆盖这类明确的收尾
// 询问/宣告，正常的长回答不会被误判。
//
// 命中后复用与自动摘要相同的续跑通路：把本轮重新排进队列并递归 Run，
// 因此不需要额外的外层循环，也不必手工重读历史。

package agent

import (
	"strings"
	"unicode"
)

// maxStallNudges 是一轮里最多把模型推回去几次。到上限就放行，
// 免得模型一直原地打转。
const maxStallNudges = 2

// stallNudge 是推回时追加的隐藏 user 消息，措辞与 hermes 的做法一致：
// 明确要求继续执行而不是继续描述。
const stallNudge = "[系统：继续执行。现在完成所需的工具调用，只有在任务真正完成后才给出最终答复。]"

// stallTailPhrases 是收尾询问，必须落在最后一句的末尾才算，否则正文里顺带
// 提到「要不要我继续」这类字眼也会被误判。
var stallTailPhrases = []string{
	"要我继续吗", "需要我继续吗", "是否继续", "要我接着", "需要我接着",
	"要不要我继续", "是否需要我继续", "继续吗",
}

// stallHeadPhrases 是宣告下一步或英文疑问句，它们出现在最后一句开头才算。
// 英文靠句首是因为宾语跟在后面，比如 "Shall I proceed with the migration?"。
var stallHeadPhrases = []string{
	"下一步我将", "接下来我将", "接下来我会", "下一步我会", "下面我将",
	"want me to continue", "shall i continue", "should i continue",
	"want me to proceed", "shall i proceed", "should i proceed",
	"let me know if you want me to", "let me know if you'd like me to",
	"i'll now proceed", "i will now proceed", "next, i'll", "next, i will",
}

// looksLikeStall 判断一段没有任何工具调用的文本，是不是模型在任务没做完时
// 收尾。只看最后一句：中文收尾询问在句尾，宣告和英文问句在句首，正文中间
// 的引用和列表因此不会被误判。
func looksLikeStall(text string) bool {
	tail := stallTail(text)
	if tail == "" {
		return false
	}
	for _, phrase := range stallTailPhrases {
		if strings.HasSuffix(tail, phrase) {
			return true
		}
	}
	for _, phrase := range stallHeadPhrases {
		if strings.HasPrefix(tail, phrase) {
			return true
		}
	}
	return false
}

// stallTail 取文本最后一句，归一化为小写、压掉多余空白，并去掉首尾标点，
// 方便做前缀/后缀比较。
func stallTail(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	// 先去掉句末标点，再找最后一个句子边界，否则结尾的「？」会把尾巴切空。
	const cutset = "。！？.!?；;，, \t\n"
	trimmed = strings.TrimRight(trimmed, cutset)

	cut := 0
	runes := []rune(trimmed)
	for i := len(runes) - 1; i >= 0; i-- {
		switch runes[i] {
		case '\n', '。', '！', '？', '.', '!', '?':
			cut = i + 1
		}
		if cut > 0 {
			break
		}
	}

	tail := strings.ToLower(string(runes[cut:]))
	return strings.Join(strings.FieldsFunc(tail, unicode.IsSpace), " ")
}
