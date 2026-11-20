package orbs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M13 计划 6.2：omp 配置立即下发。agent-config（全局 omp-home 或项目层）写入后防抖 1 秒，向每台 ready 且 hello 含
// omphome-apply 的 orb 发 omphome.apply（参数取自与 setup 相同的 Plan，协调宿主因此只拿到 coordinatorFiles），成功后
// 用同一份 Plan 刷新该 orb 的 egress 路由。冻结 / 停机的 orb 跳过，它们在 resume 时照常同步。每次下发后记下这一层
// 各文件的摘要与 config.yml 原文（<Dir>/manifest.json、<Dir>/config.yml；项目层在 <Dir>/projects/<id>/ 下），
// 下一次据此得出 Changed 与 ConfigBefore，交给 OnOmpApplied 的订阅方（threads 据此决定 reload / 需重启）。

// FeatureOmpHomeApply 是 orbd 在 hello 里上报的「支持 omphome.apply」特性。
const FeatureOmpHomeApply = "omphome-apply"

const (
	// ompApplyDebounce 是 agent-config 写入后到下发的防抖时长。
	ompApplyDebounce = time.Second
	// ompApplyTimeout 是单台 orb 的 omphome.apply 应答上限。
	ompApplyTimeout = 30 * time.Second
	// ompApplyRunTimeout 是一次下发（读配置、各 orb 并发下发、通知订阅方）的总上限。
	ompApplyRunTimeout = 2 * time.Minute
	appliedConfigFile  = "config.yml"
	appliedManifest    = "manifest.json"
)

// OmpApplyConfig 是立即下发的依赖。
type OmpApplyConfig struct {
	// Home 是全局 omp-home（读原始文件计算 Changed）。
	Home Home
	// Projects 是项目层；nil = 不支持项目层下发。
	Projects ProjectConfigs
	// Dir 是已下发记录的目录（<data_dir>/omp-home-applied）。
	Dir string
	// Debounce 是防抖时长；0 = 1 秒。
	Debounce time.Duration
	// SSHState 返回一层（"" = 全局）SSH 主机的摘要（M18）；nil = 没有 SSH。它以 ssh.json 之名并进这一层的文件参与比较，
	// 不会被下发：主机一变，这一层的 Changed 里就有 ssh.json，线程据此 reload_plugins。
	SSHState func(projectID string) ([]byte, error)
}

// OrbApply 是一台 orb 的下发结果。
type OrbApply struct {
	OrbID string
	OK    bool
	Err   string
}

// ApplyReport 是一次下发的结果。
type ApplyReport struct {
	// ProjectID 非空 = 这次下发的是该项目的项目层（只发给该项目的 orb）；空 = 全局 omp-home。
	ProjectID string
	// Orbs 是收到 omphome.apply 的 orb（ready 且带 omphome-apply 特性）。
	Orbs []OrbApply
	// Changed 是这一层相对上一次下发变化（新增、修改、删除）的文件路径，按字典序。
	Changed []string
	// ConfigBefore 是上一次下发的 config.yml（没有则为 nil）；ConfigAfter 是本次下发的 config.yml（没有则为 nil）。
	ConfigBefore, ConfigAfter []byte
}

// ompApplier 是立即下发的状态。
type ompApplier struct {
	cfg OmpApplyConfig

	// runMu 串行化下发：后一次一定读到更新的配置，并在前一次之后到达 orbd。
	runMu sync.Mutex

	mu     sync.Mutex
	timers map[string]*time.Timer // 层（"" = 全局，否则项目 id）→ 防抖定时器
	subs   []func(ApplyReport)
	// requested / applied：每层已接受的写入次数与已下发完成时覆盖到的次数；requested > applied 即「待下发」。
	requested, applied map[string]uint64
	last               map[string]OmpApplyState // 每层最近一次下发的结果
}

