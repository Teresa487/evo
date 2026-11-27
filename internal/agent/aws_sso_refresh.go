// aws_sso_refresh.go —— provider 报 401 时的 AWS SSO 凭据刷新。
//
// 由 agent 的 OnAuthRefresh 钩子调用：提取 SSO URL、登录、刷新凭据后让流重试。

package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// awsSSORefreshTimeout bounds how long AWS SSO refresh command may 运行.
// Browser-based SSO needs time, 所以 it 是 generous, 和 login runs 在 它的
// own 上下文 所以 cancelled turn doesn't abort it.
const awsSSORefreshTimeout = 5 * time.Minute

// awsSSOURLRe matches https verification URL 那 `aws sso login` 和
// related commands print 到 stdout 或 stderr.
var awsSSOURLRe = regexp.MustCompile(`https://[^\s]+`)

// extractAWSSSOURL returns first HTTPS URL 在 given command output
// line, 或 empty 如果 none 是 present.
func extractAWSSSOURL(line string) string {
	return awsSSOURLRe.FindString(line)
}

// refreshAWSCredentials runs 提供方's configured AWS SSO refresh
// command (e.g. "aws sso login") 在 machine 那 makes Bedrock
// calls, streaming verification URL 到 UI 为 display, then rebuilds
// models 所以 AWS SDK re-reads refreshed credentials. 它 returns nil 到
// signal 那 failed 请求 should 是 retried.
//
// command runs here, 在 coordinator, rather than 在 UI dialog 所以
// refreshed credentials land where 模型 calls 是 made. 这 是
// correct 在 both single-process 和 客户端/服务端 deployments.
func (c *coordinator) refreshAWSCredentials(ctx context.Context, providerCfg config.ProviderConfig) error {
	if c.notify == nil {
		return errNoInteractiveAuth
	}
	// Every 请求 那 hits expired 会话 shares one login, 所以
	// parallel subagents open one 浏览器 流程 rather than one each.
	// login owns 它的 上下文; cancelled caller 只 stops waiting.
	done := c.awsRefresh.DoChan(providerCfg.ID, func() (any, error) {
		return nil, c.loginAWS(providerCfg)
	})
	select {
	case res := <-done:
		if res.Err != nil {
			return res.Err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// login may finish just 作为 这 请求 是 cancelled. Don't 重试
	// it: any 错误 here makes fantasy skip 重试 和 surface
	// original auth 错误, same 作为 取消 during wait above.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	slog.Info("AWS SSO refresh complete, retrying request", "provider", providerCfg.ID)
	return nil
}

// loginAWS runs 提供方's refresh command once 和 rebuilds models
// 所以 AWS SDK re-reads refreshed SSO 缓存. 它 是 shared 通过 every
// waiting 请求, 所以 it runs 在 它的 own 上下文 rather than any caller's.
func (c *coordinator) loginAWS(providerCfg config.ProviderConfig) error {
	slog.Info("AWS credentials expired, running refresh command",
		"provider", providerCfg.ID, "command", providerCfg.AWSAuthRefresh)

	// Open dialog immediately 所以 用户 sees progress even before
	// command prints 它的 verification URL.
	c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		Type:         notify.TypeAWSSSOAuth,
		ProviderID:   providerCfg.ID,
		AWSSOCommand: providerCfg.AWSAuthRefresh,
	})

	runCtx, cancel := context.WithTimeout(context.Background(), awsSSORefreshTimeout)
	defer cancel()

	runErr := c.runAWSAuthRefresh(runCtx, providerCfg)

	result := notify.Notification{Type: notify.TypeAWSSSOAuthResult, ProviderID: providerCfg.ID}
	if runErr != nil {
		result.Message = runErr.Error()
	}
	c.notify.Publish(pubsub.CreatedEvent, result)

	if runErr != nil {
		slog.Error("AWS SSO refresh command failed", "provider", providerCfg.ID, "error", runErr)
		return runErr
	}
	if err := c.rebuildModels(runCtx); err != nil {
		slog.Error("Failed to update models after AWS SSO refresh", "provider", providerCfg.ID, "error", err)
		return err
	}
	return nil
}

// runAWSAuthRefresh executes 刷新 command, publishing SSO
// verification URL 到 UI 作为 soon 作为 it appears 在 output 和
// returning any failure 用 captured stderr 为 上下文.
func (c *coordinator) runAWSAuthRefresh(ctx context.Context, providerCfg config.ProviderConfig) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", providerCfg.AWSAuthRefresh)
	cmd.Dir = c.cfg.WorkingDir()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	// Drain stdout 和 stderr concurrently 所以 command 那 fills one pipe
	// 缓冲 before closing other can't deadlock. Both 是 scanned 为
	// verification URL; stderr 是 also captured 为 错误 detail.
	var (
		stderrBuf bytes.Buffer
		mu        sync.Mutex // Guards the single-shot URL publish across goroutines.
		urlSent   bool
	)
	publishURL := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if urlSent {
			return
		}
		if url := extractAWSSSOURL(line); url != "" {
			urlSent = true
			// Second phase 的 two-片段 发布: loginAWS already opened
			// dialog; 这 fills 在 URL 在 it.
			c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
				Type:         notify.TypeAWSSSOAuth,
				ProviderID:   providerCfg.ID,
				AWSSOCommand: providerCfg.AWSAuthRefresh,
				AWSSOURL:     url,
			})
		}
	}

	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			publishURL(scanner.Text())
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(io.TeeReader(stderrPipe, &stderrBuf))
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
			return fmt.Errorf("%w: %s", err, stderr)
		}
		return err
	}
	return nil
}
