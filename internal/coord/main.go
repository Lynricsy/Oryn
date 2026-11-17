package coord

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 主线（M19）：全局助手的永久对话。每段上下文很短：agent 每次启动先 clear_context，系统提示词末尾带「主线近况」
// （备忘 + 最近的对话）；主线静止一段时间或一段内回合数满了，agent 在静止时停掉，下一段重新开始。

// 主线专属工具名。stay_silent 由步骤 3.3 实现；近况按它的名字识别沉默的回合。
const (
	ToolUpdateNotes = "update_notes"
	ToolStaySilent  = "stay_silent"
)

// 主线的固定值。
const (
	// mainNotesMax 是备忘的字数上限（Unicode 字符）。
	mainNotesMax = 1500
	// mainIdleStop 是主线静止多久后停掉 agent（下一条消息开始新的一段）。
	mainIdleStop = 10 * time.Minute
	// mainEpisodeTurns 是一段内的回合上限：满了在静止时停掉 agent。
	mainEpisodeTurns = 8
	// mainBriefCount / mainBriefEach / mainBriefTotal 是近况里最近对话的条数、单条字数与合计字数上限。
	mainBriefCount = 12
	mainBriefEach  = 600
	mainBriefTotal = 6000
	// mainBriefScan 是生成近况时向前扫描的主会话消息条数。
	mainBriefScan = 300
)

// mainToolDefs 是主线专属工具（只在主线的工具表里）。
func mainToolDefs() []apigen.OmpFrame {
	return []apigen.OmpFrame{
		essentialTool(ToolUpdateNotes, "Rewrite the main conversation's notes as a whole (at most 1500 characters). The notes are shown at the start of every episode; keep there what you must remember across episodes. Compress when it gets long.", obj{
			"text": str("The complete new notes."),
		}, "text"),
	}
}

// mainGuide 是主线的固定说明（接在系统提示词末尾、近况之前）。
const mainGuide = `## This is the main conversation

- This is your permanent conversation with the owner. You speak here on your own initiative: results of schedules and trigger rules that were handed to you, reports of threads you started from here, and reminders you set here arrive as automatic messages whose first line starts with "[Oryn ".
- Combine several results into one short message. Say only what the owner would want to know or must decide.
- Every episode starts with a short context: only the notes and the recent messages below. Write what you must remember across episodes into the notes with update_notes (it rewrites the notes as a whole).
- To look further back, use read_thread with this conversation's id and search_history.
- When the owner wants to dig into one thing, suggest "Continue in a new conversation" in the interface; you cannot start conversations yourself.`

// isMain 报告 threadID 是否是主线（读库；工具与 REST 用）。
func (s *Service) isMain(ctx context.Context, threadID string) bool {
	row, err := s.st.Q.GetThread(ctx, threadID)
	return err == nil && row.Main == 1
}

// ---- 备忘 ----

// Notes 返回主线备忘。
func (s *Service) Notes(ctx context.Context) (apigen.AssistantNotes, error) {
	row, err := s.st.Q.GetAssistantSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.AssistantNotes{Text: ""}, nil
	}
	if err != nil {
		return apigen.AssistantNotes{}, err
	}
	return apigen.AssistantNotes{Text: row.MainNotes, UpdatedAt: row.MainNotesUpdatedAt}, nil
}

// PutNotes 整体改写主线备忘（上限 mainNotesMax 字）。
func (s *Service) PutNotes(ctx context.Context, text string) (apigen.AssistantNotes, error) {
	if n := utf8.RuneCountInString(text); n > mainNotesMax {
		return apigen.AssistantNotes{}, errf(CodeInvalid, "the notes have %d characters; the limit is %d — compress them", n, mainNotesMax)
	}
	now := s.nowMs()
	if err := s.st.Q.SetAssistantMainNotes(ctx, db.SetAssistantMainNotesParams{MainNotes: text, MainNotesUpdatedAt: &now}); err != nil {
		return apigen.AssistantNotes{}, err
	}
	return apigen.AssistantNotes{Text: text, UpdatedAt: &now}, nil
}

