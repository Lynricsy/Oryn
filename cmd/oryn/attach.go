package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/transcript"
	"github.com/Lynricsy/Oryn/internal/uiprompt"
)

// `oryn thread attach`（M5 计划 4.2）：挂到线程上流式打印转录并可交互；挂着即算有设备 focus 于该线程。

const (
	exitAttachAwaiting = 3
	exitAttachError    = 4
	// attachRedials 是连续重连的上限。
	attachRedials = 5
	// attachCommitWait 是线程已结束、但流式见过的消息尚未入库时最多再等多久。
	attachCommitWait = 5 * time.Second
)

// dialogMethods 是需要作答的 extension_ui_request 方法（其余如 notify / setStatus 不阻塞 agent）。
var dialogMethods = map[string]bool{
	"select": true, "confirm": true, "input": true, "editor": true, "form": true, "ask": true, "askDialog": true,
}

type attachOpts struct {
	threadID string
	// tail 是补发后打印的最近消息数；<0 = 全部。
	tail       int
	json       bool
	exitOnIdle bool
	// in 非 nil 时逐行读取输入（stdin 是终端且未给 --no-input）。
	in        io.Reader
	out, errw io.Writer
	// tty：stdout 是终端，assistant 文本按增量流式输出；否则只按已提交条目逐条打印。
	tty     bool
	dial    func(ctx context.Context) (*wsConn, error)
	backoff func(attempt int) time.Duration
}

// attacher 是一次 attach 的本地状态（跨重连保留）。
type attacher struct {
	o attachOpts

	entries []transcript.Entry
	leaf    string
	lastSeq int64
	rev     *int64

	snapSeq    int64 // 当前订阅快照的 lastSeq
	subscribed bool  // 本连接已收到快照
	backfilled bool  // 本连接的补发已完成
	tailed     bool  // 首次补发的 tail 已打印

	printed  map[string]bool // file + entryId
	streamed map[string]bool // 已流式打印过文本的 (role, timestamp)
	pendEnds map[string]bool // 见过 message_end 但尚未入库的 (role, timestamp)

	streaming bool // 本次附着后见过 message_start 的在途 assistant 消息
	inLine    bool // 正在输出一行流式文本

	pending    map[string]json.RawMessage // 未决的待答请求
	pendOrder  []string
	askPrinted map[string]bool

	state    string
	turnSeen bool
	cmdSeq   int
	settleAt time.Time
}

func newAttacher(o attachOpts) *attacher {
	if o.backoff == nil {
		o.backoff = func(n int) time.Duration { return min(time.Duration(1<<(n-1))*500*time.Millisecond, 8*time.Second) }
	}
	return &attacher{
		o: o, printed: map[string]bool{}, streamed: map[string]bool{}, pendEnds: map[string]bool{},
		pending: map[string]json.RawMessage{}, askPrinted: map[string]bool{},
	}
}

// runAttach 返回退出码：0 线程进入 idle（或用户退出），3 awaiting_input，4 error；连接失败返回错误。
func runAttach(ctx context.Context, o attachOpts) (int, error) {
	return newAttacher(o).run(ctx)
}

