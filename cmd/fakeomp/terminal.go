package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// 终端替身（M3 计划 2.4），形状取自 oryn-core `rpc-types.ts` 的 RpcTerminalInfo / RpcTerminal*Frame /
// RpcTerminalSnapshot 与 `rpc-terminal.ts`：`seq` = 该终端累计输出的字符数（替身只输出 ASCII，字节数即字符数），
// 快照保留最近 262144 个字符。帧一律以 `type` 为第一个键（与 omp 相同；orbd 按 `{"type":"terminal_output"` 前缀分流）。

const (
	termScrollback = 262144
	termFloodChunk = 32768
	termPrompt     = "fake$ "
	defaultCols    = 100
	defaultRows    = 30
)

type termInfo struct {
	TerminalID string `json:"terminalId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Cwd        string `json:"cwd"`
	Running    bool   `json:"running"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Pid        int    `json:"pid,omitempty"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

type termOpenedFrame struct {
	Type     string   `json:"type"`
	Terminal termInfo `json:"terminal"`
	Reveal   bool     `json:"reveal,omitempty"`
}

type termOutputFrame struct {
	Type       string `json:"type"`
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
	Seq        int64  `json:"seq"`
}

type termExitFrame struct {
	Type       string `json:"type"`
	TerminalID string `json:"terminalId"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Cancelled  bool   `json:"cancelled"`
	TimedOut   bool   `json:"timedOut"`
}

type termClosedFrame struct {
	Type       string `json:"type"`
	TerminalID string `json:"terminalId"`
}

type termSnapshot struct {
	Terminal termInfo `json:"terminal"`
	Data     string   `json:"data"`
	Seq      int64    `json:"seq"`
}

// fakeTerm 是一个替身终端。mu 同时保护输出缓冲与「写出帧」：持锁追加并发出，stdout 上的 seq 因此严格递增，
// 快照（持锁读取）之前发出的输出帧的 seq 一定 ≤ 快照 seq。锁序：fakeTerm.mu → server.outMu。
type fakeTerm struct {
	s *server

	mu     sync.Mutex
	info   termInfo
	buf    []byte // 最近的输出（超过 2×回滚时压缩）
	seq    int64
	line   []byte        // shell：当前输入行
	answer chan struct{} // command：收到含 `y` 的输入时关闭
	closed chan struct{} // terminal_close 时关闭
}

// terminals 是全部替身终端（按打开顺序）。
type terminals struct {
	mu    sync.Mutex
	n     int
	byID  map[string]*fakeTerm
	order []string
}

func newTerminals() *terminals { return &terminals{byID: map[string]*fakeTerm{}} }

func (ts *terminals) add(s *server, kind, title, cwd string, cols, rows int) *fakeTerm {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.n++
	id := "term-fake-" + strconv.Itoa(ts.n)
	t := &fakeTerm{
		s: s,
		info: termInfo{
			TerminalID: id, Kind: kind, Title: title, Cwd: cwd, Running: true, Pid: os.Getpid(), Cols: cols, Rows: rows,
		},
		answer: make(chan struct{}),
		closed: make(chan struct{}),
	}
	ts.byID[id] = t
	ts.order = append(ts.order, id)
	return t
}

func (ts *terminals) get(id string) *fakeTerm {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.byID[id]
}

func (ts *terminals) remove(id string) *fakeTerm {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[id]
	if t == nil {
		return nil
	}
	delete(ts.byID, id)
	for i, v := range ts.order {
		if v == id {
			ts.order = append(ts.order[:i:i], ts.order[i+1:]...)
			break
		}
	}
	return t
}

func (ts *terminals) list() []*fakeTerm {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]*fakeTerm, 0, len(ts.order))
	for _, id := range ts.order {
		out = append(out, ts.byID[id])
	}
	return out
}

