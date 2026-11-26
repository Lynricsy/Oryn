package orbdrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 沙箱的后台作业（M30 步骤 2.4）：`sandbox_exec` 的 detach。记录只在内存里（orbd 重启即丢，沙箱本就是一次性的）；
// 日志落在 $HOME/.oryn/sandbox/jobs/<id>.log（stdout 与 stderr 合写）。

const (
	sandboxMaxRunningJobs = 16
	sandboxMaxJobRecords  = 200
	sandboxJobLogMax      = 64 << 20
	sandboxJobLogPage     = 256 << 10
	sandboxJobKillWait    = 5 * time.Second
)

type sandboxJob struct {
	id      string
	seq     int
	command string
	logPath string
	started time.Time
	pid     int
	done    chan struct{}

	// 以下由 sandboxJobs.mu 保护。
	ended *time.Time
	exit  *int

	log *capLog
}

// capLog 把输出写进日志文件，写满 limit 后丢弃后续输出并记下截断。
type capLog struct {
	mu        sync.Mutex
	f         *os.File
	n, limit  int64
	truncated bool
}

func (l *capLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	room := l.limit - l.n
	b := p
	if int64(len(b)) > room {
		b = b[:max(room, 0)]
		l.truncated = true
	}
	if len(b) > 0 {
		n, err := l.f.Write(b)
		l.n += int64(n)
		if err != nil {
			// 写不进去（磁盘满等）就当截断：命令本身不该因此失败
			l.truncated = true
		}
	}
	return len(p), nil
}

func (l *capLog) stat() (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n, l.truncated
}

type sandboxJobs struct {
	r   *Runtime
	dir string

	mu   sync.Mutex
	seq  int
	jobs map[string]*sandboxJob
	// starting 是已占了运行名额、正在求登录环境与启动的作业数。
	starting int
}

// newSandboxJobs 清空上次运行留下的作业日志目录（记录只在内存里，旧日志已无主）。
func newSandboxJobs(r *Runtime) (*sandboxJobs, error) {
	dir := filepath.Join(r.home, ".oryn", "sandbox", "jobs")
	if err := resetDir(dir); err != nil {
		return nil, err
	}
	return &sandboxJobs{r: r, dir: dir, jobs: map[string]*sandboxJob{}}, nil
}

func (j *sandboxJobs) running() int {
	n := 0
	for _, jb := range j.jobs {
		if jb.ended == nil {
			n++
		}
	}
	return n
}

// evictLocked 在记录数达到上限时淘汰最早结束的作业并删除其日志。
func (j *sandboxJobs) evictLocked() {
	for len(j.jobs) >= sandboxMaxJobRecords {
		var oldest *sandboxJob
		for _, jb := range j.jobs {
			if jb.ended != nil && (oldest == nil || jb.ended.Before(*oldest.ended)) {
				oldest = jb
			}
		}
		if oldest == nil {
			return
		}
		delete(j.jobs, oldest.id)
		_ = os.Remove(oldest.logPath)
	}
}

// start 以登录 shell 在 dir 里起一个后台作业；timeout > 0 时到点杀进程组（退出码 124）。
func (j *sandboxJobs) start(command, dir string, env map[string]string, timeout time.Duration) (string, error) {
	j.mu.Lock()
	if j.running()+j.starting >= sandboxMaxRunningJobs {
		j.mu.Unlock()
		return "", errf(http.StatusConflict, "too_many_jobs", "at most %d jobs may run at the same time", sandboxMaxRunningJobs)
	}
	j.starting++
	j.seq++
	seq := j.seq
	j.mu.Unlock()
	jb, err := j.spawn(seq, command, dir, env, timeout)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.starting--
	if err != nil {
		return "", err
	}
	j.evictLocked()
	j.jobs[jb.id] = jb
	return jb.id, nil
}

// spawn 启动作业进程并在后台等它结束；登录环境求值可能要几秒，不持锁。
func (j *sandboxJobs) spawn(seq int, command, dir string, env map[string]string, timeout time.Duration) (*sandboxJob, error) {
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return nil, err
	}
	id := "j" + strconv.Itoa(seq)
	logPath := filepath.Join(j.dir, id+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmdEnv := loginEnv(j.r.ctx, dir, j.r.baseEnv())
	maps.Copy(cmdEnv, env)
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(j.r.ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(j.r.ctx)
	}
	cmd := j.r.bins.command(ctx, "bash", "-lc", command)
	cmd.Dir = dir
	cmd.Env = envList(cmdEnv)
	cmd.Stdin = nil
	lg := &capLog{f: f, limit: sandboxJobLogMax}
	cmd.Stdout, cmd.Stderr = lg, lg
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		_ = f.Close()
		_ = os.Remove(logPath)
		return nil, fmt.Errorf("start command: %w", err)
	}
	jb := &sandboxJob{
		id: id, seq: seq, command: command, logPath: logPath, started: time.Now(),
		pid: cmd.Process.Pid, done: make(chan struct{}), log: lg,
	}
	go func() {
		defer cancel()
		werr := cmd.Wait()
		// 组长退出后，组里残留的后代一并结束
		_ = syscall.Kill(-jb.pid, syscall.SIGKILL)
		code := exitCode(cmd.ProcessState, werr)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = boxExecTimeoutExit
		}
		lg.mu.Lock()
		_ = f.Close()
		lg.mu.Unlock()
		now := time.Now()
		j.mu.Lock()
		jb.ended, jb.exit = &now, &code
		j.mu.Unlock()
		close(jb.done)
	}()
	return jb, nil
}