func (a *attacher) run(ctx context.Context) (int, error) {
	var lines chan string
	if a.o.in != nil {
		lines = make(chan string)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(a.o.in)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				select {
				case lines <- sc.Text():
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	fails := 0
	for {
		conn, err := a.o.dial(ctx)
		if err == nil {
			var code int
			var done bool
			code, done, err = a.session(ctx, conn, lines)
			conn.ws.CloseNow()
			if done {
				return code, err
			}
			if a.subscribed {
				fails = 0
			}
		}
		if ctx.Err() != nil {
			return 0, nil
		}
		a.endLine()
		fails++
		if fails > attachRedials {
			return 1, fmt.Errorf("连接已断开: %w", err)
		}
		fmt.Fprintf(a.o.errw, "连接断开，重连中（%d/%d）…\n", fails, attachRedials)
		select {
		case <-ctx.Done():
			return 0, nil
		case <-time.After(a.o.backoff(fails)):
		}
	}
}

// session 处理一条连接直到断开（done=false）或 attach 结束（done=true）。
func (a *attacher) session(ctx context.Context, conn *wsConn, lines chan string) (int, bool, error) {
	a.subscribed, a.backfilled, a.streaming = false, false, false
	sub := map[string]any{"type": "sub", "threadId": a.o.threadID, "afterSeq": a.lastSeq}
	if a.rev != nil {
		sub["transcriptRev"] = *a.rev
	}
	if err := conn.send(ctx, map[string]any{"type": "focus", "threadId": a.o.threadID}); err != nil {
		return 0, false, err
	}
	if err := conn.send(ctx, sub); err != nil {
		return 0, false, err
	}
	type result struct {
		m   wsEnvelope
		err error
	}
	msgs := make(chan result, 64)
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			m, err := conn.read(rctx)
			select {
			case msgs <- result{m, err}:
			case <-rctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var timeout <-chan time.Time
		if !a.settleAt.IsZero() {
			timeout = time.After(time.Until(a.settleAt))
		}
		select {
		case <-ctx.Done():
			a.endLine()
			return 0, true, nil
		case <-timeout:
			return a.exitCode(), true, nil
		case line, ok := <-lines:
			if !ok {
				a.endLine()
				return 0, true, nil
			}
			if quit, err := a.input(ctx, conn, line); quit || err != nil {
				return 0, err == nil, err
			}
		case r := <-msgs:
			if r.err != nil {
				return 0, false, r.err
			}
			code, done, err := a.handle(ctx, conn, r.m)
			if done || err != nil {
				a.endLine()
				return code, true, err
			}
		}
	}
}

// ---- 服务端消息 ----

type snapshotMsg struct {
	Thread        apigen.Thread `json:"thread"`
	LastSeq       int64         `json:"lastSeq"`
	LeafEntryID   *string       `json:"leafEntryId"`
	TranscriptRev int64         `json:"transcriptRev"`
	FullReplay    bool          `json:"fullReplay"`
	Agent         struct {
		PendingUI []json.RawMessage `json:"pendingUi"`
	} `json:"agent"`
}

type entriesMsg struct {
	Entries     []transcript.Entry `json:"entries"`
	LastSeq     int64              `json:"lastSeq"`
	LeafEntryID *string            `json:"leafEntryId"`
}

func (a *attacher) handle(ctx context.Context, conn *wsConn, m wsEnvelope) (int, bool, error) {
	mine := m.ThreadID == a.o.threadID
	if a.o.json && mine {
		switch m.Type {
		case "thread.snapshot", "entries", "frame", "ui.resolved":
			fmt.Fprintf(a.o.out, "%s\n", m.Raw)
		}
	}
	switch m.Type {
	case "index", "index.patch":
		var idx struct {
			Threads []apigen.Thread `json:"threads"`
			Upsert  []apigen.Thread `json:"upsert"`
			Remove  []string        `json:"remove"`
		}
		if json.Unmarshal(m.Raw, &idx) != nil {
			return 0, false, nil
		}
		for _, id := range idx.Remove {
			if id == a.o.threadID {
				return 1, true, errors.New("线程已被删除")
			}
		}
		for _, t := range append(idx.Threads, idx.Upsert...) {
			if t.ID == a.o.threadID {
				a.setState(string(t.State))
			}
		}
	case "thread.snapshot":
		if !mine {
			return 0, false, nil
		}
		var s snapshotMsg
		if err := json.Unmarshal(m.Raw, &s); err != nil {
			return 1, true, err
		}
		if s.FullReplay {
			a.entries, a.lastSeq = nil, 0
		}
		rev := s.TranscriptRev
		a.rev, a.snapSeq, a.subscribed = &rev, s.LastSeq, true
		if s.LeafEntryID != nil {
			a.leaf = *s.LeafEntryID
		}
		a.pending, a.pendOrder = map[string]json.RawMessage{}, nil
		for _, f := range s.Agent.PendingUI {
			a.uiRequest(f)
		}
		a.setState(string(s.Thread.State))
		if a.lastSeq >= a.snapSeq {
			a.finishBackfill()
		}
	case "entries":
		if !mine {
			return 0, false, nil
		}
		var e entriesMsg
		if err := json.Unmarshal(m.Raw, &e); err != nil {
			return 1, true, err
		}
		a.addEntries(e)
	case "frame":
		if mine {
			a.frame(m.Frame)
		}
	case "ui.resolved":
		if mine {
			a.resolve(m.RequestID)
		}
	case "live.reset":
		if mine {
			a.endLine()
			a.streaming = false
		}
	case "transcript.reset":
		if !mine {
			return 0, false, nil
		}
		var r struct {
			TranscriptRev int64 `json:"transcriptRev"`
		}
		_ = json.Unmarshal(m.Raw, &r)
		a.endLine()
		a.entries, a.lastSeq, a.rev, a.subscribed, a.backfilled = nil, 0, &r.TranscriptRev, false, false
		if err := conn.send(ctx, map[string]any{"type": "sub", "threadId": a.o.threadID, "afterSeq": 0, "transcriptRev": r.TranscriptRev}); err != nil {
			return 0, false, err
		}
	case "res":
		var r struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if mine && json.Unmarshal(m.Response, &r) == nil && !r.Success {
			a.endLine()
			fmt.Fprintf(a.o.errw, "agent 拒绝了命令: %s\n", r.Error)
		}
	case "error":
		if m.ID != "" {
			a.endLine()
			fmt.Fprintf(a.o.errw, "命令失败: %s (%s)\n", m.Message, m.Code)
		} else if mine {
			return 1, true, &apiError{Code: m.Code, Message: m.Message}
		}
	}
	return a.checkExit()
}

func (a *attacher) addEntries(e entriesMsg) {
	if e.LeafEntryID != nil {
		a.leaf = *e.LeafEntryID
	}
	for _, en := range e.Entries {
		if en.Seq <= a.lastSeq {
			continue
		}
		a.entries = append(a.entries, en)
		a.lastSeq = en.Seq
		if k := messageKey(en.Payload); k != "" {
			delete(a.pendEnds, k)
		}
		if a.backfilled {
			a.printEntry(en)
		}
	}
	a.lastSeq = max(a.lastSeq, e.LastSeq)
	if !a.backfilled && a.subscribed && a.lastSeq >= a.snapSeq {
		a.finishBackfill()
	}
}

// finishBackfill：首次连接打印活动分支的最后 tail 条消息并把其余标为已见；重连时打印补发到的新条目。
func (a *attacher) finishBackfill() {
	a.backfilled = true
	if a.tailed {
		for _, e := range a.entries {
			a.printEntry(e)
		}
		return
	}
	a.tailed = true
	var show []transcript.Entry
	for _, e := range transcript.ActiveBranch(a.entries, a.leaf) {
		if len(a.entryLines(e)) > 0 {
			show = append(show, e)
		}
	}
	if a.o.tail >= 0 && len(show) > a.o.tail {
		show = show[len(show)-a.o.tail:]
	}
	for _, e := range a.entries {
		a.printed[e.File+"\x00"+e.EntryID] = true
	}
	if !a.o.json {
		for _, e := range show {
			for _, l := range a.entryLines(e) {
				fmt.Fprintln(a.o.out, l)
			}
		}
	}
	for _, id := range a.pendOrder {
		a.printAsk(id)
	}
}

// printEntry 打印一条新到达的主会话条目（每个 entryId 一次）。
func (a *attacher) printEntry(e transcript.Entry) {
	k := e.File + "\x00" + e.EntryID
	if a.printed[k] || !transcript.IsMainFile(e.File) {
		return
	}
	a.printed[k] = true
	if a.o.json {
		return
	}
	lines := a.entryLines(e)
	if mk := messageKey(e.Payload); mk != "" && a.streamed[mk] {
		// 文本已流式打印过：只补工具调用行
		delete(a.streamed, mk)
		if len(lines) > 0 && strings.HasPrefix(lines[0], "[assistant] ") {
			lines = lines[1:]
		}
	}
	a.endLine()
	for _, l := range lines {
		fmt.Fprintln(a.o.out, l)
	}
}

// entryLines 把消息条目渲染为输出行；非消息条目返回 nil。
func (a *attacher) entryLines(e transcript.Entry) []string {
	m, ok := transcript.ParseMessage(e)
	if !ok {
		return nil
	}
	var out []string
	switch m.Role {
	case "user":
		text := m.Text
		if text == "" && m.Images > 0 {
			text = "<image>"
		}
		if text != "" {
			out = append(out, "[user] "+text)
		}
	case "assistant":
		if m.Text != "" {
			out = append(out, "[assistant] "+m.Text)
		}
		for _, tc := range m.ToolCalls {
			out = append(out, fmt.Sprintf("[tool %s] %s", tc.Name, argSummary(tc.Args)))
		}
	case "toolResult":
		status := "ok"
		if m.IsError {
			status = "error"
		}
		out = append(out, fmt.Sprintf("[tool %s] %s", m.ToolName, status))
	default:
		if role, text := transcript.LineText(e); text != "" {
			out = append(out, "["+role+"] "+text)
		}
	}
	return out
}

// argSummary 是工具调用参数的单行摘要：优先意图 / 命令 / 路径 / 模式，否则紧凑 JSON。
func argSummary(args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) == nil {
		for _, k := range []string{"i", "intent", "command", "path", "pattern", "query", "url"} {
			if s, ok := m[k].(string); ok && s != "" {
				return transcript.Snippet(s, 120)
			}
		}
	}
	return transcript.Snippet(string(args), 120)
}

// messageKey 是消息的 (role, timestamp) 匹配键；payload 可以是条目（{message}）或 message_* 帧。
func messageKey(payload json.RawMessage) string {
	var p struct {
		Message *struct {
			Role      string      `json:"role"`
			Timestamp json.Number `json:"timestamp"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Message == nil || p.Message.Timestamp == "" {
		return ""
	}
	return p.Message.Role + "\x00" + p.Message.Timestamp.String()
}

// ---- 实时帧 ----

func (a *attacher) frame(raw json.RawMessage) {
	var f struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Target  string `json:"targetId"`
		Message *struct {
			Role string `json:"role"`
		} `json:"message"`
		Event *struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	assistant := f.Message != nil && f.Message.Role == "assistant"
	switch f.Type {
	case "agent_start":
		a.turnSeen = true
	case "message_start":
		a.streaming = assistant && a.backfilled && a.o.tty && !a.o.json
	case "message_update":
		if a.streaming && f.Event != nil && f.Event.Type == "text_delta" && f.Event.Delta != "" {
			if !a.inLine {
				fmt.Fprint(a.o.out, "[assistant] ")
				a.inLine = true
			}
			fmt.Fprint(a.o.out, f.Event.Delta)
		}
	case "message_end":
		k := messageKey(raw)
		if k != "" && !a.committed(k) {
			a.pendEnds[k] = true
		}
		if a.streaming && a.inLine && k != "" {
			a.streamed[k] = true
		}
		a.endLine()
		a.streaming = false
	case "extension_ui_request":
		if f.Method == "cancel" {
			a.resolve(f.Target)
			return
		}
		if dialogMethods[f.Method] && f.ID != "" {
			a.uiRequest(raw)
			if a.backfilled {
				a.printAsk(f.ID)
			}
		}
	}
}

// committed 报告键为 k 的消息是否已经入库（message_end 晚于条目到达时不必再等）。
func (a *attacher) committed(k string) bool {
	for i := len(a.entries) - 1; i >= 0 && i >= len(a.entries)-50; i-- {
		if messageKey(a.entries[i].Payload) == k {
			return true
		}
	}
	return false
}

func (a *attacher) uiRequest(frame json.RawMessage) {
	var f struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &f) != nil || f.ID == "" {
		return
	}
	if _, ok := a.pending[f.ID]; !ok {
		a.pendOrder = append(a.pendOrder, f.ID)
	}
	a.pending[f.ID] = frame
}

func (a *attacher) resolve(id string) {
	if _, ok := a.pending[id]; !ok {
		return
	}
	delete(a.pending, id)
	for i, p := range a.pendOrder {
		if p == id {
			a.pendOrder = append(a.pendOrder[:i], a.pendOrder[i+1:]...)
			break
		}
	}
}

// latestPending 是最近一个未决请求（输入行作答它）。
func (a *attacher) latestPending() (string, json.RawMessage) {
	if len(a.pendOrder) == 0 {
		return "", nil
	}
	id := a.pendOrder[len(a.pendOrder)-1]
	return id, a.pending[id]
}

func (a *attacher) printAsk(id string) {
	frame, ok := a.pending[id]
	if !ok || a.askPrinted[id] || a.o.json {
		return
	}
	a.askPrinted[id] = true
	p := uiprompt.Describe(frame, uiprompt.Labels{Yes: "是", No: "否"})
	a.endLine()
	fmt.Fprintf(a.o.out, "[ask] %s\n", p.Title)
	if p.Body != "" {
		fmt.Fprintln(a.o.out, p.Body)
	}
	for i, o := range p.Options {
		fmt.Fprintf(a.o.out, "  %d) %s\n", i+1, o)
	}
	switch {
	case !p.Supported:
		fmt.Fprintln(a.o.out, "（这个请求无法在命令行作答，请在 Web 端处理）")
	case a.o.in == nil:
		fmt.Fprintln(a.o.out, "（等待作答）")
	case len(p.Options) > 0 && p.FreeText:
		fmt.Fprintln(a.o.out, "（输入编号选择，或直接输入文字作答）")
	case len(p.Options) > 0:
		fmt.Fprintln(a.o.out, "（输入编号选择）")
	default:
		fmt.Fprintln(a.o.out, "（输入文字作答）")
	}
}

func (a *attacher) endLine() {
	if a.inLine {
		fmt.Fprintln(a.o.out)
		a.inLine = false
	}
}

// ---- 输入 ----

// input 处理一行输入；quit 为真表示用户要求退出。
func (a *attacher) input(ctx context.Context, conn *wsConn, line string) (bool, error) {
	line = strings.TrimSpace(line)
	switch line {
	case "":
		return false, nil
	case "/quit":
		a.endLine()
		return true, nil
	case "/abort":
		return false, a.cmd(ctx, conn, map[string]any{"type": "abort"})
	}
	if _, frame := a.latestPending(); frame != nil {
		resp, err := uiprompt.AnswerText(frame, line)
		switch {
		case errors.Is(err, uiprompt.ErrNeedOption):
			fmt.Fprintf(a.o.errw, "请输入选项编号（1-%d）\n", len(uiprompt.Describe(frame, uiprompt.Labels{}).Options))
			return false, nil
		case errors.Is(err, uiprompt.ErrUnsupported):
			fmt.Fprintln(a.o.errw, "这个请求无法在命令行作答，请在 Web 端处理")
			return false, nil
		case err != nil:
			fmt.Fprintf(a.o.errw, "无法作答: %v\n", err)
			return false, nil
		}
		return false, conn.send(ctx, map[string]any{"type": "ui.response", "threadId": a.o.threadID, "frame": resp})
	}
	return false, a.cmd(ctx, conn, map[string]any{"type": "prompt", "message": line, "streamingBehavior": "steer"})
}

func (a *attacher) cmd(ctx context.Context, conn *wsConn, frame map[string]any) error {
	a.cmdSeq++
	return conn.send(ctx, map[string]any{"type": "cmd", "threadId": a.o.threadID, "id": "attach-" + strconv.Itoa(a.cmdSeq), "frame": frame})
}

// ---- --exit-on-idle ----

func (a *attacher) setState(s string) {
	if s == "" {
		return
	}
	a.state = s
	if s == string(apigen.ThreadStateWorking) || s == string(apigen.ThreadStateProvisioning) {
		a.turnSeen = true
	}
}

func (a *attacher) settled() bool {
	switch apigen.ThreadState(a.state) {
	case apigen.ThreadStateIdle, apigen.ThreadStateAwaitingInput, apigen.ThreadStateError:
		return true
	}
	return false
}

func (a *attacher) exitCode() int {
	switch apigen.ThreadState(a.state) {
	case apigen.ThreadStateAwaitingInput:
		return exitAttachAwaiting
	case apigen.ThreadStateError:
		return exitAttachError
	}
	return 0
}

// checkExit：--exit-on-idle 时，见过一个回合（working / agent_start）且补发完成后线程进入 idle / awaiting_input /
// error 即退出；流式见过但尚未入库的消息再等至多 attachCommitWait，保证管道输出里有该回合的最后一条消息。
func (a *attacher) checkExit() (int, bool, error) {
	if !a.o.exitOnIdle || !a.turnSeen || !a.backfilled || !a.settled() {
		a.settleAt = time.Time{}
		return 0, false, nil
	}
	if len(a.pendEnds) == 0 {
		return a.exitCode(), true, nil
	}
	if a.settleAt.IsZero() {
		a.settleAt = time.Now().Add(attachCommitWait)
	}
	return 0, false, nil
}

// ---- 命令 ----

var threadAttachOpts struct {
	tail                           int
	all, noInput, json, exitOnIdle bool
}

var threadAttachCmd = &cobra.Command{
	Use:   "attach <id>",
	Short: "挂到线程上：流式打印转录，可输入消息（steer）或作答待答请求；挂着即算正在查看该线程",
	Long: "退出码（--exit-on-idle）：见到一个回合结束后，线程 idle → 0、awaiting_input → 3、error → 4。\n" +
		"输入：每行一条；有待答请求时作为作答（编号或文字），否则 steer 当前回合。/abort 中断，/quit 或 EOF 退出；Ctrl-C 退出（agent 继续）。",
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		c, err := newClient()
		if err != nil {
			fmt.Fprintln(os.Stderr, "oryn:", err)
			os.Exit(1)
		}
		o := threadAttachOpts
		opts := attachOpts{
			threadID: args[0], tail: o.tail, json: o.json, exitOnIdle: o.exitOnIdle,
			out: os.Stdout, errw: os.Stderr, tty: isTerminal(os.Stdout),
			dial: func(ctx context.Context) (*wsConn, error) { return dialWS(ctx, c) },
		}
		if o.all {
			opts.tail = -1
		}
		if !o.noInput && isTerminal(os.Stdin) {
			opts.in = os.Stdin
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		code, err := runAttach(ctx, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "oryn:", err)
			if code == 0 {
				code = 1
			}
		}
		exitIfNot0(code)
	},
}

// isTerminal 报告 f 是否是字符设备（终端）。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func init() {
	f := threadAttachCmd.Flags()
	f.IntVar(&threadAttachOpts.tail, "tail", 10, "先打印活动分支上最近 N 条消息（0 = 不打印）")
	f.BoolVar(&threadAttachOpts.all, "all", false, "先打印全部消息")
	f.BoolVar(&threadAttachOpts.noInput, "no-input", false, "不读取输入")
	f.BoolVar(&threadAttachOpts.json, "json", false, "把该线程的 thread.snapshot / entries / frame / ui.resolved 原样逐行输出（NDJSON）")
	f.BoolVar(&threadAttachOpts.exitOnIdle, "exit-on-idle", false, "回合结束后退出（idle 0 / awaiting_input 3 / error 4）")
	threadCmd.AddCommand(threadAttachCmd)
}
