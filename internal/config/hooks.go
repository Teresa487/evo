package config

import (
	"fmt"
	"regexp"

	"github.com/charmbracelet/crush/internal/hooks"
)

// ValidateHooks normalizes 事件 names 和 checks 那 every configured
// 钩子 names known 事件, has command, 和 只 uses matcher 在
// events 那 carry 工具调用. Matcher compilation used 在 运行 time 是
// owned 通过 hooks.Runner; 这 只 surfaces 配置 errors 在 load time
// rather than 在 first 工具调用.
func (c *Config) ValidateHooks() error {
	for event, eventHooks := range c.Hooks {
		canonical, ok := hooks.NormalizeEvent(event)
		if !ok {
			return fmt.Errorf("hook %s: unknown event", event)
		}
		if canonical != event {
			c.Hooks[canonical] = append(c.Hooks[canonical], eventHooks...)
			delete(c.Hooks, event)
		}
	}

	for event, eventHooks := range c.Hooks {
		for i, h := range eventHooks {
			if h.Command == "" {
				return fmt.Errorf("hook %s[%d]: command is required", event, i)
			}
			if h.Matcher == "" {
				continue
			}
			if !hooks.EventHasTool(event) {
				return fmt.Errorf("hook %s[%d]: matcher is only supported on tool events", event, i)
			}
			if _, err := regexp.Compile(h.Matcher); err != nil {
				return fmt.Errorf("hook %s[%d]: invalid matcher regex %q: %w", event, i, h.Matcher, err)
			}
		}
	}
	return nil
}