// OmpApplyState 是一层 omp 配置的下发进度（apply-status 的 push）。
type OmpApplyState struct {
	// Pending 为真：有已接受（ScheduleOmpApply）但尚未下发完成的写入。
	Pending bool
	// AppliedAt 是最近一次下发完成的时间；零值 = 还没下发过。
	AppliedAt time.Time
	// Failed 是最近一次下发里失败的 orb。
	Failed []OrbApply
	// Err 是最近一次下发整体失败的原因。
	Err string
}

// OmpApplyState 返回一层（projectID 空 = 全局）的下发进度。写入接口在应答前调用 ScheduleOmpApply，
// 所以写入成功后读到 Pending=false 即表示这次写入已下发、订阅方（会话级分类）也已处理完。
func (s *Service) OmpApplyState(projectID string) OmpApplyState {
	a := s.ompApplier()
	if a == nil {
		return OmpApplyState{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.last[projectID]
	st.Failed = slices.Clone(st.Failed)
	st.Pending = a.requested[projectID] > a.applied[projectID]
	return st
}

// EnableOmpApply 开启立即下发；须在 API 对外服务之前调用。全局层还没有下发记录时以当前 omp-home 为基线
// （此前运行中的 orb 都是按它 setup 的），免得第一次下发把所有文件都算作变化。
func (s *Service) EnableOmpApply(cfg OmpApplyConfig) error {
	if cfg.Debounce <= 0 {
		cfg.Debounce = ompApplyDebounce
	}
	a := &ompApplier{cfg: cfg, timers: map[string]*time.Timer{},
		requested: map[string]uint64{}, applied: map[string]uint64{}, last: map[string]OmpApplyState{}}
	if _, err := os.Stat(filepath.Join(a.layerDir(""), appliedManifest)); errors.Is(err, os.ErrNotExist) {
		files, err := a.load("")
		if err != nil {
			return err
		}
		if err := a.save("", files); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.omp = a
	s.mu.Unlock()
	return nil
}

// OnOmpApplied 注册下发完成的回调（在下发的后台协程里同步调用）。
func (s *Service) OnOmpApplied(fn func(ApplyReport)) {
	if a := s.ompApplier(); a != nil {
		a.mu.Lock()
		a.subs = append(a.subs, fn)
		a.mu.Unlock()
	}
}

func (s *Service) ompApplier() *ompApplier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.omp
}

// ScheduleOmpApply 在防抖时长之后下发一层配置（projectID 空 = 全局 omp-home）；期间再次调用会重新计时。未开启时不做事。
func (s *Service) ScheduleOmpApply(projectID string) {
	a := s.ompApplier()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requested[projectID]++
	if t := a.timers[projectID]; t != nil {
		t.Stop()
	}
	a.timers[projectID] = time.AfterFunc(a.cfg.Debounce, func() {
		a.mu.Lock()
		delete(a.timers, projectID)
		a.mu.Unlock()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.bg.Add(1) // 与 Close 设置 closed 同在 s.mu 下：Close 之后不再登记
		s.mu.Unlock()
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), ompApplyRunTimeout)
		defer cancel()
		var err error
		if projectID == "" {
			_, err = s.ApplyOmpHome(ctx)
		} else {
			_, err = s.ApplyProjectConfig(ctx, projectID)
		}
		if err != nil {
			slog.Warn("orbs: 下发 omp 配置失败", "project", projectID, "err", err)
		}
	})
}

// ApplyOmpHome 立即把全局 omp-home 下发到所有 ready 且支持 omphome-apply 的 orb，并通知订阅方。
func (s *Service) ApplyOmpHome(ctx context.Context) (ApplyReport, error) {
	return s.applyLayer(ctx, "")
}

// ApplyProjectConfig 立即把项目层下发到该项目所有 ready 且支持 omphome-apply 的 orb，并通知订阅方。
func (s *Service) ApplyProjectConfig(ctx context.Context, projectID string) (ApplyReport, error) {
	if projectID == "" {
		return ApplyReport{}, errors.New("orbs: 项目 id 为空")
	}
	return s.applyLayer(ctx, projectID)
}

