package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// 帧与命令的形状取自 oryn-core `packages/coding-agent/src/modes/rpc/`：
// rpc-types.ts（RpcCommand / RpcResponse / RpcReadyFrame / RpcPromptResultFrame /
// RpcSessionSettledFrame / RpcExtensionUIRequest）、rpc-mode.ts（ready 帧、success/error 应答、
// "Unknown command: X"）、rpc-session-events.ts（messageId 与 delta 投影），
// 以及 packages/agent/src/types.ts 的 AgentEvent、packages/ai/src/types.ts 的消息结构。

const fakeVersion = "omp/0.0.0-fakeomp"

type options struct {
	mode       string
	cwd        string
	sessionDir string
	script     string
	delay      time.Duration
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, bool, error) {
	var o options
	fs := flag.NewFlagSet("fakeomp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.mode, "mode", "rpc-ui", "rpc | rpc-ui")
	fs.StringVar(&o.cwd, "cwd", "", "工作目录")
	fs.StringVar(&o.sessionDir, "session-dir", "", "会话目录")
	fs.StringVar(&o.script, "script", getenv("FAKEOMP_SCRIPT"), "替换默认 prompt 帧序列的脚本文件")
	delay := 20 * time.Millisecond
	if v := getenv("FAKEOMP_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			return o, false, fmt.Errorf("FAKEOMP_DELAY_MS: %w", err)
		}
		delay = time.Duration(ms) * time.Millisecond
	}
	fs.DurationVar(&o.delay, "delay", delay, "流式帧间隔")
	version := fs.Bool("version", false, "打印版本")
	if err := fs.Parse(args); err != nil {
		return o, false, err
	}
	if *version {
		return o, true, nil
	}
	if o.mode != "rpc" && o.mode != "rpc-ui" {
		return o, false, fmt.Errorf("fakeomp: unsupported --mode %q", o.mode)
	}
	if o.cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return o, false, err
		}
		o.cwd = wd
	}
	return o, false, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	opts, showVersion, err := parseOptions(args, getenv, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if showVersion {
		fmt.Fprintln(stdout, fakeVersion)
		return 0
	}
	var script []string
	if opts.script != "" {
		b, err := os.ReadFile(opts.script)
		if err != nil {
			fmt.Fprintln(stderr, "fakeomp: read script:", err)
			return 2
		}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				script = append(script, l)
			}
		}
	}
	s := &server{
		opts:           opts,
		script:         script,
		out:            stdout,
		protocol:       1,
		sessionDir:     opts.sessionDir,
		messageUpdates: "full",
		subLevel:       "off",
		uiWait:         map[string]chan json.RawMessage{},
		prompts:        make(chan promptReq, 100),
	}
	return s.serve(ctx, stdin, stderr)
}

type promptReq struct {
	id      string
	message string
	images  []json.RawMessage
}

type server struct {
	opts   options
	script []string

	outMu    sync.Mutex
	out      io.Writer
	protocol int
	chunkSeq int

	mu             sync.Mutex
	sessionDir     string
	sess           *sessionFile
	provider       string
	modelID        string
	thinking       string
	events         map[string]bool // nil = 全部
	messageUpdates string
	askDialog      bool
	subLevel       string
	msgSeq         int
	uiSeq          int
	uiWait         map[string]chan json.RawMessage
	streaming      bool
	pending        int // 已接受但尚未结束的 prompt 数（含进行中的一个）
	messageCount   int
	cancelTurn     context.CancelFunc

	prompts chan promptReq
}

func (s *server) serve(ctx context.Context, stdin io.Reader, stderr io.Writer) int {
	s.emit(map[string]any{
		"type":                      "ready",
		"protocolVersion":           1,
		"supportedProtocolVersions": []int{1, 2},
		"maxFrameBytes":             omprpc.MaxFrameBytes,
		"maxReassembledFrameBytes":  omprpc.MaxReassembledBytes,
	})

	turnCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for p := range s.prompts {
			s.runTurn(turnCtx, p)
		}
	}()

	readDone := make(chan error, 1)
	go func() {
		r := omprpc.NewReader(stdin, 256<<20)
		for {
			line, err := r.ReadLine()
			if err != nil {
				if errors.Is(err, omprpc.ErrLineTooLong) {
					s.emit(response{Type: "response", Command: "parse", Error: "Failed to parse command: line too long"})
					continue
				}
				readDone <- err
				return
			}
			s.handle(line)
		}
	}()

	select {
	case err := <-readDone:
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			fmt.Fprintln(stderr, "fakeomp: read stdin:", err)
		}
		// stdin 关闭：放弃进行中的回合并退出（omp 同样在 stdin EOF 时收尾退出）。
		cancelAll()
		close(s.prompts)
		<-workerDone
		return 0
	case <-ctx.Done():
		cancelAll()
		return 143
	}
}

