package stats

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func openStore(t *testing.T) *store.Store {
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedOrb(t *testing.T, st *store.Store, id, typ string) {
	ctx := context.Background()
	if _, err := st.Q.GetProvider(ctx, "prov_1"); err != nil {
		require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "x", CreatedAt: 1}))
	}
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: typ, State: "ready", StateChangedAt: 1, TokenHash: id, CreatedAt: 1}))
}

func TestMetricsDownsample(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	seedOrb(t, st, "orb-a", "small")
	// 0s、15s、30s、45s、60s 五个采样。
	for i := range 5 {
		require.NoError(t, st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
			OrbID: "orb-a", Ts: int64(i) * 15000, CpuPct: float64(i * 10), MemUsedMb: int64(100 + i), MemLimitMb: 1000,
			DiskUsedMb: 5, NetRxBytes: int64(i * 100), NetTxBytes: int64(i * 10),
		}))
	}
	raw, err := Metrics(ctx, st.Q, "orb-a", 0, 1e9, 0)
	require.NoError(t, err)
	assert.Equal(t, RawStepSeconds, raw.Step)
	assert.Len(t, raw.Samples, 5)

	m, err := Metrics(ctx, st.Q, "orb-a", 0, 1e9, 30)
	require.NoError(t, err)
	assert.Equal(t, 30, m.Step)
	require.Len(t, m.Samples, 3) // [0,30) [30,60) [60,90)
	assert.EqualValues(t, 0, m.Samples[0].TS)
	assert.InDelta(t, 5, m.Samples[0].CPUPct, 0.001)
	assert.InDelta(t, 100.5, m.Samples[0].MemUsedMB, 0.001)
	assert.EqualValues(t, 100, m.Samples[0].NetRxBytes, "网络字节取桶内末值")
	assert.EqualValues(t, 30000, m.Samples[1].TS)
	assert.InDelta(t, 25, m.Samples[1].CPUPct, 0.001)
	assert.EqualValues(t, 300, m.Samples[1].NetRxBytes)
	assert.InDelta(t, 40, m.Samples[2].CPUPct, 0.001)

	// 窗口裁剪：to 不含。
	m, err = Metrics(ctx, st.Q, "orb-a", 15000, 45000, 0)
	require.NoError(t, err)
	assert.Len(t, m.Samples, 2)
}

func TestWindow(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	f, to := Window(nil, nil, now)
	assert.Equal(t, int64(1_000_000_001), to, "右开区间：当前毫秒计入")
	assert.Equal(t, to-DefaultWindow.Milliseconds(), f)
	x, y := int64(5), int64(9)
	f, to = Window(&x, &y, now)
	assert.Equal(t, [2]int64{5, 9}, [2]int64{f, to})
}