func (s *Service) applyLayer(ctx context.Context, projectID string) (ApplyReport, error) {
	a := s.ompApplier()
	if a == nil {
		return ApplyReport{}, errors.New("orbs: 未开启 omp 配置立即下发")
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	// 这次下发读到的配置覆盖此刻之前接受的全部写入：完成（含失败）后把它们记为已下发
	a.mu.Lock()
	covers := a.requested[projectID]
	a.mu.Unlock()
	rep, err := s.applyLayerLocked(ctx, a, projectID)
	st := OmpApplyState{AppliedAt: time.Now()}
	if err != nil {
		st.Err = err.Error()
	}
	for _, o := range rep.Orbs {
		if !o.OK {
			st.Failed = append(st.Failed, o)
		}
	}
	a.mu.Lock()
	a.applied[projectID] = max(a.applied[projectID], covers)
	a.last[projectID] = st
	a.mu.Unlock()
	return rep, err
}

// applyLayerLocked 是 applyLayer 的主体，调用方持有 runMu。订阅方（会话级分类）在它返回前处理完，
// 下发进度随后才记为完成，所以读到 Pending=false 时 apply-status 的会话部分已经落定。
func (s *Service) applyLayerLocked(ctx context.Context, a *ompApplier, projectID string) (ApplyReport, error) {
	if s.setup == nil {
		return ApplyReport{}, errors.New("orbs: 没有 setup 来源")
	}
	files, err := a.load(projectID)
	if err != nil {
		return ApplyReport{}, err
	}
	before, prev := a.previous(projectID)
	rep := ApplyReport{ProjectID: projectID, Changed: changedPaths(prev, digests(files)), ConfigBefore: before}
	if b, ok := files[appliedConfigFile]; ok {
		rep.ConfigAfter = b
	}
	targets, err := s.applyTargets(ctx, projectID)
	if err != nil {
		return ApplyReport{}, err
	}
	rep.Orbs = make([]OrbApply, len(targets))
	var wg sync.WaitGroup
	for i, orb := range targets {
		wg.Go(func() { rep.Orbs[i] = s.applyOrb(ctx, orb) })
	}
	wg.Wait()
	// 记录这次下发的内容：个别 orb 失败不影响基线（它们在下次 setup / resume 时拿到最新配置）
	if err := a.save(projectID, files); err != nil {
		slog.Warn("orbs: 记录已下发的 omp 配置失败", "project", projectID, "err", err)
	}
	a.mu.Lock()
	subs := slices.Clone(a.subs)
	a.mu.Unlock()
	for _, fn := range subs {
		fn(rep)
	}
	return rep, nil
}

// applyTargets 返回这一层要下发的 orb：ready、有带 omphome-apply 特性的活动 link（含在线的 runner，M21）；项目层只取
// 该项目的 orb——runner 的常驻 orb 不属于任何项目，只收全局层。
func (s *Service) applyTargets(ctx context.Context, projectID string) ([]db.Orb, error) {
	orbs, err := s.st.Q.ListOrbsByState(ctx, StateReady)
	if err != nil {
		return nil, fmt.Errorf("列出 ready 的 orb: %w", err)
	}
	out := orbs[:0]
	for _, orb := range orbs {
		if projectID != "" && (orb.ProjectID == nil || *orb.ProjectID != projectID) {
			continue
		}
		if !s.HasFeature(orb.ID, FeatureOmpHomeApply) {
			continue
		}
		out = append(out, orb)
	}
	return out, nil
}

// applyOrb 向一台 orb 下发 omphome.apply；成功后用同一份 Plan 刷新其 egress 路由。
func (s *Service) applyOrb(ctx context.Context, orb db.Orb) OrbApply {
	res := OrbApply{OrbID: orb.ID}
	gen := s.policyGeneration() // 读配置之前取代际：期间项目门禁被改时不写回旧策略
	plan, err := s.setup.Plan(ctx, orb)
	if err != nil {
		res.Err = "生成下发参数: " + err.Error()
		return res
	}
	ctl, ok := s.Ctl(orb.ID)
	if !ok {
		res.Err = ErrOrbNotReady.Error()
		return res
	}
	cctx, cancel := context.WithTimeout(ctx, ompApplyTimeout)
	defer cancel()
	params := apigen.LinkCtlOmpHomeApplyParams{OmpHome: plan.Params.OmpHome, ProjectConfig: plan.Params.ProjectConfig, SSH: plan.Params.SSH}
	var out apigen.LinkCtlOkResult
	if err := ctl.Call(cctx, string(apigen.MethodOmpHomeApply), params, &out); err != nil {
		res.Err = err.Error()
		return res
	}
	s.setPolicy(orb.ID, plan.Egress, gen)
	res.OK = true
	return res
}

// layerDir 是一层的下发记录目录。
func (a *ompApplier) layerDir(projectID string) string {
	if projectID == "" {
		return a.cfg.Dir
	}
	return filepath.Join(a.cfg.Dir, "projects", projectID)
}

// load 读这一层当前的原始文件；有 SSH 时并入合成的 ssh.json 摘要（只用于比较）。
func (a *ompApplier) load(projectID string) (map[string][]byte, error) {
	files, err := a.loadFiles(projectID)
	if err != nil || a.cfg.SSHState == nil {
		return files, err
	}
	state, err := a.cfg.SSHState(projectID)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 主机: %w", err)
	}
	if files == nil {
		files = map[string][]byte{}
	}
	files["ssh.json"] = state
	return files, nil
}

