package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var serverHost string

func init() {
	serverCmd.Flags().StringVarP(&serverHost, "host", "H", server.DefaultHost(), "Server host  (HTTP, TCP or Unix socket)")
	rootCmd.AddCommand(serverCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Crush server",
	Long: `Start the Crush server.

Usage:

  # Serve over HTTP
  crush server --host http://127.0.0.1:8080

  # Serve over TCP
  crush server --host tcp://127.0.0.1:8080

  # Serve over a Unix socket (default)
  crush server --host unix:///tmp/crush.sock

While the server is running, browse the HTTP API docs at /v1/docs/
(e.g. http://127.0.0.1:8080/v1/docs/). The OpenAPI 3.1 spec is served
at /v1/docs/openapi.json.

The server keeps running until it is stopped (Ctrl+C, SIGTERM, or a
shutdown request), and its workspaces remain hosted after their clients
detach or exit. That supports headless workflows; delete a workspace
explicitly to close it. Unlike a server a client spawns on demand, one
started this way does not reap itself or its workspaces when idle.
`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dataDir, err := cmd.Flags().GetString("data-dir")
		if err != nil {
			return fmt.Errorf("failed to get data directory: %v", err)
		}
		debug, err := cmd.Flags().GetBool("debug")
		if err != nil {
			return fmt.Errorf("failed to get debug flag: %v", err)
		}

		cfg, err := config.Load(cmd.Context(), config.GlobalWorkspaceDir(), dataDir, debug)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %v", err)
		}
		cfg.Config().Options.Experimental.ExportEnv()

		hostURL, err := server.ParseHostURL(serverHost)
		if err != nil {
			return fmt.Errorf("invalid server host: %v", err)
		}

		logFile := serverLogFile(hostURL)

		if term.IsTerminal(os.Stderr.Fd()) {
			crushlog.Setup(logFile, debug, os.Stderr)
		} else {
			crushlog.Setup(logFile, debug)
		}

		srv := server.NewServer(cfg, hostURL.Scheme, hostURL.Host)
		srv.SetLogger(slog.Default())
		if os.Getenv(spawnedServerEnv) == "" {
			// Started explicitly: keep running 当 空闲 和 keep hosted
			// workspaces open 为 headless work whose caller exits.
			srv.DisableIdleShutdown()
			srv.DisableWorkspaceAutoClose()
		}
		slog.Info("Starting Crush server...", "addr", serverHost)

		errch := make(chan error, 1)
		go func() {
			errch <- srv.ListenAndServe()
		}()

		if err = awaitStop(cmd.Context(), srv, errch); err != nil {
			if !errors.Is(err, server.ErrServerClosed) {
				_ = srv.Close()
				slog.Error("Server error", "error", err)
				return fmt.Errorf("server error: %v", err)
			}
		}

		if errors.Is(err, server.ErrServerClosed) {
			return nil
		}

		// signal already cancelled cmd.上下文(); detach 所以
		// shutdown gets 它的 full budget instead 的 none.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 15*time.Second)
		defer cancel()

		slog.Info("Shutting down...")

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("Failed to shutdown server", "error", err)
			return fmt.Errorf("failed to shutdown server: %v", err)
		}

		return nil
	},
}

// awaitStop blocks until root 上下文 是 cancelled 通过 termination
// signal, returning nil, 或 服务端 stops 在 它的 own, returning 它的
// 错误. 一个 服务端 has no 终态 到 lose, 所以 SIGHUP reloads 配置 here
// instead 的 stopping it, 和 是 ignored once stop has begun.
func awaitStop(ctx context.Context, srv *server.Server, errch <-chan error) error {
	signal.Reset(syscall.SIGHUP)
	hupch := make(chan os.Signal, 1)
	signal.Notify(hupch, syscall.SIGHUP)
	defer signal.Stop(hupch)

	for {
		select {
		case <-hupch:
			slog.Info("Received SIGHUP, reloading config...")
			reloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := srv.ReloadConfig(reloadCtx); err != nil {
				slog.Error("Failed to reload config", "error", err)
			}
			cancel()
		case <-ctx.Done():
			// On 它的 own, deferred Stop would give SIGHUP back 它的
			// 默认 action, 和 late one ( 终态 closing after
			// Ctrl+C, say) would kill the process mid-shutdown.
			signal.Ignore(syscall.SIGHUP)
			slog.Info("Received termination signal...")
			return nil
		case err := <-errch:
			return err
		}
	}
}
