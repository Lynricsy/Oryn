package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/research"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M26：后台研究的发现进主线，以及「受限的一段」。来自外部内容（网页、邮件、日历）的发现（收件 restricted = 1）只投给
// orbd 报回变体为 main_restricted 的主线 agent：只有只读工具与 stay_silent。是否受限只认 threads.AgentVariant 报回的
// 事实；该投递的回合结束后主线下一次启动恢复完整工具表。

// 受限一段的固定值。
const (
	// mainModeRetry 是等主线 agent 换成目标变体时的重查间隔。
	mainModeRetry = 5 * time.Second
	// mainRestrictMax 是受限投递的保险：超过这么久仍没有回合结束，撤回还在排队的消息、收件回到待投。
	mainRestrictMax = 30 * time.Minute
	// inboxScan 是一次 flushInbox 读出的待投收件上限（按连续的组切分）。
	inboxScan = 200
	// inboxTagPrefix 是受限投递的标签前缀。
	inboxTagPrefix = "inr_"
)

// researchHeader 是研究发现的自动消息头行（Web 的 parseOrynHeader 认它）。
const researchHeader = "[Oryn research findings · automatic]"

// restrictedMainTools 是受限的一段里主线可用的宿主工具（只读加 stay_silent）。
var restrictedMainTools = map[string]bool{
	ToolListThreads: true, ToolReadThread: true, ToolListSchedules: true, ToolListProjects: true, ToolGetStats: true,
	ToolListFiles: true, ToolSearchHistory: true, ToolRecallMemory: true, ToolRecallEngineering: true, ToolStaySilent: true,
}

// mainRestrictedNote 接在受限主线的系统提示词末尾。
const mainRestrictedNote = `## Limited turn

This episode handles findings that came from outside (web pages, mail, calendar). Only read tools and stay_silent are available: you cannot start threads, send messages, use the computer, browse or change anything. Tell the owner what you found. If they want action they will reply, and the full tools return with their next message.`

// mainRestrict 是受限一段的状态（s.mu 保护）：want 为真时主线下一次启动取受限的 Profile；inflight 是已投出、回合还没
// 结束的受限投递（键 = 标签去掉前缀）。
type mainRestrict struct {
	want     bool
	inflight map[string]*restrictedSend
}

type restrictedSend struct {
	cancel func() bool
	rows   []string
	since  time.Time
}

// mainRestricted 报告主线下一次启动是否取受限的 Profile。
func (s *Service) mainRestricted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restrict.want || len(s.restrict.inflight) > 0
}

// restrictProfile 把主线的完整 Profile 收窄成受限的一段：只留放行表里的工具，不联网、没有 MCP 凭据。
func restrictProfile(prof threads.AgentProfile) threads.AgentProfile {
	tools := make([]apigen.OmpFrame, 0, len(restrictedMainTools))
	for _, def := range prof.Tools {
		if name, _ := def["name"].(string); restrictedMainTools[name] {
			tools = append(tools, def)
		}
	}
	prof.Tools = tools
	prof.Web, prof.MCPToken = false, ""
	prof.Variant = research.VariantMainRestricted
	prof.SystemPrompt += "\n\n" + mainRestrictedNote
	return prof
}

// restrictedToolGate 拒绝受限主线调用放行表之外的宿主工具（agent 的工具表本就没有它们；这里是第二道闸）。
func (s *Service) restrictedToolGate(c threads.HostToolCall) error {
	if c.Variant == research.VariantMainRestricted && !restrictedMainTools[c.Name] {
		return errf(CodeNotAvailable, "%s is not available in this turn; it returns with the owner's next message", c.Name)
	}
	return nil
}

