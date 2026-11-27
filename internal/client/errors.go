package client

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// Typed outcomes callers need 到 distinguish 从 ordinary transport
// failures. Match them 用 errors.Is.
var (
	// ErrNotFound reports 那 服务端 answered 404. For
	// 工作区-scoped 调用 这 means 服务端 no longer knows
	// 工作区 — it was torn down, 或 服务端 was replaced under
	// 客户端 — 所以 right 响应 是 到 re-register rather than 重试
	// same ID, which 可以 never start succeeding again.
	ErrNotFound = errors.New("not found")

	// ErrServerBusy reports 那 服务端 declined 到 shut down
	// because it 是 still hosting workspaces 或 是 midway through
	// creating one. 一个 客户端 asking version-mismatched 服务端 到 stand
	// down must keep using it instead 的 assuming it 是 going away.
	ErrServerBusy = errors.New("server busy")

	// ErrServerShuttingDown reports 那 服务端 refused 请求
	// because it has already committed 到 exiting. work 是 不 lost:
	// replacement 服务端 can 是 started 和 请求 retried
	// against it.
	ErrServerShuttingDown = errors.New("server is shutting down")

	// ErrUnsupported reports 那 running 服务端 does 不 understand
	// 请求 because it predates feature. Callers must decide
	// what 是 safe 到 do 用 older 服务端 rather than treating
	// failure 作为 transient.
	ErrUnsupported = errors.New("unsupported by the running server")
)

// checkStatus returns nil 当 rsp's status code 是 one 的 ok
// (http.StatusOK 当 none 是 given). Otherwise it returns 错误
// carrying status code 和, 当 body decodes 作为 proto.错误,
// 服务端-provided 消息. Statuses 那 callers act 在 是 wrapped
// 在 matching sentinel. checkStatus may consume 响应 body.
func checkStatus(rsp *http.Response, ok ...int) error {
	if len(ok) == 0 {
		ok = []int{http.StatusOK}
	}
	if slices.Contains(ok, rsp.StatusCode) {
		return nil
	}
	var err error
	if msg := decodeErrorMessage(rsp.Body); msg != "" {
		err = fmt.Errorf("status code %d: %s", rsp.StatusCode, msg)
	} else {
		err = fmt.Errorf("status code %d", rsp.StatusCode)
	}
	switch rsp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %w", ErrServerShuttingDown, err)
	}
	return err
}
