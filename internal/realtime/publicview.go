package realtime

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// subagentTool 是派生子 agent 的工具名：它的进度帧带子 agent 的工具参数、输出片段与会话路径，不发给匿名连接（F3）。
const subagentTool = "task"

// liveFile 是把实时消息套进 transcript.Public 判定时使用的主会话文件名（只需满足 IsMainFile）。
const liveFile = "live.jsonl"

// publicFrame 返回 omp 帧在匿名连接上的形态；ok=false 表示不发（F2 / F3）。
//   - 帧类型不在 publicFrameTypes 里 → 不发；
//   - message_start / message_end：消息按条目同一条规则（transcript.Public）判定，`display:false` 的注入消息不发；
//   - message_update：只发 assistant 的（delta 投影只带 role、不带 display，无法判定；也只有 assistant 消息流式输出）；
//   - agent_end：去掉本轮全部 messages，只留 `{type, isTerminal?}`；
//   - 子 agent 工具（task）的 tool_execution_update / tool_stream_update 不发（只发开始与结束）。
func publicFrame(frame json.RawMessage) (json.RawMessage, bool) {
	var f struct {
		Type       string          `json:"type"`
		ToolName   string          `json:"toolName"`
		Message    json.RawMessage `json:"message"`
		IsTerminal *bool           `json:"isTerminal"`
	}
	if json.Unmarshal(frame, &f) != nil || !publicFrameTypes[f.Type] {
		return nil, false
	}
	switch f.Type {
	case "message_start", "message_end":
		if hiddenLiveMessage(f.Message) {
			return nil, false
		}
	case "message_update":
		var m struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(f.Message, &m) != nil || m.Role != "assistant" {
			return nil, false
		}
	case "agent_end":
		b := marshal(struct {
			Type       string `json:"type"`
			IsTerminal *bool  `json:"isTerminal,omitempty"`
		}{f.Type, f.IsTerminal})
		return b, b != nil
	case "tool_execution_update", "tool_stream_update":
		if f.ToolName == subagentTool {
			return nil, false
		}
	}
	return frame, true
}

// hiddenLiveMessage 报告实时帧里的消息是否不得公开：与已入库条目共用 transcript.Public 的判定
// （它把隐藏消息的 payload 换成只剩 type 的对象）；没有消息对象时同样不发。
func hiddenLiveMessage(msg json.RawMessage) bool {
	msg = bytes.TrimSpace(msg)
	if len(msg) == 0 || msg[0] != '{' {
		return true
	}
	payload := make([]byte, 0, len(msg)+12)
	payload = append(payload, `{"message":`...)
	payload = append(payload, msg...)
	payload = append(payload, '}')
	pe, ok := transcript.Public(transcript.Entry{File: liveFile, Type: "message", Payload: payload})
	return !ok || !bytes.Equal(pe.Payload, payload)
}

// publicLive 过滤 thread.snapshot 的在途帧（同 publicFrame）。
func publicLive(frames []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(frames))
	for _, f := range frames {
		if pf, ok := publicFrame(f); ok {
			out = append(out, pf)
		}
	}
	return out
}

// ---- 活动分支跟踪（B7） ----
//
// 匿名客户端只拿到过活动分支上的条目（补发按分支筛选），所有者切到别的分支（switch_leaf / navigate_tree，
// 或带摘要的分支跳转写出一条挂在别处的 branch_summary）后，目标分支的历史它从未收到过，只发新叶子 id 无法重建。
// 因此 hub 为每条匿名连接记下客户端手里分支的末端（tip）：新条目沿 parentId 接在末端上就前移；
// 线程叶子变成 tip 以外的条目时发 transcript.reset，客户端清空后以 after=0 重连，按新分支整段补发。

// branchLink 是条目在分支跟踪里用到的字段。
type branchLink struct {
	id, parent, file string
	root             bool
}

// linksOf 取出主会话的非合成条目（与公开过滤一致：子 agent 文件、标题槽与会话头不在任何分支上）。
func linksOf(es []threads.Entry) []branchLink {
	out := make([]branchLink, 0, len(es))
	for _, e := range es {
		if !transcript.IsMainFile(e.File) || strings.HasPrefix(e.EntryID, "__") {
			continue
		}
		l := branchLink{id: e.EntryID, file: e.File, root: e.ParentID == nil}
		if e.ParentID != nil {
			l.parent = *e.ParentID
		}
		out = append(out, l)
	}
	return out
}

// branchEvent 是一次分支相关的变化：一批新条目（entries=true）或一次线程叶子更新。
type branchEvent struct {
	entries bool
	links   []branchLink
	leaf    string
}

// branchView 是客户端手里的活动分支。
type branchView struct {
	tip, tipFile string // 客户端已收到的、沿 parentId 连续的分支末端
	leaf         string // 最近观察到的线程叶子
}

// init 以补发内容初始化：补发按 seq 升序且只含活动分支，最后一条即末端；没有补发内容时末端取 fallback
// （全量补发 = 空分支；续传 = 快照时的叶子，客户端此前已持有它）。
func (b *branchView) init(backfill []branchLink, fallback, leaf string) {
	b.tip, b.tipFile, b.leaf = fallback, "", leaf
	if n := len(backfill); n > 0 {
		b.tip, b.tipFile = backfill[n-1].id, backfill[n-1].file
	}
}

// apply 处理一个事件；返回 true 表示叶子已不在客户端持有的分支上，须让客户端重建。
func (b *branchView) apply(ev branchEvent) bool {
	if !ev.entries {
		if ev.leaf == b.leaf {
			return false
		}
		b.leaf = ev.leaf
		return ev.leaf != b.tip
	}
	arrived := false
	for _, l := range ev.links {
		// 接在末端之后、或新会话文件的第一条（新会话文件拼接在分支末尾）
		if (!l.root && l.parent == b.tip) || l.file != b.tipFile || (l.root && b.tip == "") {
			b.tip, b.tipFile = l.id, l.file
		}
		if l.id == ev.leaf {
			arrived = true
		}
	}
	b.leaf = ev.leaf
	// 叶子条目本身已到达却不在末端：它挂在客户端没有的分支上。叶子还没到（分批推送的后续批次、或应答先于条目）就等它到
	return arrived && ev.leaf != b.tip
}

// branchLocked 把分支事件交给匿名连接：补发期间暂存，之后若叶子跳出客户端的分支就发 transcript.reset。调用方持有 h.mu。
func (h *Hub) branchLocked(c *conn, ps *pubSub, ev branchEvent) {
	if ps.resetSent {
		return
	}
	if ps.buffering {
		ps.pending = append(ps.pending, ev)
		return
	}
	if ps.branch.apply(ev) {
		h.resetPublicLocked(c, ps)
	}
}

// resetPublicLocked 让匿名客户端丢弃已缓存的条目并以 after=0 重连（重连后的补发即新分支）。调用方持有 h.mu。
func (h *Hub) resetPublicLocked(c *conn, ps *pubSub) {
	ps.resetSent = true
	if b := marshal(apigen.WsPublicTranscriptReset{Type: apigen.WsPublicTranscriptResetTypeTranscriptReset, TranscriptRev: ps.rev}); b != nil {
		c.enqueue(b)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
