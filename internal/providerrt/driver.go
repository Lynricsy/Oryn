// Package providerrt 是 orb provider 的运行时（`oryn provider run`）：向控制面 hello、
// 长轮询认领任务、交给驱动执行并上报结果（计划 C1）。驱动有 docker / dispatch / process 三种。
package providerrt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
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
}

// Driver 执行任务。实现必须幂等；out 收集输出（只保留尾部上报）。
type Driver interface {
	Info() Info
	Exec(ctx context.Context, j Job, out io.Writer) error
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
	}
	return nil, fmt.Errorf("未知驱动 %q", cfg.Driver)
}

// typeInfos 把配置中的类型表转为协议形态（按名称排序）。
func typeInfos(types map[string]config.DockerType) []apigen.OrbType {
	out := make([]apigen.OrbType, 0, len(types))
	for name, t := range types {
		out = append(out, apigen.OrbType{Name: name, Cpus: float32(t.CPUs), MemoryMB: memoryMB(t.Memory)})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// memoryMB 把 docker 内存写法（"4g"、"512m"、"1024"=字节）换算为 MiB；无法解析时返回 0。
func memoryMB(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	mult := 1.0 / (1 << 20)
	switch s[len(s)-1] {
	case 'b':
		s = s[:len(s)-1]
	case 'k':
		mult, s = 1.0/1024, s[:len(s)-1]
	case 'm':
		mult, s = 1, s[:len(s)-1]
	case 'g':
		mult, s = 1024, s[:len(s)-1]
	case 't':
		mult, s = 1024*1024, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(v * mult)
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
