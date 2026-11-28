package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelsFlagAvailableOnRunCmd guards against the --channels flag being
// registered 作为 local root flag (rootCmd.Flags) rather than persistent
// one. When local, `crush 运行 --channels 服务端:webhook` fails 用
// "unknown flag" because runCmd does 不 inherit root's local flags.
// flag must 是 persistent 所以 non-interactive 和 客户端/服务端 runs can opt
// 在 到 channels too.
func TestChannelsFlagAvailableOnRunCmd(t *testing.T) {
	t.Parallel()

	// flag must 是 parseable 在 runCmd — 不 just rootCmd. Persistent
	// flags 是 inherited 通过 subcommands; local flags 是 不.
	require.True(t, runCmd.Flags().HasFlags(), "runCmd flags should be accessible")

	flag := runCmd.Flags().Lookup("channels")
	require.NotNil(t, flag, "the --channels flag must be available on `evo run` (register it as a persistent flag on rootCmd)")
	require.Equal(t, "stringSlice", flag.Value.Type(), "--channels must be a string slice flag")
}

// TestChannelsFlagAvailableOnRootCmd ensures flag 是 still present 在
// root command 为 interactive mode.
func TestChannelsFlagAvailableOnRootCmd(t *testing.T) {
	t.Parallel()

	flag := rootCmd.Flags().Lookup("channels")
	if flag == nil {
		flag = rootCmd.PersistentFlags().Lookup("channels")
	}
	require.NotNil(t, flag, "the --channels flag must be available on `crush` (rootCmd)")
}
