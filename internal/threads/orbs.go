package threads

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 状态中本包关心的几个（与 internal/orbs 的状态名一致）。
const (
	orbStateReady   = "ready"
	orbStateDeleted = "deleted"
	orbStateFailed  = "failed"
)

// orbSuspended 判断 orb 是否处于暂停或恢复途中（此时现有 agent 流不可用于下发命令）。
func orbSuspended(state string) bool {
	switch state {
	case "freezing", "frozen", "stopping", "stopped", "waking", "failed", "deleting":
		return true
	}
	return false
}

// Orb 事件种类（与 internal/orbs 的 Event.Kind 一致）。
const (
	OrbEventState    = "state"
	OrbEventLinkUp   = "link_up"
	OrbEventLinkDown = "link_down"
	// OrbEventSetup 表示 orb 新增了一条 setup / resume 运行记录（只需推送 orb 更新）。
	OrbEventSetup = "setup"
)

// OrbCreate 是新建 orb 的参数（对应 orbs.CreateParams）；空 Provider / Type 由 orbs 按 C3 规则取默认。
type OrbCreate struct {
	Provider, Type, ProjectID, SnapshotOf, SnapshotID string
}

// OrbEvent 是 orb 状态或 link 变化（对应 orbs.Event，字段须与之逐一相同）。
type OrbEvent struct {
	OrbID string
	Kind  string
	From  string
	To    string
	Orb   db.Orb
	// Seq 是该次状态变化写入的 orb_events.id（单调）；非状态事件为当时已知的最大值。
	// 事件在事务提交后才发出、可能乱序到达：Seq 小于已应用值的状态事件是过时的。
	Seq int64
}

// Orbs 是线程服务依赖的 orb 生命周期接口（契约 §2.1，由 internal/orbs.Service 经薄适配实现）。
type Orbs interface {
	Create(ctx context.Context, p OrbCreate) (db.Orb, error)
	Wake(ctx context.Context, orbID string) error
	// Snapshot 入队 snapshot 任务并等待完成，返回快照 id。
	Snapshot(ctx context.Context, orbID string) (string, error)
	Ctl(orbID string) (*link.Ctl, bool)
	HandleRequest(method string, fn func(ctx context.Context, orbID string, params json.RawMessage) (any, error))
	HandleNotify(method string, fn func(ctx context.Context, orbID string, params json.RawMessage))
	HandleStream(t string, fn func(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream))
	Subscribe(fn func(OrbEvent))
	TouchUser(ctx context.Context, orbID string)
	TouchAgent(ctx context.Context, orbID string)
	// SetQuiescence 注册冻结 / 停机闸门（契约 §1 TryQuiesce）：fn 持有该 orb 全部线程的锁确认静止后调用 commit
	// （orbs 在其中做状态 CAS），返回 commit 的结果；命令准入同样持线程锁，因此与冻结串行。
	SetQuiescence(fn func(orbID string, commit func() bool) bool)
	Kick(orbID, reason string)
}
