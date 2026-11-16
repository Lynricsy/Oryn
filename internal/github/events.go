package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// WebhookMaxBody 是 webhook 请求体上限（REST 层用）。
const WebhookMaxBody = webhookMaxBody

// jobQueueSize 是后台任务队列的缓冲。
const jobQueueSize = 256

// handler 处理一条投递。outcome 形如 "handled" / "ignored:<reason>"；err 非 nil 且 retryable(err) 时该投递稍后重试。
type handler func(ctx context.Context, action string, body []byte) (outcome string, err error)

// eventHandlers 是按事件名登记的处理器；各文件在 init 里用 on 登记，New 时绑定到服务实例。
var eventHandlers = map[string]func(s *Service, ctx context.Context, action string, body []byte) (string, error){}

// on 登记一个事件处理器（只在 init 里调用）。
func on(event string, h func(s *Service, ctx context.Context, action string, body []byte) (string, error)) {
	eventHandlers[event] = h
}

// bindHandlers 把登记的处理器绑定到 s。
func (s *Service) bindHandlers() map[string]handler {
	out := make(map[string]handler, len(eventHandlers))
	for ev, h := range eventHandlers {
		out[ev] = func(ctx context.Context, action string, body []byte) (string, error) { return h(s, ctx, action, body) }
	}
	return out
}

func init() {
	on("ping", func(*Service, context.Context, string, []byte) (string, error) { return "ignored:ping", nil })
	on("repository", (*Service).onRepository)
}

// HandleWebhook 校验并受理一次投递；返回 HTTP 状态码（M15 步骤 1.3）。
//
// 仓库 hook：按 X-GitHub-Hook-ID（十进制）查 github_hooks 行，用该行的 secret 验 X-Hub-Signature-256，
// 再要求 X-GitHub-Hook-Installation-Target-ID 等于该行的 repo_id；任一不满足返回 401。验签通过后记 last_webhook_at。
func (s *Service) HandleWebhook(ctx context.Context, h http.Header, body []byte) int {
	row, found, err := s.hookOfRequest(ctx, h)
	switch {
	case err != nil:
		slog.Warn("github: 读取仓库 hook 失败", "err", err)
		return http.StatusInternalServerError
	case !found:
		return http.StatusUnauthorized
	}
	secret, err := s.cfg.Box.Open(aadHookSecret(row.RepoID), row.SecretEnc)
	if err != nil {
		slog.Warn("github: 解密 hook secret 失败", "repo", row.FullName, "err", err)
		return http.StatusInternalServerError
	}
	if !validSignature(secret, h.Get("X-Hub-Signature-256"), body) ||
		h.Get("X-GitHub-Hook-Installation-Target-ID") != strconv.FormatInt(row.RepoID, 10) {
		return http.StatusUnauthorized
	}
	guid, event := h.Get("X-GitHub-Delivery"), h.Get("X-GitHub-Event")
	if guid == "" || event == "" {
		return http.StatusBadRequest
	}
	now := s.nowMs()
	if err := s.st.Q.TouchGithubHookWebhook(ctx, db.TouchGithubHookWebhookParams{LastWebhookAt: &now, RepoID: row.RepoID}); err != nil {
		slog.Warn("github: 记录 webhook 到达时间失败", "err", err)
	}
	s.ingest(ctx, guid, event, body, "webhook", row.RepoID)
	return http.StatusAccepted
}

// hookOfRequest 按 X-GitHub-Hook-ID 查 Oryn 管理的仓库 hook；头缺失、不是十进制或查不到时 found 为假。
func (s *Service) hookOfRequest(ctx context.Context, h http.Header) (row db.GithubHook, found bool, err error) {
	id, perr := strconv.ParseInt(h.Get("X-GitHub-Hook-ID"), 10, 64)
	if perr != nil || id <= 0 {
		return row, false, nil
	}
	row, err = s.st.Q.GetGithubHookByHookID(ctx, &id)
	if err != nil {
		if isNoRows(err) {
			return row, false, nil
		}
		return row, false, err
	}
	return row, len(row.SecretEnc) > 0, nil
}

// validSignature 校验 `sha256=<hex>` 形式的 HMAC-SHA256 签名。
func validSignature(secret []byte, header string, body []byte) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ingest 是 webhook 与轮询共用的入口：按 guid 去重、落库、入队。repoID 非 0 时把最近投递记到该仓库的 hook 行。
func (s *Service) ingest(ctx context.Context, guid, event string, body []byte, src string, repoID int64) {
	now := s.nowMs()
	n, err := s.st.Q.InsertGithubDelivery(ctx, db.InsertGithubDeliveryParams{
		Guid: guid, Event: event, Source: src, Payload: body, ReceivedAt: now,
	})
	if err != nil {
		slog.Warn("github: 记录投递失败", "guid", guid, "err", err)
		return
	}
	if n == 0 {
		return // 重复投递
	}
	if repoID != 0 {
		if err := s.st.Q.TouchGithubHookDelivery(ctx, db.TouchGithubHookDeliveryParams{
			LastDeliveryAt: &now, LastDeliverySrc: &src, RepoID: repoID,
		}); err != nil {
			slog.Warn("github: 记录最近投递失败", "err", err)
		}
	}
	s.enqueueDelivery(guid, event, body)
}

// enqueue 把任务推进队列；队列满时放弃并返回 false（不阻塞）。
func (s *Service) enqueue(job func(ctx context.Context)) bool {
	select {
	case s.jobs <- job:
		return true
	default:
		return false
	}
}

