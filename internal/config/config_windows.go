//go:build windows

package config

// systemConfigPath 是 empty 在 Windows: there 是 no system-wide 配置.
// loadFromConfigPaths skips paths 那 do 不 exist, 所以 no special-case
// handling 是 needed.
const systemConfigPath = ""
