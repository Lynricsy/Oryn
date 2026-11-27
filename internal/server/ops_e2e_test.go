package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/opsconf"
)

// 设置页改保留期（控制面运维参数）：订阅把新值推给统计服务，Meta 立即反映；重启后覆盖仍在。
func TestOpsOverrideReachesStatsAndSurvivesRestart(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	ctx := context.Background()
	const want = int64(48 * time.Hour / time.Millisecond)

	_, err := s.srv.Ops.Apply(ctx, opsconf.Patch{"stats.retention.samples": new(want)})
	require.NoError(t, err)
	m, err := s.srv.Stats.Meta(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, m.Retention.SamplesMs)

	s.restart()
	m, err = s.srv.Stats.Meta(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, m.Retention.SamplesMs)
	assert.Equal(t, []string{"stats.retention.samples"}, s.srv.Ops.State().Overridden)
}
