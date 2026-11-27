// stream.go —— 命令输出的流式读取。

package shell

import (
	"context"
	"os"
)

// progressWriter captures output 和 calls onProgress 用 each 写入.
// stdout 和 stderr share one, 所以 onProgress may 是 called concurrently
// 和 它的 order can differ 从 captured output.
type progressWriter struct {
	buf        *cappedBuffer
	onProgress func(string)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if n > 0 && w.onProgress != nil {
		w.onProgress(string(p[:n]))
	}
	return n, err
}

// RunAndCaptureStream executes shell command 和 streams output chunks
// 到 onProgress 作为 they arrive. Returns 完成 output 和 exit code.
// onProgress must 是 safe 为 concurrent use.
func RunAndCaptureStream(ctx context.Context, opts RunOptions, onProgress func(string)) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	opts.Env = append(opts.Env, ptyColorEnvVars...)

	buf := &progressWriter{buf: newCappedBuffer(maxKeptOutput), onProgress: onProgress}
	opts.Stdout = buf
	opts.Stderr = buf

	runErr := Run(ctx, opts)

	exitCode := 0
	if runErr != nil {
		exitCode = ExitCode(runErr)
	}

	return CaptureResult{
		Output:   buf.buf.String(),
		ExitCode: exitCode,
	}, nil
}
