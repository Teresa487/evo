package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// terminationSignals 是 signals 那 mean "stop now, 但 tidily":
// Ctrl+C, SIGTERM 从 service manager (Go also delivers closed Windows
// console 作为 SIGTERM), 和 SIGHUP 从 closed 终态. 服务端 reads
// SIGHUP 作为 配置 reload instead; see awaitStop.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// notifyContext 是 signal.NotifyContext, except 那 上下文's cause
// keeps signal 那 cancelled it, 所以 signalExitCode can 报告 it.
func notifyContext(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	go func() {
		select {
		case sig := <-ch:
			cancel(signalError{sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel(nil)
		signal.Stop(ch)
	}
}

// signalExitCode returns status shell reports 为 process killed 通过
// signal 那 cancelled ctx, 128 plus 它的 number (130 为 Ctrl+C, 143
// 为 SIGTERM), 或 0 如果 no signal cancelled ctx.
func signalExitCode(ctx context.Context) int {
	var se signalError
	if !errors.As(context.Cause(ctx), &se) {
		return 0
	}
	if n, ok := se.sig.(syscall.Signal); ok {
		return 128 + int(n)
	}
	// 一个 signal without number, such 作为 Plan 9 note, reads 作为 Ctrl+C.
	return 130
}

// signalError 是 cause 的 上下文 cancelled 通过 notifyContext. Like
// cause signal.NotifyContext uses, it matches 上下文.Canceled.
type signalError struct{ sig os.Signal }

func (e signalError) Error() string { return e.sig.String() + " signal received" }

func (e signalError) Is(target error) bool { return target == context.Canceled }