// PostFinding 实现 research.Host：把一条发现写进主线收件（不截断：字段长度由研究服务保证）。
func (s *Service) PostFinding(ctx context.Context, f research.Finding) {
	body, err := json.Marshal(f)
	if err != nil {
		slog.Warn("coord: 编码研究发现失败", "run", f.RunID, "err", err)
		return
	}
	var restricted int64
	if f.Restricted {
		restricted = 1
	}
	p := db.InsertAssistantInboxParams{
		ID: ids.New("inb"), Kind: "research", SourceID: f.RunID, SourceName: f.Title, ThreadID: f.ThreadID,
		Outcome: "finding", Body: string(body), CreatedAt: s.nowMs(), Restricted: restricted,
	}
	if err := s.st.Q.InsertAssistantInbox(ctx, p); err != nil {
		slog.Warn("coord: 写入研究发现失败", "run", f.RunID, "err", err)
		return
	}
	s.armInbox()
}

// inboxGroup 是待投收件里一段连续的同类行（受限与否、是否研究发现都相同）。
type inboxGroup struct {
	restricted, research bool
	rows                 []db.AssistantInbox
}

// groupInbox 把待投收件按 (restricted, kind == research) 切成连续的组，每组至多 inboxBatch 条。
func groupInbox(rows []db.AssistantInbox) []inboxGroup {
	var out []inboxGroup
	for _, r := range rows {
		restricted, isResearch := r.Restricted == 1, r.Kind == "research"
		if n := len(out); n > 0 && out[n-1].restricted == restricted && out[n-1].research == isResearch && len(out[n-1].rows) < inboxBatch {
			out[n-1].rows = append(out[n-1].rows, r)
			continue
		}
		out = append(out, inboxGroup{restricted: restricted, research: isResearch, rows: []db.AssistantInbox{r}})
	}
	return out
}

// researchText 把一组研究发现拼成一条自动消息；解不出的行标为已投递并跳过（返回空串表示整组都解不出）。
func (s *Service) researchText(ctx context.Context, g *inboxGroup) string {
	items := make([]json.RawMessage, 0, len(g.rows))
	kept := g.rows[:0]
	for _, r := range g.rows {
		if !json.Valid([]byte(r.Body)) {
			slog.Warn("coord: 研究发现不是合法 JSON，丢弃", "inbox", r.ID)
			s.markInbox(ctx, []db.AssistantInbox{r})
			continue
		}
		items = append(items, json.RawMessage(r.Body))
		kept = append(kept, r)
	}
	g.rows = kept
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf("%s %d finding(s).\n", researchHeader, len(items)) + wrapUntrusted("", map[string]any{"findings": items})
}

// markInbox 把收件标为已投递。
func (s *Service) markInbox(ctx context.Context, rows []db.AssistantInbox) {
	now := s.nowMs()
	for _, r := range rows {
		if err := s.st.Q.MarkAssistantInboxDelivered(ctx, db.MarkAssistantInboxDeliveredParams{DeliveredAt: &now, ID: r.ID}); err != nil {
			slog.Warn("coord: 标记主线收件已投递失败", "inbox", r.ID, "err", err)
		}
	}
}

// sendRestricted 把一组受限收件投给受限的主线 agent。先占位（此后任何一次启动取到的都是受限 Profile），再看 orbd 报回的
// 变体：不是 main_restricted 就让主线在静止后重启、排一次重查，本次不投。调用方持 inboxMu、不持 s.mu。
func (s *Service) sendRestricted(ctx context.Context, mainID string, rows []db.AssistantInbox, text string) bool {
	key := ids.New("inr")
	rowIDs := make([]string, len(rows))
	for i, r := range rows {
		rowIDs[i] = r.ID
	}
	s.mu.Lock()
	s.restrict.want = true
	if s.restrict.inflight == nil {
		s.restrict.inflight = map[string]*restrictedSend{}
	}
	s.restrict.inflight[key] = &restrictedSend{rows: rowIDs, since: s.cfg.Now()}
	s.mu.Unlock()

	if v, known := s.th.AgentVariant(ctx, mainID); !known || v != research.VariantMainRestricted {
		s.dropRestricted(key)
		s.th.RefreshAgent(ctx, mainID)
		if err := s.th.KeepWarm(ctx, mainID); err == nil {
			s.retryModeLater()
		} else {
			slog.Info("coord: 主线暂不能以受限方式启动，稍后重试", "err", err)
		}
		return false
	}
	cancel, err := s.th.Deliver(ctx, mainID, threads.AutoMessage{Text: text, Tag: inboxTagPrefix + key}, nil)
	if err != nil {
		slog.Info("coord: 受限收件投递失败，稍后重试", "pending", len(rows), "err", err)
		s.dropRestricted(key)
		return false
	}
	s.mu.Lock()
	if p := s.restrict.inflight[key]; p != nil {
		p.cancel = cancel
	}
	s.mu.Unlock()
	return true
}

