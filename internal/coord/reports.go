package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 线程回报（契约 C3）：协调者 send_message / create_thread 时写一份登记（目标线程 → 回报对象，代号 gen 同时是投递标签），
// 目标线程这次投递的回合得到终态后向回报对象投递一次回报并消费登记。「登记 + 投递」与回报处理在 repMu 下互斥；
// Observer 回调只取 mu 入队。锁顺序 repMu → 线程锁。

// reportTagPrefix 是回报登记代号（= 投递标签）的前缀。
const reportTagPrefix = "rep_"

// reportAnchor 是协调者投递的消息正文的开头（来源头行，见 sourceLine）：重启前写下的登记没有标签关联，
// 回报时按它在转录里找本次投递的锚点（补充契约 C1.6）。登记时间之后的第一条这样的消息就是本次投递——
// 同一目标线程只有一份登记，后来者替换。
const reportAnchor = "[Oryn message from "

// armedReport 是本进程内写下的登记：gen 为代号，seq 为登记前的事件序号（序号不大于它的状态事件属于登记之前）。
type armedReport struct {
	gen string
	seq uint64
}

// repEvent 是排队的回报事件：状态变化、带标签投递的终态，或「条目已入库」的重查（三者恰有其一）。
type repEvent struct {
	seq     uint64
	state   *threads.StateEvent
	turn    *threads.TurnEvent
	entries string
}

func (e repEvent) threadID() string {
	switch {
	case e.state != nil:
		return e.state.ThreadID
	case e.turn != nil:
		return e.turn.ThreadID
	}
	return e.entries
}

// push 入队一个事件并编号。调用方不持 mu。
func (s *Service) push(e repEvent) {
	s.mu.Lock()
	s.seq++
	e.seq = s.seq
	s.events = append(s.events, e)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// StateChanged 实现 threads.Observer：只入队（回调持线程锁）。进入 error（任意前态），或由 working 进入 idle /
// awaiting_input 的变化才可能产生回报。
func (s *Service) StateChanged(ev threads.StateEvent) {
	switch {
	case ev.State == threads.StateError:
	case ev.Prev == threads.StateWorking && (ev.State == threads.StateIdle || ev.State == threads.StateAwaitingInput):
	default:
		return
	}
	s.push(repEvent{state: &ev})
}

// TurnEnded 实现 threads.Observer：回报登记的投递（标签 rep_…）得到终态时入队。
func (s *Service) TurnEnded(ev threads.TurnEvent) {
	if !strings.HasPrefix(ev.Tag, reportTagPrefix) {
		return
	}
	s.push(repEvent{turn: &ev})
}

// EntriesAppended 实现 threads.Observer（契约 C1.7）：只有该线程有「非 tagged 登记且上次 RecoveredOutcome 为 !ok」时
// 才入队重查；O(1)，不读库。
func (s *Service) EntriesAppended(threadID string) {
	s.mu.Lock()
	need := s.recheck[threadID]
	s.mu.Unlock()
	if need {
		s.push(repEvent{entries: threadID})
	}
}

// InputRequested 实现 threads.Observer：记下待答问题的标题，awaiting_input 的回报里给出。
func (s *Service) InputRequested(ev threads.InputEvent) {
	title := "plan proposal"
	if !ev.Plan {
		var f struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Frame, &f)
		title = f.Title
		if title == "" {
			title = f.Message
		}
	}
	s.mu.Lock()
	s.questions[ev.ThreadID] = truncRunes(title, maxReportRunes)
	s.mu.Unlock()
}

// InputResolved 实现 threads.Observer（不关心）。
func (s *Service) InputResolved(string, string) {}

// Seen 实现 threads.Observer（不关心）。
func (s *Service) Seen(string) {}

// arm 在持 repMu 时写下一份新登记（替换旧的），返回代号。
func (s *Service) arm(ctx context.Context, target, reportTo string, requestedAt int64) (string, error) {
	gen := ids.New("rep")
	if err := s.st.Q.UpsertThreadReport(ctx, db.UpsertThreadReportParams{ThreadID: target, ReportTo: reportTo, RequestedAt: requestedAt, Gen: gen}); err != nil {
		return "", err
	}
	return gen, nil
}

