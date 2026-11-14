package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M11a 预热池端到端（process 驱动 + 真实 orbd + fakeomp）：项目开预热后对账建出池 orb，新线程直接认领它
// （线程的 orbId 等于此前那台池 orb），认领后离开池、转录正常。
func TestPoolE2E(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)},
		func(c *config.Orynd) { c.Pool.MaxTotal = 2 })

	var prj apigen.Project
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "warm", "setupScript": "echo warming"}, &prj, 201)
	// 活跃窗口：项目 72 小时内要有新线程
	first := s.newThread(map[string]any{"prompt": "first", "projectId": prj.ID})

	// 开预热：PATCH 立即触发对账
	s.rest("PATCH", "/api/v1/projects/"+prj.ID, map[string]any{"warmOrbs": 1}, &prj, 200)
	assert.Equal(t, 1, prj.WarmOrbs)
	var pool apigen.Orb
	require.Eventually(t, func() bool {
		var list apigen.OrbList
		s.rest("GET", "/api/v1/orbs", nil, &list, 200)
		for _, o := range list.Orbs {
			if o.Pool != nil && *o.Pool == apigen.OrbPoolStateIdle && o.State == apigen.OrbState("ready") {
				pool = o
				return true
			}
		}
		return false
	}, 60*time.Second, 100*time.Millisecond, "池 orb 就绪")
	assert.NotEqual(t, first.OrbID, pool.ID)
	assert.Empty(t, pool.ThreadIds)

	// 池 orb 不能直接被挂线程
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "orb", "orbId": pool.ID}}, nil, 409)

	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "hello from the pool", "projectId": prj.ID, "executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	assert.Equal(t, pool.ID, created.Thread.OrbID, "新线程认领池 orb")
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	assert.Equal(t, pool.ID, th.OrbID)
	s.waitEntry(th.ID, "hello from the pool", 30*time.Second)
	assert.NotEmpty(t, s.settledReply(th.ID))

	o := s.orb(pool.ID)
	assert.Nil(t, o.Pool, "认领后离开池")
	assert.Contains(t, o.ThreadIds, th.ID)
	events := s.orbEvents(pool.ID)
	assert.Contains(t, events, "ready>connecting")
	assert.Contains(t, events, "resuming>ready")
}
