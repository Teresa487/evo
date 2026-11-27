//go:build !windows

package config

// systemConfigPath 是 system-wide configuration 文件 路径. 它 是
// loaded 在 lowest priority 所以 用户 和 project configs override it.
const systemConfigPath = "/etc/evo/evo.json"
