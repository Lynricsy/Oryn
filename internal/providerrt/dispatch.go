package providerrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Dispatch 驱动：每个任务执行一次配置的脚本（无参数），输入经环境变量传递，
// 与 Amp 的 dispatch 契约同形。退出码 0 = 成功，stdout / stderr 作为输出。
// 脚本须在 spawn 后于机器内以分离方式启动 `orbd run`；对已存在的机器（解冻 / 启动 / 已在运行），
// 脚本须把 ORYN_ORB_TOKEN 写入机器内 orbd 用户的 `$HOME/.oryn/orb-token`（0600，原子替换）并向 orbd 发 SIGHUP，
// 因为每次 spawn 都会轮换 orb 令牌（见 docs/deploy.md）。spawn 时脚本应打一行 SnapshotUsedMarker（见下）说明是否真的用了快照。
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
// ORYN_ORB_CA_PEM 是 orb link 的 TLS CA（PEM，可能为空），脚本须原样作为同名环境变量交给机器内的 orbd。
func (d *Dispatch) Env(j Job) []string {
	var image, snap, typ, token, url, caPEM string
	optional := "0"
	if s := j.Spawn; s != nil {
		image, typ, token, url = s.Image, s.Type, s.Env.OrynOrbToken, s.Env.OrynURL
		if s.SnapshotID != nil {
			snap = *s.SnapshotID
		}
		if s.SnapshotOptional != nil && *s.SnapshotOptional {
			optional = "1"
		}
		if s.OrbCaPem != nil {
			caPEM = *s.OrbCaPem
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
		"ORYN_ORB_CA_PEM=" + caPEM,
		"ORYN_URL=" + url,
	}
}

// SnapshotUsedMarker 是 dispatch 脚本在 spawn 输出里报告「是否真的用了快照」的行前缀（S-P1-1）：
// 整行为 `ORYN_SNAPSHOT_USED=1` 或 `ORYN_SNAPSHOT_USED=0`，多行时以最后一行为准；没有这一行即「未知」，不上报 snapshotUsed。
const SnapshotUsedMarker = "ORYN_SNAPSHOT_USED="

// markerScanner 把输出原样转给 w，同时逐行找 SnapshotUsedMarker。只缓存当前行的前 maxLine 字节（更长的行不可能是标记）。
type markerScanner struct {
	w       io.Writer
	line    []byte
	long    bool
	used    *bool
	maxLine int
}

func (m *markerScanner) Write(p []byte) (int, error) {
	for _, c := range p {
		switch {
		case c == '\n':
			m.flush()
		case len(m.line) < m.maxLine:
			m.line = append(m.line, c)
		default:
			m.long = true
		}
	}
	return m.w.Write(p)
}

// flush 结束当前行：是标记行就记下取值。
func (m *markerScanner) flush() {
	if !m.long {
		switch strings.TrimSpace(string(m.line)) {
		case SnapshotUsedMarker + "1":
			m.used = new(true)
		case SnapshotUsedMarker + "0":
			m.used = new(false)
		}
	}
	m.line, m.long = m.line[:0], false
}

// Exec 实现 Driver。spawn 时从输出里解析 SnapshotUsedMarker 行上报 snapshotUsed，其余操作不上报结构化结果。
func (d *Dispatch) Exec(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	scan := &markerScanner{w: out, maxLine: 64}
	cmd := exec.CommandContext(ctx, d.Script)
	cmd.Env = append(os.Environ(), d.Env(j)...)
	// 同一个指针：os/exec 保证同一时刻至多一个 goroutine 调用 Write。
	cmd.Stdout = scan
	cmd.Stderr = scan
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
		err = nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("dispatch %s: %w（%v）", j.Op, ctx.Err(), err)
		}
		return nil, fmt.Errorf("dispatch %s: %w", j.Op, err)
	}
	if j.Op != "spawn" {
		return nil, nil
	}
	scan.flush() // 最后一行可能没有换行
	if scan.used == nil {
		return nil, nil
	}
	return &apigen.JobResult{SnapshotUsed: scan.used}, nil
}
