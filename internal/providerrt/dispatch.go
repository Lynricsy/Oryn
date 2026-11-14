package providerrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Dispatch 驱动：每个任务执行一次配置的脚本（无参数），输入经环境变量传递，
// 与 Amp 的 dispatch 契约同形。退出码 0 = 成功，stdout / stderr 作为输出。
// 脚本须在 spawn 后于机器内以分离方式启动 `orbd run`；对已存在的机器（解冻 / 启动 / 已在运行），
// 脚本须把 ORYN_ORB_TOKEN 写入机器内 orbd 用户的 `$HOME/.oryn/orb-token`（0600，原子替换）并向 orbd 发 SIGHUP，
// 因为每次 spawn 都会轮换 orb 令牌（见 docs/deploy.md）。
//
// 脚本在独立进程组中运行：任务超时（ctx 结束）时整组 SIGKILL；脚本退出后若仍有后代进程占着输出管道，
// 至多再等 DispatchWaitDelay 就返回（脚本退出码为 0 时仍算成功）。
type Dispatch struct {
	Script string
	// Types 仅用于 hello 声明（复用 docker.types 配置）。
	Types       map[string]config.DockerType
	DefaultType string
	// Image 是 spawn 未指定镜像时的 ORB_IMAGE。
	Image string
	// WaitDelay 覆盖 DispatchWaitDelay（0 = 默认）。
	WaitDelay time.Duration
}

// DispatchWaitDelay 是脚本退出（或被终止）后等待输出管道关闭的上限。
const DispatchWaitDelay = 5 * time.Second

// NewDispatch 创建 dispatch 驱动。
func NewDispatch(script string, dc config.Docker) *Dispatch {
	types := dc.Types
	if len(types) == 0 {
		types = config.DefaultDockerTypes()
	}
	return &Dispatch{Script: script, Types: types, DefaultType: dc.DefaultType, Image: dc.Image}
}

// Info 实现 Driver。
func (d *Dispatch) Info() Info {
	return Info{
		Driver:       apigen.Dispatch,
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true},
		Types:        typeInfos(d.Types),
		DefaultType:  d.DefaultType,
		BaseImageID:  d.baseImageID(),
	}
}

// baseImageID 是 "ref:" + 镜像引用：只跟随标签、不跟随镜像内容（标签指向的内容变了也认不出，
// 靠项目快照的最长复用期兜底）；没有配置镜像时为空。
func (d *Dispatch) baseImageID() string {
	if d.Image == "" {
		return ""
	}
	return "ref:" + d.Image
}

// Env 返回任务的 dispatch 环境变量（不含进程继承的环境）。
// ORB_SNAPSHOT_OPTIONAL=1 时，ORB_SNAPSHOT_ID 对应的快照不存在应退回 ORB_IMAGE 启动（不算失败）；0 时应失败。
func (d *Dispatch) Env(j Job) []string {
	var image, snap, typ, token, url string
	optional := "0"
	if s := j.Spawn; s != nil {
		image, typ, token, url = s.Image, s.Type, s.Env.OrynOrbToken, s.Env.OrynURL
		if s.SnapshotID != nil {
			snap = *s.SnapshotID
		}
		if s.SnapshotOptional != nil && *s.SnapshotOptional {
			optional = "1"
		}
	}
	if image == "" {
		image = d.Image
	}
	if snap == "" {
		snap = j.SnapshotID
	}
	return []string{
		"ORB_OPERATION=" + j.Op,
		"ORB_ID=" + j.OrbID,
		"ORB_IMAGE=" + image,
		"ORB_SNAPSHOT_ID=" + snap,
		"ORB_SNAPSHOT_OPTIONAL=" + optional,
		"ORB_TYPE=" + typ,
		"ORB_PAUSE_MODE=" + j.PauseMode,
		"ORYN_ORB_TOKEN=" + token,
		"ORYN_URL=" + url,
	}
}

// Exec 实现 Driver。dispatch 脚本不上报结构化结果。
func (d *Dispatch) Exec(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	cmd := exec.CommandContext(ctx, d.Script)
	cmd.Env = append(os.Environ(), d.Env(j)...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// 负 pid = 整个进程组：脚本连同它拉起、仍在前台的子孙一起终止。
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = DispatchWaitDelay
	if d.WaitDelay > 0 {
		cmd.WaitDelay = d.WaitDelay
	}
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// 脚本已以 0 退出，只是有后代进程继承了输出管道（例如未重定向输出的后台 orbd）。
		fmt.Fprintln(out, "oryn: dispatch 脚本已退出，但有后代进程仍持有输出管道（不再等待）")
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("dispatch %s: %w（%v）", j.Op, ctx.Err(), err)
		}
		return nil, fmt.Errorf("dispatch %s: %w", j.Op, err)
	}
	return nil, nil
}