func (j *sandboxJobs) view(jb *sandboxJob) apigen.OrbSandboxJob {
	n, trunc := jb.log.stat()
	v := apigen.OrbSandboxJob{
		ID: jb.id, Command: jb.command, State: apigen.OrbSandboxJobRunning,
		StartedAt: jb.started.UnixMilli(), LogBytes: n, LogTruncated: trunc,
	}
	if jb.ended != nil {
		v.State = apigen.OrbSandboxJobExited
		v.EndedAt = new(jb.ended.UnixMilli())
		v.Exit = new(*jb.exit)
	}
	return v
}

// list 按启动顺序返回全部记录。
func (j *sandboxJobs) list() []apigen.OrbSandboxJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	jobs := slices.SortedFunc(maps.Values(j.jobs), func(a, b *sandboxJob) int { return a.seq - b.seq })
	out := make([]apigen.OrbSandboxJob, 0, len(jobs))
	for _, jb := range jobs {
		out = append(out, j.view(jb))
	}
	return out
}

func (j *sandboxJobs) get(id string) (*sandboxJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	jb := j.jobs[id]
	if jb == nil {
		return nil, errf(http.StatusNotFound, "job_not_found", "no job %q", id)
	}
	return jb, nil
}

// log 读一段日志：单次至多 256 KiB；没给 offset 时取末尾 256 KiB。段首与段尾都落在 UTF-8 字符边界上
// （作业已结束且读到文件末尾时，末尾的残片原样给出）。
func (j *sandboxJobs) log(id string, offset int64, hasOffset bool) (apigen.OrbSandboxJobLog, error) {
	jb, err := j.get(id)
	if err != nil {
		return apigen.OrbSandboxJobLog{}, err
	}
	// 先取状态再取大小：已结束时大小即最终大小。
	j.mu.Lock()
	exited, exit := jb.ended != nil, jb.exit
	j.mu.Unlock()
	size, _ := jb.log.stat()
	f, err := os.Open(jb.logPath)
	if err != nil {
		return apigen.OrbSandboxJobLog{}, err
	}
	defer f.Close()
	if !hasOffset {
		offset = max(size-sandboxJobLogPage, 0)
	}
	if offset > size {
		return apigen.OrbSandboxJobLog{}, badRequest("offset %d is past the end of the log (%d bytes)", offset, size)
	}
	buf := make([]byte, min(size-offset, sandboxJobLogPage))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return apigen.OrbSandboxJobLog{}, err
	}
	buf = buf[:n]
	if !hasOffset {
		// 从末尾截取时，跳过开头不完整字符的后续字节
		skip := 0
		for skip < 3 && skip < len(buf) && !utf8.RuneStart(buf[skip]) {
			skip++
		}
		buf, offset = buf[skip:], offset+int64(skip)
	}
	if end := offset + int64(len(buf)); !(exited && end == size) {
		buf = buf[:len(buf)-incompleteRuneTail(buf)]
	}
	next := offset + int64(len(buf))
	res := apigen.OrbSandboxJobLog{
		Text: string(buf), Offset: offset, NextOffset: next, State: apigen.OrbSandboxJobRunning,
		EOF: exited && next == size,
	}
	if exited {
		res.State, res.Exit = apigen.OrbSandboxJobExited, new(*exit)
	}
	return res, nil
}

// kill 杀掉作业的进程组并等它结束（至多 5 秒）；已结束的作业不做事。
func (j *sandboxJobs) kill(id string) error {
	jb, err := j.get(id)
	if err != nil {
		return err
	}
	select {
	case <-jb.done:
		return nil
	default:
	}
	_ = syscall.Kill(-jb.pid, syscall.SIGKILL)
	select {
	case <-jb.done:
	case <-time.After(sandboxJobKillWait):
	}
	return nil
}

// killAll 杀掉全部仍在运行的作业（orbd 退出）。
func (j *sandboxJobs) killAll() {
	j.mu.Lock()
	var pids []int
	for _, jb := range j.jobs {
		if jb.ended == nil {
			pids = append(pids, jb.pid)
		}
	}
	j.mu.Unlock()
	for _, pid := range pids {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
