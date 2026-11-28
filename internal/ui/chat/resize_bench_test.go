package chat

import (
	"context"
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// BenchmarkResizeSession reproduces resize re-render 路径 over real
// 会话's messages. Point EVO_BENCH_SESSION 在 full 会话 id 和
// EVO_BENCH_DATADIR 在 crush data dir (defaults 到 ./.evo).
//
// EVO_BENCH_SESSION=e6368d820207a406 go 测试 ./internal/ui/chat/ \
// -运行 x -bench BenchmarkResizeSession -benchtime 20x -cpuprofile /tmp/cpu.out
func BenchmarkResizeSession(b *testing.B) {
	sessionID := os.Getenv("EVO_BENCH_SESSION")
	if sessionID == "" {
		b.Skip("set EVO_BENCH_SESSION to a full session id")
	}
	dataDir := os.Getenv("EVO_BENCH_DATADIR")
	if dataDir == "" {
		dataDir = ".evo"
	}

	ctx := context.Background()
	conn, err := db.Connect(ctx, dataDir, db.WithDataDirLock(false))
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	// Note: intentionally 不 closing conn. db.Connect pools connections 通过
	// 路径, 和 testing framework may invoke 这 function more than
	// once; closing would break shared pooled *sql.DB 在 re-条目.

	svc := message.NewService(db.New(conn))
	msgs, err := svc.List(ctx, sessionID)
	if err != nil {
		b.Fatalf("list messages: %v", err)
	}
	if len(msgs) == 0 {
		b.Fatalf("no messages for session %s", sessionID)
	}
	b.Logf("loaded %d messages", len(msgs))

	ptrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		ptrs[i] = &msgs[i]
	}
	toolResults := BuildToolResultMap(ptrs)

	sty := styles.CharmtonePantera()
	var items []list.Item
	for _, m := range ptrs {
		for _, it := range ExtractMessageItems(&sty, m, toolResults, "") {
			items = append(items, it)
		}
	}
	b.Logf("built %d items", len(items))

	l := list.NewList(items...)

	b.ResetTimer()
	// Alternate widths 所以 every iteration 是 genuine width change, which
	// 是 what invalidates caches 和 forces full re-render — exactly
	// what a resize drag does.
	widths := []int{100, 99}
	i := 0
	for b.Loop() {
		l.SetSize(widths[i%2], 40)
		_ = l.TotalHeight()
		i++
	}
}
