// Package memory 是 M16 内置 Hindsight 记忆的控制面：连接设置与开关、状态探测、`.env` 生成，
// 以及给 `agent.start` 的记忆参数。Hindsight 的真实 key 与中转令牌只在 orynd 内使用，从不下发给 orb 或 runner。
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 固定值（见计划「全局约束」）。
const (
	// BankID 是全部记忆共用的 bank；网关把请求路径里的 bank 一律改写为它。
	BankID = "oryn"
	// GlobalTag 是已批准的全局记忆的标签。
	GlobalTag = "oryn:global"
	// keyAAD 是 Hindsight API key 在 secrets.Box 里的 AAD 名称（与迁移 0036 的注释一致）。
	keyAAD = "memory/hindsight-key"
	// statusTimeout 是一次状态探测的总时限。
	statusTimeout = 10 * time.Second
)

// ProjectLabel 是项目在 Hindsight 里的标识：项目 ID 转小写（Core 的 `ORYN_HINDSIGHT_PROJECT`）。
func ProjectLabel(projectID string) string { return strings.ToLower(projectID) }

// ProjectTag 是项目记忆的标签 `project:<项目 ID 转小写>`。
func ProjectTag(projectID string) string { return "project:" + ProjectLabel(projectID) }

// Caller 是记忆请求的可信调用方身份，由 orynd 依据 orb 令牌与线程归属解析，从不取自请求内容。
type Caller struct {
	OrbID, ThreadID string
	// ProjectID 为 nil 表示无项目线程（只召回全局记忆，写入一律进候选队列）。
	ProjectID *string
	// Runner 表示请求来自本机 Runner（项目按线程确定）。
	Runner bool
}

// Config 是记忆服务的依赖。
type Config struct {
	Q   *db.Queries
	Box *secrets.Box
	// HTTP 是访问 Hindsight 的客户端；nil 时用 http.DefaultClient。
	HTTP *http.Client
	// Now 是时钟；nil 时用 time.Now。
	Now func() time.Time
	// PublicURL 是 orynd 的公开根地址，`.env` 里的中转地址以它为前缀。
	PublicURL string
	// Restart 在开关的生效值变化时调用，把受影响的运行中线程标为需重启；projectID 为 nil 表示全部线程。可为 nil。
	Restart func(ctx context.Context, projectID *string)
	// Family 按中央模型目录查出模型的协议类型；nil 或该用途没选模型时按 openai 处理。
	Family func(ctx context.Context, providerKey, modelID string) (apigen.MemoryFamily, error)
	// Upstream 按中央模型目录查出模型的上游（模型中转用，见 relay.go）；nil 时 LLM 中转返回 503。
	Upstream func(ctx context.Context, providerKey, modelID string) (ModelUpstream, error)
}

// Service 是记忆服务。
type Service struct {
	cfg Config
}

// New 构造记忆服务。构造时清空上个进程留下的删除 hold（见 RecoverStaleModels），这发生在本进程
// 处理任何遗忘请求之前。
func New(cfg Config) *Service {
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	s := &Service{cfg: cfg}
	if cfg.Q != nil {
		if err := s.RecoverStaleModels(context.Background()); err != nil {
			slog.Warn("清空上次退出时进行中的记忆删除 hold 失败", "err", err)
		}
	}
	return s
}

// Error 是带 HTTP 状态与错误码的领域错误。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrNotConfigured 表示还没有填 Hindsight 地址或没有生成 `.env`。
var ErrNotConfigured = &Error{Status: http.StatusConflict, Code: "memory_not_configured", Message: "尚未配置 Hindsight 地址或尚未生成 .env"}

// loadSettings 读设置行；没有行时返回零值（未配置、开关关闭）。
func (s *Service) loadSettings(ctx context.Context) (db.MemorySetting, error) {
	st, err := s.cfg.Q.GetMemorySettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return db.MemorySetting{ID: 1, DeployedFamilies: "{}"}, nil
	}
	return st, err
}

// configured 判断设置是否足以访问 Hindsight：有地址、有 key、有中转令牌。
func configured(st db.MemorySetting) bool {
	return st.HindsightUrl != nil && *st.HindsightUrl != "" && len(st.HindsightKeyEnc) > 0 &&
		st.RelayTokenHash != nil && *st.RelayTokenHash != ""
}

// Enabled 判断记忆的全局生效值：总开关打开且已配置（地址与两个密钥都在）。查询出错按未启用处理。
func (s *Service) Enabled(ctx context.Context) bool {
	st, err := s.loadSettings(ctx)
	return err == nil && st.Enabled != 0 && configured(st)
}

// ProjectEnabled 判断项目的生效值：全局生效且项目没有单独关闭。查询出错按未启用处理。
func (s *Service) ProjectEnabled(ctx context.Context, projectID string) bool {
	if !s.Enabled(ctx) {
		return false
	}
	on, _, err := s.projectSwitch(ctx, projectID, true)
	return err == nil && on
}

// projectSwitch 返回项目自己的开关值（没有单独设置时取 global）与是否单独设置过。
func (s *Service) projectSwitch(ctx context.Context, projectID string, global bool) (bool, bool, error) {
	row, err := s.cfg.Q.GetMemoryProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return global, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return row.Enabled != 0, true, nil
}

