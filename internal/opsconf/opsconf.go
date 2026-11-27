// Package opsconf 是控制面运维参数的运行时覆盖层：设置页里改过的值存在 config_overrides 表里，
// 压在 orynd.yaml（基线）之上。优先级：覆盖 > orynd.yaml > 内建缺省。
//
// 可覆盖的键只有 Fields 这一张表；API 解析、持久化与校验都以它为准。
package opsconf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Kind 是可覆盖字段的取值类型。
type Kind uint8

const (
	// KindDurationMs 时长，API 与库里都是毫秒整数。
	KindDurationMs Kind = iota
	// KindGB 容量，GB 整数。
	KindGB
	// KindBool 布尔；Patch 里用 0 / 1。
	KindBool
)

// Field 是一个可覆盖的字段。Key 是 YAML 点路径（也是库里的键）；Section / Name 是 JSON 的两层键。
type Field struct {
	Key, Section, Name string
	Kind               Kind
}

// Fields 是全部可覆盖字段（键表）。
var Fields = []Field{
	{"idle.agent_idle", "idle", "agentIdleMs", KindDurationMs},
	{"idle.user_idle", "idle", "userIdleMs", KindDurationMs},
	{"idle.stop_after_frozen", "idle", "stopAfterFrozenMs", KindDurationMs},
	{"idle.wake_on_focus", "idle", "wakeOnFocus", KindBool},
	{"stats.retention.samples", "statsRetention", "samplesMs", KindDurationMs},
	{"stats.retention.events", "statsRetention", "eventsMs", KindDurationMs},
	{"stats.retention.usage", "statsRetention", "usageMs", KindDurationMs},
	{"repos.max_mirror_gb", "repos", "maxMirrorGb", KindGB},
	{"repos.min_free_gb", "repos", "minFreeGb", KindGB},
	{"repos.max_lfs_cache_gb", "repos", "maxLfsCacheGb", KindGB},
	{"snapshots.max_total_gb", "snapshots", "maxTotalGb", KindGB},
	{"snapshots.max_snapshot_gb", "snapshots", "maxSnapshotGb", KindGB},
}

// 取值上限：时长 100 年，容量 1,000,000 GB。
const (
	maxDurationMs = 3_153_600_000_000
	maxGB         = 1_000_000
)

// ErrInvalid 表示补丁或覆盖不合法；用 errors.Is 判定，错误文本带具体原因。
var ErrInvalid = errors.New("opsconf: invalid")

// Patch 是一次覆盖修改：键 = Field.Key；nil = 删除覆盖；KindBool 用 0 / 1。
type Patch map[string]*int64

// State 是当前的生效值、基线与覆盖情况。
type State struct {
	// Effective 与 YAML 只有键表涉及的字段有意义。
	Effective, YAML config.Orynd
	// Overridden 是被覆盖的键，升序；没有覆盖时为非 nil 的空切片。
	Overridden []string
	// UpdatedAt 是现存覆盖里最近一次写入的毫秒时间；没有覆盖时为 0。
	UpdatedAt int64
}

// override 是一条生效的覆盖。
type override struct {
	value     int64
	updatedAt int64
}

// Conf 持有基线与覆盖，并把生效值广播给订阅者。
type Conf struct {
	st   *store.Store
	base config.Orynd
	cur  atomic.Pointer[config.Orynd]

	mu   sync.Mutex // 保护 over、subs，并串行化 Apply
	over map[string]override
	subs []func(config.Orynd)
}

