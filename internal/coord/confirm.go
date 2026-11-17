package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 确认卡（M19 步骤 2.1）：删除与对外公开类的助手工具不直接执行，而是在该对话里挂一张确认卡（控制面发起的
// confirm 请求，threads.RequestUI）。主人在卡片或通知上作答后由 orynd 执行，结果以自动消息回到对话；模型拿不到
// 替主人作答的途径——应答只经客户端的 ui.response 或通知的一触作答到达，并且不写进 agent 流。
const (
	// confirmTTL 是确认卡的有效期：超过后置 expired、撤回卡片。
	confirmTTL = 24 * time.Hour
	// confirmSweepEvery 是过期检查的间隔。
	confirmSweepEvery = time.Hour
	// maxPendingConfirms 是每个对话同时待答的确认卡上限。
	maxPendingConfirms = 5
)

// 确认卡的状态（assistant_confirmations.state）。
const (
	confirmPending  = "pending"
	confirmApproved = "approved"
	confirmRejected = "rejected"
	confirmExpired  = "expired"
	confirmFailed   = "failed"
)

// confirmExec 执行一条主人已批准的确认；args 是 requestConfirm 时的 Args。detail 非空时作为结果消息的第二段
// （如分享链接、失败的具体原因）；返回错误时该确认记 failed、错误码进结果消息。
type confirmExec func(ctx context.Context, row db.AssistantConfirmation, args json.RawMessage) (detail string, err error)

// onConfirm 登记 action 的执行函数；须在 New 里（Run 之前）调用。
func (s *Service) onConfirm(action string, fn confirmExec) { s.confirmers[action] = fn }

// confirmReq 是一张确认卡的内容。
type confirmReq struct {
	// Action 是工具名，也是审计动作名与 Web 文案键 assistant.confirm.<action>。
	Action string
	// Target 是目标对象 id（线程、项目、Space、orb…），进结果消息的首行。
	Target string
	// TargetLabel 是目标的显示名（标题、名字）；空 = Target。
	TargetLabel string
	// Title 是卡片标题的英文一句（客户端没有对应文案时显示），如 `Delete thread "x" (thr_…)?`。
	Title string
	// Summary 是卡片正文。
	Summary string
	// AuditThread / AuditSchedule 是审计记录的目标线程与定时（可空）。
	AuditThread, AuditSchedule string
	// Args 是执行函数需要的参数（编成 JSON 存库）。
	Args any
}

