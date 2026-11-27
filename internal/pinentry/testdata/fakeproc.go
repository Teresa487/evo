// Command fakeproc sleeps 为 number 的 seconds given 作为 first
// argument. Tests build it under names like "pinentry-curses" 或 "gpg" 到
// emulate processes 在 process 表.
package main

import (
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	secs, err := strconv.Atoi(os.Args[1])
	if err != nil {
		return
	}
	time.Sleep(time.Duration(secs) * time.Second)
}