// toolUpdateNotes 是主线的 update_notes。
func (s *Service) toolUpdateNotes(ctx context.Context, c toolCtx) (any, error) {
	if c.Kind != threads.KindAssistant || !s.isMain(ctx, c.ThreadID) {
		return nil, errf("unknown_tool", "no tool named %q", c.Name)
	}
	var a struct {
		Text *string `json:"text"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	if a.Text == nil {
		return nil, errf(CodeInvalid, "text is required")
	}
	n, err := s.PutNotes(ctx, *a.Text)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "characters": utf8.RuneCountInString(n.Text)}, nil
}

// ---- 近况 ----

// mainBrief 生成「主线近况」：备忘全文与最近的对话（主人的消息、每个回合说出口的最后一条助手消息；沉默的回合不取）。
func (s *Service) mainBrief(ctx context.Context, threadID string) (string, error) {
	notes, err := s.Notes(ctx)
	if err != nil {
		return "", err
	}
	rows, err := s.st.Q.ListRecentMainMessagesTs(ctx, db.ListRecentMainMessagesTsParams{ThreadID: threadID, Limit: mainBriefScan})
	if err != nil {
		return "", err
	}
	slices.Reverse(rows) // 时间正序
	type item struct {
		ts   int64
		who  string
		text string
	}
	var items []item
	// 一个回合：一条 user 消息之后的助手消息，直到下一条 user 消息
	var turnText string
	var turnTS int64
	silent := false
	flush := func() {
		if turnText != "" && !silent {
			items = append(items, item{ts: turnTS, who: "你", text: turnText})
		}
		turnText, turnTS, silent = "", 0, false
	}
	for _, r := range rows {
		m, ok := transcript.ParseMessage(transcript.Entry{Type: "message", Payload: r.Payload})
		if !ok {
			continue
		}
		ts := int64(0)
		if r.Ts != nil {
			ts = *r.Ts
		}
		switch m.Role {
		case "user":
			flush()
			if t := strings.TrimSpace(m.Text); t != "" && !transcript.IsAutomatedUserText(t) {
				items = append(items, item{ts: ts, who: "主人", text: t})
			}
		case "assistant":
			for _, tc := range m.ToolCalls {
				if tc.Name == ToolStaySilent {
					silent = true
				}
			}
			if t := strings.TrimSpace(m.Text); t != "" {
				turnText, turnTS = t, ts
			}
		}
	}
	flush()
	if len(items) > mainBriefCount {
		items = items[len(items)-mainBriefCount:]
	}
	// 从新到旧累计预算，超出的更早条目丢弃
	loc := s.cfg.Now().Location()
	lines := make([]string, 0, len(items))
	total := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		text := truncRunes(it.text, mainBriefEach)
		n := utf8.RuneCountInString(text)
		if total+n > mainBriefTotal {
			break
		}
		total += n
		when := ""
		if it.ts > 0 {
			when = "[" + time.UnixMilli(it.ts).In(loc).Format("01-02 15:04") + "] "
		}
		lines = append(lines, when+it.who+"："+text)
	}
	slices.Reverse(lines)
	var b strings.Builder
	b.WriteString("## 主线近况\n\n### 备忘\n\n")
	if strings.TrimSpace(notes.Text) == "" {
		b.WriteString("（空）")
	} else {
		b.WriteString(notes.Text)
	}
	b.WriteString("\n\n### 最近的对话\n\n")
	if len(lines) == 0 {
		b.WriteString("（还没有）")
	} else {
		b.WriteString(strings.Join(lines, "\n\n"))
	}
	return b.String(), nil
}

// ---- 分段 ----

// mainEpisode 是主线当前一段的计数与静止定时器（s.mu 保护）。
type mainEpisode struct {
	turns int
	stop  func()
}

// setMainID 记下主线 id（Observer 回调据此识别主线，不读库）。
func (s *Service) setMainID(id string) {
	s.mu.Lock()
	s.mainID = id
	s.mu.Unlock()
}

// loadMainID 在启动时读出主线 id。
func (s *Service) loadMainID(ctx context.Context) {
	row, err := s.st.Q.GetMainThread(ctx)
	if err == nil {
		s.setMainID(row.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("coord: 读取主线失败", "err", err)
	}
}

// mainStateChanged 在 StateChanged（持线程锁、只能入队）里维护主线的分段：working→idle 计一个回合并排静止定时器，
// 进入 working 取消定时器；定时器到期或回合满 mainEpisodeTurns 时让主线在静止后停掉 agent（RefreshAgent 在别的 goroutine
// 里调，不在线程锁下）。
func (s *Service) mainStateChanged(ev threads.StateEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mainID == "" || ev.ThreadID != s.mainID {
		return
	}
	ep := &s.mainEp
	switch {
	case ev.State == threads.StateWorking:
		if ep.stop != nil {
			ep.stop()
			ep.stop = nil
		}
	case ev.Prev == threads.StateWorking && ev.State == threads.StateIdle:
		ep.turns++
		if ep.stop != nil {
			ep.stop()
			ep.stop = nil
		}
		id := ev.ThreadID
		if ep.turns >= mainEpisodeTurns {
			ep.turns = 0
			go s.endMainEpisode(id, "turns")
			return
		}
		ep.stop = s.cfg.AfterFunc(mainIdleStop, func() {
			s.mu.Lock()
			s.mainEp.turns, s.mainEp.stop = 0, nil
			s.mu.Unlock()
			s.endMainEpisode(id, "idle")
		})
	}
}

// endMainEpisode 让主线在静止后停掉 agent：下一条消息以新的 AgentProfile（新的近况）开始新的一段。
func (s *Service) endMainEpisode(threadID, reason string) {
	slog.Debug("coord: 主线一段结束", "thread", threadID, "reason", reason)
	if s.afterEpisode != nil {
		s.afterEpisode(threadID, reason)
	}
	s.th.RefreshAgent(context.Background(), threadID)
}

// mainProfile 给主线的系统提示词追加固定说明与近况（失败只记日志：这次没有近况也能启动）。
func (s *Service) mainProfile(ctx context.Context, threadID string, prompt string) string {
	prompt += "\n\n" + mainGuide
	brief, err := s.mainBrief(ctx, threadID)
	if err != nil {
		slog.Warn("coord: 生成主线近况失败", "thread", threadID, "err", err)
		return prompt
	}
	return prompt + "\n\n" + brief
}
