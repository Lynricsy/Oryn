// Package cfgsync 把用户在 orb 里用 /omfg 保存的规则写回 Oryn 中央配置（M12.2 契约 C5、C6）。
//
// 回写由控制面发起：threads 在用户发起的 omfg 应答 `saved` 之后调用 Saver.SaveRule。来自 orb 的一切输入
// （保存位置、文件内容）都不可信：保存位置必须匹配全串锚定的白名单，写入中央的相对路径由捕获到的规则名重新拼出，
// 内容有大小与文本校验；全局范围还要求控制面自己记录到用户点选了全局选项。
package cfgsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// MaxRuleBytes 是回写规则文件的大小上限。
const MaxRuleBytes = 256 << 10

// 范围（契约 C5 的 scope）。
const (
	ScopeProject = "project"
	ScopeGlobal  = "global"
)

// 未写入中央的原因（契约 C5 的 reason）。
const (
	ReasonNoProject        = "no_project"
	ReasonUnsupportedPath  = "unsupported_path"
	ReasonScopeUnconfirmed = "scope_unconfirmed"
	ReasonOrbReadFailed    = "orb_read_failed"
	ReasonTooLarge         = "too_large"
	ReasonInvalidContent   = "invalid_content"
	ReasonStoreFailed      = "store_failed"
)

// OrbFS 经 fs 流向 orbd 本机 API 发请求（*orbs.Service）。
type OrbFS interface {
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
}

// GlobalStore 是全局 omp-home（*agentcfg.Home）。
type GlobalStore interface {
	Put(ctx context.Context, p string, content []byte, mode fs.FileMode, vault *secrets.Vault) (agentcfg.WriteResult, error)
}

// ProjectStores 是项目级配置（*agentcfg.ProjectHomes）。
type ProjectStores interface {
	For(projectID string) (*agentcfg.Home, error)
}

// Saver 实现 threads.ConfigSync。
type Saver struct {
	Store    *store.Store
	Orbs     OrbFS
	Global   GlobalStore
	Projects ProjectStores
	Vault    *secrets.Vault
	// Applied 在写入中央成功后调用（M13：按层立即下发到其它 orb；projectID 空 = 全局层）。nil = 不下发。
	Applied func(projectID string)
}

var _ threads.ConfigSync = (*Saver)(nil)

// 保存位置白名单（契约 C2，shortenPath 之后的形式）。全部全串锚定；`<name>` 是 sanitizeRuleName 的字符集。
// Go 的 `$` 在非多行模式下只匹配文本末尾（不像 PCRE 会放过末尾换行）。
const ruleName = `([a-z0-9][a-z0-9_-]{0,127})`

var (
	globalRuleRe = regexp.MustCompile(`^~/\.omp/agent/rules/` + ruleName + `\.md$`)
	// Core ≥ oryn-v18.19.0-8 的项目层
	projectRuleRe = regexp.MustCompile(`^~/\.oryn/project-config/\.omp/rules/` + ruleName + `\.md$`)
	// 旧 Core / 上游：项目保存落在仓库（或工作区）的 .omp/rules
	legacyProjectRuleRe = regexp.MustCompile(`^~/workspace/(?:repo/)?\.omp/rules/` + ruleName + `\.md$`)
)

// classify 把保存位置归入范围并给出中央的相对路径（恒为 rules/<name>.md）；不在白名单内返回 ok=false。
func classify(savedPath string) (scope, rel string, ok bool) {
	if m := globalRuleRe.FindStringSubmatch(savedPath); m != nil {
		return ScopeGlobal, "rules/" + m[1] + ".md", true
	}
	for _, re := range []*regexp.Regexp{projectRuleRe, legacyProjectRuleRe} {
		if m := re.FindStringSubmatch(savedPath); m != nil {
			return ScopeProject, "rules/" + m[1] + ".md", true
		}
	}
	return "", "", false
}

