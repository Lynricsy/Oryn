package threads

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// M4 §3.4：线程短码落库、Thread.portalUrlTemplate、EventServices 只推送 orb。

// orbRecSink 记录推送的 orb 与线程索引。
type orbRecSink struct {
	nopSink
	mu    sync.Mutex
	orbs  []apigen.Orb
	index []apigen.Thread
}

func (r *orbRecSink) Orb(o apigen.Orb) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orbs = append(r.orbs, o)
}

func (r *orbRecSink) IndexUpsert(t apigen.Thread) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index = append(r.index, t)
}

func TestThreadShortCodeAndPortalTemplate(t *testing.T) {
	const base = "https://t-{code}-p{port}.orb.test/"
	f := newFixture(t, Config{PortalURLTemplate: func(id string) string {
		code, ok := portalhost.Code(id)
		if !ok {
			return ""
		}
		return portalhost.Expand(base, code, 0)
	}})
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	code, ok := portalhost.Code(out.ID)
	require.True(t, ok)
	want := "https://t-" + code + "-p{port}.orb.test/"
	require.NotNil(t, out.PortalURLTemplate)
	assert.Equal(t, want, *out.PortalURLTemplate)

	row, err := f.st.Q.GetThreadByShortCode(f.ctx, code)
	require.NoError(t, err, "short_code 落库并可反查")
	assert.Equal(t, out.ID, row.ID)

	got, err := f.s.Get(f.ctx, out.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PortalURLTemplate)
	assert.Equal(t, want, *got.PortalURLTemplate)

	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].PortalURLTemplate)
	assert.Equal(t, want, *list[0].PortalURLTemplate)

	// 未配置 portal_domain：不输出该字段
	g := newFixture(t, Config{})
	out, err = g.s.Create(g.ctx, CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	assert.Nil(t, out.PortalURLTemplate)
	row, err = g.st.Q.GetThread(g.ctx, out.ID)
	require.NoError(t, err)
	require.NotNil(t, row.ShortCode, "短码与 portal_domain 无关，总是落库")
}

func TestServicesEventPushesOrbOnly(t *testing.T) {
	f := newFixture(t, Config{})
	sink := &orbRecSink{}
	f.s.SetSink(sink)
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)

	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventServices, From: "ready", To: "ready", Seq: 1})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.orbs, 1)
	assert.Equal(t, orb, sink.orbs[0].ID)
	assert.Empty(t, sink.index, "线程不因服务变化重新推送")
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, StateIdle, row.State)
	f.s.mu.Lock()
	_, cached := f.s.orbStates[orb]
	f.s.mu.Unlock()
	assert.False(t, cached, "服务事件不写 orb 状态缓存")
}
