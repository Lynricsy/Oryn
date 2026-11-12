package server

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// orbsAdapter 让 *orbs.Service 满足 threads.Orbs（处理器参数在 orbs 中是具名函数类型）。
type orbsAdapter struct{ *orbs.Service }

func (a orbsAdapter) Create(ctx context.Context, p threads.OrbCreate) (db.Orb, error) {
	return a.Service.Create(ctx, orbs.CreateParams(p))
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
