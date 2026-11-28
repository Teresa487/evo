package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLooksLikeStall(t *testing.T) {
	t.Parallel()

	stalls := []string{
		"我已经把缓存层抽出来了。要我继续吗？",
		"已完成第一步。需要我继续吗?",
		"分析完毕，是否继续？",
		"接下来我将修改调用方。",
		"下一步我会补测试。",
		"I've refactored the cache. Want me to continue?",
		"Shall I proceed with the migration?",
		"Let me know if you want me to wire it up.",
		"I'll now proceed with the second file.",
	}
	for _, text := range stalls {
		require.True(t, looksLikeStall(text), "should be a stall: %q", text)
	}

	healthy := []string{
		"",
		"完成。缓存层已抽出，调用方也已更新，测试通过。",
		"All done: the cache layer is extracted and tests pass.",
		"要我继续吗这句话只是文档里的示例，实际改完了。",      // 不在结尾
		strings.Repeat("已完成大量工作。", 20), // 长回答，无询问
		"以下是结果：\n- 文件 A 已更新\n- 文件 B 已更新\n",
	}
	for _, text := range healthy {
		require.False(t, looksLikeStall(text), "should not be a stall: %q", text)
	}
}

func TestStallTailKeepsLastSentences(t *testing.T) {
	t.Parallel()

	require.Equal(t, "要我继续吗", stallTail("第一句。第二句。要我继续吗"))
	require.Equal(t, "want me to continue", stallTail("Done here.\n\nWant me to continue"))
	require.Empty(t, stallTail("   "))
}