// dropRestricted 删掉一个占位（不改 want）。
func (s *Service) dropRestricted(key string) {
	s.mu.Lock()
	delete(s.restrict.inflight, key)
	s.mu.Unlock()
}

// retryModeLater 在 mainModeRetry 之后重跑一次 flushInbox（已排过则不重排）。
func (s *Service) retryModeLater() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inbox.retry != nil {
		return
	}
	s.inbox.retry = s.cfg.AfterFunc(mainModeRetry, func() {
		s.mu.Lock()
		s.inbox.retry = nil
		s.mu.Unlock()
		s.flushInbox(context.Background())
	})
}

// restrictedTurnEnded 处理受限投递的回合终态（Observer 回调，持线程锁：只改 s.mu 下的状态并起 goroutine）。
func (s *Service) restrictedTurnEnded(ev threads.TurnEvent) {
	key := strings.TrimPrefix(ev.Tag, inboxTagPrefix)
	s.mu.Lock()
	if _, ok := s.restrict.inflight[key]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.restrict.inflight, key)
	ended := len(s.restrict.inflight) == 0
	if ended {
		s.restrict.want = false
	}
	s.mu.Unlock()
	if ended {
		go func() {
			ctx := context.Background()
			s.resetMainMode(ctx, ev.ThreadID)
			s.flushInbox(ctx)
		}()
	}
}

// resetMainMode 让仍是受限变体的主线 agent 在静止后停掉：下一次启动恢复完整工具表。不持 s.mu 调用。
func (s *Service) resetMainMode(ctx context.Context, mainID string) {
	if mainID == "" {
		return
	}
	if v, known := s.th.AgentVariant(ctx, mainID); known && v == research.VariantMainRestricted {
		s.th.RefreshAgent(ctx, mainID)
	}
}

// expireRestricted 是受限投递的保险（retryInbox 调用）：超过 mainRestrictMax 的占位先撤回还在排队的消息（撤回成功时
// 收件回到待投），再删除；占位因此清空时复位。
func (s *Service) expireRestricted(ctx context.Context) {
	cutoff := s.cfg.Now().Add(-mainRestrictMax)
	s.mu.Lock()
	var old []*restrictedSend
	for k, p := range s.restrict.inflight {
		if p.since.Before(cutoff) {
			old = append(old, p)
			delete(s.restrict.inflight, k)
		}
	}
	ended := len(old) > 0 && len(s.restrict.inflight) == 0
	if ended {
		s.restrict.want = false
	}
	mainID := s.mainID
	s.mu.Unlock()
	for _, p := range old {
		slog.Warn("coord: 受限投递超时仍没有回合结束", "rows", len(p.rows))
		if p.cancel == nil || !p.cancel() {
			continue
		}
		for _, id := range p.rows {
			if err := s.st.Q.UnmarkAssistantInboxDelivered(ctx, id); err != nil {
				slog.Warn("coord: 收件放回待投失败", "inbox", id, "err", err)
			}
		}
	}
	if ended {
		s.resetMainMode(ctx, mainID)
	}
}