// response 对应 rpc-mode.ts 的 success()/error()：未设置的 id 被省略。
type response struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *server) ok(id, command string, data any) {
	s.emit(response{ID: id, Type: "response", Command: command, Success: true, Data: data})
}

func (s *server) fail(id, command, msg string) {
	s.emit(response{ID: id, Type: "response", Command: command, Success: false, Error: msg})
}

// emit 写出一个帧；协议 v2 下超过物理行上限的帧按 rpc_chunk 分块连续写出。
func (s *server) emit(frame any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(frame); err != nil {
		panic(fmt.Sprintf("fakeomp: encode frame: %v", err))
	}
	line := buf.Bytes()
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if len(line) > omprpc.MaxFrameBytes && s.protocol == 2 {
		s.chunkSeq++
		chunks, err := omprpc.EncodeChunks(line[:len(line)-1], fmt.Sprintf("rpc-%d", s.chunkSeq))
		if err == nil {
			for _, c := range chunks {
				_, _ = s.out.Write(append(c, '\n'))
			}
			return
		}
	}
	_, _ = s.out.Write(line)
}

// sessionEventTypes 是受 set_event_filter 约束的会话事件。
var sessionEventTypes = map[string]bool{
	"agent_start": true, "agent_end": true, "turn_start": true, "turn_end": true,
	"message_start": true, "message_update": true, "message_end": true,
	"tool_execution_start": true, "tool_execution_update": true, "tool_execution_end": true, "tool_stream_update": true,
}

func (s *server) emitEvent(frame map[string]any) {
	t, _ := frame["type"].(string)
	s.mu.Lock()
	filtered := sessionEventTypes[t] && s.events != nil && !s.events[t]
	s.mu.Unlock()
	if !filtered {
		s.emit(frame)
	}
}

