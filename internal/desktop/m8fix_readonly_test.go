package desktop

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// lateReadonly 让一条迟到的 profile.mode 在 orbd 上生效（标记变只读），但不上报 desktop.state：
// 即「纠正还没开始」的那一刻。
func (e *env) lateReadonly(orbID string) {
	e.t.Helper()
	o := e.fo.orbd(orbID)
	o.mu.Lock()
	o.mode = "readonly"
	o.mu.Unlock()
}

// shutdownThenDelete 按 orbs.shutdownOrbd 的顺序走一遍删除：同步调 BeforeShutdown，返回后才「入队删除」
// （记进跨 orb 请求顺序）。
func (e *env) shutdownThenDelete(orbID string) {
	e.t.Helper()
	e.setState(orbID, orbs.StateDeleting)
	e.svc.BeforeShutdown(e.ctx, orbID)
	e.fo.mu.Lock()
	e.fo.order = append(e.fo.order, orbID+" DELETE-JOB")
	e.fo.mu.Unlock()
}

// indexOf 返回 order 里第一个以 prefix 开头的位置；没有为 -1。
func indexOf(order []string, prefix string) int {
	for i, r := range order {
		if strings.HasPrefix(r, prefix) {
			return i
		}
	}
	return -1
}

// 迟到的只读通知落在库里仍是当前持有者的 orb 上，纠正之前就删除：关闭式保存先改回独占、重试导出一次，
// 删除之前存下了 orbd 当时的数据。
func TestLateReadonlyBeforeDeleteStillSaves(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "cookie-right-before-delete")
	e.lateReadonly(a)

	e.shutdownThenDelete(a)
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision, "删除前保存了新修订")
	order := e.fo.orderOf()
	iFirst := indexOf(order, a+" GET /desktop/profile/export")
	iFix := -1
	for i := iFirst + 1; iFirst >= 0 && i < len(order); i++ {
		if strings.HasPrefix(order[i], a+" PUT /desktop/profile?") {
			iFix = i
			break
		}
	}
	iExport := -1
	for i, r := range order {
		if strings.HasPrefix(r, a+" GET /desktop/profile/export?mode=closed") {
			iExport = i // 最后一次导出（第一次被 profile_readonly 拒绝）
		}
	}
	iDelete := indexOf(order, a+" DELETE-JOB")
	require.GreaterOrEqual(t, iFix, 0)
	assert.Contains(t, order[iFix], "keep=1")
	assert.Contains(t, order[iFix], "mode=exclusive")
	assert.Less(t, iFirst, iFix, "先被拒绝再纠正")
	assert.Less(t, iFix, iExport, "纠正之后重试导出")
	assert.Less(t, iExport, iDelete, "保存完成之后才删除")
	assert.Equal(t, 2, e.fo.orbd(a).exports(), "只重试一次")

	// 新修订的内容就是 orbd 当时的数据
	_, err := e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, a)
	require.NoError(t, err)
	b := e.orb("B")
	e.bind(b, p, "")
	e.svc.BeforeReady(e.ctx, b)
	assert.Equal(t, "cookie-right-before-delete", string(e.fo.orbd(b).lastPut()))
}

// 同样的情形发生在冻结前：活体保存也被保住。
func TestLateReadonlyBeforeFreezeStillSaves(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "cookie-before-freeze")
	e.lateReadonly(a)

	require.True(t, e.svc.NeedsFreezeSave(e.ctx, a))
	e.svc.BeforeFreeze(e.ctx, a)
	assert.EqualValues(t, 1, e.get(p).Revision)
	reqs := e.fo.orbd(a).requests()
	assert.Equal(t, "GET /desktop/profile/export?mode=live", last(reqs))
	assert.Equal(t, 2, e.fo.orbd(a).exports())
	o := e.fo.orbd(a)
	o.mu.Lock()
	assert.Equal(t, "exclusive", o.mode, "orbd 已改回独占")
	o.mu.Unlock()
	assert.Equal(t, apigen.OrbDesktopProfileStatusAttached, e.view(a).Status)
}

// 反例：库里确实是只读（被合法接管）——不纠正、不导出、不保存。
func TestReadonlyInDBNotReasserted(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.fo.usable[a] = orbs.ErrOrbNotReady // 接管前跳过对 A 的保存，A 留着未存的改动
	e.dirty(a, "unsaved-on-A")
	b := e.orb("B")
	e.bind(b, p, apigen.BrowserProfileModeTakeover)
	e.svc.BeforeReady(e.ctx, b)
	require.Equal(t, b, *e.get(p).HolderOrbID)
	require.Equal(t, apigen.OrbDesktopProfileStatusReadonly, e.view(a).Status)
	e.lateReadonly(a)

	n := len(e.fo.orbd(a).requests())
	e.shutdownThenDelete(a)
	e.svc.BeforeFreeze(e.ctx, a)
	assert.Len(t, e.fo.orbd(a).requests(), n, "没有纠正也没有导出")
	assert.EqualValues(t, 0, e.get(p).Revision)
	assert.Equal(t, b, *e.get(p).HolderOrbID)
}

// 反例：库里绑定仍是 exclusive，但租约已被别的 orb 持有（复核不通过）——不纠正、不保存。
func TestReassertRequiresCurrentLease(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	d := e.desk(a)
	e.dirty(a, "a-data")
	e.lateReadonly(a)
	// 导出被拒之后、复核之前租约换过手：代数不符或已不再持有都不纠正
	assert.False(t, e.svc.reassertExclusive(e.ctx, a, p, d.Fence+1), "代数不符")
	_, err := e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, a)
	require.NoError(t, err)
	assert.False(t, e.svc.reassertExclusive(e.ctx, a, p, d.Fence), "不再持有")
	for _, r := range e.fo.orbd(a).requests() {
		assert.NotContains(t, r, "keep=1&mode=exclusive")
	}
}
