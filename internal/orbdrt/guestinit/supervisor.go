//go:build linux

package guestinit

import (
	"errors"
	"log/slog"
	"os"
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
type supervisor struct {
	log  *slog.Logger
	cred *syscall.Credential

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
	return &supervisor{log: slog.Default().With("component", "guest-init"), cred: cred, backoff: restartMin}
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
	p, err := os.StartProcess(orbdBin, []string{"orbd", "run"}, &os.ProcAttr{
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

// scheduleRestartLocked 在当前退避之后重启 orbd（重启失败继续按退避重试），并把退避翻倍（上限 restartMax）。
func (s *supervisor) scheduleRestartLocked() {
	delay := s.backoff
	s.backoff = min(s.backoff*2, restartMax)
	s.retry = time.AfterFunc(delay, func() {
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