func (s *server) handle(line []byte) {
	env, err := omprpc.ParseEnvelope(line)
	if err != nil {
		s.emit(response{Type: "response", Command: "parse", Error: "Failed to parse command: " + err.Error()})
		return
	}
	id := env.ID
	switch env.Type {
	case "negotiate_protocol":
		var c struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		_ = json.Unmarshal(line, &c)
		if c.ProtocolVersion != 2 {
			s.fail(id, env.Type, fmt.Sprintf("Unsupported RPC protocol version: %d", c.ProtocolVersion))
			return
		}
		s.ok(id, env.Type, map[string]int{"protocolVersion": 2})
		s.outMu.Lock()
		s.protocol = 2
		s.outMu.Unlock()

	case "open_session":
		var c struct {
			SessionDir string `json:"sessionDir"`
			Provider   string `json:"provider"`
			ModelID    string `json:"modelId"`
		}
		if err := json.Unmarshal(line, &c); err != nil || c.SessionDir == "" {
			s.fail(id, env.Type, "open_session requires sessionDir")
			return
		}
		s.mu.Lock()
		if s.streaming {
			s.mu.Unlock()
			s.fail(id, env.Type, "Cannot open a session while the agent is streaming")
			return
		}
		sess, resumed, err := openSessionDir(c.SessionDir, s.opts.cwd, time.Now())
		if err == nil {
			s.sessionDir, s.sess = c.SessionDir, sess
			if c.Provider != "" {
				s.provider, s.modelID = c.Provider, c.ModelID
			}
		}
		s.mu.Unlock()
		if err != nil {
			s.fail(id, env.Type, err.Error())
			return
		}
		s.ok(id, env.Type, map[string]any{"cancelled": false, "resumed": resumed, "sessionId": sess.id, "sessionFile": sess.path})

	case "set_event_filter":
		var c struct {
			Events         *[]string `json:"events"`
			MessageUpdates string    `json:"messageUpdates"`
		}
		_ = json.Unmarshal(line, &c)
		if c.MessageUpdates == "" {
			c.MessageUpdates = "full"
		}
		if c.MessageUpdates != "full" && c.MessageUpdates != "delta" {
			s.fail(id, env.Type, `messageUpdates must be "full" or "delta"`)
			return
		}
		s.mu.Lock()
		s.messageUpdates = c.MessageUpdates
		var events any
		if c.Events == nil {
			s.events = nil
		} else {
			s.events = map[string]bool{}
			for _, e := range *c.Events {
				s.events[e] = true
			}
			events = *c.Events
		}
		s.mu.Unlock()
		s.ok(id, env.Type, map[string]any{"events": events, "messageUpdates": c.MessageUpdates})

	case "set_ask_dialog":
		var c struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.Unmarshal(line, &c)
		s.mu.Lock()
		s.askDialog = c.Enabled
		s.mu.Unlock()
		s.ok(id, env.Type, map[string]bool{"enabled": c.Enabled})

	case "set_subagent_subscription":
		var c struct {
			Level string `json:"level"`
		}
		_ = json.Unmarshal(line, &c)
		if c.Level != "off" && c.Level != "progress" && c.Level != "events" {
			s.fail(id, env.Type, `level must be "off", "progress" or "events"`)
			return
		}
		s.mu.Lock()
		s.subLevel = c.Level
		s.mu.Unlock()
		s.ok(id, env.Type, map[string]string{"level": c.Level})

	case "set_thinking_level":
		var c struct {
			Level string `json:"level"`
		}
		_ = json.Unmarshal(line, &c)
		s.mu.Lock()
		s.thinking = c.Level
		s.mu.Unlock()
		s.ok(id, env.Type, map[string]string{"thinkingLevel": c.Level, "thinkingConfigured": c.Level})

	case "get_state":
		s.ok(id, env.Type, s.state())

	case "prompt", "follow_up":
		var c struct {
			Message           string            `json:"message"`
			Images            []json.RawMessage `json:"images"`
			StreamingBehavior string            `json:"streamingBehavior"`
		}
		_ = json.Unmarshal(line, &c)
		if err := s.ensureSession(); err != nil {
			s.fail(id, env.Type, err.Error())
			return
		}
		s.mu.Lock()
		if env.Type == "prompt" && s.pending > 0 && c.StreamingBehavior == "" {
			s.mu.Unlock()
			s.fail(id, env.Type, "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")
			return
		}
		if s.pending >= cap(s.prompts) {
			s.mu.Unlock()
			s.fail(id, env.Type, "fakeomp: prompt queue full")
			return
		}
		s.pending++
		s.mu.Unlock()
		// 先应答再入队，保证 response 先于该回合的任何事件帧。
		if env.Type == "prompt" {
			s.ok(id, env.Type, map[string]bool{"agentInvoked": true})
		} else {
			s.ok(id, env.Type, nil)
		}
		s.prompts <- promptReq{id: id, message: c.Message, images: c.Images}

	case "abort":
		s.mu.Lock()
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
		s.mu.Unlock()
		s.ok(id, env.Type, nil)

	case "extension_ui_response":
		s.mu.Lock()
		ch := s.uiWait[id]
		delete(s.uiWait, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- json.RawMessage(line)
		}

	default:
		cmd := env.Type
		if cmd == "" {
			cmd = "unknown"
		}
		s.fail(id, cmd, "Unknown command: "+cmd)
	}
}

func (s *server) ensureSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess != nil {
		return nil
	}
	if s.sessionDir == "" {
		return errors.New("No session is open: pass --session-dir or send open_session")
	}
	sess, _, err := openSessionDir(s.sessionDir, s.opts.cwd, time.Now())
	if err != nil {
		return err
	}
	s.sess = sess
	return nil
}

func (s *server) state() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := map[string]any{
		"thinkingLevel":         nilIfEmpty(s.thinking),
		"isStreaming":           s.streaming,
		"isCompacting":          false,
		"steeringMode":          "one-at-a-time",
		"followUpMode":          "one-at-a-time",
		"interruptMode":         "immediate",
		"cwd":                   s.opts.cwd,
		"sessionId":             "",
		"autoCompactionEnabled": false,
		"autoRetryEnabled":      false,
		"fastModeEnabled":       false,
		"fastModeActive":        false,
		"slowModeSupported":     false,
		"slowModeEnabled":       false,
		"tokensPerSecond":       nil,
		"messageCount":          s.messageCount,
		"queuedMessageCount":    max(s.pending-1, 0),
		"hasPendingAsyncWork":   false,
		"isSettled":             s.pending == 0,
		"queuedMessages":        map[string]any{"steering": []string{}, "followUp": []string{}, "liveSteered": 0},
		"todoPhases":            []any{},
	}
	if s.sess != nil {
		st["sessionId"] = s.sess.id
		st["sessionFile"] = s.sess.path
	}
	provider, model := s.modelLocked()
	st["model"] = map[string]any{"provider": provider, "id": model, "name": model, "api": "fake"}
	return st
}

func (s *server) modelLocked() (string, string) {
	provider, model := s.provider, s.modelID
	if provider == "" {
		provider = "fake"
	}
	if model == "" {
		model = "fake-model"
	}
	return provider, model
}

func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