// outputLocked 追加输出并发出一帧 terminal_output；调用方持有 t.mu。
func (t *fakeTerm) outputLocked(data string) {
	if data == "" {
		return
	}
	t.buf = append(t.buf, data...)
	if len(t.buf) > 2*termScrollback {
		t.buf = append(t.buf[:0:0], t.buf[len(t.buf)-termScrollback:]...)
	}
	t.seq += int64(len(data))
	t.s.emit(termOutputFrame{Type: "terminal_output", TerminalID: t.info.TerminalID, Data: data, Seq: t.seq})
}

func (t *fakeTerm) output(data string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.outputLocked(data)
}

// exit 结束终端进程（只生效一次）并发出 terminal_exit。
func (t *fakeTerm) exit(code *int, cancelled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.info.Running {
		return
	}
	t.info.Running = false
	t.info.ExitCode = code
	t.s.emit(termExitFrame{Type: "terminal_exit", TerminalID: t.info.TerminalID, ExitCode: code, Cancelled: cancelled})
}

func (t *fakeTerm) snapshot() termSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	data := t.buf
	if len(data) > termScrollback {
		data = data[len(data)-termScrollback:]
	}
	return termSnapshot{Terminal: t.info, Data: string(data), Seq: t.seq}
}

func (t *fakeTerm) infoCopy() termInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info
}

// input 处理 terminal_input。shell：逐字符回显，`\r` 输出为 `\r\n` 并执行整行（`flood <n>` 连续输出 n 字节
// 后回到提示符；`exit` 结束终端；其余直接给出提示符）；command：回显，收到 `y` 时放行等待它的回合。
func (t *fakeTerm) input(data string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.info.Running {
		return fmt.Errorf("Terminal %s is not running", t.info.TerminalID)
	}
	if t.info.Kind == "command" {
		t.outputLocked(strings.ReplaceAll(data, "\r", "\r\n"))
		if strings.Contains(data, "y") {
			select {
			case <-t.answer:
			default:
				close(t.answer)
			}
		}
		return nil
	}
	var echo strings.Builder
	flush := func() {
		t.outputLocked(echo.String())
		echo.Reset()
	}
	for i := range len(data) {
		c := data[i]
		switch c {
		case '\r':
			echo.WriteString("\r\n")
			flush()
			line := strings.TrimSpace(string(t.line))
			t.line = t.line[:0]
			switch {
			case line == "exit":
				t.info.Running = false
				code := 0
				t.info.ExitCode = &code
				t.s.emit(termExitFrame{Type: "terminal_exit", TerminalID: t.info.TerminalID, ExitCode: &code})
				return nil
			case strings.HasPrefix(line, "flood "):
				if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "flood "))); err == nil && n > 0 {
					// 洪泛同步完成（持终端锁）：其后的命令（如 terminal_snapshot）一定看到全部洪泛输出，测试据此确定顺序。
					t.floodLocked(n)
					return nil
				}
				echo.WriteString(termPrompt)
			default:
				echo.WriteString(termPrompt)
			}
		case 0x7f: // 退格
			if len(t.line) > 0 {
				t.line = t.line[:len(t.line)-1]
				echo.WriteString("\b \b")
			}
		default:
			t.line = append(t.line, c)
			echo.WriteByte(c)
		}
	}
	flush()
	return nil
}

// floodLocked 以每帧 32768 个 `x` 连续输出 n 字节，结束后输出 `\r\n` 与提示符；调用方持有 t.mu。
func (t *fakeTerm) floodLocked(n int) {
	chunk := strings.Repeat("x", termFloodChunk)
	for n > 0 {
		k := min(n, termFloodChunk)
		t.outputLocked(chunk[:k])
		n -= k
	}
	t.outputLocked("\r\n" + termPrompt)
}

