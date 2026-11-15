package threads

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/modelcat"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// M13 计划 6.3：omp 配置下发后的会话级生效。orbs 每下发一层配置就把 ApplyReport 交给 ConfigApplied，这里按
// 「config.yml 键级差分 + 其余文件按路径」分类出这次变化对会话的作用，再作用到 agent 在跑的线程：
//   - reload：会话静止时发 reload_plugins（忙的线程在 session_settled 时补发）；
//   - restart：把线程标为需重启并记下原因，由 apply-status 列出；RestartSessions 让它们在静止后 agent.stop，
//     下一条命令经唤醒路径以 open_session 拉起原会话。
// agent 退出（含主动停止）时清掉这些标记：新进程启动时读的就是新配置。标记只在内存里，orynd 重启后不保留。

// configSchemaTimeout 是分类时等设置 schema 的上限（首次需要运行一次 omp）。
const configSchemaTimeout = 40 * time.Second

// ConfigSchema 提供 omp 的设置 schema（*modelcat.Catalog 实现），用来判断 config.yml 的键是否要重启会话。
type ConfigSchema interface {
	SettingsSchema(ctx context.Context) (modelcat.SettingsSchema, error)
}

// SetConfigSchema 设置设置 schema 来源；须在服务对外可用之前调用。未设置（或取不到）时 config.yml 里
// disabledExtensions、task.* 之外的键一律按需重启处理。
func (s *Service) SetConfigSchema(c ConfigSchema) { s.cfgSchema = c }

// configEffect 是一次配置变化对会话的作用。
type configEffect struct {
	reload  bool
	restart []string // 需重启的原因（条目、键名或路径），去重、保持出现顺序
}

func (e *configEffect) addRestart(reason string) {
	if !slices.Contains(e.restart, reason) {
		e.restart = append(e.restart, reason)
	}
}

func (e configEffect) empty() bool { return !e.reload && len(e.restart) == 0 }

// disabledReloadKinds / disabledRestartKinds 是 disabledExtensions 条目前缀（冒号前）的分类。
var (
	disabledReloadKinds = map[string]bool{
		"skill": true, "slash-command": true, "rule": true, "prompt": true, "instruction": true, "context-file": true, "mcp": true,
	}
	disabledRestartKinds = map[string]bool{"hook": true, "extension-module": true, "tool": true}
)

// reloadDirs / restartDirs 是按目录分类的文件；reloadFiles / restartFiles 是按文件名分类的根文件。
var (
	reloadDirs   = []string{"skills/", "commands/", "rules/", "prompts/", "instructions/"}
	restartDirs  = []string{"hooks/", "extensions/", "tools/"}
	reloadFiles  = map[string]bool{"AGENTS.md": true, "RULES.md": true, "mcp.json": true}
	restartFiles = map[string]bool{"SYSTEM.md": true, "APPEND_SYSTEM.md": true, "PERSONALITY.md": true, agentcfg.ModelsFile: true}
)

const configFile = "config.yml"

// configEffectOf 按计划 6.3 分类一次配置变化。coordinator 为真时按协调宿主处理：它只收到 config.yml 与 models.yml，
// 且不加载技能 / 扩展，disabledExtensions 与其余文件都与它无关。restartKey 为 nil 表示没有设置 schema。
func configEffectOf(changed []string, before, after []byte, coordinator bool, restartKey func(string) bool) configEffect {
	var e configEffect
	for _, p := range changed {
		switch {
		case p == configFile:
			configYmlEffect(&e, before, after, coordinator, restartKey)
		case coordinator:
			if p == agentcfg.ModelsFile {
				e.addRestart(p)
			}
		case strings.HasPrefix(p, "agents/"):
			// 子 agent 定义：task 发起前重新读取
		case reloadFiles[p] || hasAnyPrefix(p, reloadDirs):
			e.reload = true
		case restartFiles[p] || hasAnyPrefix(p, restartDirs):
			e.addRestart(p)
		default:
			// 其它文件（TITLE_SYSTEM.md 等）：不确定 omp 何时读取，按需重启处理
			e.addRestart(p)
		}
	}
	return e
}

func hasAnyPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// configYmlEffect 对 config.yml 做键级差分：disabledExtensions 按增删的条目前缀分类；task.* 不处理；其余键按设置
// schema 的 restartRequired 决定（不需要重启的由 omp 的文件监听生效）。解析失败时整份按需重启处理。
func configYmlEffect(e *configEffect, before, after []byte, coordinator bool, restartKey func(string) bool) {
	b, errB := parseConfigYml(before)
	a, errA := parseConfigYml(after)
	if errB != nil || errA != nil {
		e.addRestart(configFile)
		return
	}
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "disabledExtensions":
			if !coordinator {
				disabledEffect(e, b[k], a[k])
			}
		case "task":
		default:
			for _, path := range diffPaths(b[k], a[k], k) {
				if restartKey == nil || restartKey(path) {
					e.addRestart(path)
				}
			}
		}
	}
}

