//go:build !windows

package server

import (
	"os"
	"strconv"
)

// currentUserID returns uid 的 current process, same string
// [os/用户.Current] reports 作为 Uid. Unlike 用户.Current it cannot fail,
// whereas pure-Go 用户.Current 的 cgo-less Linux builds 只 knows
// users 在 /etc/passwd 和 otherwise needs $用户 和 $HOME.
func currentUserID() string {
	return strconv.Itoa(os.Getuid())
}
