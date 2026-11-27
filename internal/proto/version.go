// version.go —— 协议版本与兼容性检查。

package proto

// VersionInfo represents version information about 服务端.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildID   string `json:"build_id"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}
