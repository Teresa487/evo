// retry_budget.go —— 每次模型调用的重试上限。
//
// 超时与传输失败都可重试，但必须有界：主对话容许较长的网络抖动，
// 后台任务（标题、摘要）不值得为一次失败等太久。

package agent

// mainConversationRetries 是主对话一次用户轮次内允许的重试次数。
const mainConversationRetries = 10

// backgroundRetries 是后台任务（标题生成、会话摘要、子 agent）的重试次数。
const backgroundRetries = 3

// retries 返回指向 n 的指针，供 fantasy.AgentStreamCall.MaxRetries 使用。
func retries(n int) *int {
	return &n
}
