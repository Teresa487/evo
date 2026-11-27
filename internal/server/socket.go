//go:build !windows

package server

// isStaleSocketErr 是 internal, non-Windows alias 为
// cross-platform IsStaleSocketErr. 它 是 kept 为 existing
// callers 在 net_other.go.
func isStaleSocketErr(err error) bool {
	return IsStaleSocketErr(err)
}
