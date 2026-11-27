package server

import (
	"errors"
	"io/fs"
	"net"
	"syscall"
)

// IsStaleSocketErr reports whether err indicates 那 Unix-domain
// socket 文件 exists 在 disk 但 no process 是 listening 在 it ( stale
// 或 orphaned socket). 它 returns false 为 nil 和 为 超时 errors.
//
// classification 是 cross-platform: ECONNREFUSED 和 fs.ErrNotExist
// 是 defined 在 every supported OS, 所以 callers 在 Windows can use
// same helper even though stale-socket recovery 只 applies 到 Unix
// sockets 在 practice.
func IsStaleSocketErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist)
}
