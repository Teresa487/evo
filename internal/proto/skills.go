// skills.go —— 协议里的技能类型。

package proto

// SkillDiscoveryState mirrors skills.DiscoveryState across the wire.
// Values must stay 在 sync 用 internal/skills.DiscoveryState; do 不
// reorder without coordinated 服务端/客户端 bump.
type SkillDiscoveryState int

const (
	// SkillStateNormal indicates skill was parsed 和 validated
	// successfully.
	SkillStateNormal SkillDiscoveryState = iota
	// SkillStateError indicates discovery encountered a scan/parse/validate
	// 错误.
	SkillStateError
)

// SkillState 是 wire representation 的 skills.SkillState.
type SkillState struct {
	Name  string              `json:"name"`
	Path  string              `json:"path"`
	State SkillDiscoveryState `json:"state"`
	Error string              `json:"error,omitempty"`
}

// SkillsEvent 是 wire representation 的 skills.事件.
type SkillsEvent struct {
	States []SkillState `json:"states"`
}