// setArmed 记下本进程写下的登记（替换旧的；不再需要按条目重查）。
func (s *Service) setArmed(target string, a armedReport) {
	s.mu.Lock()
	s.armed[target] = a
	delete(s.recheck, target)
	s.mu.Unlock()
}

// curSeq 返回当前的事件序号。
func (s *Service) curSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// consume 按代号消费登记，并清掉内存里的对应状态。调用方持 repMu。
func (s *Service) consume(ctx context.Context, threadID, gen string) {
	if _, err := s.st.Q.DeleteThreadReportGen(context.WithoutCancel(ctx), db.DeleteThreadReportGenParams{ThreadID: threadID, Gen: gen}); err != nil {
		slog.Warn("coord: 删除回报登记失败", "thread", threadID, "err", err)
	}
	s.mu.Lock()
	if a, ok := s.armed[threadID]; ok && a.gen == gen {
		delete(s.armed, threadID)
	}
	delete(s.recheck, threadID)
	s.mu.Unlock()
}

// processReports 处理排队的事件；每个事件在 repMu 下处理（与「登记 + 投递」互斥）。
func (s *Service) processReports(ctx context.Context) {
	for {
		s.mu.Lock()
		evs := s.events
		s.events = nil
		s.mu.Unlock()
		if len(evs) == 0 {
			return
		}
		for _, ev := range evs {
			s.repMu.Lock()
			s.handleReport(ctx, ev)
			s.repMu.Unlock()
			if s.afterReport != nil {
				s.afterReport(ev.threadID())
			}
		}
	}
}

// reportBody 是回报包裹内的 JSON（C9.2）。
type reportBody struct {
	Title    string `json:"title"`
	Outcome  string `json:"outcome"`
	Reply    string `json:"reply,omitempty"`
	Error    string `json:"error,omitempty"`
	Question string `json:"question,omitempty"`
}

// 回报的 outcome 取值（TurnEvent.Status 之外）。
const outcomeNeedsInput = "needs_input"

// handleReport 按契约 C3 的事件处理表（及 C1.6 / C1.7 对非 tagged 登记的修订）处理一个事件。调用方持 repMu。
func (s *Service) handleReport(ctx context.Context, ev repEvent) {
	id := ev.threadID()
	r, err := s.st.Q.GetThreadReport(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		s.mu.Lock()
		delete(s.recheck, id)
		s.mu.Unlock()
		return
	}
	if err != nil {
		slog.Warn("coord: 读取回报登记失败", "thread", id, "err", err)
		return
	}
	s.mu.Lock()
	a, has := s.armed[id]
	s.mu.Unlock()
	tagged := has && a.gen == r.Gen
	switch {
	case ev.turn != nil:
		// 只有本次投递的终态能消费登记（重启前排队的首条提示词带着同一代号，重启后照样结算）
		if ev.turn.Tag != r.Gen {
			return
		}
		s.finalFromTurn(ctx, r, *ev.turn)
	case ev.state != nil:
		st := *ev.state
		if tagged && ev.seq <= a.seq {
			return // 登记之前的旧事件
		}
		switch {
		case st.State == threads.StateAwaitingInput:
			s.mu.Lock()
			q := s.questions[id]
			s.mu.Unlock()
			s.deliverReport(ctx, r, threads.StateAwaitingInput, reportBody{Outcome: outcomeNeedsInput, Question: q}, false, false)
		case st.State == threads.StateError && !st.PromptError:
			s.deliverReport(ctx, r, threads.StateError, reportBody{Outcome: threads.TurnError, Error: st.Reason}, false, true)
		case st.State == threads.StateError:
			if tagged {
				return // 等 TurnEnded
			}
			if out, ok := s.recovered(ctx, r); ok {
				s.finalFromTurn(ctx, r, out)
				return
			}
			s.deliverReport(ctx, r, threads.StateError, reportBody{Outcome: threads.TurnError}, false, true)
		case st.State == threads.StateIdle:
			if tagged {
				return // 等 TurnEnded
			}
			s.recheckUntagged(ctx, r)
		}
	default:
		if !tagged {
			s.recheckUntagged(ctx, r)
		}
	}
}

