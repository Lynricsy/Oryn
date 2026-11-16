package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// REST 轮询（M15 步骤 3.1，R10）。仓库没有 active 的仓库 hook 时，事件靠轮询补全：
//   - 仓库级 watch（github_repo_watches）：已绑定、hook 不是 active、且有启用规则的仓库，轮询新 issue、新评论、
//     打开的 PR 与 push 触发的 workflow run；以我的账号的令牌轮询。
//   - 对象级 watch（github_watches）：线程关联的 issue / PR（含向上游开的 PR），所在仓库 hook 不是 active 时轮询对象本身、
//     评论、评审、行内评审评论与 head 的 CI；令牌取线程 orb 的槽位。
//
// 请求都带 If-None-Match（304 不产生事件、不计额度）。变化合成与 webhook 载荷同形的内部事件，guid 为
// rest:<事件>.<action>:<owner/repo 小写>:<对象 id>:<时间>，经 ingest(…, "rest") 交给与 webhook 相同的处理器；
// 仓库级与对象级对同一变化合成的 guid 相同，重叠时只处理一次。事件表见 docs/plan-m15.md 附录 C。

const (
	maxWatchPages   = 5  // 一个列表接口一轮最多翻的页数
	maxEventLookups = 20 // 仓库级 watch 一轮最多查的 issues/{n}/events 次数
)

// timelineEvents 是按 issues/{n}/events 合成的事件：时间线事件名 → (webhook 事件, action)。issue 与 PR 分开。
var (
	issueTimeline = map[string]string{"labeled": "labeled", "unlabeled": "unlabeled", "closed": "closed", "reopened": "reopened"}
	pullTimeline  = map[string]string{
		"reopened": "reopened", "ready_for_review": "ready_for_review", "convert_to_draft": "converted_to_draft", "closed": "closed",
	}
)

