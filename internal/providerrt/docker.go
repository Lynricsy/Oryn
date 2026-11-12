package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

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

// OrbHome 是 orb 镜像内 orbd 用户的 HOME（镜像约定，见 images/orb/Dockerfile）。
const OrbHome = "/home/user"

// OrbTokenFile 是 orb 内 orbd 优先读取的令牌文件（相对 $HOME）。每次 spawn 都会轮换 orb 令牌，
// 对已存在的容器只能经此文件下发新令牌（容器的环境变量在创建时就固定了）。
const OrbTokenFile = ".oryn/orb-token"

// writeTokenScript 在容器内以 orbd 用户原子替换令牌文件（内容取自 stdin，0600）。
const writeTokenScript = `umask 077 && mkdir -p "$HOME/.oryn" && cat > "$HOME/.oryn/orb-token.tmp" && mv -f "$HOME/.oryn/orb-token.tmp" "$HOME/.oryn/orb-token"`

func (d *Docker) spawn(ctx context.Context, j Job, out io.Writer) error {
	p := j.Spawn
	if p == nil {
		return errors.New("docker: spawn 缺少参数")
	}
	status, exists, err := d.status(ctx, j.OrbID, out)
	if err != nil {
		return err
	}
	if exists {
		switch status {
		case "running", "restarting":
		case "paused":
			if err := d.run(ctx, out, nil, "unpause", j.OrbID); err != nil {
				return err
			}
		case "exited", "created":
			if err := d.run(ctx, out, nil, "start", j.OrbID); err != nil {
				return err
			}
		case "dead":
			if err := d.run(ctx, out, nil, "rm", "-f", j.OrbID); err != nil {
				return err
			}
			exists = false
		default:
			return fmt.Errorf("docker: 容器 %s 处于 %q，无法 spawn", j.OrbID, status)
		}
	}
	if exists {
		// 已有容器：写入本次轮换后的令牌，再让 orbd 立即用它重建 link。
		if err := d.writeToken(ctx, j.OrbID, p.Env.OrynOrbToken, out); err != nil {
			return err
		}
		d.hupOrbd(ctx, j.OrbID, out)
		return nil
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
	args := []string{"create", "--init",
		"--name", j.OrbID, "--hostname", j.OrbID, "--label", "oryn-orb=1",
		"--cpus", strconv.FormatFloat(typ.CPUs, 'f', -1, 64), "--memory", typ.Memory,
		"--shm-size", "1g", "--add-host", "host.docker.internal:host-gateway",
		// agent 在容器内有 root（免密 sudo）：去掉原始套接字与网络配置能力（防 ARP 欺骗 / 改路由），
		// 网络隔离另见 docs/deploy.md「安全」一节。
		"--cap-drop", "NET_RAW", "--cap-drop", "NET_ADMIN",
		"-e", "ORYN_URL", "-e", "ORYN_ORB_ID", "-e", "ORYN_ORB_TOKEN"}
	args = append(args, d.Cfg.ExtraRunArgs...)
	args = append(args, image, "orbd", "run")
	// 令牌经环境变量传给 docker CLI（`-e NAME` 取调用方环境），不出现在 argv 中。
	env := []string{"ORYN_URL=" + p.Env.OrynURL, "ORYN_ORB_ID=" + j.OrbID, "ORYN_ORB_TOKEN=" + p.Env.OrynOrbToken}
	if err := d.run(ctx, out, env, args...); err != nil {
		return err
	}
	// 快照镜像里带着源 orb 的令牌文件，而 orbd 优先读文件：启动前替换成本 orb 的令牌，
	// 否则新容器会以源 orb 的身份连上控制面。
	if err := d.replaceStaleToken(ctx, j.OrbID, p.Env.OrynOrbToken, out); err != nil {
		return err
	}
	return d.run(ctx, out, nil, "start", j.OrbID)
}

// writeToken 在运行中的容器内写入令牌文件（令牌经 stdin 传入，不进 argv 与输出）。
func (d *Docker) writeToken(ctx context.Context, orbID, token string, out io.Writer) error {
	return d.execIn(ctx, strings.NewReader(token), out, out, nil, "exec", "-i", orbID, "sh", "-c", writeTokenScript)
}

// replaceStaleToken 在尚未启动的容器里，若镜像自带令牌文件，则以相同属主与 0600 写入新令牌；没有则什么也不做。
func (d *Docker) replaceStaleToken(ctx context.Context, orbID, token string, out io.Writer) error {
	dir := OrbHome + "/" + path.Dir(OrbTokenFile)
	var archive bytes.Buffer
	err := d.exec(ctx, &archive, io.Discard, nil, "cp", orbID+":"+OrbHome+"/"+OrbTokenFile, "-")
	if isNoSuch(err) {
		return nil
	}
	if err != nil {
		return err
	}
	hdr, err := tar.NewReader(&archive).Next()
	if err != nil {
		return fmt.Errorf("docker: 读取镜像内令牌文件: %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: path.Base(OrbTokenFile), Mode: 0o600, Size: int64(len(token)),
		Uid: hdr.Uid, Gid: hdr.Gid, Uname: hdr.Uname, Gname: hdr.Gname, ModTime: time.Now(),
	}); err != nil {
		return err
	}
	if _, err := io.WriteString(tw, token); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	fmt.Fprintln(out, "oryn: 替换快照中遗留的 orb 令牌文件")
	return d.execIn(ctx, &buf, out, out, nil, "cp", "-a", "-", orbID+":"+dir)
}

// hupOrbd 让 orbd 立即用新令牌重建 link（计划 Assumptions 的 docker pause 预案）；失败只记录。
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
	return d.execIn(ctx, nil, stdout, out, env, args...)
}

// execIn 同 exec，stdin 非空时作为命令的标准输入。
func (d *Docker) execIn(ctx context.Context, stdin io.Reader, stdout, out io.Writer, env []string, args ...string) error {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	fmt.Fprintf(out, "$ docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
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
		strings.Contains(s, "no such image") || strings.Contains(s, "could not find the file")
}