// recovered 从转录推断非 tagged 登记那次投递的终态（补充契约 C1.6）。
func (s *Service) recovered(ctx context.Context, r db.ThreadReport) (threads.TurnEvent, bool) {
	out, ok, err := s.th.RecoveredOutcome(ctx, r.ThreadID, reportAnchor, r.RequestedAt)
	if err != nil {
		slog.Warn("coord: 推断回合终态失败", "thread", r.ThreadID, "err", err)
		return threads.TurnEvent{}, false
	}
	return out, ok
}

// recheckUntagged 对非 tagged 登记查一次转录：有终态则回报并消费，否则保留登记、等条目入库后再查（C1.7）。
func (s *Service) recheckUntagged(ctx context.Context, r db.ThreadReport) {
	if out, ok := s.recovered(ctx, r); ok {
		s.finalFromTurn(ctx, r, out)
		return
	}
	s.mu.Lock()
	s.recheck[r.ThreadID] = true
	s.mu.Unlock()
}

// finalFromTurn 以回合终态投递终态回报并消费登记（C2：正文取 TurnEvent.Text；HasText 为假时才读库）。
func (s *Service) finalFromTurn(ctx context.Context, r db.ThreadReport, ev threads.TurnEvent) {
	body := reportBody{Outcome: ev.Status, Error: ev.Error}
	if ev.HasText {
		body.Reply = truncRunes(ev.Text, maxReportRunes)
	}
	state := threads.StateIdle
	if ev.Status == threads.TurnError {
		state = threads.StateError
	}
	s.deliverReport(ctx, r, state, body, !ev.HasText, true)
}

// deliverReport 向回报对象投递一次回报；final 为真时随后消费登记。回报对象已归档 / 不存在、超过回报限速、
// 目标线程已不存在时同样按代号消费登记后丢弃。readReply 为真时正文从库里取最后一条回复（没有看到回合的 agent_end）。
func (s *Service) deliverReport(ctx context.Context, r db.ThreadReport, state string, body reportBody, readReply, final bool) {
	drop := func(why string) {
		slog.Info("coord: 丢弃线程回报", "thread", r.ThreadID, "reportTo", r.ReportTo, "why", why)
		s.consume(ctx, r.ThreadID, r.Gen)
	}
	to, err := s.st.Q.GetThread(ctx, r.ReportTo)
	if err != nil || to.ArchivedAt != nil {
		drop("report target archived or gone")
		return
	}
	if !s.lim.allow(r.ReportTo+"|report", s.cfg.Now(), reportRules) {
		drop("report rate limit")
		return
	}
	t, err := s.st.Q.GetThread(ctx, r.ThreadID)
	if err != nil {
		drop("thread gone")
		return
	}
	body.Title = t.Title
	if readReply {
		if body.Reply, err = s.lastAssistantText(ctx, t, maxReportRunes); err != nil {
			slog.Warn("coord: 读取线程回复失败", "thread", t.ID, "err", err)
		}
	}
	text := reportText(t.ID, state, body)
	if _, err := s.th.Deliver(ctx, r.ReportTo, threads.AutoMessage{Text: text}, nil); err != nil {
		slog.Warn("coord: 投递线程回报失败", "thread", t.ID, "reportTo", r.ReportTo, "err", err)
	} else if s.sentReport != nil {
		s.sentReport(r.ReportTo, text)
	}
	if final {
		s.consume(ctx, r.ThreadID, r.Gen)
	}
}

// reportText 是回报正文（C9.2）：首行只有线程 id 与状态，来自线程的内容一律在包裹内的 JSON 里。
func reportText(threadID, state string, body reportBody) string {
	return "[Oryn thread report · automatic] " + threadID + " is now " + state + ".\n" + wrapUntrusted(threadID, body)
}