// restItem 是 REST 列表项里轮询用到的公共字段（issue、PR、评论、评审、workflow run、时间线事件）。
type restItem struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	State       string          `json:"state"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	SubmittedAt string          `json:"submitted_at"` // 评审
	IssueURL    string          `json:"issue_url"`    // 仓库级评论列表：所属 issue 的 API 地址
	PullRequest json.RawMessage `json:"pull_request"` // issue 视图：非空表示是 PR
	Event       string          `json:"event"`        // 时间线事件名
	HeadSHA     string          `json:"head_sha"`     // workflow run
}

func (it restItem) isPull() bool { return len(it.PullRequest) > 0 && string(it.PullRequest) != "null" }

// decodeRest 把一个列表项解成公共字段与原样的对象（数字保持原文，合成载荷时不丢精度）。
func decodeRest(raw json.RawMessage) (restItem, map[string]any, error) {
	var it restItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return it, nil, err
	}
	obj, err := decodeObj(raw)
	return it, obj, err
}

func decodeObj(raw []byte) (map[string]any, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// watchBaseline 是新 watch 的游标起点：当前时间向下取整到秒（GitHub 的时间只到秒，同一秒里稍后创建的对象不能漏掉）。
func (s *Service) watchBaseline() int64 { return s.nowMs() / 1000 * 1000 }

// restTime 把毫秒时间写成 since 参数（RFC 3339，UTC）。
func restTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

// restGUID 是合成事件的 guid。at 是 GitHub 返回的时间原文（创建类事件取 created_at，其余取 updated_at / 事件时间）。
func restGUID(event, action, full string, id any, at string) string {
	return fmt.Sprintf("rest:%s.%s:%s:%v:%s", event, action, strings.ToLower(full), id, at)
}

// emitRest 把一个合成事件交给 ingest（来源 rest；repoID 记 0：rest 不是 hook 的投递来源）。
func (s *Service) emitRest(ctx context.Context, guid, event string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("github: 编码合成事件失败", "guid", guid, "err", err)
		return
	}
	s.ingest(ctx, guid, event, body, "rest", 0)
}

// ---------- 登记 ----------

// hookActive 报告仓库是否有 active 的仓库 hook（此时事件由 webhook 与投递日志轮询送达，不做 REST 轮询）。
func (s *Service) hookActive(ctx context.Context, repoID int64) bool {
	h, err := s.st.Q.GetGithubHook(ctx, repoID)
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取仓库 hook 失败", "repo", repoID, "err", err)
		}
		return false
	}
	return h.Status == "active" && h.HookID != nil
}

// threadSlot 返回线程 orb 所用的身份槽位；线程没有 orb 或读不到时按我的账号。
func (s *Service) threadSlot(ctx context.Context, threadID string) string {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil || t.OrbID == nil {
		return slotUser
	}
	return s.orbSlot(ctx, *t.OrbID)
}

// repoSlot 返回读取仓库所用的槽位：已绑定的仓库用我的账号；否则（向上游开的 PR、无项目线程的 PR）用目标线程 orb 的槽位。
func (s *Service) repoSlot(ctx context.Context, repoID int64, threadID string) (string, error) {
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &repoID)
	if err != nil {
		return "", err
	}
	if len(prjs) > 0 || threadID == "" {
		return slotUser, nil
	}
	return s.threadSlot(ctx, threadID), nil
}

// registerWatch 为线程关联的 issue / PR（kind 为 issue / pull）登记对象级 watch；所在仓库 hook 为 active 时不登记。
// 已有的 watch 保留 ETag 与游标，新登记的从现在起算。失败只记日志。
func (s *Service) registerWatch(ctx context.Context, kind string, repoID int64, full string, number int64, threadID string) {
	if s.hookActive(ctx, repoID) {
		return
	}
	_, err := s.st.Q.GetGithubWatch(ctx, db.GetGithubWatchParams{Kind: kind, FullName: full, Number: number})
	fresh := isNoRows(err)
	if err != nil && !fresh {
		slog.Warn("github: 读取对象 watch 失败", "repo", full, "number", number, "err", err)
		return
	}
	if err := s.st.Q.UpsertGithubWatch(ctx, db.UpsertGithubWatchParams{
		Kind: kind, RepoID: repoID, FullName: full, Number: number, ThreadID: threadID, Slot: s.threadSlot(ctx, threadID),
	}); err != nil {
		slog.Warn("github: 登记对象 watch 失败", "repo", full, "number", number, "thread", threadID, "err", err)
		return
	}
	if fresh {
		now := s.watchBaseline()
		if err := s.st.Q.SetGithubWatchState(ctx, db.SetGithubWatchStateParams{
			LastSeenAt: &now, Kind: kind, FullName: full, Number: number,
		}); err != nil {
			slog.Warn("github: 初始化对象 watch 失败", "repo", full, "number", number, "err", err)
		}
	}
}

// syncRepoWatch 让仓库级 watch 跟随仓库的现状：已绑定（有未归档项目）、hook 不是 active、且有启用规则时登记，否则注销。
func (s *Service) syncRepoWatch(ctx context.Context, repoID int64) error {
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &repoID)
	if err != nil {
		return err
	}
	full, want := "", false
	for _, prj := range prjs {
		if p, ok := githubRepoPath(prj.RepoUrl); ok && full == "" {
			full = p
		}
		trigs, err := s.st.Q.ListGithubTriggers(ctx, prj.ID)
		if err != nil {
			return err
		}
		for _, t := range trigs {
			want = want || t.Enabled
		}
	}
	if want && full != "" && !s.hookActive(ctx, repoID) {
		return s.st.Q.UpsertGithubRepoWatch(ctx, db.UpsertGithubRepoWatchParams{RepoID: repoID, FullName: full, UpdatedAt: s.nowMs()})
	}
	_, err = s.st.Q.DeleteGithubRepoWatch(ctx, repoID)
	return err
}

// syncRepoWatchOf 在规则启停后按项目所绑仓库同步仓库级 watch（项目未绑定时什么都不做）。失败只记日志。
func (s *Service) syncRepoWatchOf(ctx context.Context, projectID string) {
	prj, err := s.st.Q.GetProject(ctx, projectID)
	if err != nil || prj.GithubRepoID == nil {
		return
	}
	if err := s.syncRepoWatch(ctx, *prj.GithubRepoID); err != nil {
		slog.Warn("github: 同步仓库级 watch 失败", "repo", *prj.GithubRepoID, "err", err)
	}
}

// syncRepoWatches 对全部已绑定仓库与现有的仓库级 watch 做一遍 syncRepoWatch（每轮轮询之前）。
func (s *Service) syncRepoWatches(ctx context.Context) error {
	prjs, err := s.st.Q.ListGithubCandidateProjects(ctx)
	if err != nil {
		return err
	}
	rows, err := s.st.Q.ListGithubRepoWatches(ctx)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, p := range prjs {
		if p.GithubRepoID != nil {
			add(*p.GithubRepoID)
		}
	}
	for _, r := range rows {
		add(r.RepoID)
	}
	var errs []error
	for _, id := range ids {
		errs = append(errs, s.syncRepoWatch(ctx, id))
	}
	return errors.Join(errs...)
}

// backfillWatches 为还没有对象级 watch 的线程 PR 关联补登记（升级前建立的关联、hook 从 active 变为不可用的仓库）：
// 线程未归档、PR 未进入终态。issue 关联只来自触发规则，所在仓库有规则就有仓库级 watch，不补。随 hookSyncInterval 运行。
func (s *Service) backfillWatches(ctx context.Context) error {
	rows, err := s.st.Q.ListThreadPullSummaries(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State != "open" {
			continue
		}
		if _, err := s.st.Q.GetGithubWatch(ctx, db.GetGithubWatchParams{Kind: "pull", FullName: r.RepoFullName, Number: r.Number}); err == nil {
			continue
		} else if !isNoRows(err) {
			return err
		}
		t, err := s.st.Q.GetThread(ctx, r.ThreadID)
		if err != nil || t.ArchivedAt != nil {
			continue
		}
		s.registerWatch(ctx, "pull", r.RepoID, r.RepoFullName, r.Number, r.ThreadID)
	}
	return nil
}

// ---------- 轮询 ----------

// pollWatches 是每 restPollInterval 一轮的 REST 轮询：先同步仓库级 watch、清掉退场的对象级 watch，再按槽位轮询。
// 单个 watch 失败只记日志。
func (s *Service) pollWatches(ctx context.Context) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	start := s.now()
	if err := s.syncRepoWatches(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("github: 同步仓库级 watch 失败", "err", err)
	}
	before := start.Add(-watchRetire).UnixMilli()
	if err := s.st.Q.DeleteRetiredGithubWatches(ctx, &before); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理退场的对象 watch 失败", "err", err)
	}
	usable := map[string]bool{}
	due := func(slot string) bool {
		ok, seen := usable[slot]
		if !seen {
			_, err := s.tokenFor(ctx, slot)
			ok = err == nil && s.slotDue(slot, start)
			usable[slot] = ok
		}
		return ok && !s.now().Before(s.watchPause[slot])
	}
	objs, err := s.st.Q.ListGithubWatches(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取对象 watch 失败", "err", err)
		}
		return
	}
	for _, w := range objs {
		if ctx.Err() != nil {
			return
		}
		if s.hookActive(ctx, w.RepoID) {
			// 事件改由 webhook 送达；hook 之后不可用时由 backfillWatches 从当时起重新登记，不回放期间的事件
			if _, err := s.st.Q.DeleteGithubWatch(ctx, db.DeleteGithubWatchParams{Kind: w.Kind, FullName: w.FullName, Number: w.Number}); err != nil {
				slog.Warn("github: 注销对象 watch 失败", "repo", w.FullName, "number", w.Number, "err", err)
			}
			continue
		}
		if !due(w.Slot) {
			continue
		}
		if err := s.pollObject(ctx, w); err != nil && ctx.Err() == nil {
			slog.Warn("github: 轮询对象失败", "kind", w.Kind, "repo", w.FullName, "number", w.Number, "err", err)
		}
	}
	repos, err := s.st.Q.ListGithubRepoWatches(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取仓库级 watch 失败", "err", err)
		}
		return
	}
	for _, w := range repos {
		if ctx.Err() != nil {
			return
		}
		if !due(slotUser) {
			break
		}
		if err := s.pollRepo(ctx, w); err != nil && ctx.Err() == nil {
			slog.Warn("github: 轮询仓库失败", "repo", w.FullName, "err", err)
		}
	}
	for slot, ok := range usable {
		if ok {
			s.slotPolled(slot, start)
		}
	}
}

// slotDue 报告槽位本轮是否到期（上一轮定下的下次时刻已到）。调用方持 watchMu。
func (s *Service) slotDue(slot string, now time.Time) bool {
	return !now.Before(s.watchNext[slot]) && !now.Before(s.watchPause[slot])
}

// slotPolled 在槽位轮询一轮后定下次时刻：额度低于 rateLimitFloor 时间隔加倍。减去半个周期作容差：
// 定时器按 restPollInterval 触发，正常时每轮都到期，加倍时隔一轮。调用方持 watchMu。
func (s *Service) slotPolled(slot string, start time.Time) {
	every := restPollInterval
	if s.watchLow[slot] {
		every *= 2
	}
	if s.watchNext == nil {
		s.watchNext = map[string]time.Time{}
	}
	s.watchNext[slot] = start.Add(every - restPollInterval/2)
}

// watchGet 以槽位的令牌 GET 一个轮询接口：etag 非空时带 If-None-Match，304 时 notModified 为真（out 不变）。
// 按响应的 X-RateLimit-* 记录该槽位的核心额度（同 poll.go 的投递日志额度）。调用方持 watchMu。
func (s *Service) watchGet(ctx context.Context, slot, path, etag string, out any) (h http.Header, notModified bool, err error) {
	tok, err := s.tokenFor(ctx, slot)
	if err != nil {
		return nil, false, err
	}
	target := path
	if strings.HasPrefix(path, "/") {
		target = s.cfg.APIURL + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	h, err = s.send(req, "Bearer "+tok, out)
	s.noteWatchQuota(slot, h)
	if isStatus(err, http.StatusNotModified) {
		return h, true, nil
	}
	if errKind(err) == kindReauth {
		if merr := s.markReauth(ctx, slot); merr != nil {
			return h, false, errors.Join(err, merr)
		}
	}
	return h, false, err
}

// noteWatchQuota 按响应头记录槽位的核心额度：低于 rateLimitFloor 时该槽位间隔加倍，耗尽时暂停到 X-RateLimit-Reset。调用方持 watchMu。
func (s *Service) noteWatchQuota(slot string, h http.Header) {
	if h == nil {
		return
	}
	left, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	if s.watchLow == nil {
		s.watchLow = map[string]bool{}
	}
	s.watchLow[slot] = left < rateLimitFloor
	if left > 0 {
		return
	}
	if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if s.watchPause == nil {
			s.watchPause = map[string]time.Time{}
		}
		s.watchPause[slot] = time.Unix(reset, 0)
		slog.Info("github: 核心额度耗尽，暂停 REST 轮询", "slot", slot, "until", s.watchPause[slot])
	}
}

// watchList 拉取一个列表接口：第一页带 If-None-Match（304 时 notModified 为真），其后按 Link 翻页（至多 maxWatchPages 页）。
// 返回第一页的 ETag。调用方持 watchMu。
func (s *Service) watchList(ctx context.Context, slot, path, etag string) (items []json.RawMessage, newEtag string, notModified bool, err error) {
	var page []json.RawMessage
	h, nm, err := s.watchGet(ctx, slot, path, etag, &page)
	if err != nil || nm {
		return nil, etag, nm, err
	}
	newEtag, items = h.Get("ETag"), page
	for i := 1; i < maxWatchPages; i++ {
		next := nextLink(h)
		if next == "" {
			break
		}
		page = nil
		if h, _, err = s.watchGet(ctx, slot, next, "", &page); err != nil {
			return nil, etag, false, err
		}
		items = append(items, page...)
	}
	return items, newEtag, false, nil
}

// watchRepoOf 返回合成载荷里的 repository（id、node_id、full_name）：按仓库 id 缓存 GET /repos/{full} 的结果，
// full_name 取 GitHub 的规范写法（与 webhook 载荷一致）。调用方持 watchMu。
func (s *Service) watchRepoOf(ctx context.Context, slot string, id int64, full string) (map[string]any, error) {
	r, ok := s.repoNodes[id]
	if !ok {
		if _, _, err := s.watchGet(ctx, slot, "/repos/"+full, "", &r); err != nil {
			return nil, err
		}
		if r.FullName == "" {
			r.FullName = full
		}
		if s.repoNodes == nil {
			s.repoNodes = map[int64]repoPayload{}
		}
		s.repoNodes[id] = r
	}
	return map[string]any{"id": id, "node_id": r.NodeID, "full_name": r.FullName}, nil
}

// emitTimeline 读 issues/{n}/events，把 since 之后、在 table 里的时间线事件合成为 event 事件（issues 或 pull_request），
// sender 取事件的 actor。subjKey 是载荷里 subject 的键（issue / pull_request）。调用方持 watchMu。
func (s *Service) emitTimeline(ctx context.Context, slot string, repo map[string]any, full string, number, since int64,
	event, subjKey string, subject map[string]any, table map[string]string,
) (latest int64, err error) {
	items, _, _, err := s.watchList(ctx, slot, fmt.Sprintf("/repos/%s/issues/%d/events?per_page=100", full, number), "")
	if err != nil {
		return 0, err
	}
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		if err != nil {
			continue
		}
		at := githubTimeMs(it.CreatedAt)
		action, ok := table[it.Event]
		if !ok || at < since {
			continue
		}
		latest = max(latest, at)
		payload := map[string]any{"action": action, subjKey: subject, "repository": repo, "sender": obj["actor"]}
		if event == "pull_request" {
			payload["number"] = number
		}
		if l, ok := obj["label"]; ok && l != nil {
			payload["label"] = l
		}
		s.emitRest(ctx, restGUID(event, action, full, it.ID, it.CreatedAt), event, payload)
	}
	return latest, nil
}

// ---------- 仓库级 ----------

// pollRepo 轮询一个仓库级 watch。首轮只建立 since 基线。各列表各自保存 ETag；全部成功才推进 since（推进到看到的最大
// updated_at；since 含端点，重复项靠 guid 去重）。调用方持 watchMu。
func (s *Service) pollRepo(ctx context.Context, w db.GithubRepoWatch) error {
	now := s.nowMs()
	st := db.SetGithubRepoWatchStateParams{
		EtagIssues: w.EtagIssues, EtagComments: w.EtagComments, EtagPulls: w.EtagPulls, EtagRuns: w.EtagRuns,
		SinceAt: w.SinceAt, UpdatedAt: now, RepoID: w.RepoID,
	}
	if w.SinceAt == nil {
		base := s.watchBaseline()
		st.SinceAt = &base
		return s.st.Q.SetGithubRepoWatchState(ctx, st)
	}
	repo, err := s.watchRepoOf(ctx, slotUser, w.RepoID, w.FullName)
	if err != nil {
		return err
	}
	since, full := *w.SinceAt, w.FullName
	q := "since=" + url.QueryEscape(restTime(since))
	next := since
	seen := func(at string) { next = max(next, githubTimeMs(at)) }
	lookups := 0
	subjects := map[int64]map[string]any{}
	var errs []error

	// issue：新建的合成 opened；其余有更新的查时间线（打标签、关闭、重开）
	items, etag, nm, err := s.watchList(ctx, slotUser, fmt.Sprintf("/repos/%s/issues?state=all&sort=updated&direction=asc&per_page=100&%s", full, q), deref(w.EtagIssues))
	ok := err == nil
	for _, raw := range items {
		it, obj, derr := decodeRest(raw)
		if derr != nil {
			continue
		}
		seen(it.UpdatedAt)
		subjects[it.Number] = obj
		if it.isPull() {
			continue // PR 走 pulls 列表
		}
		if githubTimeMs(it.CreatedAt) >= since {
			s.emitRest(ctx, restGUID("issues", "opened", full, it.ID, it.CreatedAt), "issues",
				map[string]any{"action": "opened", "issue": obj, "repository": repo, "sender": obj["user"]})
			continue
		}
		if githubTimeMs(it.UpdatedAt) < since || lookups >= maxEventLookups {
			continue
		}
		lookups++
		if _, terr := s.emitTimeline(ctx, slotUser, repo, full, it.Number, since, "issues", "issue", obj, issueTimeline); terr != nil {
			ok, err = false, terr
		}
	}
	if ok && !nm {
		st.EtagIssues = strPtr(etag)
	}
	errs = append(errs, err)

	// 评论：只合成新建的（编辑不回流）
	items, etag, nm, err = s.watchList(ctx, slotUser, fmt.Sprintf("/repos/%s/issues/comments?sort=updated&direction=asc&per_page=100&%s", full, q), deref(w.EtagComments))
	ok = err == nil
	for _, raw := range items {
		it, obj, derr := decodeRest(raw)
		if derr != nil {
			continue
		}
		seen(it.UpdatedAt)
		if githubTimeMs(it.CreatedAt) < since {
			continue
		}
		number := issueNumberOf(it.IssueURL)
		subj, have := subjects[number]
		if !have {
			var body json.RawMessage
			if _, _, gerr := s.watchGet(ctx, slotUser, fmt.Sprintf("/repos/%s/issues/%d", full, number), "", &body); gerr != nil {
				ok, err = false, gerr
				continue
			}
			if subj, derr = decodeObj(body); derr != nil {
				continue
			}
			subjects[number] = subj
		}
		s.emitRest(ctx, restGUID("issue_comment", "created", full, it.ID, it.CreatedAt), "issue_comment",
			map[string]any{"action": "created", "issue": subj, "comment": obj, "repository": repo, "sender": obj["user"]})
	}
	if ok && !nm {
		st.EtagComments = strPtr(etag)
	}
	errs = append(errs, err)

	// 打开的 PR：新建的合成 opened；其余有更新的查时间线（重开、转为可评审、转为草稿）
	items, etag, nm, err = s.watchList(ctx, slotUser, fmt.Sprintf("/repos/%s/pulls?state=open&sort=updated&direction=desc&per_page=100", full), deref(w.EtagPulls))
	ok = err == nil
	for _, raw := range items {
		it, obj, derr := decodeRest(raw)
		if derr != nil || githubTimeMs(it.UpdatedAt) < since {
			continue
		}
		seen(it.UpdatedAt)
		if githubTimeMs(it.CreatedAt) >= since {
			s.emitRest(ctx, restGUID("pull_request", "opened", full, it.ID, it.CreatedAt), "pull_request",
				map[string]any{"action": "opened", "number": it.Number, "pull_request": obj, "repository": repo, "sender": obj["user"]})
			continue
		}
		if lookups >= maxEventLookups {
			continue
		}
		lookups++
		if _, terr := s.emitTimeline(ctx, slotUser, repo, full, it.Number, since, "pull_request", "pull_request", obj, pullTimeline); terr != nil {
			ok, err = false, terr
		}
	}
	if ok && !nm {
		st.EtagPulls = strPtr(etag)
	}
	errs = append(errs, err)

	// push 触发、已完成的 workflow run；仓库没开 Actions（404 / 403）按没有处理
	var runs struct {
		WorkflowRuns []json.RawMessage `json:"workflow_runs"`
	}
	h, nm, err := s.watchGet(ctx, slotUser, fmt.Sprintf("/repos/%s/actions/runs?event=push&status=completed&per_page=30", full), deref(w.EtagRuns), &runs)
	if errKind(err) == kindNotFound || isStatus(err, http.StatusForbidden) {
		err = nil
	}
	for _, raw := range runs.WorkflowRuns {
		it, obj, derr := decodeRest(raw)
		if derr != nil || githubTimeMs(it.UpdatedAt) < since {
			continue
		}
		seen(it.UpdatedAt)
		who := obj["triggering_actor"]
		if who == nil {
			who = obj["actor"]
		}
		s.emitRest(ctx, restGUID("workflow_run", "completed", full, it.ID, it.UpdatedAt), "workflow_run",
			map[string]any{"action": "completed", "workflow_run": obj, "repository": repo, "sender": who})
	}
	if err == nil && !nm && h != nil {
		st.EtagRuns = strPtr(h.Get("ETag"))
	}
	errs = append(errs, err)

	if joined := errors.Join(errs...); joined != nil {
		return errors.Join(joined, s.st.Q.SetGithubRepoWatchState(ctx, st))
	}
	st.SinceAt = &next
	return s.st.Q.SetGithubRepoWatchState(ctx, st)
}

// issueNumberOf 取 issue API 地址（…/issues/<n>）末段的编号。
func issueNumberOf(u string) int64 {
	i := strings.LastIndex(u, "/")
	n, _ := strconv.ParseInt(u[i+1:], 10, 64)
	return n
}

// ---------- 对象级 ----------

// objectPoll 是一个对象级 watch 本轮的状态。
type objectPoll struct {
	w     db.GithubWatch
	repo  map[string]any
	since int64 // 本轮的游标（评论类接口的 since）
	next  int64 // 下一轮的游标：看到的最大时间
	st    db.SetGithubWatchStateParams
	subj  map[string]any // issue / PR 本体（按需读取）
	pr    *pullPayload   // 本轮读到的 PR 本体（仅 pull；304 时为 nil）
}

// pollObject 轮询一个对象级 watch：线程已归档或关联已取消时注销；首轮（没有游标）只建立基线。调用方持 watchMu。
func (s *Service) pollObject(ctx context.Context, w db.GithubWatch) error {
	key := db.DeleteGithubWatchParams{Kind: w.Kind, FullName: w.FullName, Number: w.Number}
	var live bool
	var err error
	if w.Kind == "pull" {
		_, live, err = s.pullTarget(ctx, w.RepoID, w.Number)
	} else {
		_, live, err = s.issueTarget(ctx, w.RepoID, w.Number)
	}
	if err != nil {
		return err
	}
	if !live {
		_, err := s.st.Q.DeleteGithubWatch(ctx, key)
		return err
	}
	now := s.watchBaseline()
	p := &objectPoll{w: w, st: db.SetGithubWatchStateParams{
		EtagIssue: w.EtagIssue, EtagComments: w.EtagComments, EtagReviews: w.EtagReviews,
		EtagReviewComments: w.EtagReviewComments, EtagChecks: w.EtagChecks, LastSeenAt: w.LastSeenAt,
		Kind: w.Kind, FullName: w.FullName, Number: w.Number,
	}}
	if w.LastSeenAt == nil {
		p.st.LastSeenAt = &now
		return s.st.Q.SetGithubWatchState(ctx, p.st)
	}
	if p.repo, err = s.watchRepoOf(ctx, w.Slot, w.RepoID, w.FullName); err != nil {
		return err
	}
	p.since, p.next = *w.LastSeenAt, *w.LastSeenAt
	var errs []error
	if w.Kind == "pull" {
		errs = append(errs, s.pollPullBody(ctx, p), s.pollComments(ctx, p), s.pollReviews(ctx, p), s.pollReviewComments(ctx, p), s.pollChecks(ctx, p))
	} else {
		errs = append(errs, s.pollIssueBody(ctx, p), s.pollComments(ctx, p))
	}
	if joined := errors.Join(errs...); joined != nil {
		return errors.Join(joined, s.st.Q.SetGithubWatchState(ctx, p.st))
	}
	p.st.LastSeenAt = &p.next
	return s.st.Q.SetGithubWatchState(ctx, p.st)
}

// subject 返回对象本体（PR 取 pulls/{n}，issue 取 issues/{n}）；本轮已读过时直接用。
func (s *Service) subject(ctx context.Context, p *objectPoll) (map[string]any, error) {
	if p.subj != nil {
		return p.subj, nil
	}
	path := fmt.Sprintf("/repos/%s/issues/%d", p.w.FullName, p.w.Number)
	if p.w.Kind == "pull" {
		path = fmt.Sprintf("/repos/%s/pulls/%d", p.w.FullName, p.w.Number)
	}
	var raw json.RawMessage
	if _, _, err := s.watchGet(ctx, p.w.Slot, path, "", &raw); err != nil {
		return nil, err
	}
	obj, err := decodeObj(raw)
	p.subj = obj
	return obj, err
}

// issueView 是评论事件里的 issue：PR 的本体补上 pull_request 标记（与 issue_comment 载荷一致）。
func issueView(kind string, subj map[string]any) map[string]any {
	if kind != "pull" {
		return subj
	}
	out := make(map[string]any, len(subj)+1)
	for k, v := range subj {
		out[k] = v
	}
	out["pull_request"] = map[string]any{"html_url": subj["html_url"]}
	return out
}

// markDone 按对象是否在终态写 done_at（已有的不覆盖；重开时清空）。
func (s *Service) markDone(ctx context.Context, w db.GithubWatch, terminal bool) error {
	if terminal == (w.DoneAt != nil) {
		return nil
	}
	var at *int64
	if terminal {
		now := s.nowMs()
		at = &now
	}
	return s.st.Q.SetGithubWatchDone(ctx, db.SetGithubWatchDoneParams{DoneAt: at, Kind: w.Kind, FullName: w.FullName, Number: w.Number})
}

// pollPullBody 读 PR 本体：与库里的 github_pulls 比较得出 action（关闭 / 合并、重开、推送、草稿切换、编辑），
// 合成 pull_request 事件。sender：合并取 merged_by，关闭 / 重开 / 草稿切换取时间线事件的 actor，其余取 PR 作者。
func (s *Service) pollPullBody(ctx context.Context, p *objectPoll) error {
	var raw json.RawMessage
	h, nm, err := s.watchGet(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/pulls/%d", p.w.FullName, p.w.Number), deref(p.w.EtagIssue), &raw)
	if err != nil || nm {
		return err
	}
	var pr pullPayload
	var extra struct {
		ID        int64           `json:"id"`
		UpdatedAt string          `json:"updated_at"`
		MergedBy  json.RawMessage `json:"merged_by"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return err
	}
	obj, err := decodeObj(raw)
	if err != nil {
		return err
	}
	p.subj = obj
	var prev *db.GithubPull
	if row, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams{RepoID: p.w.RepoID, Number: p.w.Number}); err == nil {
		prev = &row
	} else if !isNoRows(err) {
		return err
	}
	if action := pullAction(prev, pr); action != "" {
		who := obj["user"]
		switch {
		case action == "closed" && pr.merged() && obj["merged_by"] != nil:
			who = obj["merged_by"]
		case action == "closed" || action == "reopened" || action == "ready_for_review" || action == "converted_to_draft":
			if actor, err := s.lastActor(ctx, p, action); err != nil {
				return err
			} else if actor != nil {
				who = actor
			}
		}
		s.emitRest(ctx, restGUID("pull_request", action, p.w.FullName, extra.ID, extra.UpdatedAt), "pull_request",
			map[string]any{"action": action, "number": pr.Number, "pull_request": obj, "repository": p.repo, "sender": who})
	}
	if err := s.markDone(ctx, p.w, pullState(pr) != "open"); err != nil {
		return err
	}
	p.pr = &pr
	p.st.EtagIssue = strPtr(h.Get("ETag"))
	return nil
}

