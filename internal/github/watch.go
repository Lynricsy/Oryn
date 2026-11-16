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
//   - 仓库级 watch（github_repo_watches）：已绑定、hook 不是 active、且有启用规则的仓库，轮询 issue 列表（含 PR：新建、
//     打标签、关闭、重开等）、新评论与 push 触发的 workflow run；以我的账号的令牌轮询。
//   - 对象级 watch（github_watches）：线程关联的 issue / PR（含向上游开的 PR），所在仓库 hook 不是 active 时轮询对象本身、
//     评论、评审、行内评审评论与 head 的 CI；令牌取线程 orb 的槽位。
//
// 请求都带 If-None-Match（304 不产生事件、不计额度）。变化合成与 webhook 载荷同形的内部事件，guid 为
// rest:<事件>.<action>:<owner/repo 小写>:<对象 id>:<时间>，经 ingest(…, "rest") 交给与 webhook 相同的处理器；
// 仓库级与对象级对同一变化合成的 guid 相同，重叠时只处理一次。单轮读不完时进度可以续跑（见 pollRepo）。事件表见
// docs/plan-m15.md 附录 C。

const (
	maxWatchPages   = 5  // 一个列表接口一轮最多翻的页数
	maxEventLookups = 20 // 仓库级 watch 一轮最多发的逐项查询（时间线、PR 本体）次数
)

// errWatchTruncated 表示一个只能从头或从尾读的列表（时间线、评审）在 maxWatchPages 页内没能读到游标处：本轮不提交游标，
// 下一轮重读。只在一个对象在一个轮询间隔里新增几百条记录时出现。
var errWatchTruncated = errors.New("github: 列表超过单轮页数上限，游标之后还有未读的页")

// watchLookKey 标识仓库级 watch 已经逐项查询过的一个 (仓库, 编号, updated_at)。
type watchLookKey struct {
	repoID, number, updatedAt int64
}

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

// backfillWatches 为还没有对象级 watch 的线程关联补登记（升级前建立的关联、hook 从 active 变为不可用的仓库）：
// 线程未归档，PR 未进入终态、issue 任务不在终态。issue 关联也要补：仓库级 watch 只在仓库还有启用规则时存在，规则停用后
// 已有 issue 线程的回流只能靠对象级 watch。随 hookSyncInterval 运行。
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
		if live, err := s.threadLive(ctx, r.ThreadID); err != nil {
			return err
		} else if live {
			s.registerWatch(ctx, "pull", r.RepoID, r.RepoFullName, r.Number, r.ThreadID)
		}
	}
	issues, err := s.st.Q.ListThreadIssues(ctx)
	if err != nil {
		return err
	}
	// 已有的 issue watch 按仓库 id 查：规则建的 watch 用载荷里的 full_name，与 repo_url 的大小写可能不同
	type repoInfo struct {
		full    string
		watched map[int64]bool
	}
	repos := map[int64]*repoInfo{}
	for _, ti := range issues {
		if issueTaskTerminal(ti.State) {
			continue
		}
		ri := repos[ti.RepoID]
		if ri == nil {
			full, err := s.repoFullName(ctx, ti.RepoID)
			if err != nil {
				return err
			}
			ws, err := s.st.Q.ListGithubWatchesByRepo(ctx, ti.RepoID)
			if err != nil {
				return err
			}
			ri = &repoInfo{full: full, watched: map[int64]bool{}}
			for _, w := range ws {
				if w.Kind == "issue" {
					ri.watched[w.Number] = true
				}
			}
			repos[ti.RepoID] = ri
		}
		if ri.full == "" || ri.watched[ti.Number] {
			continue // 仓库已没有绑定的项目（读不到名字，也无从轮询），或已有 watch
		}
		if live, err := s.threadLive(ctx, ti.ThreadID); err != nil {
			return err
		} else if live {
			s.registerWatch(ctx, "issue", ti.RepoID, ri.full, ti.Number, ti.ThreadID)
		}
	}
	return nil
}