// AgentMemory 返回线程 `agent.start` 的记忆参数；记忆对该线程不生效时返回 nil。
// 无项目线程（projectID 为 nil）关闭自动写入。查询出错时返回 nil 与错误，调用方按未启用处理。
func (s *Service) AgentMemory(ctx context.Context, threadID string, projectID *string) (*apigen.LinkCtlAgentMemory, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取记忆设置（线程 %s）：%w", threadID, err)
	}
	if st.Enabled == 0 || !configured(st) {
		return nil, nil
	}
	if projectID == nil {
		return &apigen.LinkCtlAgentMemory{AutoRetain: false}, nil
	}
	on, _, err := s.projectSwitch(ctx, *projectID, true)
	if err != nil {
		return nil, fmt.Errorf("读取项目记忆开关（线程 %s）：%w", threadID, err)
	}
	if !on {
		return nil, nil
	}
	label := ProjectLabel(*projectID)
	return &apigen.LinkCtlAgentMemory{Project: &label, AutoRetain: true}, nil
}

// Client 按当前设置构造 Hindsight 客户端；未配置时返回 ErrNotConfigured。
func (s *Service) Client(ctx context.Context) (*Hindsight, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	return s.clientFor(st)
}

func (s *Service) clientFor(st db.MemorySetting) (*Hindsight, error) {
	if !configured(st) {
		return nil, ErrNotConfigured
	}
	key, err := s.cfg.Box.Open(keyAAD, st.HindsightKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("解密 Hindsight API key：%w", err)
	}
	return NewHindsight(*st.HindsightUrl, string(key), s.cfg.HTTP), nil
}

// Status 实时探测 Hindsight（总时限 10 秒）：`/version`、`/health/ready`、bank `oryn` 的记忆数。
// bank 不存在时由 orynd 创建；上次记忆数大于 0、这次为 0 时状态为 suspect_empty（issue #6485），
// 此时不覆盖上次的记忆数，警告一直保留到数据回来。
func (s *Service) Status(ctx context.Context) (apigen.MemoryStatus, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.MemoryStatus{}, err
	}
	out := apigen.MemoryStatus{TestedMinor: TestedMinor, CheckedAt: s.cfg.Now().Unix(), EmbedLocked: st.EmbedLocked != 0}
	tombs, err := s.cfg.Q.ListMemoryTombstones(ctx)
	if err != nil {
		return apigen.MemoryStatus{}, err
	}
	cands, err := s.cfg.Q.ListMemoryCandidates(ctx)
	if err != nil {
		return apigen.MemoryStatus{}, err
	}
	// 遗忘后等待刷新的 mental model 也算未完成的删除（与 pending-deletes 列表一致）
	stale, err := s.cfg.Q.ListMemoryStaleModels(ctx)
	if err != nil {
		return apigen.MemoryStatus{}, err
	}
	out.PendingDeletes, out.Candidates = len(tombs)+len(stale), len(cands)
	switch {
	case st.HindsightUrl == nil || *st.HindsightUrl == "":
		out.State = apigen.MemoryStateUnconfigured
		return out, nil
	case !configured(st):
		out.State = apigen.MemoryStateNoKeys
		return out, nil
	}
	h, err := s.clientFor(st)
	if err != nil {
		return apigen.MemoryStatus{}, err
	}
	pctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	state, count, probeErr := s.probe(pctx, h, &out)
	out.State = state
	if probeErr != nil {
		msg := probeErr.Error()
		out.Error = &msg
		return out, nil
	}
	out.MemoryCount = &count
	if st.LastMemoryCount != nil && *st.LastMemoryCount > 0 && count == 0 {
		out.State = apigen.MemoryStateSuspectEmpty
		msg := fmt.Sprintf("上次检查时 bank 有 %d 条记忆，这次为 0：Hindsight 可能连不上外部数据库而换成了空库，见 docs/deploy.md", *st.LastMemoryCount)
		out.Error = &msg
		return out, nil
	}
	if err := s.cfg.Q.SetMemoryHealth(ctx, &count); err != nil {
		return apigen.MemoryStatus{}, err
	}
	if count > 0 {
		out.EmbedLocked = true
	}
	if st.Enabled == 0 {
		out.State = apigen.MemoryStateDisabled
	}
	return out, nil
}

// probe 依次探测版本、就绪与 bank 统计，返回状态与记忆数；失败时返回给人看的原因。
func (s *Service) probe(ctx context.Context, h *Hindsight, out *apigen.MemoryStatus) (apigen.MemoryStatusState, int64, error) {
	fail := func(err error, what string) (apigen.MemoryStatusState, int64, error) {
		switch code := statusOf(err); {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return apigen.MemoryStateUnauthorized, 0, fmt.Errorf("%s：API key 不被接受（%d），请重新生成 .env 并重启 Hindsight", what, code)
		case code != 0:
			return apigen.MemoryStateNotReady, 0, fmt.Errorf("%s：%w", what, err)
		}
		return apigen.MemoryStateUnreachable, 0, fmt.Errorf("%s：连不上 Hindsight：%w", what, err)
	}
	v, err := h.Version(ctx)
	if err != nil {
		return fail(err, "读取版本")
	}
	out.Version = &v
	out.VersionMismatch = VersionMismatch(v)
	if err := h.Ready(ctx); err != nil {
		return fail(err, "就绪检查")
	}
	stats, err := h.BankStats(ctx, BankID)
	if statusOf(err) == http.StatusNotFound {
		if err := h.EnsureBank(ctx, BankID); err != nil {
			return fail(err, "创建 bank "+BankID)
		}
		stats, err = h.BankStats(ctx, BankID)
	}
	if err != nil {
		return fail(err, "读取 bank 统计")
	}
	return apigen.MemoryStateOk, stats.TotalNodes, nil
}