// pullAction 比较库里的 PR 与 REST 读到的 PR，返回合成的 pull_request action；没有变化返回空串。库里没有时按状态取
// closed 或 edited（onPullRequest 据此入库，已合并的设 shippedAt）。
func pullAction(prev *db.GithubPull, pr pullPayload) string {
	st := pullState(pr)
	if prev == nil {
		if st == "open" {
			return "edited"
		}
		return "closed"
	}
	switch {
	case st != "open" && prev.State == "open", st == "merged" && prev.State != "merged":
		return "closed"
	case st == "open" && prev.State != "open":
		return "reopened"
	case st != "open":
		return ""
	case pr.Head.SHA != prev.HeadSha:
		return "synchronize"
	case pr.Draft && !prev.Draft:
		return "converted_to_draft"
	case !pr.Draft && prev.Draft:
		return "ready_for_review"
	case pr.Title != prev.Title || pr.Base.Ref != prev.BaseRef:
		return "edited"
	}
	return ""
}

// lastActor 取时间线上与 action 对应的最近一个事件的 actor；没有返回 nil。
func (s *Service) lastActor(ctx context.Context, p *objectPoll, action string) (any, error) {
	name := action
	if action == "converted_to_draft" {
		name = "convert_to_draft"
	}
	items, _, _, err := s.watchList(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/issues/%d/events?per_page=100", p.w.FullName, p.w.Number), "")
	if err != nil {
		return nil, err
	}
	var actor any
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		if err == nil && it.Event == name {
			actor = obj["actor"]
		}
	}
	return actor, nil
}

