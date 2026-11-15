//go:build linux

package guestinit

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	restartMin = time.Second
	restartMax = 10 * time.Second
	// stableRun 是 orbd 运行多久后把重启退避复位到 restartMin。
	stableRun = time.Minute
)

// supervisor 监督 `orbd run`，同时是 PID 1 的回收者：SIGCHLD 时 reap 以 wait4(-1, WNOHANG) 回收所有子进程
// （包括被领养的孤儿）。启动子进程与回收持同一把锁，因此不会出现「进程已退出并被回收、pid 却还没记下」。
//
// orbd 意外退出后，它的受管进程（agent、服务、桌面……各有自己的进程组、不随 orbd 退出）被 PID 1 领养；
// 重启前先 sweep 掉上一代的全部存活进程，新一代不会与它们并存。
type supervisor struct {
	log  *slog.Logger
	cred *syscall.Credential
	bin  string
	argv []string
	// sweep 杀掉并回收本进程的全部子进程（上一代 orbd 留下的进程树），返回时一个不剩。
	sweep func()

	mu       sync.Mutex
	env      []string
	dir      string
	started  bool
	stopping bool
	pid      int           // 当前 orbd 进程；0 = 没有
	exited   chan struct{} // 当前进程退出时关闭
	since    time.Time     // 当前进程的启动时间
	backoff  time.Duration
	retry    *time.Timer
}

func newSupervisor(cred *syscall.Credential) *supervisor {
	return &supervisor{
		log: slog.Default().With("component", "guest-init"), cred: cred,
		bin: orbdBin, argv: []string{"orbd", "run"}, sweep: sweepChildren, backoff: restartMin,
	}
}

func (s *supervisor) Start(env []string, dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("orbd already started")
	}
	s.env, s.dir = env, dir
	if err := s.spawnLocked(); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *supervisor) spawnLocked() error {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	if _, err := os.Stat(s.dir); err != nil {
		s.dir = userHome
	}
	p, err := os.StartProcess(s.bin, s.argv, &os.ProcAttr{
		Dir:   s.dir,
		Env:   s.env,
		Files: []*os.File{devnull, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Credential: s.cred, Setsid: true},
	})
	if err != nil {
		return err
	}
	s.pid, s.exited, s.since = p.Pid, make(chan struct{}), time.Now()
	_ = p.Release() // 回收由 reap 负责
	s.log.Info("orbd started", "pid", s.pid)
	return nil
}

func (s *supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid != 0
}

func (s *supervisor) Signal(sig syscall.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pid == 0 {
		return nil
	}
	return syscall.Kill(s.pid, sig)
}

func (s *supervisor) Stop(grace time.Duration) {
	s.mu.Lock()
	s.stopping = true
	if s.retry != nil {
		s.retry.Stop()
	}
	pid, exited := s.pid, s.exited
	s.mu.Unlock()
	if pid == 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-exited:
	case <-t.C:
		s.log.Warn("orbd did not exit within the grace period", "grace", grace)
	}
}

// reap 回收所有已退出的子进程；orbd 意外退出时按退避安排重启。
func (s *supervisor) reap() {
	for {
		s.mu.Lock()
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if err != nil || pid <= 0 {
			s.mu.Unlock()
			return
		}
		if pid == s.pid {
			s.onExitLocked(ws)
		}
		s.mu.Unlock()
	}
}

func (s *supervisor) onExitLocked(ws unix.WaitStatus) {
	ran := time.Since(s.since)
	s.pid = 0
	close(s.exited)
	if s.stopping {
		s.log.Info("orbd exited", "status", ws.ExitStatus())
		return
	}
	if ran >= stableRun {
		s.backoff = restartMin
	}
	s.log.Warn("orbd exited unexpectedly; restarting", "status", ws.ExitStatus(), "signaled", ws.Signaled(), "in", s.backoff)
	s.scheduleRestartLocked()
}

// scheduleRestartLocked 在当前退避之后清掉上一代的残留进程并重启 orbd（重启失败继续按退避重试），并把退避
// 翻倍（上限 restartMax）。
func (s *supervisor) scheduleRestartLocked() {
	delay := s.backoff
	s.backoff = min(s.backoff*2, restartMax)
	s.retry = time.AfterFunc(delay, func() {
		// 不持锁：sweep 要等进程退出（可能卡在冻结盘上直到解冻），期间 reap 与 Stop 照常进行。
		s.sweep()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopping || s.pid != 0 {
			return
		}
		if err := s.spawnLocked(); err != nil {
			s.log.Error("restart orbd failed", "err", err, "retryIn", s.backoff)
			s.scheduleRestartLocked()
		}
	})
}

// sweepChildren 杀掉并回收本进程（PID 1）的全部子进程，直到一个不剩：被杀进程的子进程在它退出时改由本进程
// 领养，下一轮接着杀。guest 里 PID 1 的子进程只有 orbd 一代及其孤儿。
func sweepChildren() {
	self := os.Getpid()
	for {
		kids := childPIDs(self)
		if len(kids) == 0 {
			return
		}
		slog.Warn("guest-init: killing leftover processes of the previous orbd", "pids", kids)
		for _, pid := range kids {
			_ = unix.Kill(pid, unix.SIGKILL)
		}
		for _, pid := range kids {
			// 已被 reap 抢先回收时是 ECHILD。
			_, _ = unix.Wait4(pid, nil, 0, nil)
		}
	}
}

// childPIDs 扫 /proc 找父进程为 ppid 的进程（含尚未回收的僵尸）。
func childPIDs(ppid int) []int {
	des, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, de := range des {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + de.Name() + "/stat")
		if err != nil {
			continue // 已退出
		}
		// 格式：pid (comm) state ppid …；comm 里可能有括号与空格，从最后一个 ')' 之后取。
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) >= 2 && f[1] == strconv.Itoa(ppid) {
			out = append(out, pid)
		}
	}
	return out
}
