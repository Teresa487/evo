// Command injectctrl exercises pinentry 终态 handover helpers end
// 到 end: it configures 它的 终态 via pinentry.SetTerminalRawNoEcho,
// injects Ctrl-L via pinentry.InjectCtrlL, then reads byte back 作为 如果
// it were pinentry dialog receiving it. 一个 lone byte arriving without
// newline proves 终态 是 在 non-canonical mode.
package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/crush/internal/pinentry"
)

func main() {
	if err := pinentry.SetTerminalRawNoEcho(); err != nil {
		fmt.Println("MODE-ERROR:", err)
		os.Exit(1)
	}

	if err := pinentry.InjectCtrlL(); err != nil {
		fmt.Println("INJECT-ERROR:", err)
		os.Exit(1)
	}

	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil {
		fmt.Println("READ-ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("GOT-%02x\n", b[0])
}