// pollIssueBody 读 issue 本体：有变化时把游标之后的时间线事件（打标签、去标签、关闭、重开）合成为 issues 事件。
// 规则在 PR 上建的 issue 关联（如 fork 来的 PR）只轮询评论：PR 的状态变化对应 pull_request 事件，而这里只有 issue 视图。
func (s *Service) pollIssueBody(ctx context.Context, p *objectPoll) error {
	var raw json.RawMessage
	h, nm, err := s.watchGet(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/issues/%d", p.w.FullName, p.w.Number), deref(p.w.EtagIssue), &raw)
	if err != nil || nm {
		return err
	}
	var it restItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return err
	}
	obj, err := decodeObj(raw)
	if err != nil {
		return err
	}
	p.subj = obj
	if !it.isPull() {
		latest, err := s.emitTimeline(ctx, p.w.Slot, p.repo, p.w.FullName, p.w.Number, p.since, "issues", "issue", obj, issueTimeline)
		if err != nil {
			return err
		}
		p.next = max(p.next, latest)
	}
	if err := s.markDone(ctx, p.w, it.State == "closed"); err != nil {
		return err
	}
	p.st.EtagIssue = strPtr(h.Get("ETag"))
	return nil
}

// pollComments 读游标之后新建的会话评论，合成 issue_comment.created。
func (s *Service) pollComments(ctx context.Context, p *objectPoll) error {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&since=%s", p.w.FullName, p.w.Number, url.QueryEscape(restTime(p.since)))
	items, etag, nm, err := s.watchList(ctx, p.w.Slot, path, deref(p.w.EtagComments))
	if err != nil || nm {
		return err
	}
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		if err != nil || githubTimeMs(it.CreatedAt) < p.since {
			continue
		}
		subj, err := s.subject(ctx, p)
		if err != nil {
			return err
		}
		p.next = max(p.next, githubTimeMs(it.CreatedAt))
		s.emitRest(ctx, restGUID("issue_comment", "created", p.w.FullName, it.ID, it.CreatedAt), "issue_comment",
			map[string]any{"action": "created", "issue": issueView(p.w.Kind, subj), "comment": obj, "repository": p.repo, "sender": obj["user"]})
	}
	p.st.EtagComments = strPtr(etag)
	return nil
}

