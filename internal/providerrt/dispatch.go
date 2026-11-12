package providerrt

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Dispatch 驱动：每个任务执行一次配置的脚本（无参数），输入经环境变量传递，
// 与 Amp 的 dispatch 契约同形。退出码 0 = 成功，stdout / stderr 作为输出。
// 脚本须在 spawn 后于机器内以分离方式启动 `orbd run`。
type Dispatch struct {
	Script string
	// Types 仅用于 hello 声明（复用 docker.types 配置）。
	Types       map[string]config.DockerType
	DefaultType string
	// Image 是 spawn 未指定镜像时的 ORB_IMAGE。
	Image string
}

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
	}
}

// Env 返回任务的 dispatch 环境变量（不含进程继承的环境）。
func (d *Dispatch) Env(j Job) []string {
	var image, snap, typ, token, url string
	if s := j.Spawn; s != nil {
		image, typ, token, url = s.Image, s.Type, s.Env.OrynOrbToken, s.Env.OrynURL
		if s.SnapshotID != nil {
			snap = *s.SnapshotID
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
		"ORB_TYPE=" + typ,
		"ORB_PAUSE_MODE=" + j.PauseMode,
		"ORYN_ORB_TOKEN=" + token,
		"ORYN_URL=" + url,
	}
}

// Exec 实现 Driver。
func (d *Dispatch) Exec(ctx context.Context, j Job, out io.Writer) error {
	cmd := exec.CommandContext(ctx, d.Script)
	cmd.Env = append(os.Environ(), d.Env(j)...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dispatch %s: %w", j.Op, err)
	}
	return nil
}