// SaveRule 按「路径白名单 → 范围确认 → 读 orb → 写中央 → 审计」把一条 /omfg 规则写回中央配置。
func (s *Saver) SaveRule(ctx context.Context, p threads.ConfigSaveParams) threads.ConfigSaveResult {
	lg := slog.With("orb", p.OrbID, "thread", p.ThreadID)
	scope, rel, ok := classify(p.SavedPath)
	if !ok {
		lg.Info("cfgsync: omfg 保存位置不在白名单，不回写", "savedPath", p.SavedPath)
		return threads.ConfigSaveResult{Reason: ReasonUnsupportedPath}
	}
	res := threads.ConfigSaveResult{Scope: scope, Path: rel}
	fail := func(reason string) threads.ConfigSaveResult {
		res.Reason = reason
		return res
	}
	if scope == ScopeGlobal && !p.GlobalChosen {
		lg.Warn("cfgsync: omfg 报告保存到全局，但没有记录到用户点选全局选项（可能是 orb 伪造了保存位置）", "savedPath", p.SavedPath)
		return fail(ReasonScopeUnconfirmed)
	}
	orb, err := s.Store.Q.GetOrb(ctx, p.OrbID)
	if err != nil {
		lg.Error("cfgsync: 读取 orb 失败", "err", err)
		return fail(ReasonStoreFailed)
	}
	// 项目 id 只取自数据库里的 orb 行，orb 无法指定
	var projectID string
	if scope == ScopeProject {
		if orb.ProjectID == nil || *orb.ProjectID == "" {
			return fail(ReasonNoProject)
		}
		projectID = *orb.ProjectID
		if _, err := s.Store.Q.GetProject(ctx, projectID); errors.Is(err, sql.ErrNoRows) {
			return fail(ReasonNoProject)
		} else if err != nil {
			lg.Error("cfgsync: 读取项目失败", "project", projectID, "err", err)
			return fail(ReasonStoreFailed)
		}
	}
	content, reason := s.readOrb(ctx, p.OrbID, p.SavedPath)
	if reason != "" {
		lg.Warn("cfgsync: 读取 orb 里的规则失败", "savedPath", p.SavedPath, "reason", reason)
		return fail(reason)
	}
	if reason := s.put(ctx, scope, projectID, rel, content); reason != "" {
		return fail(reason)
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	s.audit(ctx, orb, scope, projectID, rel, sha, p.ThreadID)
	lg.Info("cfgsync: omfg 规则已写回中央配置", "scope", scope, "project", projectID, "path", rel, "sha256", sha)
	res.Synced = true
	return res
}

// readOrb 经 orbd 的 fs 接口读回规则文件；失败时返回 C5 的 reason。
func (s *Saver) readOrb(ctx context.Context, orbID, savedPath string) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://orbd/fs/file?path="+url.QueryEscape(savedPath), nil)
	if err != nil {
		return nil, ReasonOrbReadFailed
	}
	resp, err := s.Orbs.Do(ctx, orbID, req)
	if err != nil {
		slog.Warn("cfgsync: 请求 orb fs 失败", "orb", orbID, "err", err)
		return nil, ReasonOrbReadFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("cfgsync: orb fs 返回非 200", "orb", orbID, "status", resp.StatusCode)
		return nil, ReasonOrbReadFailed
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxRuleBytes+1))
	if err != nil {
		return nil, ReasonOrbReadFailed
	}
	if len(b) > MaxRuleBytes {
		return nil, ReasonTooLarge
	}
	if len(b) == 0 || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return nil, ReasonInvalidContent
	}
	return b, ""
}

// put 写入中央（同名规则直接覆盖：用户已在 orb 内确认过覆盖）；失败时返回 C5 的 reason。
func (s *Saver) put(ctx context.Context, scope, projectID, rel string, content []byte) string {
	var err error
	if scope == ScopeGlobal {
		_, err = s.Global.Put(ctx, rel, content, 0o644, s.Vault)
	} else {
		var h *agentcfg.Home
		if h, err = s.Projects.For(projectID); err == nil {
			_, err = h.Put(ctx, rel, content, 0o644, nil)
		}
	}
	switch {
	case err == nil:
		if s.Applied != nil {
			if scope == ScopeGlobal {
				projectID = ""
			}
			s.Applied(projectID)
		}
		return ""
	case errors.Is(err, agentcfg.ErrTooLarge):
		slog.Warn("cfgsync: 规则超出中央配置配额", "scope", scope, "project", projectID, "path", rel, "err", err)
		return ReasonTooLarge
	default:
		slog.Error("cfgsync: 写入中央配置失败", "scope", scope, "project", projectID, "path", rel, "err", err)
		return ReasonStoreFailed
	}
}

// audit 写一条 orb 审计事件 agent_config_saved（带内容哈希）；失败只告警。
func (s *Saver) audit(ctx context.Context, orb db.Orb, scope, projectID, rel, sha, threadID string) {
	d := map[string]any{"event": "agent_config_saved", "scope": scope, "path": rel, "sha256": sha, "threadId": threadID}
	if projectID != "" {
		d["projectId"] = projectID
	}
	detail, _ := json.Marshal(d)
	if _, err := s.Store.Q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{
		OrbID: orb.ID, Ts: time.Now().UnixMilli(), FromState: orb.State, ToState: orb.State, Detail: string(detail),
	}); err != nil {
		slog.Warn("cfgsync: 写审计事件失败", "orb", orb.ID, "err", err)
	}
}