// pollReviews 读游标之后提交的评审（待提交的跳过），合成 pull_request_review.submitted。
func (s *Service) pollReviews(ctx context.Context, p *objectPoll) error {
	items, etag, nm, err := s.watchList(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", p.w.FullName, p.w.Number), deref(p.w.EtagReviews))
	if err != nil || nm {
		return err
	}
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		at := githubTimeMs(it.SubmittedAt)
		if err != nil || it.State == "PENDING" || it.SubmittedAt == "" || at < p.since {
			continue
		}
		subj, err := s.subject(ctx, p)
		if err != nil {
			return err
		}
		p.next = max(p.next, at)
		s.emitRest(ctx, restGUID("pull_request_review", "submitted", p.w.FullName, it.ID, it.SubmittedAt), "pull_request_review",
			map[string]any{"action": "submitted", "review": obj, "pull_request": subj, "repository": p.repo, "sender": obj["user"]})
	}
	p.st.EtagReviews = strPtr(etag)
	return nil
}

// pollReviewComments 读游标之后新建的行内评审评论，合成 pull_request_review_comment.created。
func (s *Service) pollReviewComments(ctx context.Context, p *objectPoll) error {
	path := fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100&since=%s", p.w.FullName, p.w.Number, url.QueryEscape(restTime(p.since)))
	items, etag, nm, err := s.watchList(ctx, p.w.Slot, path, deref(p.w.EtagReviewComments))
	if err != nil || nm {
		return err
	}
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		if err != nil || githubTimeMs(it.CreatedAt) < p.since {
			continue
		}
		subj, err := s.subject(ctx, p)
		if err != nil {
			return err
		}
		p.next = max(p.next, githubTimeMs(it.CreatedAt))
		s.emitRest(ctx, restGUID("pull_request_review_comment", "created", p.w.FullName, it.ID, it.CreatedAt), "pull_request_review_comment",
			map[string]any{"action": "created", "comment": obj, "pull_request": subj, "repository": p.repo, "sender": obj["user"]})
	}
	p.st.EtagReviewComments = strPtr(etag)
	return nil
}