// threadLive 报告线程是否存在且未归档。
func (s *Service) threadLive(ctx context.Context, threadID string) (bool, error) {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return t.ArchivedAt == nil, nil
}

// issueTaskTerminal 报告 issue 任务是否已在终态（不再需要回流）。issue 关闭后由 4.3 的状态机置为 closed，
// 已退场的 watch 因此不会被补登记复活。
func issueTaskTerminal(state string) bool {
	return state == "closed" || state == "merged" || state == "abandoned"
}

// repoFullName 取仓库的 owner/repo：任一绑定了它的项目（含已归档）的 repo_url；都没有时返回空串。
func (s *Service) repoFullName(ctx context.Context, repoID int64) (string, error) {
	prjs, err := s.st.Q.ListProjectsByGithubRepoAny(ctx, &repoID)
	if err != nil {
		return "", err
	}
	for _, p := range prjs {
		if full, ok := githubRepoPath(p.RepoUrl); ok {
			return full, nil
		}
	}
	return "", nil
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

// watchList 拉取一个按时间正序、带 since 的列表：第一页带 If-None-Match（304 时 notModified 为真），其后按 Link 翻页
// （至多 maxWatchPages 页）。返回第一页的 ETag；more 为真表示翻满页数后仍有下一页：本轮没读完，调用方要从读到的最后
// 一项续上，不能提交 ETag 与越过它的游标。调用方持 watchMu。
func (s *Service) watchList(ctx context.Context, slot, path, etag string) (items []json.RawMessage, newEtag string, more, notModified bool, err error) {
	var page []json.RawMessage
	h, nm, err := s.watchGet(ctx, slot, path, etag, &page)
	if err != nil || nm {
		return nil, etag, false, nm, err
	}
	newEtag, items = h.Get("ETag"), page
	next := nextLink(h)
	for i := 1; i < maxWatchPages && next != ""; i++ {
		page = nil
		if h, _, err = s.watchGet(ctx, slot, next, "", &page); err != nil {
			return nil, etag, false, false, err
		}
		items = append(items, page...)
		next = nextLink(h)
	}
	return items, newEtag, next != "", false, nil
}

// watchListTail 读一个按时间正序、没有 since 参数的列表（时间线、评审）里游标之后的部分：第一页之后还有页时，按 Link 的
// last / prev 从最后一页往前读（连同第一页至多 maxWatchPages 页），直到读到早于 since 的项或与第一页接上。at 取一项的
// 时间（0 表示不参与判断）。complete 为假表示中间还有没读到、可能晚于 since 的页。只有整个列表就一页时才返回 ETag
// （多页时第一页的 ETag 反映不了末尾的变化）。调用方持 watchMu。
func (s *Service) watchListTail(ctx context.Context, slot, path, etag string, since int64, at func(restItem) int64) (items []json.RawMessage, newEtag string, notModified, complete bool, err error) {
	var first []json.RawMessage
	h, nm, err := s.watchGet(ctx, slot, path, etag, &first)
	if err != nil || nm {
		return nil, etag, nm, err == nil, err
	}
	if nextLink(h) == "" {
		return first, h.Get("ETag"), false, true, nil
	}
	var tail [][]json.RawMessage
	cur := linkRel(h, "last")
	for i := 1; i < maxWatchPages && cur != ""; i++ {
		var page []json.RawMessage
		ph, _, err := s.watchGet(ctx, slot, cur, "", &page)
		if err != nil {
			return nil, "", false, false, err
		}
		tail = append(tail, page)
		prev := linkRel(ph, "prev")
		if prev == "" || pageOf(prev) <= 1 {
			complete = true // 与第一页接上
			break
		}
		if len(page) > 0 {
			if it, _, derr := decodeRest(page[0]); derr == nil && at(it) != 0 && at(it) < since {
				complete = true // 这一页之前的都早于游标
				break
			}
		}
		cur = prev
	}
	items = first
	for i := len(tail) - 1; i >= 0; i-- {
		items = append(items, tail[i]...)
	}
	return items, "", false, complete, nil
}

// linkRel 取 Link 头里给定 rel 的地址；没有返回空串。
func linkRel(h http.Header, rel string) string {
	for _, v := range h.Values("Link") {
		for _, part := range strings.Split(v, ",") {
			u, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if ok && strings.Contains(params, `rel="`+rel+`"`) {
				return strings.Trim(strings.TrimSpace(u), "<>")
			}
		}
	}
	return ""
}

// pageOf 取分页地址里的 page 参数；没有或无法解析时为 1。
func pageOf(u string) int {
	pu, err := url.Parse(u)
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(pu.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
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
// sender 取事件的 actor。subjKey 是载荷里 subject 的键（issue / pull_request）。时间线读不到游标处时返回
// errWatchTruncated（已读到的仍会合成，靠 guid 去重）。调用方持 watchMu。
func (s *Service) emitTimeline(ctx context.Context, slot string, repo map[string]any, full string, number, since int64,
	event, subjKey string, subject map[string]any, table map[string]string,
) (latest int64, err error) {
	items, _, _, complete, err := s.watchListTail(ctx, slot, fmt.Sprintf("/repos/%s/issues/%d/events?per_page=100", full, number), "", since,
		func(it restItem) int64 { return githubTimeMs(it.CreatedAt) })
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
	if !complete {
		return latest, fmt.Errorf("%w: %s#%d 的时间线", errWatchTruncated, full, number)
	}
	return latest, nil
}

// ---------- 仓库级 ----------

// repoItemCost 是仓库级 issue 列表里一项要发的逐项查询数：新 issue 0；新 PR 读本体 1；有更新的旧 issue 查时间线 1；
// 有更新的打开的旧 PR 读本体加查时间线 2；已关闭的旧 PR 0（状态变化由对象级 watch 合成）。
func repoItemCost(it restItem, since int64) int {
	created := githubTimeMs(it.CreatedAt) >= since
	switch {
	case !it.isPull() && created:
		return 0
	case !it.isPull(), created:
		return 1
	case it.State == "open":
		return 2
	}
	return 0
}

// pollRepoItem 处理仓库级 issue 列表里的一项（issue 或 PR），合成 opened 或时间线事件。调用方持 watchMu。
func (s *Service) pollRepoItem(ctx context.Context, repo map[string]any, full string, since int64, it restItem, obj map[string]any) error {
	created := githubTimeMs(it.CreatedAt) >= since
	if !it.isPull() {
		if created {
			s.emitRest(ctx, restGUID("issues", "opened", full, it.ID, it.CreatedAt), "issues",
				map[string]any{"action": "opened", "issue": obj, "repository": repo, "sender": obj["user"]})
			return nil
		}
		_, err := s.emitTimeline(ctx, slotUser, repo, full, it.Number, since, "issues", "issue", obj, issueTimeline)
		return err
	}
	if !created && it.State != "open" {
		return nil
	}
	var raw json.RawMessage
	if _, _, err := s.watchGet(ctx, slotUser, fmt.Sprintf("/repos/%s/pulls/%d", full, it.Number), "", &raw); err != nil {
		return err
	}
	var pr restItem
	if err := json.Unmarshal(raw, &pr); err != nil {
		return err
	}
	prObj, err := decodeObj(raw)
	if err != nil {
		return err
	}
	if created {
		s.emitRest(ctx, restGUID("pull_request", "opened", full, pr.ID, pr.CreatedAt), "pull_request",
			map[string]any{"action": "opened", "number": it.Number, "pull_request": prObj, "repository": repo, "sender": prObj["user"]})
		return nil
	}
	_, err = s.emitTimeline(ctx, slotUser, repo, full, it.Number, since, "pull_request", "pull_request", prObj, pullTimeline)
	return err
}

// pollRepo 轮询一个仓库级 watch。首轮只建立 since 基线。
//
// 进度可以续跑：issue 列表（含 PR）与评论列表都按更新时间正序读，逐项查询的额度（maxEventLookups）用完、或列表翻满
// maxWatchPages 页还有下一页时，本轮停在第一个没处理的项，since 只推进到它的 updated_at，该列表的 ETag 清空（否则下一轮
// 同样的请求会得到 304 而跳过剩余项）。since 含端点，重复处理的项靠 guid 去重；停下的项与 since 同一秒时不受额度限制，
// 保证同一秒内的大批变化也能推进。任一请求出错时不推进 since。调用方持 watchMu。
func (s *Service) pollRepo(ctx context.Context, w db.GithubRepoWatch) error {
	now := s.nowMs()
	st := db.SetGithubRepoWatchStateParams{
		EtagIssues: w.EtagIssues, EtagComments: w.EtagComments, EtagRuns: w.EtagRuns,
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
	seen := func(at int64) { next = max(next, at) }
	hold := int64(-1) // 没读完的列表的续点（取最小）；-1 表示都读完了
	holdAt := func(at int64) {
		if hold < 0 || at < hold {
			hold = at
		}
	}
	lookups := 0
	subjects := map[int64]map[string]any{}
	var errs []error

	// issue 与 PR：新建的合成 opened；其余有更新的查时间线（打标签、关闭、重开；PR 的重开、转为可评审、转为草稿）
	items, etag, more, nm, err := s.watchList(ctx, slotUser, fmt.Sprintf("/repos/%s/issues?state=all&sort=updated&direction=asc&per_page=100&%s", full, q), deref(w.EtagIssues))
	stopped, listMax := false, int64(-1)
	for _, raw := range items {
		it, obj, derr := decodeRest(raw)
		if derr != nil {
			continue
		}
		at := githubTimeMs(it.UpdatedAt)
		if at < since {
			continue
		}
		subjects[it.Number] = obj
		cost := repoItemCost(it, since)
		key := watchLookKey{repoID: w.RepoID, number: it.Number, updatedAt: at}
		if _, done := s.watchLooked[key]; done {
			cost = 0 // 上一轮（游标停在同一秒）已查过
		} else if cost > 0 && lookups+cost > maxEventLookups && at > since {
			holdAt(at) // 额度用完：下一轮从这一项续上
			stopped = true
			break
		}
		if cost > 0 {
			lookups += cost
			if perr := s.pollRepoItem(ctx, repo, full, since, it, obj); perr != nil {
				err = errors.Join(err, perr)
			} else {
				if s.watchLooked == nil {
					s.watchLooked = map[watchLookKey]struct{}{}
				}
				s.watchLooked[key] = struct{}{}
			}
		} else if _, done := s.watchLooked[key]; !done {
			_ = s.pollRepoItem(ctx, repo, full, since, it, obj) // 不发请求：新 issue 合成 opened
		}
		seen(at)
		listMax = max(listMax, at)
	}
	if more && !stopped && listMax >= 0 {
		holdAt(listMax) // 翻满页数：下一轮从读到的最后一项续上
	}
	more = more || stopped

	switch {
	case more:
		st.EtagIssues = nil
	case err == nil && !nm:
		st.EtagIssues = strPtr(etag)
	}
	errs = append(errs, err)

	// 评论：只合成新建的（编辑不回流）
	items, etag, more, nm, err = s.watchList(ctx, slotUser, fmt.Sprintf("/repos/%s/issues/comments?sort=updated&direction=asc&per_page=100&%s", full, q), deref(w.EtagComments))
	listMax = -1
	for _, raw := range items {
		it, obj, derr := decodeRest(raw)
		if derr != nil {
			continue
		}
		at := githubTimeMs(it.UpdatedAt)
		seen(at)
		listMax = max(listMax, at)
		if githubTimeMs(it.CreatedAt) < since {
			continue
		}
		number := issueNumberOf(it.IssueURL)
		subj, have := subjects[number]
		if !have {
			var body json.RawMessage
			if _, _, gerr := s.watchGet(ctx, slotUser, fmt.Sprintf("/repos/%s/issues/%d", full, number), "", &body); gerr != nil {
				err = errors.Join(err, gerr)
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
	if more && listMax >= 0 {
		holdAt(listMax)
	}
	switch {
	case more:
		st.EtagComments = nil
	case err == nil && !nm:
		st.EtagComments = strPtr(etag)
	}
	errs = append(errs, err)

	// push 触发、已完成的 workflow run（按更新时间倒序，翻到早于 since 的为止）；仓库没开 Actions（404 / 403）按没有处理
	path := fmt.Sprintf("/repos/%s/actions/runs?event=push&status=completed&per_page=100", full)
	for page := 0; page < maxWatchPages && path != ""; page++ {
		var runs struct {
			WorkflowRuns []json.RawMessage `json:"workflow_runs"`
		}
		etagIn := ""
		if page == 0 {
			etagIn = deref(w.EtagRuns)
		}
		h, nm, err := s.watchGet(ctx, slotUser, path, etagIn, &runs)
		if errKind(err) == kindNotFound || isStatus(err, http.StatusForbidden) {
			path = ""
			break
		}
		if err != nil {
			errs = append(errs, err)
			path = ""
			break
		}
		if nm {
			path = ""
			break
		}
		if page == 0 {
			st.EtagRuns = strPtr(h.Get("ETag"))
		}
		path = nextLink(h)
		for _, raw := range runs.WorkflowRuns {
			it, obj, derr := decodeRest(raw)
			if derr != nil {
				continue
			}
			if githubTimeMs(it.UpdatedAt) < since {
				path = "" // 之后的页都更早
				continue
			}
			seen(githubTimeMs(it.UpdatedAt))
			who := obj["triggering_actor"]
			if who == nil {
				who = obj["actor"]
			}
			s.emitRest(ctx, restGUID("workflow_run", "completed", full, it.ID, it.UpdatedAt), "workflow_run",
				map[string]any{"action": "completed", "workflow_run": obj, "repository": repo, "sender": who})
		}
	}
	if path != "" {
		// 一个轮询间隔里完成了超过 maxWatchPages 页的 run：倒序列表续不上，更早的那部分不再合成
		st.EtagRuns = nil
		slog.Warn("github: 轮询期间完成的 workflow run 超过单轮页数上限，较早的 run 未合成事件", "repo", full)
	}

	if joined := errors.Join(errs...); joined != nil {
		return errors.Join(joined, s.st.Q.SetGithubRepoWatchState(ctx, st))
	}
	if hold >= 0 {
		next = min(next, hold)
	}
	st.SinceAt = &next
	for k := range s.watchLooked {
		if k.repoID == w.RepoID && k.updatedAt < next {
			delete(s.watchLooked, k) // 游标已越过：不会再列出
		}
	}
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
	hold  int64 // 没读完的列表的续点（取最小，-1 表示都读完了）：游标最多推进到这里
	st    db.SetGithubWatchStateParams
	subj  map[string]any // issue / PR 本体（按需读取）
	pr    *pullPayload   // 本轮读到的 PR 本体（仅 pull；304 时为 nil）
}

// holdAt 记下一个没读完的列表的续点。
func (p *objectPoll) holdAt(at int64) {
	if p.hold < 0 || at < p.hold {
		p.hold = at
	}
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
	p.since, p.next, p.hold = *w.LastSeenAt, *w.LastSeenAt, -1
	var errs []error
	if w.Kind == "pull" {
		errs = append(errs, s.pollPullBody(ctx, p), s.pollComments(ctx, p), s.pollReviews(ctx, p), s.pollReviewComments(ctx, p), s.pollChecks(ctx, p))
	} else {
		errs = append(errs, s.pollIssueBody(ctx, p), s.pollComments(ctx, p))
	}
	if joined := errors.Join(errs...); joined != nil {
		return errors.Join(joined, s.st.Q.SetGithubWatchState(ctx, p.st))
	}
	if p.hold >= 0 {
		p.next = min(p.next, p.hold)
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
	// 最近的事件在列表末尾：从尾部读
	items, _, _, _, err := s.watchListTail(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/issues/%d/events?per_page=100", p.w.FullName, p.w.Number), "", 0,
		func(restItem) int64 { return 0 })
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
	items, etag, more, nm, err := s.watchList(ctx, p.w.Slot, path, deref(p.w.EtagComments))
	if err != nil || nm {
		return err
	}
	return s.emitCommentList(ctx, p, items, more, &p.st.EtagComments, etag, func(it restItem, obj, subj map[string]any) {
		s.emitRest(ctx, restGUID("issue_comment", "created", p.w.FullName, it.ID, it.CreatedAt), "issue_comment",
			map[string]any{"action": "created", "issue": issueView(p.w.Kind, subj), "comment": obj, "repository": p.repo, "sender": obj["user"]})
	})
}

// emitCommentList 把带 since 的评论列表（按创建时间正序）里游标之后新建的项交给 emit；列表没读完（more）时从读到的最后
// 一项续上，并清空该列表的 ETag（否则下一轮同样的请求会 304 而跳过剩余项）。
func (s *Service) emitCommentList(ctx context.Context, p *objectPoll, items []json.RawMessage, more bool, etagField **string, etag string,
	emit func(it restItem, obj, subj map[string]any),
) error {
	last := int64(-1)
	for _, raw := range items {
		it, obj, err := decodeRest(raw)
		if err != nil {
			continue
		}
		at := githubTimeMs(it.CreatedAt)
		last = max(last, at)
		if at < p.since {
			continue
		}
		subj, err := s.subject(ctx, p)
		if err != nil {
			return err
		}
		p.next = max(p.next, at)
		emit(it, obj, subj)
	}
	if more {
		if last >= 0 {
			p.holdAt(last)
		}
		*etagField = nil
		return nil
	}
	*etagField = strPtr(etag)
	return nil
}

// pollReviews 读游标之后提交的评审（待提交的跳过），合成 pull_request_review.submitted。
func (s *Service) pollReviews(ctx context.Context, p *objectPoll) error {
	// 评审没有 since 参数、按提交顺序排列：从尾部读到游标处
	items, etag, nm, complete, err := s.watchListTail(ctx, p.w.Slot, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", p.w.FullName, p.w.Number), deref(p.w.EtagReviews), p.since,
		func(it restItem) int64 { return githubTimeMs(it.SubmittedAt) })
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
	if !complete {
		return fmt.Errorf("%w: %s#%d 的评审", errWatchTruncated, p.w.FullName, p.w.Number)
	}
	p.st.EtagReviews = nil // 多页时没有可用的 ETag
	if etag != "" {
		p.st.EtagReviews = strPtr(etag)
	}
	return nil
}

// pollReviewComments 读游标之后新建的行内评审评论，合成 pull_request_review_comment.created。
func (s *Service) pollReviewComments(ctx context.Context, p *objectPoll) error {
	path := fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100&since=%s", p.w.FullName, p.w.Number, url.QueryEscape(restTime(p.since)))
	items, etag, more, nm, err := s.watchList(ctx, p.w.Slot, path, deref(p.w.EtagReviewComments))
	if err != nil || nm {
		return err
	}
	return s.emitCommentList(ctx, p, items, more, &p.st.EtagReviewComments, etag, func(it restItem, obj, subj map[string]any) {
		s.emitRest(ctx, restGUID("pull_request_review_comment", "created", p.w.FullName, it.ID, it.CreatedAt), "pull_request_review_comment",
			map[string]any{"action": "created", "comment": obj, "pull_request": subj, "repository": p.repo, "sender": obj["user"]})
	})
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