// handleTerminal 处理 terminal_* 命令；不是终端命令时返回 false。
func (s *server) handleTerminal(typ, id string, line []byte) bool {
	switch typ {
	case "terminal_configure":
		var c struct {
			InteractiveCommands bool `json:"interactiveCommands"`
			AgentTools          bool `json:"agentTools"`
		}
		_ = json.Unmarshal(line, &c)
		s.ok(id, typ, map[string]bool{"interactiveCommands": c.InteractiveCommands, "agentTools": c.AgentTools})

	case "terminal_open":
		var c struct {
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
			Cwd  string `json:"cwd"`
		}
		_ = json.Unmarshal(line, &c)
		if c.Cols <= 0 {
			c.Cols = defaultCols
		}
		if c.Rows <= 0 {
			c.Rows = defaultRows
		}
		if c.Cwd == "" {
			c.Cwd = s.opts.cwd
		}
		t := s.terms.add(s, "shell", "fake shell", c.Cwd, c.Cols, c.Rows)
		t.mu.Lock()
		s.emit(termOpenedFrame{Type: "terminal_opened", Terminal: t.info})
		t.outputLocked(termPrompt)
		info := t.info
		t.mu.Unlock()
		s.ok(id, typ, info)

	case "terminal_input":
		var c struct {
			TerminalID string `json:"terminalId"`
			Data       string `json:"data"`
		}
		_ = json.Unmarshal(line, &c)
		t := s.terms.get(c.TerminalID)
		if t == nil {
			s.fail(id, typ, "Unknown terminal: "+c.TerminalID)
			return true
		}
		if err := t.input(c.Data); err != nil {
			s.fail(id, typ, err.Error())
			return true
		}
		s.ok(id, typ, nil)

	case "terminal_resize":
		var c struct {
			TerminalID string `json:"terminalId"`
			Cols       int    `json:"cols"`
			Rows       int    `json:"rows"`
		}
		_ = json.Unmarshal(line, &c)
		t := s.terms.get(c.TerminalID)
		switch {
		case t == nil:
			s.fail(id, typ, "Unknown terminal: "+c.TerminalID)
		case c.Cols <= 0 || c.Rows <= 0:
			s.fail(id, typ, "terminal_resize requires positive cols and rows")
		default:
			t.mu.Lock()
			t.info.Cols, t.info.Rows = c.Cols, c.Rows
			t.mu.Unlock()
			s.ok(id, typ, nil)
		}

	case "terminal_close":
		var c struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(line, &c)
		t := s.terms.remove(c.TerminalID)
		if t == nil {
			s.fail(id, typ, "Unknown terminal: "+c.TerminalID)
			return true
		}
		t.mu.Lock()
		t.info.Running = false
		close(t.closed)
		s.emit(termClosedFrame{Type: "terminal_closed", TerminalID: c.TerminalID})
		t.mu.Unlock()
		s.ok(id, typ, nil)

	case "terminal_list":
		infos := []termInfo{}
		for _, t := range s.terms.list() {
			infos = append(infos, t.infoCopy())
		}
		s.ok(id, typ, map[string]any{"terminals": infos})

	case "terminal_snapshot":
		var c struct {
			TerminalID string `json:"terminalId"`
		}
		_ = json.Unmarshal(line, &c)
		t := s.terms.get(c.TerminalID)
		if t == nil {
			s.fail(id, typ, "Unknown terminal: "+c.TerminalID)
			return true
		}
		s.ok(id, typ, t.snapshot())

	default:
		return false
	}
	return true
}

// ptyCommand 模拟一次 `pty: true` 的 bash 调用：打开一个 reveal 的 command 终端并提问，
// 等到该终端收到含 `y` 的输入后以退出码 0 结束。回合被中断时以 cancelled 结束并返回 false。
func (t *turn) ptyCommand() bool {
	term := t.s.terms.add(t.s, "command", "fake pty command", t.s.opts.cwd, defaultCols, defaultRows)
	term.mu.Lock()
	t.s.emit(termOpenedFrame{Type: "terminal_opened", Terminal: term.info, Reveal: true})
	term.outputLocked("Continue? [y/N] ")
	term.mu.Unlock()
	select {
	case <-term.answer:
		code := 0
		term.exit(&code, false)
		return true
	case <-term.closed:
		return true
	case <-t.ctx.Done():
		term.exit(nil, true)
		return false
	}
}