// disabledEffect 比较 disabledExtensions 前后两个集合，按增删条目的前缀分类；前缀不认识的条目按需重启处理。
func disabledEffect(e *configEffect, before, after any) {
	b, a := stringSet(before), stringSet(after)
	var diff []string
	for id := range a {
		if !b[id] {
			diff = append(diff, id)
		}
	}
	for id := range b {
		if !a[id] {
			diff = append(diff, id)
		}
	}
	sort.Strings(diff)
	for _, id := range diff {
		kind, _, _ := strings.Cut(id, ":")
		switch {
		case disabledReloadKinds[kind]:
			e.reload = true
		case disabledRestartKinds[kind]:
			e.addRestart(id)
		default:
			// 不认识的前缀：不确定 omp 何时读取，按需重启处理
			e.addRestart(id)
		}
	}
}

// parseConfigYml 解析 config.yml 的顶层映射；空内容（或文件不存在）为空映射。
func parseConfigYml(b []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(b) == 0 {
		return m, nil
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// diffPaths 返回 a、b 之间不同的点分路径：两边都是映射时逐键比较，否则整体比较（数组按整体算一处变化）。
func diffPaths(a, b any, path string) []string {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		if reflect.DeepEqual(a, b) {
			return nil
		}
		return []string{path}
	}
	keys := make([]string, 0, len(am)+len(bm))
	for k := range am {
		keys = append(keys, k)
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, diffPaths(am[k], bm[k], path+"."+k)...)
	}
	return out
}

func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	list, _ := v.([]any)
	for _, x := range list {
		if s, ok := x.(string); ok {
			out[s] = true
		}
	}
	return out
}

// restartKeyFunc 返回设置 schema 的 restartRequired 判定；取不到 schema 时返回 nil。
func (s *Service) restartKeyFunc(ctx context.Context) func(string) bool {
	if s.cfgSchema == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, configSchemaTimeout)
	defer cancel()
	schema, err := s.cfgSchema.SettingsSchema(cctx)
	if err != nil {
		slog.Warn("threads: 取 omp 设置 schema 失败，config.yml 的变化按需重启处理", "err", err)
		return nil
	}
	return schema.RestartRequired
}

// ConfigApplied 处理一次 omp 配置下发（orbs.OnOmpApplied）：分类后作用到 agent 在跑的线程。全局层作用于全部线程
// （协调对话只看 config.yml 与 models.yml）；项目层只作用于该项目 orb 上的工作线程。下发失败、或 ready 却没收到
// 下发（orbd 不支持）的 orb 上的线程不处理——它们的配置文件没变；冻结 / 停机等 orb 会在恢复时同步，照常处理。
func (s *Service) ConfigApplied(ctx context.Context, rep orbs.ApplyReport) {
	if len(rep.Changed) == 0 {
		return
	}
	var restartKey func(string) bool
	if slices.Contains(rep.Changed, configFile) {
		restartKey = s.restartKeyFunc(ctx)
	}
	work := configEffectOf(rep.Changed, rep.ConfigBefore, rep.ConfigAfter, false, restartKey)
	var coord configEffect
	if rep.ProjectID == "" {
		coord = configEffectOf(rep.Changed, rep.ConfigBefore, rep.ConfigAfter, true, restartKey)
	}
	if work.empty() && coord.empty() {
		return
	}
	applied := make(map[string]bool, len(rep.Orbs)) // orb → 是否下发成功
	for _, o := range rep.Orbs {
		applied[o.OrbID] = o.OK
	}
	reached := func(orbID string) bool {
		if ok, sent := applied[orbID]; sent {
			return ok
		}
		return s.orbState(orbID) != orbStateReady
	}
	projectOf := map[string]string{}
	inProject := func(orbID string) bool {
		p, ok := projectOf[orbID]
		if !ok {
			if orb, err := s.st.Q.GetOrb(ctx, orbID); err == nil && orb.ProjectID != nil {
				p = *orb.ProjectID
			}
			projectOf[orbID] = p
		}
		return p == rep.ProjectID
	}
	for _, t := range s.loadedThreads() {
		t.mu.Lock()
		kind, orbID, alive := t.kind, t.orbID, t.agentAlive()
		t.mu.Unlock()
		if !alive || orbID == "" {
			continue
		}
		eff := work
		if kind != KindWork {
			eff = coord
		}
		if eff.empty() || (rep.ProjectID != "" && !inProject(orbID)) || !reached(orbID) {
			continue
		}
		t.mu.Lock()
		s.applyConfigEffectLocked(t, eff)
		t.mu.Unlock()
	}
}

