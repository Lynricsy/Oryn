package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Entry 是条目的线路形态（REST `GET threads/{id}/entries` 与 WS `entries` 共用）。
// 与 apigen.Entry 同形，但 payload 保持原始字节，避免把每行 JSON 解码成 map 再编码。
type Entry struct {
	Seq      int64           `json:"seq"`
	File     string          `json:"file"`
	EntryID  string          `json:"entryId"`
	ParentID *string         `json:"parentId,omitempty"`
	Type     string          `json:"type"`
	TS       *int64          `json:"ts,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

func entryFromDB(e db.Entry) Entry {
	return Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload}
}

// modelString / parseModel 在 threads.model 列（`<provider>/<id>`）与 ModelRef 之间转换。
func modelString(m *apigen.ModelRef) *string {
	if m == nil || m.Provider == "" || m.ID == "" {
		return nil
	}
	s := m.Provider + "/" + m.ID
	return &s
}

func parseModel(s *string) *apigen.ModelRef {
	if s == nil || *s == "" {
		return nil
	}
	p, id, ok := strings.Cut(*s, "/")
	if !ok {
		return nil
	}
	return &apigen.ModelRef{Provider: p, ID: id}
}

// ThreadToAPI 把线程行转换成 API 形态。
func ThreadToAPI(t db.Thread, labels []string) apigen.Thread {
	if labels == nil {
		labels = []string{}
	}
	out := apigen.Thread{
		ID:                t.ID,
		ProjectID:         t.ProjectID,
		OrbID:             t.OrbID,
		Title:             t.Title,
		TitleSource:       apigen.ThreadTitleSource(t.TitleSource),
		State:             apigen.ThreadState(t.State),
		StateChangedAt:    t.StateChangedAt,
		ParentThreadID:    t.ParentThreadID,
		ForkedFromEntryID: t.ForkedFromEntryID,
		LeafEntryID:       t.LeafEntryID,
		Model:             parseModel(t.Model),
		ThinkingLevel:     t.ThinkingLevel,
		Color:             t.Color,
		Pinned:            t.Pinned,
		SnoozedUntil:      t.SnoozedUntil,
		ArchivedAt:        t.ArchivedAt,
		Unread:            t.Unread,
		Labels:            labels,
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
		LastMessageAt:     t.LastMessageAt,
		SpaceID:           t.SpaceID,
		ShippedAt:         t.ShippedAt,
	}
	if t.Kind != "" && t.Kind != KindWork {
		k := apigen.ThreadKind(t.Kind)
		out.Kind = &k
	}
	if t.Main == 1 {
		out.Main = new(true)
	}
	if t.ScheduleID != nil && t.ScheduleRun != nil {
		out.ScheduleRun = &apigen.ThreadScheduleRun{ScheduleID: *t.ScheduleID, Run: *t.ScheduleRun}
	}
	if t.Cwd != nil && *t.Cwd != "" {
		out.Cwd = t.Cwd
	}
	out.Worktree = parseWorktree(t.Worktree)
	return out
}

// orbAPI 把 orb 行转换成 API 形态（orbs.View），特性取活动 link 的 hello（M11b）；读库失败时记录并返回已有字段。
func (s *Service) orbAPI(ctx context.Context, o db.Orb) apigen.Orb {
	v, err := orbs.View(ctx, s.st.Q, o)
	if err != nil {
		slog.Warn("threads: 转换 orb 失败", "orb", o.ID, "err", err)
		return apigen.Orb{ID: o.ID, ProviderID: o.ProviderID, Type: o.Type, Image: o.Image, ProjectID: o.ProjectID,
			State: apigen.OrbState(o.State), StateChangedAt: o.StateChangedAt, CreatedAt: o.CreatedAt, ThreadIds: []string{}}
	}
	if _, f := s.orbs.Features(o.ID); f != nil {
		v.Features = f
	}
	return v
}

// portalTemplate 返回线程的 portal 地址模板（Config.PortalURLTemplate）；未配置或为空时 nil。
func (s *Service) portalTemplate(threadID string) *string {
	if s.cfg.PortalURLTemplate == nil {
		return nil
	}
	if v := s.cfg.PortalURLTemplate(threadID); v != "" {
		return &v
	}
	return nil
}

// shortCode 返回线程 portal 主机名里的短码（threads.short_code）；id 不是 UUIDv7 形态时为 nil。
func shortCode(threadID string) *string {
	if code, ok := portalhost.Code(threadID); ok {
		return &code
	}
	return nil
}

// threadError 返回线程 state=error 的原因：所在 orb 为 failed 时为 {orb_failed, reason}；否则线程 failed
// （agent 没能启动）时为 {agent_failed, reason}。reasons 缓存 orb id → orb 失败原因（nil = orb 未失败），供列表批量转换复用。
// 只取 s.mu（agent 失败原因），不取线程锁：publish 在持有 t.mu 时经 threadAPI 调到这里。
func (s *Service) threadError(ctx context.Context, t db.Thread, reasons map[string]*apigen.ThreadError) *apigen.ThreadError {
	if t.State != StateError {
		return nil
	}
	if e := s.orbError(ctx, t.OrbID, reasons); e != nil {
		return e
	}
	if reason, ok := s.agentFailReason(t.ID); ok {
		return newThreadError(apigen.AgentFailed, reason)
	}
	return nil
}

// orbError 返回 orb 为 failed 时的 {orb_failed, reason}，否则 nil；结果按 orb id 缓存在 reasons 里。
func (s *Service) orbError(ctx context.Context, orbID *string, reasons map[string]*apigen.ThreadError) *apigen.ThreadError {
	if orbID == nil {
		return nil
	}
	if e, ok := reasons[*orbID]; ok {
		return e
	}
	var out *apigen.ThreadError
	if o, err := s.st.Q.GetOrb(ctx, *orbID); err == nil && o.State == orbStateFailed {
		reason, err := orbs.FailureReason(ctx, s.st.Q, o.ID)
		if err != nil {
			slog.Warn("threads: 读取 orb 失败原因失败", "orb", o.ID, "err", err)
		}
		out = newThreadError(apigen.OrbFailed, reason)
	}
	reasons[*orbID] = out
	return out
}

// threadAPI 读取线程行、标签与子线程计数。
func (s *Service) threadAPI(ctx context.Context, t db.Thread) (apigen.Thread, error) {
	labels, err := s.st.Q.ListThreadLabels(ctx, t.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out := ThreadToAPI(t, labels)
	out.PortalURLTemplate = s.portalTemplate(t.ID)
	out.Error = s.threadError(ctx, t, map[string]*apigen.ThreadError{})
	children, err := s.childCounts(ctx)
	if err != nil {
		return apigen.Thread{}, err
	}
	if c, ok := children[t.ID]; ok {
		out.ChildCount, out.ChildDoneCount = &c[0], &c[1]
	}
	out.Activity = s.activityPtr(t.ID)
	out.PublicShares = s.publicShares(ctx, t.ID)
	pulls, err := s.st.Q.ListThreadPulls(ctx, t.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	for _, p := range pulls {
		out.Pulls = append(out.Pulls, threadPullAPI(p))
	}
	task, err := s.threadTask(ctx, t.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out.GithubTask = task
	sum, err := s.st.Q.GetScheduleSummary(ctx, t.ID)
	switch {
	case err == nil:
		out.Schedule = &apigen.ThreadScheduleSummary{Enabled: sum.Enabled, NextRunAt: sum.NextRunAt}
	case !errors.Is(err, sql.ErrNoRows):
		return apigen.Thread{}, err
	}
	return out, nil
}

// threadTask 取单个线程的页头任务徽标：先取最新的 issue 任务，没有时取最新的发布哨兵行；都没有返回 nil。
// 一个线程按设计只挂其中一种（触发规则建 issue 线程，发布哨兵另建修复线程）；万一两者都在，issue 任务优先，
// 与列表（List）一致。
func (s *Service) threadTask(ctx context.Context, threadID string) (*apigen.GitHubThreadTask, error) {
	issue, err := s.st.Q.GetThreadTaskSummary(ctx, threadID)
	switch {
	case err == nil:
		return issueTaskAPI(db.ListThreadTaskSummariesRow(issue)), nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	rel, err := s.st.Q.GetThreadReleaseSummary(ctx, threadID)
	switch {
	case err == nil:
		return releaseTaskAPI(db.ListThreadReleaseSummariesRow(rel)), nil
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	default:
		return nil, err
	}
}

// issueTaskAPI 把线程的 issue 任务摘要转成页头徽标（M15 R15；单线程与列表共用）。仓库取线程项目的 repo_url 路径。
func issueTaskAPI(r db.ListThreadTaskSummariesRow) *apigen.GitHubThreadTask {
	return &apigen.GitHubThreadTask{
		Kind: apigen.GitHubThreadTaskKindIssue, Repo: repoPathOf(r.RepoUrl), Number: &r.Number, State: r.State,
	}
}

// releaseTaskAPI 把线程的发布哨兵摘要转成页头徽标（M15 步骤 6.1 / R15；单线程与列表共用）。github_releases 不存仓库名，
// 仓库同 issue 路径取线程项目的 repo_url 路径；状态原样取发布行（awaiting_ci / fixing / green / failed / superseded）。
func releaseTaskAPI(r db.ListThreadReleaseSummariesRow) *apigen.GitHubThreadTask {
	return &apigen.GitHubThreadTask{
		Kind: apigen.GitHubThreadTaskKindRelease, Repo: repoPathOf(r.RepoUrl), Version: &r.Version, State: r.State,
	}
}

// repoPathOf 取仓库地址的 owner/repo（去掉主机、结尾的 `.git` 与斜杠）；解析不出时返回空串。
func repoPathOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// threadPullAPI 把线程 ↔ PR 关联摘要行转成 API 形态（单线程与列表共用；M14）。
func threadPullAPI(r db.ListThreadPullsRow) apigen.ThreadPull {
	out := apigen.ThreadPull{
		Repo: r.RepoFullName, Number: r.Number, Title: r.Title, State: apigen.ThreadPullState(r.State),
		Draft: r.Draft, URL: r.HtmlUrl, Listening: r.Listening,
	}
	if r.Checks != nil {
		c := apigen.ThreadPullChecks(*r.Checks)
		out.Checks = &c
	}
	return out
}

// childCounts 返回 父线程 id → [子线程数, 处于 idle 的子线程数]。
func (s *Service) childCounts(ctx context.Context) (map[string][2]int, error) {
	all, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	return childCountsOf(all), nil
}

func childCountsOf(all []db.Thread) map[string][2]int {
	out := map[string][2]int{}
	for _, t := range all {
		if t.ParentThreadID == nil {
			continue
		}
		c := out[*t.ParentThreadID]
		c[0]++
		if t.State == StateIdle {
			c[1]++
		}
		out[*t.ParentThreadID] = c
	}
	return out
}
