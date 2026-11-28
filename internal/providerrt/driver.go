// Package providerrt 是 orb provider 的运行时（`oryn provider run`）：向控制面 hello、
// 长轮询认领任务、交给驱动执行并上报结果（计划 C1）。驱动有 docker / dispatch / process / firecracker 四种。
package providerrt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// Info 是驱动在 hello 中声明的能力。
type Info struct {
	Driver       apigen.ProviderDriver
	Capabilities apigen.ProviderCapabilities
	Types        []apigen.OrbType
	DefaultType  string
	// BaseImageID 是驱动当前基础镜像的身份（M11a）：docker = 镜像 id，process = "process"，
	// dispatch = "ref:<镜像引用>"；空 = 未知（控制面对该 provider 不启用项目快照）。
	BaseImageID string
	// DiskPath 是宿主机存储采样目录（空 = 根目录 "/"）。
	DiskPath string
	// Limits 是驱动配置的显式容量上限（如 Firecracker 的 limits；nil = 无显式上限）。
	Limits *apigen.ProviderHostLimits
}

// Job 是解析后的任务。
type Job struct {
	ID      string
	Op      string
	OrbID   string
	Attempt int
	// Spawn 仅 op=spawn 时非空。
	Spawn *apigen.JobSpawnParams
	// PauseMode 仅 op=pause 时有值（freeze | stop）。
	PauseMode string
	// SnapshotID 用于 snapshot / delete_snapshot。
	SnapshotID string
	// Network 仅 op=set_network 时有值（none | egress，M30）。
	Network string
}

// Driver 执行任务。实现必须幂等；out 收集输出（只保留尾部上报）。
// Exec 成功时可返回结构化结果（随 complete 上报；没有则为 nil）。
type Driver interface {
	Info() Info
	Exec(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error)
}

// Preflighter 是驱动可选实现的启动前检查：`oryn provider run` 在 Runner.Run 之前调用，出错即退出。
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// OrbDiskSizer 是驱动可选实现的 orb 可写层/可写盘大小统计接口（随心跳采样上报）。
// totalBytes 为全部 orb 可写层/盘之和（Docker 为逻辑 SizeRw，Firecracker 为 st_blocks*512 可能含 reflink 共享块），
// 仅作独立参考读数，不应用于从宿主机 statfs 已用空间中扣减。
type OrbDiskSizer interface {
	OrbDiskUsage(ctx context.Context) (totalBytes int64, byOrb map[string]int64, ok bool)
}

// ParseJob 把协议任务按 op 解析为 Job。
func ParseJob(pj apigen.ProviderJob) (Job, error) {
	j := Job{ID: pj.ID, Op: string(pj.Op), OrbID: pj.OrbID, Attempt: pj.Attempt}
	raw, err := json.Marshal(pj.Params)
	if err != nil {
		return j, err
	}
	switch pj.Op {
	case apigen.Spawn:
		var p apigen.JobSpawnParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return j, fmt.Errorf("spawn params: %w", err)
		}
		j.Spawn = &p
	case apigen.Pause:
		var p apigen.JobPauseParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return j, fmt.Errorf("pause params: %w", err)
		}
		if p.Mode != apigen.Freeze && p.Mode != apigen.Stop {
			return j, fmt.Errorf("pause mode %q 无效", p.Mode)
		}
		j.PauseMode = string(p.Mode)
	case apigen.Snapshot, apigen.DeleteSnapshot:
		var p apigen.JobSnapshotParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return j, fmt.Errorf("%s params: %w", pj.Op, err)
		}
		if p.SnapshotID == "" {
			return j, fmt.Errorf("%s: snapshotId 必填", pj.Op)
		}
		j.SnapshotID = p.SnapshotID
	case apigen.SetNetwork:
		var p apigen.JobNetworkParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return j, fmt.Errorf("set_network params: %w", err)
		}
		if p.Network != apigen.SandboxNetworkNone && p.Network != apigen.SandboxNetworkEgress {
			return j, fmt.Errorf("set_network network %q 无效", p.Network)
		}
		j.Network = string(p.Network)
	case apigen.Delete:
	default:
		return j, fmt.Errorf("未知 op %q", pj.Op)
	}
	if j.OrbID == "" && pj.Op != apigen.DeleteSnapshot {
		return j, fmt.Errorf("%s: orbId 必填", pj.Op)
	}
	return j, nil
}

// New 按配置创建驱动。
func New(cfg config.Provider) (Driver, error) {
	switch cfg.Driver {
	case "docker":
		return NewDocker(cfg.Docker), nil
	case "dispatch":
		return NewDispatch(cfg.Dispatch.Script, cfg.Docker), nil
	case "process":
		return NewProcess("", "", cfg.Docker), nil
	case "firecracker":
		return NewFirecracker(cfg.Firecracker, cfg.OrbURL), nil
	}
	return nil, fmt.Errorf("未知驱动 %q", cfg.Driver)
}

// typeInfos 把配置中的类型表转为协议形态（按名称排序）。nested_docker 的类型带特性 "docker"（M11a）；
// cpus / memoryMB 为 0 表示不限。
func typeInfos(types map[string]config.DockerType) []apigen.OrbType {
	out := make([]apigen.OrbType, 0, len(types))
	for name, t := range types {
		ot := apigen.OrbType{Name: name, Cpus: float32(t.CPUs), MemoryMB: t.MemoryMB()}
		if t.NestedDocker {
			ot.Features = []string{"docker"}
		}
		out = append(out, ot)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// tailBuffer 是只保留最后 limit 字节的并发安全 Writer。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - 2*b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf
	if len(s) > b.limit {
		s = s[len(s)-b.limit:]
	}
	return strings.ToValidUTF8(string(s), "")
}
