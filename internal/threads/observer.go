package threads

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

// Observer 接收通知所需的线程事件（由 internal/notify 实现，M5 计划 3.1）。
// 回调在持有线程锁时同步调用：实现方只可入队，不得回调 Service。
type Observer interface {
	// StateChanged 在线程状态确实变化后调用。
	StateChanged(ev StateEvent)
	// InputRequested 在新的待答请求进入实时缓冲时调用（已存在的请求重复到达不再调用）。
	InputRequested(ev InputEvent)
	// InputResolved 在待答请求被作答 / 取消 / 随实时缓冲重置消失时调用；计划提案的 requestID 为 "plan"。
	InputResolved(threadID, requestID string)
	// Seen 在用户与线程交互时调用（focus、cmd、ui.response）：热路径，实现须 O(1)。
	Seen(threadID string)
}

// StateEvent 是一次线程状态变化。Reason 只在 State == error 时可能非空（所在 orb 失败的原因）。
type StateEvent struct {
	ThreadID, Prev, State, Reason string
	// Focused 为真表示变化发生时有连接正在看该线程。
	Focused bool
}

// InputEvent 是一个新的待答请求。
type InputEvent struct {
	ThreadID, RequestID string
	// Frame 是 extension_ui_request 或 plan_proposal 帧。
	Frame   json.RawMessage
	Plan    bool
	Focused bool
}

// PlanRequestID 是计划提案在 Observer 回调里的请求 id。
const PlanRequestID = "plan"

type observerBox struct{ Observer }

// nopObserver 在未设置 Observer 时丢弃事件。
type nopObserver struct{}

func (nopObserver) StateChanged(StateEvent)      {}
func (nopObserver) InputRequested(InputEvent)    {}
func (nopObserver) InputResolved(string, string) {}
func (nopObserver) Seen(string)                  {}

// SetObserver 设置通知事件接收方。
func (s *Service) SetObserver(o Observer) { s.obs.Store(&observerBox{o}) }

func (s *Service) observer() Observer { return s.obs.Load().Observer }

// Publish 取线程锁后推送该线程的 index.patch（分享增删后刷新 Thread.publicShares 用）。
func (s *Service) Publish(ctx context.Context, threadID string) {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	s.publish(ctx, t.id)
	t.mu.Unlock()
}

// resetLiveLocked 丢弃实时缓冲，并对随之消失的待答请求与计划提案逐个发 InputResolved。调用方持有 t.mu。
func (s *Service) resetLiveLocked(t *thread) {
	ids := t.live.PendingUIIDs()
	plan := t.live.PlanProposal != nil
	t.live.Reset()
	o := s.observer()
	for _, id := range ids {
		o.InputResolved(t.id, id)
	}
	if plan {
		o.InputResolved(t.id, PlanRequestID)
	}
}

// errorReason 返回 error 状态的原因：所在 orb 为 failed 时取导致失败的原因，其余为空。调用方持有 t.mu。
func (s *Service) errorReason(ctx context.Context, t *thread) string {
	if t.orbID == "" || s.orbState(t.orbID) != orbStateFailed {
		return ""
	}
	reason, err := orbs.FailureReason(ctx, s.st.Q, t.orbID)
	if err != nil {
		slog.Warn("threads: 读取 orb 失败原因失败", "orb", t.orbID, "err", err)
	}
	return reason
}

// publicShares 返回线程未撤销的公开分享数（0 时 nil，字段省略）。
func (s *Service) publicShares(ctx context.Context, threadID string) *int {
	n, err := s.st.Q.CountActiveThreadShares(ctx, threadID)
	if err != nil || n == 0 {
		return nil
	}
	v := int(n)
	return &v
}
