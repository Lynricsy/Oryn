package server

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// orbsAdapter 让 *orbs.Service 满足 threads.Orbs（处理器参数在 orbs 中是具名函数类型）。
type orbsAdapter struct{ *orbs.Service }

func (a orbsAdapter) Create(ctx context.Context, p threads.OrbCreate) (db.Orb, error) {
	return a.Service.Create(ctx, orbs.CreateParams(p))
}

func (a orbsAdapter) ClaimPool(ctx context.Context, p threads.OrbCreate) (db.Orb, bool) {
	return a.Service.ClaimPool(ctx, orbs.CreateParams(p))
}

func (a orbsAdapter) HandleRequest(m string, fn func(context.Context, string, json.RawMessage) (any, error)) {
	a.Service.HandleRequest(m, fn)
}

func (a orbsAdapter) HandleNotify(m string, fn func(context.Context, string, json.RawMessage)) {
	a.Service.HandleNotify(m, fn)
}

func (a orbsAdapter) HandleStream(t string, fn func(context.Context, string, link.StreamHeader, *link.Stream)) {
	a.Service.HandleStream(t, fn)
}

func (a orbsAdapter) Subscribe(fn func(threads.OrbEvent)) {
	a.Service.Subscribe(func(e orbs.Event) { fn(threads.OrbEvent(e)) })
}

// revocations 把 auth.Service 的吊销订阅适配为 realtime.Revocations。
type revocations struct{ s *auth.Service }

func (r revocations) Subscribe(fn func(kind, id string)) { _ = r.s.Subscribe(fn) }

func (a orbsAdapter) SetQuiescence(fn func(orbID string, commit func() bool) bool) {
	a.Service.SetQuiescence(fn)
}

// artifactViewers 让 *realtime.Hub 满足 artifacts.ActivityHub（观看者换成 artifacts 包自己的类型）。
type artifactViewers struct{ *realtime.Hub }

func (h artifactViewers) Viewers(id string) []artifacts.Viewer {
	ws := h.Watchers(id)
	out := make([]artifacts.Viewer, len(ws))
	for i, w := range ws {
		out[i] = artifacts.Viewer{Conn: w.Conn, Page: w.Page}
	}
	return out
}

// lazyMemory 让线程服务在记忆服务构造之前就能拿到 threads.MemoryStarter：线程服务必须先于 orb link 构造，
// 记忆服务依赖更晚构造的 orb 与模型路由。agent.start 只发生在 link 建立之后，届时 s.Memory 已就绪。
type lazyMemory struct{ s *Server }

func (m lazyMemory) AgentMemory(ctx context.Context, threadID string, projectID *string) (*apigen.LinkCtlAgentMemory, error) {
	if m.s.Memory == nil {
		return nil, nil
	}
	return m.s.Memory.AgentMemory(ctx, threadID, projectID)
}
