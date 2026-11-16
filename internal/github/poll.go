package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 投递日志轮询（M15 步骤 1.3）：GitHub 不可达 webhook 地址时，按 hookPollInterval 读每个 active 仓库 hook 的投递日志补漏。
// 投递日志接口单独计额（webhook_deliveries，500 次/小时，列表与详情各计 1 次），带 If-None-Match 的 304 不计数
// （docs/plan-m15.md 附录 B-3）；最近 hookRecentWebhook 内收到过 webhook 的 hook 放宽到 hookPollRelaxed。

// maxPollPages 是一轮里单个 hook 最多翻的投递日志页数。
const maxPollPages = 10

// hookDelivery 是投递日志列表里的一条。
type hookDelivery struct {
	ID    int64  `json:"id"`
	GUID  string `json:"guid"`
	Event string `json:"event"`
}

// pollHooks 轮询全部 active 仓库 hook 的投递日志；额度见底时暂停到 X-RateLimit-Reset。单个 hook 失败只记日志。
func (s *Service) pollHooks(ctx context.Context) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	now := s.now()
	if now.Before(s.hookPause) {
		return
	}
	rows, err := s.st.Q.ListActiveGithubHooks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取仓库 hook 失败", "err", err)
		}
		return
	}
	if len(rows) == 0 {
		return
	}
	if _, err := s.tokenFor(ctx, slotUser); err != nil {
		return // 我的账号不可用：等重新连接
	}
	polled := make(map[int64]time.Time, len(rows))
	for _, row := range rows {
		last := s.hookPolled[row.RepoID]
		polled[row.RepoID] = last
		if row.LastWebhookAt != nil && now.Sub(time.UnixMilli(*row.LastWebhookAt)) < hookRecentWebhook && now.Sub(last) < hookPollRelaxed {
			continue
		}
		if s.now().Before(s.hookPause) {
			continue
		}
		polled[row.RepoID] = now
		if err := s.pollHook(ctx, row); err != nil && ctx.Err() == nil {
			slog.Warn("github: 拉取 hook 投递日志失败", "repo", row.FullName, "err", err)
		}
	}
	s.hookPolled = polled // 顺带丢掉已删除 hook 的记录
}

