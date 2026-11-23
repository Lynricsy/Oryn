package threadstest

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 同一 orb 同时只拉起一个 fake orbd，已 ready 的不再拉起（同真实 orbs.Wake）：Create 的启动在途时线程服务的唤醒
// 不得再拨一条 link——第二个 orbd 会让线程的 agent 在它上面另起一个进程，旧进程里的回合再也收不到 abort。
func TestStartOrbdOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	o, err := NewOrbs(st, "", filepath.Join(dir, "orbs"))
	require.NoError(t, err)
	t.Cleanup(o.Close)
	var readies atomic.Int32
	o.Subscribe(func(ev threads.OrbEvent) {
		if ev.Kind == threads.OrbEventState && ev.To == "ready" {
			readies.Add(1)
		}
	})
	o.Hold(true)
	orb, err := o.Create(ctx, threads.OrbCreate{})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() { o.startOrbd(orb.ID) })
	}
	wg.Wait()
	assert.EqualValues(t, 1, readies.Load(), "并发的拉起只有一次生效")
	o.startOrbd(orb.ID)
	require.NoError(t, o.Wake(ctx, orb.ID))
	assert.EqualValues(t, 1, readies.Load(), "已 ready 的 orb 不再拉起")
}