func (a *ompApplier) loadFiles(projectID string) (map[string][]byte, error) {
	if projectID == "" {
		if a.cfg.Home == nil {
			return map[string][]byte{}, nil
		}
		files, err := a.cfg.Home.LoadAll()
		if err != nil {
			return nil, fmt.Errorf("读取 omp-home: %w", err)
		}
		return files, nil
	}
	if a.cfg.Projects == nil {
		return nil, errors.New("orbs: 不支持项目层配置")
	}
	files, _, err := a.cfg.Projects.Load(projectID)
	if err != nil {
		return nil, fmt.Errorf("读取项目 omp 配置: %w", err)
	}
	return files, nil
}

// previous 返回上一次下发的 config.yml 与文件摘要；没有记录时摘要为 nil（此时全部文件都算变化）。
func (a *ompApplier) previous(projectID string) ([]byte, map[string]string) {
	dir := a.layerDir(projectID)
	cfgYml, err := os.ReadFile(filepath.Join(dir, appliedConfigFile))
	if err != nil {
		cfgYml = nil
	}
	b, err := os.ReadFile(filepath.Join(dir, appliedManifest))
	if err != nil {
		return cfgYml, nil
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return cfgYml, nil
	}
	return cfgYml, m
}

// save 记下这一层这次下发的文件摘要与 config.yml。
func (a *ompApplier) save(projectID string, files map[string][]byte) error {
	dir := a.layerDir(projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if b, ok := files[appliedConfigFile]; ok {
		if err := writeAtomic(filepath.Join(dir, appliedConfigFile), b); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(dir, appliedConfigFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m, err := json.Marshal(digests(files))
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, appliedManifest), m)
}

// digests 返回各文件内容的 sha256（十六进制）。
func digests(files map[string][]byte) map[string]string {
	out := make(map[string]string, len(files))
	for p, b := range files {
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:])
	}
	return out
}

// changedPaths 比较两份摘要，返回新增、修改、删除的路径（按字典序）；prev 为 nil 时 cur 的全部路径都算变化。
func changedPaths(prev, cur map[string]string) []string {
	out := []string{}
	for p, h := range cur {
		if old, ok := prev[p]; !ok || old != h {
			out = append(out, p)
		}
	}
	for p := range prev {
		if _, ok := cur[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// writeAtomic 经同目录临时文件 + rename 写入（0600）。
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