// loadedThreads 返回已加载到内存的线程（agent 在跑的线程必然已加载）。
func (s *Service) loadedThreads() []*thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		out = append(out, t)
	}
	return out
}

// agentAlive 报告线程的 agent 进程是否可能在跑（含正在启动）。调用方持有 t.mu。
func (t *thread) agentAlive() bool {
	return t.agent != nil || t.starting || t.agentStatus == apigen.AgentStatusReady || t.agentStatus == apigen.AgentStatusStarting
}

// applyConfigEffectLocked 把分类结果作用到线程。调用方持有 t.mu。
func (s *Service) applyConfigEffectLocked(t *thread, e configEffect) {
	for _, r := range e.restart {
		if !slices.Contains(t.restartReasons, r) {
			t.restartReasons = append(t.restartReasons, r)
		}
	}
	if e.reload {
		t.reloadPending = true
		s.reloadPluginsLocked(t)
	}
}

// reloadPluginsLocked 在会话静止、agent 可接收命令时补发挂起的 reload_plugins；否则留到下一次 session_settled。
// 即将被停掉（refresh / stopping）的不发：新进程本来就读新配置。调用方持有 t.mu。
func (s *Service) reloadPluginsLocked(t *thread) {
	if !t.reloadPending || t.refresh || t.stopping || !t.settled || t.unsynced || !t.agentReady() || orbSuspended(s.orbState(t.orbID)) {
		return
	}
	t.reloadPending = false
	frame := []byte(`{"type":"reload_plugins"}`)
	err := s.enqueueLocked(t, frame, &pendingCmd{cmdType: "reload_plugins", after: func(t *thread, env omprpc.Envelope, _ json.RawMessage) {
		if undelivered(env) {
			// 进程在命令到达前退出：新进程读的就是新配置，不必重发
			return
		}
		if !env.OK() {
			slog.Warn("threads: reload_plugins 失败", "thread", t.id, "err", env.ErrorText())
		}
	}})
	if err != nil {
		t.reloadPending = true
	}
}

// resetConfigMarksLocked 在 agent 退出时清掉配置标记：下一个进程启动时读的就是新配置。调用方持有 t.mu。
func resetConfigMarksLocked(t *thread) {
	t.reloadPending = false
	t.restartReasons = nil
}

// RestartNeed 是一个需重启才能让配置生效的线程。
type RestartNeed struct {
	ThreadID, Kind, Title string
	// Reasons 是需重启的原因：disabledExtensions 条目、config.yml 键名或文件路径。
	Reasons []string
	// Scheduled 为真表示已安排重启（RestartSessions），等会话静止。
	Scheduled bool
}

// ConfigApplyStatus 是 omp 配置在会话上的生效状态。
type ConfigApplyStatus struct {
	// Restart 是需重启的线程，按线程 id 排序。
	Restart []RestartNeed
	// PendingReloads 是还在等会话静止才能 reload_plugins 的线程数。
	PendingReloads int
}

// ApplyStatus 返回 omp 配置在会话上的生效状态（GET agent-config/apply-status）。
func (s *Service) ApplyStatus(ctx context.Context) ConfigApplyStatus {
	out := ConfigApplyStatus{Restart: []RestartNeed{}}
	for _, t := range s.loadedThreads() {
		t.mu.Lock()
		need := RestartNeed{ThreadID: t.id, Kind: t.kind, Reasons: slices.Clone(t.restartReasons), Scheduled: t.refresh || t.stopping}
		if t.reloadPending {
			out.PendingReloads++
		}
		t.mu.Unlock()
		if len(need.Reasons) == 0 {
			continue
		}
		if row, err := s.st.Q.GetThread(ctx, t.id); err == nil {
			need.Title = row.Title
		}
		out.Restart = append(out.Restart, need)
	}
	sort.Slice(out.Restart, func(i, j int) bool { return out.Restart[i].ThreadID < out.Restart[j].ThreadID })
	return out
}

// RestartSessions 让所有被标为需重启的线程在静止后停掉 agent（正在跑的回合不受影响），下一条命令经唤醒路径以
// open_session 拉起原会话。返回已安排重启的线程 id（按 id 排序）；agent 已不在运行的线程直接清掉标记。
func (s *Service) RestartSessions(ctx context.Context) []string {
	out := []string{}
	for _, t := range s.loadedThreads() {
		t.mu.Lock()
		if len(t.restartReasons) > 0 {
			if s.refreshLocked(t, "omp 配置需重启："+strings.Join(t.restartReasons, ", ")) {
				out = append(out, t.id)
			} else {
				resetConfigMarksLocked(t)
			}
		}
		t.mu.Unlock()
	}
	sort.Strings(out)
	return out
}
