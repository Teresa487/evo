//go:build windows

package server

import "golang.org/x/sys/windows"

// currentUserID returns SID 的 用户 那 owns current
// process, same string [os/用户.Current] reports 作为 Uid, 或 "" 如果 it
// cannot 是 读取. Unlike 用户.Current it does 不 also look up 用户's
// profile 目录 和 account name, 所以 it keeps working 当 只 those
// lookups fail.
func currentUserID() string {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return tu.User.Sid.String()
}
