package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
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
	on("installation", (*Service).onInstallation)
	on("installation_repositories", (*Service).onInstallation)
	on("github_app_authorization", (*Service).onAppAuthorization)
	on("repository", (*Service).onRepository)
}

// HandleWebhook 校验签名并受理一次投递；返回 HTTP 状态码。
func (s *Service) HandleWebhook(ctx context.Context, h http.Header, body []byte) int {
	app, err := s.st.Q.GetGithubApp(ctx)
	if err != nil {
		if isNoRows(err) {
			return http.StatusNotFound
		}
		slog.Warn("github: 读取 App 失败", "err", err)
		return http.StatusInternalServerError
	}
	secret, err := s.cfg.Box.Open(aadWebhookSecret, app.WebhookSecretEnc)
	if err != nil {
		slog.Warn("github: 解密 webhook secret 失败", "err", err)
		return http.StatusInternalServerError
	}
	sig, ok := strings.CutPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
	if !ok {
		return http.StatusUnauthorized
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return http.StatusUnauthorized
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return http.StatusUnauthorized
	}
	guid, event := h.Get("X-GitHub-Delivery"), h.Get("X-GitHub-Event")
	if guid == "" || event == "" {
		return http.StatusBadRequest
	}
	s.ingest(ctx, guid, event, body, "webhook")
	return http.StatusAccepted
}

// ingest 是 webhook 与轮询共用的入口：去重、落库、入队。
func (s *Service) ingest(ctx context.Context, guid, event string, body []byte, src string) {
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
	if err := s.st.Q.TouchGithubDelivery(ctx, db.TouchGithubDeliveryParams{LastDeliveryAt: &now, LastDeliverySrc: &src}); err != nil {
		slog.Warn("github: 记录最近投递失败", "err", err)
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

// Run 运行后台循环直到 ctx 结束：任务队列、恢复与轮询、回流、每小时清理。
func (s *Service) Run(ctx context.Context) {
	done := make(chan struct{}, 4)
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
		if err := s.pollOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("github: 拉取投递日志失败", "err", err)
		}
	})
	go loop(s.cfg.FlushInterval, false, s.flush)
	go loop(time.Hour, true, s.cleanup)
	for range 4 {
		<-done
	}
}

// cleanup 是每小时的清理：过期的一次性 state、超过保留期的投递记录。
func (s *Service) cleanup(ctx context.Context) {
	now := s.now()
	if err := s.st.Q.DeleteExpiredGithubStates(ctx, now.UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理过期 state 失败", "err", err)
	}
	if err := s.st.Q.DeleteGithubDeliveriesBefore(ctx, now.Add(-deliveryRetention).UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("github: 清理投递记录失败", "err", err)
	}
}

// flush 投递到期的回流消息（步骤 6.1 实现）。
func (s *Service) flush(context.Context) {}

// ---------- 本步的处理器 ----------

type senderPayload struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (s *Service) onInstallation(ctx context.Context, _ string, _ []byte) (string, error) {
	if err := s.SyncInstallations(ctx); err != nil {
		return "", err
	}
	return "handled", nil
}

func (s *Service) onAppAuthorization(ctx context.Context, action string, body []byte) (string, error) {
	if action != "revoked" {
		return "ignored:action", nil
	}
	var p struct {
		Sender senderPayload `json:"sender"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	rows, err := s.st.Q.ListGithubIdentities(ctx)
	if err != nil {
		return "", err
	}
	hit := false
	for _, id := range rows {
		if !strings.EqualFold(id.Login, p.Sender.Login) {
			continue
		}
		if err := s.st.Q.SetGithubIdentityStatus(ctx, db.SetGithubIdentityStatusParams{Status: "reauth", UpdatedAt: s.nowMs(), Slot: id.Slot}); err != nil {
			return "", err
		}
		hit = true
	}
	if !hit {
		return "ignored:unknown_user", nil
	}
	s.changed()
	return "handled", nil
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