// confirmParams 是 assistant_confirmations.params：卡片的显示字段（重启后重建请求帧用）、审计目标与执行参数。
type confirmParams struct {
	Title         string          `json:"title"`
	TargetLabel   string          `json:"targetLabel"`
	AuditThread   string          `json:"auditThread,omitempty"`
	AuditSchedule string          `json:"auditSchedule,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
}

// requestConfirm 在对话 c 里挂一张确认卡并返回工具结果 {status: "awaiting_owner", confirmationId}。
// 该对话待答的确认卡已有 maxPendingConfirms 张时回 rate_limited。审计在主人作答、动作真正执行后才写。
func (s *Service) requestConfirm(ctx context.Context, c toolCtx, r confirmReq) (any, error) {
	if _, ok := s.confirmers[r.Action]; !ok {
		return nil, fmt.Errorf("coord: no confirmation executor for %q", r.Action)
	}
	n, err := s.st.Q.CountPendingAssistantConfirmations(ctx, c.ThreadID)
	if err != nil {
		return nil, err
	}
	if n >= maxPendingConfirms {
		return nil, errf(CodeRateLimited, "%d confirmations are already waiting for the owner in this conversation; wait for their answers first", n)
	}
	args, err := json.Marshal(r.Args)
	if err != nil {
		return nil, err
	}
	if r.TargetLabel == "" {
		r.TargetLabel = r.Target
	}
	params, err := json.Marshal(confirmParams{Title: r.Title, TargetLabel: r.TargetLabel, AuditThread: r.AuditThread, AuditSchedule: r.AuditSchedule, Args: args})
	if err != nil {
		return nil, err
	}
	row := db.AssistantConfirmation{
		ID: ids.New("cfm"), ThreadID: c.ThreadID, Action: r.Action, Target: r.Target, Summary: truncRunes(r.Summary, 2000),
		Params: string(params), State: confirmPending, CreatedAt: s.nowMs(),
	}
	if err := s.st.Q.InsertAssistantConfirmation(ctx, db.InsertAssistantConfirmationParams{
		ID: row.ID, ThreadID: row.ThreadID, Action: row.Action, Target: row.Target, Summary: row.Summary, Params: row.Params, CreatedAt: row.CreatedAt,
	}); err != nil {
		return nil, err
	}
	if err := s.showConfirm(ctx, row); err != nil {
		code := errCode(err)
		now := s.nowMs()
		if _, e := s.st.Q.DecideAssistantConfirmation(context.WithoutCancel(ctx), db.DecideAssistantConfirmationParams{
			State: confirmFailed, Error: &code, DecidedAt: &now, ID: row.ID,
		}); e != nil {
			slog.Warn("coord: 记录确认卡失败", "confirmation", row.ID, "err", e)
		}
		return nil, err
	}
	return map[string]any{
		"status": "awaiting_owner", "confirmationId": row.ID,
		"note": "Waiting for the owner's confirmation. The result will arrive as an automatic message in this conversation; do not call the tool again.",
	}, nil
}

// showConfirm 把确认卡作为 confirm 请求挂到对话上（RequestUI 对同一 id 幂等）。
func (s *Service) showConfirm(ctx context.Context, row db.AssistantConfirmation) error {
	var p confirmParams
	_ = json.Unmarshal([]byte(row.Params), &p)
	if p.TargetLabel == "" {
		p.TargetLabel = row.Target
	}
	if p.Title == "" {
		p.Title = row.Action + " " + row.Target + "?"
	}
	frame, err := json.Marshal(map[string]any{
		"type": "extension_ui_request", "id": row.ID, "method": "confirm", "title": p.Title, "message": row.Summary,
		"oryn": map[string]string{"action": row.Action, "targetLabel": p.TargetLabel},
	})
	if err != nil {
		return err
	}
	return s.th.RequestUI(ctx, row.ThreadID, frame)
}

// UIAnswered 实现 threads.Coordinator：主人对确认卡作答。pending → approved / rejected 是 CAS，同一张卡至多执行一次；
// 批准后执行登记的动作（失败改记 failed），真正执行过的写审计（origin = owner），最后把结果以自动消息投递回对话。
func (s *Service) UIAnswered(ctx context.Context, threadID, requestID string, response json.RawMessage) {
	row, err := s.st.Q.GetAssistantConfirmation(ctx, requestID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("coord: 读取确认卡失败", "confirmation", requestID, "err", err)
		}
		return
	}
	if row.ThreadID != threadID || row.State != confirmPending {
		return
	}
	var resp struct {
		Confirmed *bool `json:"confirmed"`
	}
	_ = json.Unmarshal(response, &resp)
	state := confirmRejected
	if resp.Confirmed != nil && *resp.Confirmed {
		state = confirmApproved
	}
	now := s.nowMs()
	n, err := s.st.Q.DecideAssistantConfirmation(ctx, db.DecideAssistantConfirmationParams{State: state, DecidedAt: &now, ID: row.ID})
	if err != nil {
		slog.Warn("coord: 记录确认卡应答失败", "confirmation", row.ID, "err", err)
		return
	}
	if n == 0 {
		return // 已在别处作答或过期
	}
	if state == confirmRejected {
		s.deliverConfirm(ctx, row, "rejected by the owner.", "")
		return
	}
	var p confirmParams
	_ = json.Unmarshal([]byte(row.Params), &p)
	detail, err := s.execConfirmed(ctx, row, p.Args)
	if err != nil {
		code := errCode(err)
		if e := s.st.Q.SetAssistantConfirmationResult(context.WithoutCancel(ctx), db.SetAssistantConfirmationResultParams{
			State: confirmFailed, Error: &code, ID: row.ID,
		}); e != nil {
			slog.Warn("coord: 记录确认卡结果失败", "confirmation", row.ID, "err", e)
		}
	}
	s.audit(ctx, threads.KindAssistant, row.ThreadID, true, row.Action, p.AuditThread, p.AuditSchedule, row.Summary, err)
	if err != nil {
		if detail == "" {
			detail = errMessage(err)
		}
		s.deliverConfirm(ctx, row, "approved but failed: "+errCode(err)+".", detail)
		return
	}
	s.deliverConfirm(ctx, row, "approved and done.", detail)
}

// execConfirmed 调用 action 登记的执行函数。
func (s *Service) execConfirmed(ctx context.Context, row db.AssistantConfirmation, args json.RawMessage) (string, error) {
	fn, ok := s.confirmers[row.Action]
	if !ok {
		return "", errf(CodeInvalid, "unknown confirmation action %q", row.Action)
	}
	return fn(ctx, row, args)
}

// deliverConfirm 把确认结果投递回发起的对话：首行 `[Oryn confirmation · automatic] <action> <target>: <outcome>`，
// detail 非空时另起一段。
func (s *Service) deliverConfirm(ctx context.Context, row db.AssistantConfirmation, outcome, detail string) {
	text := "[Oryn confirmation · automatic] " + row.Action + " " + row.Target + ": " + outcome
	if detail != "" {
		text += "\n\n" + truncRunes(detail, 4000)
	}
	if _, err := s.th.Deliver(context.WithoutCancel(ctx), row.ThreadID, threads.AutoMessage{Text: text}, nil); err != nil {
		slog.Warn("coord: 投递确认结果失败", "confirmation", row.ID, "thread", row.ThreadID, "err", err)
		return
	}
	if s.sentConfirm != nil {
		s.sentConfirm(row.ThreadID, text)
	}
}

// restoreConfirmations 把仍待答的确认卡重新挂到各自的对话上（控制面重启后）。
func (s *Service) restoreConfirmations(ctx context.Context) {
	rows, err := s.st.Q.ListPendingAssistantConfirmations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("coord: 读取待答确认卡失败", "err", err)
		}
		return
	}
	for _, row := range rows {
		if err := s.showConfirm(ctx, row); err != nil {
			slog.Warn("coord: 恢复确认卡失败", "confirmation", row.ID, "thread", row.ThreadID, "err", err)
		}
	}
}

// expireConfirmations 让超过 confirmTTL 仍未作答的确认卡过期：撤回卡片，并告知发起的对话。
func (s *Service) expireConfirmations(ctx context.Context) {
	rows, err := s.st.Q.ListExpiredAssistantConfirmations(ctx, s.cfg.Now().Add(-confirmTTL).UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("coord: 读取过期确认卡失败", "err", err)
		}
		return
	}
	for _, row := range rows {
		now := s.nowMs()
		n, err := s.st.Q.DecideAssistantConfirmation(ctx, db.DecideAssistantConfirmationParams{State: confirmExpired, DecidedAt: &now, ID: row.ID})
		if err != nil || n == 0 {
			continue
		}
		s.th.WithdrawUI(ctx, row.ThreadID, row.ID)
		s.deliverConfirm(ctx, row, "expired without an answer.", "")
	}
}