// pollChecks 读打开的 PR 的 head 上的 check-runs 与 status（ETag 以换行拼接存一列）；任一有变化时合成 check_suite.completed，
// 交给 CI 汇总。guid 的时间段取两个新 ETag 的摘要。
func (s *Service) pollChecks(ctx context.Context, p *objectPoll) error {
	pull, err := s.st.Q.GetGithubPull(ctx, db.GetGithubPullParams{RepoID: p.w.RepoID, Number: p.w.Number})
	sha := ""
	if err == nil && pull.State == "open" {
		sha = pull.HeadSha
	} else if err != nil && !isNoRows(err) {
		return err
	}
	if p.pr != nil {
		sha = ""
		if pullState(*p.pr) == "open" {
			sha = p.pr.Head.SHA
		}
	}
	if sha == "" {
		return nil
	}
	oldRuns, oldStatus, _ := strings.Cut(deref(p.w.EtagChecks), "\n")
	base := fmt.Sprintf("/repos/%s/commits/%s", p.w.FullName, sha)
	h1, nm1, err := s.watchGet(ctx, p.w.Slot, base+"/check-runs?filter=latest&per_page=100", oldRuns, nil)
	if err != nil {
		return err
	}
	h2, nm2, err := s.watchGet(ctx, p.w.Slot, base+"/status", oldStatus, nil)
	if err != nil {
		return err
	}
	if nm1 && nm2 {
		return nil
	}
	newRuns, newStatus := oldRuns, oldStatus
	if !nm1 {
		newRuns = h1.Get("ETag")
	}
	if !nm2 {
		newStatus = h2.Get("ETag")
	}
	tag := newRuns + "\n" + newStatus
	sum := sha256.Sum256([]byte(tag))
	s.emitRest(ctx, restGUID("check_suite", "completed", p.w.FullName, sha, hex.EncodeToString(sum[:8])), "check_suite",
		map[string]any{"action": "completed", "check_suite": map[string]any{"head_sha": sha}, "repository": p.repo})
	p.st.EtagChecks = &tag
	return nil
}