// pollHook 拉取一个 hook 的投递日志，补齐 webhook 没送到的投递。首次只建立基线，不回放历史。调用方持 pollMu。
func (s *Service) pollHook(ctx context.Context, row db.GithubHook) error {
	if row.HookID == nil {
		return nil
	}
	base := fmt.Sprintf("/repos/%s/hooks/%d/deliveries", row.FullName, *row.HookID)
	etag := ""
	if row.DeliveriesEtag != nil {
		etag = *row.DeliveriesEtag
	}
	var items []hookDelivery
	h, err := s.deliveriesGet(ctx, base+"?per_page=100", etag, &items)
	if isStatus(err, http.StatusNotModified) {
		return nil // 没有新投递，也不计额度
	}
	if err != nil {
		return err
	}
	var newEtag *string
	if v := h.Get("ETag"); v != "" {
		newEtag = &v
	}
	cursor := row.DeliveryCursor
	if cursor == 0 {
		// 首次：游标设为当前最新的投递，之前的不回放
		next := int64(1)
		for _, it := range items {
			next = max(next, it.ID)
		}
		return s.setDeliveryCursor(ctx, row.RepoID, next, newEtag)
	}
	var fresh []hookDelivery
	next := ""
	for page := 0; ; page++ {
		older := false
		for _, it := range items {
			if it.ID > cursor {
				fresh = append(fresh, it)
			} else {
				older = true
			}
		}
		if next = nextLink(h); older || next == "" || page+1 >= maxPollPages {
			break
		}
		if s.now().Before(s.hookPause) {
			return nil // 额度见底：没翻到游标处，下轮从头再来（ETag 不更新）
		}
		items = nil
		if h, err = s.deliveriesGet(ctx, next, "", &items); err != nil {
			return err
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	seen := cursor
	for _, it := range fresh {
		exists, err := s.st.Q.GithubDeliveryExists(ctx, it.GUID)
		if err != nil {
			return err
		}
		if !exists {
			if s.now().Before(s.hookPause) {
				// 额度见底：已处理的部分落游标；ETag 清空，恢复后重新取列表
				return s.setDeliveryCursor(ctx, row.RepoID, seen, nil)
			}
			if err := s.pollDelivery(ctx, row, base, it); err != nil {
				return errors.Join(fmt.Errorf("delivery %d: %w", it.ID, err), s.setDeliveryCursor(ctx, row.RepoID, seen, nil))
			}
		}
		seen = max(seen, it.ID)
	}
	return s.setDeliveryCursor(ctx, row.RepoID, seen, newEtag)
}

func (s *Service) setDeliveryCursor(ctx context.Context, repoID, cursor int64, etag *string) error {
	return s.st.Q.SetGithubHookDeliveryCursor(ctx, db.SetGithubHookDeliveryCursorParams{
		DeliveryCursor: cursor, DeliveriesEtag: etag, RepoID: repoID,
	})
}

// pollDelivery 取一条投递的详情，按请求头里的 X-GitHub-Event 与 request.payload 受理（来源 poll）。
func (s *Service) pollDelivery(ctx context.Context, row db.GithubHook, base string, it hookDelivery) error {
	var detail struct {
		Event   string `json:"event"`
		Request struct {
			Headers map[string]any  `json:"headers"`
			Payload json.RawMessage `json:"payload"`
		} `json:"request"`
	}
	if _, err := s.deliveriesGet(ctx, fmt.Sprintf("%s/%d", base, it.ID), "", &detail); err != nil {
		return err
	}
	if len(detail.Request.Payload) == 0 || string(detail.Request.Payload) == "null" {
		return errors.New("github: 投递详情缺少 request.payload")
	}
	body, err := json.Marshal(detail.Request.Payload)
	if err != nil {
		return err
	}
	event := ""
	for k, v := range detail.Request.Headers {
		if strings.EqualFold(k, "X-GitHub-Event") {
			event, _ = v.(string)
		}
	}
	if event == "" {
		event = detail.Event
	}
	if event == "" {
		event = it.Event
	}
	s.ingest(ctx, it.GUID, event, body, "poll", row.RepoID)
	return nil
}

// deliveriesGet 以我的账号 GET 投递日志接口：etag 非空时带 If-None-Match（304 以 *ghError 返回，isStatus 可判）；
// 按响应的 X-RateLimit-* 记录额度，剩余低于 deliveriesQuotaMin 时暂停到 X-RateLimit-Reset。调用方持 pollMu。
func (s *Service) deliveriesGet(ctx context.Context, path, etag string, out any) (http.Header, error) {
	tok, err := s.tokenFor(ctx, slotUser)
	if err != nil {
		return nil, err
	}
	target := path
	if strings.HasPrefix(path, "/") {
		target = s.cfg.APIURL + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	h, err := s.send(req, "Bearer "+tok, out)
	s.noteDeliveriesQuota(h)
	if errKind(err) == kindReauth {
		if merr := s.markReauth(ctx, slotUser); merr != nil {
			return h, errors.Join(err, merr)
		}
	}
	return h, err
}

// noteDeliveriesQuota 按响应头记录投递日志额度：剩余低于 deliveriesQuotaMin 时暂停轮询到重置时刻。调用方持 pollMu。
func (s *Service) noteDeliveriesQuota(h http.Header) {
	if h == nil {
		return
	}
	left, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err != nil || left >= deliveriesQuotaMin {
		return
	}
	reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	s.hookPause = time.Unix(reset, 0)
	slog.Info("github: 投递日志额度见底，暂停轮询", "remaining", left, "until", s.hookPause)
}
