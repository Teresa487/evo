// experimental.go —— 实验性配置项。

package config

import (
	"context"
	"os"
	"strconv"
)

// LoadExperimental reads 只 experimental options 从 配置
// files 为 workingDir, skipping 提供方 setup full [Load] does. 它
// 是 为 decisions made before full load, such 作为 whether 到 start 在
// 客户端/服务端 mode.
func LoadExperimental(ctx context.Context, workingDir string) (*ExperimentalOptions, error) {
	cfg, _, err := loadFromConfigPaths(ctx, lookupConfigs(workingDir))
	if err != nil || cfg.Options == nil {
		return nil, err
	}
	return cfg.Options.Experimental, nil
}

// GetClientServer reports whether 客户端/服务端 mode 是 configured 在,
// ignoring the environment.
func (e *ExperimentalOptions) GetClientServer() bool {
	return e != nil && e.ClientServer != nil && *e.ClientServer
}

// ExportEnv sets the environment variable behind each configured option,
// leaving any variable 那 是 already set alone 所以 environment keeps
// priority over 配置. Processes Crush spawns, such 作为 服务端,
// inherit 结果.
func (e *ExperimentalOptions) ExportEnv() {
	if e == nil {
		return
	}
	setDefault := func(name, value string) {
		if os.Getenv(name) == "" {
			_ = os.Setenv(name, value)
		}
	}
	if e.ClientServer != nil {
		setDefault("CRUSH_CLIENT_SERVER", strconv.FormatBool(*e.ClientServer))
	}
	if e.ServerReadyTimeout != nil {
		setDefault("CRUSH_SERVER_READY_TIMEOUT", strconv.Itoa(*e.ServerReadyTimeout)+"s")
	}
	if e.ServerIdleTimeout != nil {
		setDefault("CRUSH_SERVER_IDLE_TIMEOUT", strconv.Itoa(*e.ServerIdleTimeout))
	}
	if e.ServerDetachGrace != nil {
		setDefault("CRUSH_SERVER_DETACH_GRACE", strconv.Itoa(*e.ServerDetachGrace))
	}
}
