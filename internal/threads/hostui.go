package threads

import (
	"context"
	"encoding/json"
	"strings"
)

// hostUIPrefix 是控制面发起的待答请求的 id 前缀（M19 确认卡，coord 用 ids.New("cfm") 生成）。
const hostUIPrefix = "cfm_"

// RequestUI 在线程里挂一个控制面发起的待答请求（M19 确认卡）：只接受 id 以 cfm_ 开头的 confirm 类
// extension_ui_request 帧。请求像 agent 的请求一样进实时缓冲、广播给客户端、触发 InputRequested 并让线程变为
// awaiting_input；主人的应答不写 agent 流，而在后台交给 Coordinator.UIAnswered。同一 id 重复调用幂等。
func (s *Service) RequestUI(ctx context.Context, threadID string, frame json.RawMessage) error {
	var h frameHead
	if json.Unmarshal(frame, &h) != nil || h.Type != "extension_ui_request" || h.Method != "confirm" || !strings.HasPrefix(h.ID, hostUIPrefix) {
		return errf("invalid_frame", "host ui request must be a confirm extension_ui_request with a "+hostUIPrefix+" id")
	}
	t, err := s.get(ctx, threadID)
	if err != nil {
		return err
	}
	frame = append(json.RawMessage(nil), frame...)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.hostUI[h.ID]; ok {
		return nil
	}
	if t.hostUI == nil {
		t.hostUI = map[string]json.RawMessage{}
	}
	t.hostUI[h.ID] = frame
	t.live.ApplyFrame(frame)
	s.out().Frame(t.id, frame)
	s.observer().InputRequested(InputEvent{ThreadID: t.id, RequestID: h.ID, Frame: frame, Focused: s.Focused(t.id)})
	s.recompute(ctx, t)
	return nil
}

// WithdrawUI 撤回一个控制面发起的待答请求（过期）：从实时缓冲移除并广播 ui.resolved、InputResolved。
// 不存在时什么都不做。
func (s *Service) WithdrawUI(ctx context.Context, threadID, id string) {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.hostUI[id]; !ok {
		return
	}
	delete(t.hostUI, id)
	t.live.ResolveUI(id)
	s.out().UIResolved(t.id, id)
	s.observer().InputResolved(t.id, id)
	s.recompute(ctx, t)
}

// answerHostUILocked 处理主人对 hostUI 请求的应答：与 agent 请求同样解决与广播，但不写 agent 流、不唤醒 orb；
// 应答在后台交给 Coordinator.UIAnswered。调用方持有 t.mu（返回后仍持有）。
func (s *Service) answerHostUILocked(ctx context.Context, t *thread, id string, frame json.RawMessage) {
	delete(t.hostUI, id)
	t.live.ResolveUI(id)
	s.out().UIResolved(t.id, id)
	s.observer().InputResolved(t.id, id)
	s.clearUnreadLocked(ctx, t)
	s.recompute(ctx, t)
	s.observer().Seen(t.id)
	if c := s.coord; c != nil {
		threadID, resp := t.id, append(json.RawMessage(nil), frame...)
		s.go_(func(ctx context.Context) { c.UIAnswered(ctx, threadID, id, resp) })
	}
}
