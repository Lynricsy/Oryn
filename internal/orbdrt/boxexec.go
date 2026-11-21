package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// POST /box/exec（M23 步骤 2.3）：在 box 上以登录 shell 跑一条命令，输出以 NDJSON 流回（每行一个对象并立即
// flush）：`{"s":"o"|"e","d":"<文本块>"}`（每块至多 32 KiB），最后一行 `{"exit": <退出码>}`；超时杀进程组并回
// `{"exit": 124}`；对端断开时杀进程组。

const (
	maxBoxExecTimeout = 3600
	boxExecChunkMax   = 32 << 10
	// boxExecTimeoutExit 是超时时回报的退出码（同 coreutils timeout）。
	boxExecTimeoutExit = 124
)

type boxExecChunk struct {
	S string `json:"s"`
	D string `json:"d"`
}

type boxExecExit struct {
	Exit int `json:"exit"`
}

// ndjsonOut 串行化两路输出的写入与 flush。
type ndjsonOut struct {
	mu  sync.Mutex
	enc *json.Encoder
	fl  http.Flusher
}

func (o *ndjsonOut) emit(v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.enc.Encode(v) // 对端已断开时写失败无妨：ctx 结束会杀掉进程组
	if o.fl != nil {
		o.fl.Flush()
	}
}

// ndjsonStream 是一路输出：按块发出，块尾不完整的 UTF-8 字符留到下一块（否则会被 JSON 编码成替换字符）。
type ndjsonStream struct {
	o     *ndjsonOut
	s     string
	carry []byte
}

func (st *ndjsonStream) Write(p []byte) (int, error) {
	buf := append(st.carry, p...)
	cut := len(buf) - incompleteRuneTail(buf)
	st.carry = append([]byte(nil), buf[cut:]...)
	st.send(buf[:cut])
	return len(p), nil
}

func (st *ndjsonStream) send(b []byte) {
	for len(b) > 0 {
		n := min(len(b), boxExecChunkMax)
		for n < len(b) && n > 0 && !utf8.RuneStart(b[n]) {
			n--
		}
		if n == 0 {
			n = min(len(b), boxExecChunkMax)
		}
		st.o.emit(boxExecChunk{S: st.s, D: string(b[:n])})
		b = b[n:]
	}
}

// flush 发出留着的残片（命令已结束）。
func (st *ndjsonStream) flush() {
	if len(st.carry) > 0 {
		st.send(st.carry)
		st.carry = nil
	}
}

// incompleteRuneTail 返回 b 末尾不完整 UTF-8 字符的字节数（0–3）。
func incompleteRuneTail(b []byte) int {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if utf8.FullRune(b[len(b)-i:]) {
				return 0
			}
			return i
		}
	}
	return 0
}

// boxExecDir 解析 exec 的工作目录：`~` / `~/…` / 绝对路径，须（跟随符号链接后）落在 $HOME 之下或等于 $HOME，且是目录。
func (r *Runtime) boxExecDir(cwd string) (string, error) {
	if cwd == "" {
		cwd = "~"
	}
	if strings.ContainsRune(cwd, 0) || (cwd != "~" && !strings.HasPrefix(cwd, "~/") && !filepath.IsAbs(cwd)) {
		return "", badPath("cwd must be absolute or start with ~/: %q", cwd)
	}
	abs := r.resolvePath(cwd)
	real, err := evalExisting(abs)
	if err != nil {
		return "", fsError(err)
	}
	home, err := filepath.EvalSymlinks(r.home)
	if err != nil {
		return "", err
	}
	if !within(real, home) {
		return "", badPath("cwd %q is outside $HOME", cwd)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fsError(err)
	}
	if !st.IsDir() {
		return "", errf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs)
	}
	return abs, nil
}

func (r *Runtime) handleBoxExec(w http.ResponseWriter, req *http.Request) {
	if r.boxMgr(w) == nil {
		return
	}
	var body apigen.OrbBoxExecRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(body.Command) == "" {
		writeErr(w, badRequest("command is required"))
		return
	}
	if body.TimeoutSec < 1 || body.TimeoutSec > maxBoxExecTimeout {
		writeErr(w, badRequest("timeoutSec must be 1-%d", maxBoxExecTimeout))
		return
	}
	dir, err := r.boxExecDir(body.Cwd)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), time.Duration(body.TimeoutSec)*time.Second)
	defer cancel()
	env := loginEnv(ctx, dir, r.baseEnv())
	cmd := r.bins.command(ctx, "bash", "-lc", body.Command)
	cmd.Dir = dir
	cmd.Env = envList(env)
	cmd.Stdin = nil
	// 独立进程组：超时或对端断开时连同后代一起杀掉
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	out := &ndjsonOut{enc: enc}
	out.fl, _ = w.(http.Flusher)
	stdout, stderr := &ndjsonStream{o: out, s: "o"}, &ndjsonStream{o: out, s: "e"}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if out.fl != nil {
		out.fl.Flush()
	}
	if err := cmd.Start(); err != nil {
		out.emit(boxExecChunk{S: "e", D: "start command: " + err.Error() + "\n"})
		out.emit(boxExecExit{Exit: 127})
		return
	}
	werr := cmd.Wait()
	// 组长退出后，组里残留的后代一并结束
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	stdout.flush()
	stderr.flush()
	code := exitCode(cmd.ProcessState, werr)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && req.Context().Err() == nil {
		code = boxExecTimeoutExit
	}
	out.emit(boxExecExit{Exit: code})
}

// exitCode 返回进程的退出码：被信号结束时为 128 + 信号值。
func exitCode(ps *os.ProcessState, err error) int {
	if ps == nil {
		if err != nil {
			return -1
		}
		return 0
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