// New 读出库里的覆盖并叠到 base 上。无效的覆盖（未知键、值越界、叠上后校验不过）会被丢弃并从库里删除，
// 不作为错误返回；只有读写库失败才返回错误。base 本身不校验。
func New(ctx context.Context, st *store.Store, base config.Orynd) (*Conf, error) {
	rows, err := st.Q.ListConfigOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取运维参数覆盖: %w", err)
	}
	kept := map[string]override{}
	var dropped []db.ConfigOverride
	for _, r := range rows {
		f, ok := fieldByKey(r.Key)
		if !ok {
			dropped = append(dropped, r)
			continue
		}
		v, err := parseStored(f, r.Value)
		if err != nil {
			dropped = append(dropped, r)
			continue
		}
		kept[r.Key] = override{value: v, updatedAt: r.UpdatedAt}
	}
	if len(kept) > 0 && apply(base, kept).ValidateOps() != nil {
		// 逐个试：单独叠上就不合法的丢掉；剩下的合起来仍不合法就全部丢掉
		bad := map[string]bool{}
		for _, k := range sortedKeys(kept) {
			if apply(base, map[string]override{k: kept[k]}).ValidateOps() != nil {
				bad[k] = true
			}
		}
		rest := map[string]override{}
		for k, o := range kept {
			if !bad[k] {
				rest[k] = o
			}
		}
		if apply(base, rest).ValidateOps() != nil {
			rest = map[string]override{}
		}
		for _, r := range rows {
			if _, ok := kept[r.Key]; ok {
				if _, still := rest[r.Key]; !still {
					dropped = append(dropped, r)
				}
			}
		}
		kept = rest
	}
	if len(dropped) > 0 {
		for _, r := range dropped {
			slog.Warn("opsconf: 丢弃无效的覆盖", "key", r.Key, "value", r.Value)
		}
		if err := st.Tx(ctx, func(q *db.Queries) error {
			for _, r := range dropped {
				if err := q.DeleteConfigOverride(ctx, r.Key); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("删除无效的运维参数覆盖: %w", err)
		}
	}
	c := &Conf{st: st, base: base, over: kept}
	eff := apply(base, kept)
	c.cur.Store(&eff)
	return c, nil
}

// Current 返回当前生效的配置（基线 + 覆盖）；无锁。
func (c *Conf) Current() config.Orynd { return *c.cur.Load() }

// State 返回生效值、基线与覆盖情况。
func (c *Conf) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

// Subscribe 注册生效值变更的回调：Apply 成功后在持锁状态下按注册顺序同步调用；fn 不得回调 Apply。
func (c *Conf) Subscribe(fn func(config.Orynd)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = append(c.subs, fn)
}

// Apply 套用补丁：叠上后校验不过返回包了 ErrInvalid 的错误，不落库；成功则落库、更新生效值并通知订阅者。
func (c *Conf) Apply(ctx context.Context, p Patch) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := make(map[string]override, len(c.over)+len(p))
	for k, o := range c.over {
		next[k] = o
	}
	now := time.Now().UnixMilli()
	for k, v := range p {
		f, ok := fieldByKey(k)
		if !ok {
			return State{}, fmt.Errorf("%w: 未知的键 %q", ErrInvalid, k)
		}
		if v == nil {
			delete(next, k)
			continue
		}
		if err := checkRange(f, *v); err != nil {
			return State{}, fmt.Errorf("%w: %s %v", ErrInvalid, k, err)
		}
		next[k] = override{value: *v, updatedAt: now}
	}
	cand := apply(c.base, next)
	if err := cand.ValidateOps(); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := c.st.Tx(ctx, func(q *db.Queries) error {
		for _, k := range sortedKeys(p) {
			f, _ := fieldByKey(k)
			if p[k] == nil {
				if err := q.DeleteConfigOverride(ctx, k); err != nil {
					return err
				}
				continue
			}
			if err := q.UpsertConfigOverride(ctx, db.UpsertConfigOverrideParams{
				Key: k, Value: formatStored(f, *p[k]), UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return State{}, fmt.Errorf("保存运维参数覆盖: %w", err)
	}

	old := *c.cur.Load()
	c.over = next
	c.cur.Store(&cand)
	for _, f := range Fields {
		if o, n := get(&old, f), get(&cand, f); o != n {
			slog.Info("opsconf: 运维参数已更新", "key", f.Key, "old", o, "new", n)
		}
	}
	for _, fn := range c.subs {
		fn(cand)
	}
	return c.stateLocked(), nil
}

func (c *Conf) stateLocked() State {
	s := State{Effective: *c.cur.Load(), YAML: c.base, Overridden: sortedKeys(c.over)}
	for _, o := range c.over {
		s.UpdatedAt = max(s.UpdatedAt, o.updatedAt)
	}
	return s
}

// ParsePatch 解析 OpsSettingsPatch 形状的 JSON：未知键、类型不对或越界一律返回包了 ErrInvalid 的错误。
// 生成的结构体分不出「没传」和 null，所以这里自己解析。
func ParsePatch(body []byte) (Patch, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, fmt.Errorf("%w: 请求体必须是 JSON 对象", ErrInvalid)
	}
	p := Patch{}
	for sec, raw := range top {
		if !knownSection(sec) {
			return nil, fmt.Errorf("%w: 未知的键 %s", ErrInvalid, sec)
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil || inner == nil {
			return nil, fmt.Errorf("%w: %s 必须是对象", ErrInvalid, sec)
		}
		for name, raw := range inner {
			path := sec + "." + name
			f, ok := fieldByJSON(sec, name)
			if !ok {
				return nil, fmt.Errorf("%w: 未知的键 %s", ErrInvalid, path)
			}
			v, err := parseJSONValue(f, bytes.TrimSpace(raw))
			if err != nil {
				return nil, fmt.Errorf("%w: %s %v", ErrInvalid, path, err)
			}
			p[f.Key] = v
		}
	}
	return p, nil
}

func parseJSONValue(f Field, raw []byte) (*int64, error) {
	s := string(raw)
	if s == "null" {
		return nil, nil
	}
	var v int64
	if f.Kind == KindBool {
		switch s {
		case "true":
			v = 1
		case "false":
			v = 0
		default:
			return nil, errors.New("必须是 true 或 false")
		}
		return &v, nil
	}
	// ParseInt 拒绝小数、指数、字符串与数组
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, errors.New("必须是整数")
	}
	if err := checkRange(f, v); err != nil {
		return nil, err
	}
	return &v, nil
}

func checkRange(f Field, v int64) error {
	switch f.Kind {
	case KindDurationMs:
		if v < 0 || v > maxDurationMs {
			return fmt.Errorf("必须在 0–%d 之间", int64(maxDurationMs))
		}
	case KindGB:
		if v < 0 || v > maxGB {
			return fmt.Errorf("必须在 0–%d 之间", maxGB)
		}
	case KindBool:
		if v != 0 && v != 1 {
			return errors.New("必须是 0 或 1")
		}
	}
	return nil
}

// parseStored 解析库里的值：数字为十进制文本，布尔为 true / false。
func parseStored(f Field, s string) (int64, error) {
	if f.Kind == KindBool {
		switch s {
		case "true":
			return 1, nil
		case "false":
			return 0, nil
		}
		return 0, fmt.Errorf("无效的布尔值 %q", s)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return v, checkRange(f, v)
}

func formatStored(f Field, v int64) string {
	if f.Kind == KindBool {
		return strconv.FormatBool(v != 0)
	}
	return strconv.FormatInt(v, 10)
}

// apply 返回 base 叠上 over 之后的配置。
func apply(base config.Orynd, over map[string]override) config.Orynd {
	c := base
	for k, o := range over {
		f, _ := fieldByKey(k)
		set(&c, f, o.value)
	}
	return c
}

// ref 返回字段在配置里的地址：*config.Duration、*int 或 *bool。
func ref(c *config.Orynd, key string) any {
	switch key {
	case "idle.agent_idle":
		return &c.Idle.AgentIdle
	case "idle.user_idle":
		return &c.Idle.UserIdle
	case "idle.stop_after_frozen":
		return &c.Idle.StopAfterFrozen
	case "idle.wake_on_focus":
		return &c.Idle.WakeOnFocus
	case "stats.retention.samples":
		return &c.Stats.Retention.Samples
	case "stats.retention.events":
		return &c.Stats.Retention.Events
	case "stats.retention.usage":
		return &c.Stats.Retention.Usage
	case "repos.max_mirror_gb":
		return &c.Repos.MaxMirrorGB
	case "repos.min_free_gb":
		return &c.Repos.MinFreeGB
	case "repos.max_lfs_cache_gb":
		return &c.Repos.MaxLFSCacheGB
	case "snapshots.max_total_gb":
		return &c.Snapshots.MaxTotalGB
	case "snapshots.max_snapshot_gb":
		return &c.Snapshots.MaxSnapshotGB
	}
	panic("opsconf: 键表之外的键 " + key)
}

func get(c *config.Orynd, f Field) int64 {
	switch p := ref(c, f.Key).(type) {
	case *config.Duration:
		return p.D().Milliseconds()
	case *int:
		return int64(*p)
	case *bool:
		if *p {
			return 1
		}
		return 0
	}
	panic("unreachable")
}

func set(c *config.Orynd, f Field, v int64) {
	switch p := ref(c, f.Key).(type) {
	case *config.Duration:
		*p = config.Duration(time.Duration(v) * time.Millisecond)
	case *int:
		*p = int(v)
	case *bool:
		*p = v != 0
	}
}

func fieldByKey(k string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == k {
			return f, true
		}
	}
	return Field{}, false
}

func fieldByJSON(sec, name string) (Field, bool) {
	for _, f := range Fields {
		if f.Section == sec && f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func knownSection(sec string) bool {
	return slices.ContainsFunc(Fields, func(f Field) bool { return f.Section == sec })
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
