package providerrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Docker 驱动通过 `docker` CLI（os/exec）管理 orb 容器（计划 C1 表中的命令）。
type Docker struct {
	Cfg config.Docker
	// Bin 是 docker 可执行文件，默认 "docker"（按 PATH 查找）。
	Bin string
}

// NewDocker 创建 docker 驱动。
func NewDocker(cfg config.Docker) *Docker {
	if len(cfg.Types) == 0 {
		cfg.Types = config.DefaultDockerTypes()
	}
	return &Docker{Cfg: cfg, Bin: "docker"}
}

// Info 实现 Driver。
func (d *Docker) Info() Info {
	return Info{
		Driver:       apigen.Docker,
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true},
		Types:        typeInfos(d.Cfg.Types),
		DefaultType:  d.Cfg.DefaultType,
	}
}

// Exec 实现 Driver。
func (d *Docker) Exec(ctx context.Context, j Job, out io.Writer) error {
	switch j.Op {
	case "spawn":
		return d.spawn(ctx, j, out)
	case "pause":
		return d.pause(ctx, j, out)
	case "snapshot":
		exists, err := d.imageExists(ctx, j.SnapshotID, out)
		if err != nil || exists {
			return err
		}
		return d.run(ctx, out, nil, "commit", j.OrbID, j.SnapshotID)
	case "delete":
		err := d.run(ctx, out, nil, "rm", "-f", j.OrbID)
		if isNoSuch(err) {
			return nil
		}
		return err
	case "delete_snapshot":
		err := d.run(ctx, out, nil, "rmi", j.SnapshotID)
		if isNoSuch(err) {
			return nil
		}
		return err
	}
	return fmt.Errorf("docker: 不支持的操作 %q", j.Op)
}

func (d *Docker) spawn(ctx context.Context, j Job, out io.Writer) error {
	status, exists, err := d.status(ctx, j.OrbID, out)
	if err != nil {
		return err
	}
	if exists {
		switch status {
		case "running", "restarting":
			return nil
		case "paused":
			if err := d.run(ctx, out, nil, "unpause", j.OrbID); err != nil {
				return err
			}
			d.hupOrbd(ctx, j.OrbID, out)
			return nil
		case "exited", "created":
			return d.run(ctx, out, nil, "start", j.OrbID)
		case "dead":
			if err := d.run(ctx, out, nil, "rm", "-f", j.OrbID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("docker: 容器 %s 处于 %q，无法 spawn", j.OrbID, status)
		}
	}
	p := j.Spawn
	if p == nil {
		return errors.New("docker: spawn 缺少参数")
	}
	typ, ok := d.Cfg.Types[p.Type]
	if !ok {
		typ = d.Cfg.Types[d.Cfg.DefaultType]
	}
	image := d.Cfg.Image
	if p.Image != "" {
		image = p.Image
	}
	if p.SnapshotID != nil && *p.SnapshotID != "" {
		image = *p.SnapshotID
	}
	args := []string{"run", "-d", "--init",
		"--name", j.OrbID, "--hostname", j.OrbID, "--label", "oryn-orb=1",
		"--cpus", strconv.FormatFloat(typ.CPUs, 'f', -1, 64), "--memory", typ.Memory,
		"--shm-size", "1g", "--add-host", "host.docker.internal:host-gateway",
		"-e", "ORYN_URL", "-e", "ORYN_ORB_ID", "-e", "ORYN_ORB_TOKEN"}
	args = append(args, d.Cfg.ExtraRunArgs...)
	args = append(args, image, "orbd", "run")
	// 令牌经环境变量传给 docker CLI（`-e NAME` 取调用方环境），不出现在 argv 中。
	env := []string{"ORYN_URL=" + p.Env.OrynURL, "ORYN_ORB_ID=" + j.OrbID, "ORYN_ORB_TOKEN=" + p.Env.OrynOrbToken}
	return d.run(ctx, out, env, args...)
}

// hupOrbd 让解冻后的 orbd 立即重建 link（计划 Assumptions 的 docker pause 预案）；失败只记录。
func (d *Docker) hupOrbd(ctx context.Context, orbID string, out io.Writer) {
	if err := d.run(ctx, out, nil, "exec", orbID, "pkill", "-HUP", "-x", "orbd"); err != nil {
		fmt.Fprintf(out, "oryn: 通知 orbd 重连失败（忽略）: %v\n", err)
	}
}

func (d *Docker) pause(ctx context.Context, j Job, out io.Writer) error {
	status, exists, err := d.status(ctx, j.OrbID, out)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("docker: 容器 %s 不存在", j.OrbID)
	}
	if j.PauseMode == "freeze" {
		if status != "running" && status != "restarting" {
			return nil
		}
		return d.run(ctx, out, nil, "pause", j.OrbID)
	}
	switch status {
	case "exited", "created", "dead":
		return nil
	case "paused":
		if err := d.run(ctx, out, nil, "unpause", j.OrbID); err != nil {
			return err
		}
	}
	return d.run(ctx, out, nil, "stop", "-t", "30", j.OrbID)
}

// status 返回容器状态（created / running / paused / restarting / exited / dead …）。
func (d *Docker) status(ctx context.Context, orbID string, out io.Writer) (string, bool, error) {
	var stdout bytes.Buffer
	err := d.exec(ctx, &stdout, out, nil, "container", "inspect", "--format", "{{.State.Status}}", orbID)
	if isNoSuch(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(stdout.String()), true, nil
}

func (d *Docker) imageExists(ctx context.Context, image string, out io.Writer) (bool, error) {
	err := d.exec(ctx, io.Discard, out, nil, "image", "inspect", "--format", "{{.Id}}", image)
	if isNoSuch(err) {
		return false, nil
	}
	return err == nil, err
}

func (d *Docker) run(ctx context.Context, out io.Writer, env []string, args ...string) error {
	return d.exec(ctx, out, out, env, args...)
}

// cmdError 是 docker 命令的非零退出，带 stderr 便于识别 "No such …"。
type cmdError struct {
	args   []string
	err    error
	stderr string
}

func (e *cmdError) Error() string {
	return fmt.Sprintf("docker %s: %v: %s", strings.Join(e.args, " "), e.err, strings.TrimSpace(e.stderr))
}

func (d *Docker) exec(ctx context.Context, stdout, out io.Writer, env []string, args ...string) error {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	fmt.Fprintf(out, "$ docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(&stderr, out)
	if err := cmd.Run(); err != nil {
		return &cmdError{args: args, err: err, stderr: stderr.String()}
	}
	return nil
}

// isNoSuch 判断错误是否为「对象不存在」。
func isNoSuch(err error) bool {
	var ce *cmdError
	if !errors.As(err, &ce) {
		return false
	}
	s := strings.ToLower(ce.stderr)
	return strings.Contains(s, "no such container") || strings.Contains(s, "no such object") ||
		strings.Contains(s, "no such image")
}
