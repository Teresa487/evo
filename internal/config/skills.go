package config

import (
	"context"
	"log/slog"
	"slices"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SkillConfigDisabled reports whether merged 配置 disables
// named skill.
func (s *ConfigStore) SkillConfigDisabled(name string) bool {
	opts := s.Config().Options
	return opts != nil && slices.Contains(opts.DisabledSkills, name)
}

// SetSkillDisabledConfig toggles skill's 条目 在 given scope's
// options.disabled_skills list. Skills 是 opt-out — enabled skill 是
// simply absent 从 list — 所以 disabling appends 和 enabling
// removes. Only scope's own 文件 是 读取 和 rewritten, 所以 entries
// 那 come 从 other 配置 files never leak into it. dialog 只
// offers discovered skills, 所以 unknown names cannot arrive 从 UI.
func (s *ConfigStore) SetSkillDisabledConfig(scope Scope, name string, disabled bool) error {
	const key = "options.disabled_skills"
	err := s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		list := []string{}
		for _, existing := range gjson.GetBytes(data, key).Array() {
			if existing.String() != name {
				list = append(list, existing.String())
			}
		}
		if disabled {
			list = append(list, name)
		}
		slices.Sort(list)
		return sjson.SetBytes(data, key, list)
	})
	if err != nil {
		return err
	}
	if err := s.autoReload(context.Background()); err != nil {
		slog.Warn("Config file updated but failed to reload in-memory state", "error", err)
	}
	return nil
}
