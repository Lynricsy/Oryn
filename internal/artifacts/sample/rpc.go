package sample

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// 帧循环（omp `--mode rpc`，帧类型见 web/src/omp/rpc-types.d.ts）：
//  1. 等 `ready`，写一条 `{"id": "q", "type": "prompt", "message", "images"?}`；
//  2. `message_update` 里 `assistantMessageEvent.type == "text_delta"` 的 `delta` 是流式增量；
//  3. `message_end` 里 `message.role == "assistant"` 的文本块拼成最终答案（取最后一条助手消息，自己累积，不依赖
//     `agent_end`）；
//  4. 读到 id 为 q 的 `prompt_result` 即结束：`status != "completed"` 算失败（原因取 `error.message`）。
//
// 之后关闭 stdin 让 omp 自行退出并把 stdout 读到 EOF，exitGrace 内没退出就取消运行（杀进程组）。
// 需要回答的 `extension_ui_request`（对话框类）一律回 cancelled，免得 omp 卡在等界面上。

// promptID 是 prompt 帧的 id。
const promptID = "q"

// ompArgs 是 omp 的命令行参数：没有工具、技能、扩展、规则、上下文文件与 LSP 的一次性 RPC 会话。
var ompArgs = []string{
	"--mode", "rpc", "--no-session", "--no-tools", "--no-skills", "--no-extensions",
	"--no-rules", "--no-context-files", "--no-lsp",
}

// dialogMethods 是需要回答的 extension_ui_request。
var dialogMethods = map[string]bool{
	"select": true, "confirm": true, "askDialog": true, "input": true, "editor": true, "ask": true, "form": true,
}

// errNoResult 表示 omp 在给出 prompt_result 之前关闭了输出。
var errNoResult = errors.New("omp 在给出结果前关闭了输出")

type promptFrame struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Images  []imageContent `json:"images,omitempty"`
}

type imageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// event 是帧里本循环要看的字段。
type event struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Success *bool           `json:"success"`
	Error   json.RawMessage `json:"error"`
	Status  string          `json:"status"`
	Delta   *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// converseRun 运行 Config.Run 并在它的 stdout 上走帧循环。返回时 Run 已经返回（进程已退出）。
func (s *Sampler) converseRun(ctx context.Context, home, prompt string, images []Image, onText func(string) error) (string, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	runErr := make(chan error, 1)
	go func() {
		err := s.cfg.Run(runCtx, home, inR, outW)
		_ = outW.Close()
		_ = inR.Close()
		runErr <- err
	}()
	// ctx 结束时让两端的读写立刻失败，帧循环不必等 Run 自己退出
	stop := context.AfterFunc(runCtx, func() {
		_ = outR.CloseWithError(runCtx.Err())
		_ = inW.CloseWithError(runCtx.Err())
	})
	defer stop()
	answer, err := converse(outR, inW, prompt, images, onText)
	_ = inW.Close()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	if err == nil {
		// 已经拿到回答：omp 的退出方式不影响结果，只是不能让它一直挂着
		select {
		case <-runErr:
		case <-time.After(exitGrace):
			cancelRun()
			<-runErr
		}
		return answer, nil
	}
	if !errors.Is(err, errNoResult) {
		cancelRun()
	}
	rerr := <-runErr
	if errors.Is(err, errNoResult) && rerr != nil && ctx.Err() == nil {
		return "", fmt.Errorf("%w: %w", err, rerr)
	}
	return "", err
}

// converse 走一次帧循环（见文件开头的说明），读到 prompt_result 即返回。
func converse(stdout io.Reader, stdin io.Writer, prompt string, images []Image, onText func(string) error) (string, error) {
	dec := omprpc.NewDecoder(stdout)
	w := omprpc.NewWriter(stdin, 0)
	var (
		sent     bool
		answer   string
		rejected string
	)
	for {
		frame, err := dec.Next()
		if err != nil {
			if omprpc.IsRecoverable(err) {
				continue
			}
			return "", errNoResult
		}
		var ev event
		if json.Unmarshal(frame, &ev) != nil {
			continue
		}
		switch ev.Type {
		case omprpc.TypeReady:
			if sent {
				continue
			}
			sent = true
			pf := promptFrame{ID: promptID, Type: "prompt", Message: prompt}
			for _, im := range images {
				pf.Images = append(pf.Images, imageContent{Type: "image", Data: im.Data, MimeType: im.MimeType})
			}
			if err := w.WriteFrame(pf); err != nil {
				return "", fmt.Errorf("写 prompt 帧: %w", err)
			}
		case "extension_ui_request":
			if dialogMethods[ev.Method] && ev.ID != "" {
				if err := w.WriteFrame(map[string]any{"type": "extension_ui_response", "id": ev.ID, "cancelled": true}); err != nil {
					return "", fmt.Errorf("回答界面请求: %w", err)
				}
			}
		case omprpc.TypeResponse:
			if ev.ID == promptID && ev.Success != nil && !*ev.Success {
				rejected = errorText(ev.Error)
			}
		case "message_update":
			if ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Delta != "" && onText != nil {
				if err := onText(ev.Delta.Delta); err != nil {
					return "", err
				}
			}
		case "message_end":
			if ev.Message != nil && ev.Message.Role == "assistant" {
				if text := contentText(ev.Message.Content); text != "" {
					answer = text
				}
			}
		case "prompt_result":
			if ev.ID != promptID {
				continue
			}
			if ev.Status != "completed" {
				msg := errorText(ev.Error)
				if msg == "" {
					msg = rejected
				}
				if msg == "" {
					msg = "状态 " + ev.Status
				}
				return "", &ModelError{Message: msg}
			}
			return answer, nil
		}
	}
}

// errorText 取 error 字段的可读文本：字符串原样，对象取 message。
func errorText(raw json.RawMessage) string {
	e := omprpc.Envelope{Error: raw}
	return strings.TrimSpace(e.ErrorText())
}

// contentText 把消息内容（字符串或内容块数组）里的文本块按顺序拼起来；思考、工具调用等块不算。
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// runOmp 是默认的 Config.Run：环境变量从零构造、独立进程组，ctx 结束时杀掉整个进程组。
func (s *Sampler) runOmp(ctx context.Context, home string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, s.cfg.OmpPath, ompArgs...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PI_CODING_AGENT_DIR=" + AgentDir(home), "PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	stderr := &tailBuffer{max: maxErrDetail}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			return fmt.Errorf("%w: %s", err, tail)
		}
		return err
	}
	return nil
}

// tailBuffer 只保留写入内容的最后 max 个字节，并发安全。
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
