package providerrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Process 驱动（仅测试）：把 `orbd run` 作为本机子进程启动，HOME = 每 orb 一个目录；
// freeze = SIGSTOP / SIGCONT，stop = SIGTERM，snapshot = 复制 HOME 目录。
//
// 目录布局：<Root>/orbs/<orbId>/home、<Root>/orbs/<orbId>/orbd.log、<Root>/snapshots/<snapshotId>。
type Process struct {
	Root    string
	OrbdBin string
	Types   map[string]config.DockerType
	Default string
	// StopTimeout 是 SIGTERM 后等待退出的时长，超时 SIGKILL。
	StopTimeout time.Duration

	mu    sync.Mutex
	procs map[string]*orbProc
}

type orbProc struct {
	cmd    *exec.Cmd
	done   chan struct{}
	frozen bool
}

// NewProcess 创建 process 驱动。root 为空时取环境变量 ORYN_PROCESS_ROOT，再缺省为临时目录下的 oryn-process；
// orbdBin 为空时取 ORYN_ORBD_BIN，再缺省为 PATH 中的 orbd。
func NewProcess(root, orbdBin string, dc config.Docker) *Process {
	if root == "" {
		root = os.Getenv("ORYN_PROCESS_ROOT")
	}
	if root == "" {
		root = filepath.Join(os.TempDir(), "oryn-process")
	}
	if orbdBin == "" {
		orbdBin = os.Getenv("ORYN_ORBD_BIN")
	}
	if orbdBin == "" {
		orbdBin = "orbd"
	}
	types := dc.Types
	if len(types) == 0 {
		types = config.DefaultDockerTypes()
	}
	def := dc.DefaultType
	if def == "" {
		def = "medium"
	}
	return &Process{Root: root, OrbdBin: orbdBin, Types: types, Default: def, StopTimeout: 30 * time.Second, procs: map[string]*orbProc{}}
}

// Info 实现 Driver。
func (p *Process) Info() Info {
	return Info{
		Driver:       apigen.Process,
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true},
		Types:        typeInfos(p.Types),
		DefaultType:  p.Default,
	}
}

// HomeDir 返回 orb 的 HOME 目录。
func (p *Process) HomeDir(orbID string) string { return filepath.Join(p.Root, "orbs", orbID, "home") }

func (p *Process) snapshotDir(id string) string { return filepath.Join(p.Root, "snapshots", id) }

// Exec 实现 Driver。
func (p *Process) Exec(ctx context.Context, j Job, out io.Writer) error {
	switch j.Op {
	case "spawn":
		return p.spawn(j, out)
	case "pause":
		return p.pause(j, out)
	case "snapshot":
		dst := p.snapshotDir(j.SnapshotID)
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		tmp := dst + ".tmp"
		_ = os.RemoveAll(tmp)
		if err := copyTree(p.HomeDir(j.OrbID), tmp); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	case "delete":
		p.kill(j.OrbID)
		return os.RemoveAll(filepath.Join(p.Root, "orbs", j.OrbID))
	case "delete_snapshot":
		return os.RemoveAll(p.snapshotDir(j.SnapshotID))
	}
	return fmt.Errorf("process: 不支持的操作 %q", j.Op)
}

func (p *Process) running(orbID string) *orbProc {
	pr := p.procs[orbID]
	if pr == nil {
		return nil
	}
	select {
	case <-pr.done:
		delete(p.procs, orbID)
		return nil
	default:
		return pr
	}
}

func (p *Process) spawn(j Job, out io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j.Spawn == nil {
		return errors.New("process: spawn 缺少参数")
	}
	home := p.HomeDir(j.OrbID)
	if pr := p.running(j.OrbID); pr != nil {
		if pr.frozen {
			if err := pr.cmd.Process.Signal(syscall.SIGCONT); err != nil {
				return err
			}
			pr.frozen = false
			fmt.Fprintln(out, "SIGCONT")
		}
		// 与 docker 驱动一致：写入本次轮换后的令牌，再让 orbd 立即用它重建 link。
		if err := writeTokenFile(home, j.Spawn.Env.OrynOrbToken); err != nil {
			return err
		}
		return pr.cmd.Process.Signal(syscall.SIGHUP)
	}
	if _, err := os.Stat(home); errors.Is(err, fs.ErrNotExist) && j.Spawn.SnapshotID != nil && *j.Spawn.SnapshotID != "" {
		if err := copyTree(p.snapshotDir(*j.Spawn.SnapshotID), home); err != nil {
			return fmt.Errorf("process: 从快照恢复: %w", err)
		}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	// orbd 优先读令牌文件：启动前写入（同时覆盖快照里带来的源 orb 令牌）。
	if err := writeTokenFile(home, j.Spawn.Env.OrynOrbToken); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(filepath.Dir(home), "orbd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(p.OrbdBin, "run")
	cmd.Dir = home
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"ORYN_URL="+j.Spawn.Env.OrynURL,
		"ORYN_ORB_ID="+j.OrbID,
		"ORYN_ORB_TOKEN="+j.Spawn.Env.OrynOrbToken,
	)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf("process: 启动 %s: %w", p.OrbdBin, err)
	}
	pr := &orbProc{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = logf.Close()
		close(pr.done)
	}()
	p.procs[j.OrbID] = pr
	fmt.Fprintf(out, "started pid %d\n", cmd.Process.Pid)
	return nil
}

// writeTokenFile 原子写入 <home>/.oryn/orb-token（0600）。
func writeTokenFile(home, token string) error {
	dir := filepath.Join(home, ".oryn")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "orb-token.tmp")
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "orb-token"))
}

func (p *Process) pause(j Job, out io.Writer) error {
	p.mu.Lock()
	pr := p.running(j.OrbID)
	if pr == nil {
		p.mu.Unlock()
		if j.PauseMode == "stop" {
			return nil
		}
		return fmt.Errorf("process: orb %s 未运行", j.OrbID)
	}
	if j.PauseMode == "freeze" {
		defer p.mu.Unlock()
		if pr.frozen {
			return nil
		}
		if err := pr.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			return err
		}
		pr.frozen = true
		fmt.Fprintln(out, "SIGSTOP")
		return nil
	}
	if pr.frozen {
		_ = pr.cmd.Process.Signal(syscall.SIGCONT)
		pr.frozen = false
	}
	p.mu.Unlock()
	_ = pr.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-pr.done:
	case <-time.After(p.StopTimeout):
		_ = pr.cmd.Process.Kill()
		<-pr.done
	}
	fmt.Fprintln(out, "stopped")
	return nil
}

func (p *Process) kill(orbID string) {
	p.mu.Lock()
	pr := p.running(orbID)
	delete(p.procs, orbID)
	p.mu.Unlock()
	if pr == nil {
		return
	}
	_ = pr.cmd.Process.Kill()
	<-pr.done
}

// Close 杀掉全部子进程（测试清理用）。
func (p *Process) Close() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.procs))
	for id := range p.procs {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.kill(id)
	}
}

// copyTree 递归复制目录（保留权限与符号链接）。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // 跳过 socket / fifo 等
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