// enqueueDelivery 把一条投递的处理推进队列；已在队列或处理中的 guid 不重复入队。
// 队列满时放弃：该行仍是 pending，留给下个恢复周期。
func (s *Service) enqueueDelivery(guid, event string, body []byte) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.inflight[guid] {
		return
	}
	s.inflight[guid] = true
	if !s.enqueue(func(ctx context.Context) { s.process(ctx, guid, event, body) }) {
		delete(s.inflight, guid)
		slog.Warn("github: 任务队列已满，投递留待恢复", "guid", guid)
	}
}

// recoverPending 把库里仍为 pending、且不在队列中的投递重新入队。
func (s *Service) recoverPending(ctx context.Context) {
	rows, err := s.st.Q.ListPendingGithubDeliveries(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 读取待处理投递失败", "err", err)
		}
		return
	}
	for _, r := range rows {
		s.enqueueDelivery(r.Guid, r.Event, r.Payload)
	}
}

// process 处理一条投递并写回结果。
func (s *Service) process(ctx context.Context, guid, event string, body []byte) {
	defer func() {
		s.qmu.Lock()
		delete(s.inflight, guid)
		s.qmu.Unlock()
	}()
	h, ok := s.handlers[event]
	if !ok {
		s.finish(ctx, guid, "ignored:event")
		return
	}
	var head struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		s.finish(ctx, guid, "error:"+clip("invalid payload: "+err.Error(), 200))
		return
	}
	outcome, err := h(ctx, head.Action, body)
	if err == nil {
		s.finish(ctx, guid, outcome)
		return
	}
	if ctx.Err() != nil {
		return // 停机中：保持 pending，重启后恢复
	}
	if retryable(err) {
		n, berr := s.st.Q.BumpGithubDeliveryAttempts(ctx, guid)
		if berr != nil {
			slog.Warn("github: 记录投递重试次数失败", "guid", guid, "err", berr)
			return
		}
		if n < maxDeliveryAttempts {
			slog.Info("github: 投递处理失败，稍后重试", "guid", guid, "event", event, "attempts", n, "err", err)
			return
		}
	}
	slog.Warn("github: 投递处理失败", "guid", guid, "event", event, "err", err)
	s.finish(ctx, guid, "error:"+clip(err.Error(), 200))
}

func (s *Service) finish(ctx context.Context, guid, outcome string) {
	if err := s.st.Q.FinishGithubDelivery(ctx, db.FinishGithubDeliveryParams{Outcome: outcome, Guid: guid}); err != nil && ctx.Err() == nil {
		slog.Warn("github: 写回投递结果失败", "guid", guid, "err", err)
	}
}

// clip 把字符串截到至多 n 个字符。
func clip(v string, n int) string {
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	r := []rune(v)
	return string(r[:n])
}

// Run 运行后台循环直到 ctx 结束：任务队列、恢复待处理投递与轮询 hook 投递日志、每 hookSyncInterval 校正仓库 hook、回流、每小时清理。
func (s *Service) Run(ctx context.Context) {
	const loops = 5
	done := make(chan struct{}, loops)
	loop := func(every time.Duration, now bool, f func(ctx context.Context)) {
		defer func() { done <- struct{}{} }()
		t := time.NewTicker(every)
		defer t.Stop()
		if now {
			f(ctx)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f(ctx)
			}
		}
	}
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-s.jobs:
				job(ctx)
			}
		}
	}()
	go loop(s.cfg.PollInterval, true, func(ctx context.Context) {
		s.recoverPending(ctx)
		s.pollHooks(ctx)
	})
	go loop(hookSyncInterval, true, func(ctx context.Context) {
		// 先按仓库地址重新绑定（跟随改名、重试此前读不到的仓库），再校正 hook
		if err := errors.Join(s.BindProjects(ctx), s.ResyncHooks(ctx)); err != nil && ctx.Err() == nil {
			slog.Warn("github: 同步项目绑定与仓库 hook 失败", "err", err)
		}
	})
	go loop(s.cfg.FlushInterval, false, s.flush)
	go loop(time.Hour, true, s.cleanup)
	for range loops {
		<-done
	}
}

// cleanup 是每小时的清理：过期的一次性 state、超过保留期的投递记录、已投递的回流行、自身写入记录与审计。
func (s *Service) cleanup(ctx context.Context) {
	now := s.now()
	if err := s.st.Q.DeleteExpiredGithubStates(ctx, now.UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理过期 state 失败", "err", err)
	}
	if err := s.st.Q.DeleteGithubDeliveriesBefore(ctx, now.Add(-deliveryRetention).UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理投递记录失败", "err", err)
	}
	before := now.Add(-deliveryRetention).UnixMilli()
	if err := s.st.Q.DeleteGithubOutboxBefore(ctx, &before); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理回流记录失败", "err", err)
	}
	s.cleanupSelfWrites(ctx, now)
}

// ---------- 本步的处理器 ----------

type senderPayload struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (s *Service) onRepository(ctx context.Context, action string, body []byte) (string, error) {
	if action != "renamed" && action != "transferred" {
		return "ignored:action", nil
	}
	var p struct {
		Repository repoPayload `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	if p.Repository.ID == 0 || p.Repository.FullName == "" {
		return "ignored:payload", nil
	}
	if err := s.followRename(ctx, p.Repository.ID, p.Repository.FullName); err != nil {
		return "", err
	}
	if action == "transferred" {
		prjs, err := s.st.Q.ListProjectsByGithubRepoAny(ctx, &p.Repository.ID)
		if err != nil {
			return "", err
		}
		for _, prj := range prjs {
			if err := s.BindProject(ctx, prj.ID); err != nil {
				return "", err
			}
		}
	}
	return "handled", nil
}
