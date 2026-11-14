package threads

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Close 开始后 go_ 不再登记任务：链路协程在关闭期间收到帧（touchAgent 等）时，
// 若仍调 wg.Add，会与 Close 里的 wg.Wait 并发（-race 报 DATA RACE，见 TestFullStackRestartDuringStreaming）。
func TestGoAfterCloseIsDropped(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := New(Config{DataDir: t.TempDir()}, st, nopOrbs{})
	s.Close()

	var ran atomic.Bool
	s.go_(func(context.Context) { ran.Store(true) })
	// 若任务被登记，Wait 会等它跑完，ran 必为 true；未登记则立即返回
	s.wg.Wait()
	assert.False(t, ran.Load(), "Close 之后 go_ 不应启动任务")
}
