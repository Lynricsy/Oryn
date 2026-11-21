package repos

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/version"
)

// 托管仓库的推送后 Webhook（M24 阶段 ③）：每次推送成功后异步 POST 一次 JSON，不阻塞推送、不重试、不跟随重定向。
// 每个项目一个容量 webhookQueueCap 的队列和一个投递 goroutine，同一时刻只投一个；队列满时丢弃这次事件并记在最近投递上。

const (
	webhookQueueCap    = 8
	webhookBodyReadMax = 64 << 10
	webhookTimeout     = 10 * time.Second
	webhookQueueFull   = "skipped: delivery queue full"
	webhookBadSecret   = "签名密钥无法解密"
)

// WebhookAAD 是 Webhook 签名密钥密文的附加数据：绑定项目，密文挪到别的项目解不开。分隔符 `:` 不在全局密钥名的
// 字符集里，不会与某个全局密钥的 AAD 相同。
func WebhookAAD(projectID string) string { return "project-webhook:" + projectID }

// SealWebhookSecret 用控制面的密钥盒封存项目的 Webhook 签名密钥。
func (s *Service) SealWebhookSecret(projectID, secret string) ([]byte, error) {
	if s.cfg.Box == nil {
		return nil, errors.New("没有密钥盒，无法保存 Webhook 签名密钥")
	}
	return s.cfg.Box.Seal(WebhookAAD(projectID), []byte(secret))
}

// webhookQueues 是各项目的投递队列；队列空了投递 goroutine 就退出并删掉队列。
type webhookQueues struct {
	mu sync.Mutex
	q  map[string]chan webhookEvent
}

// webhookEvent 是一次待投递的推送。
type webhookEvent struct {
	prj     db.Project
	pusher  egress.HostedPusher
	updates []egress.RefUpdate
	at      time.Time
}

// webhookPayload 是投递的 JSON 载荷。
type webhookPayload struct {
	Event      string           `json:"event"`
	DeliveryID string           `json:"deliveryId"`
	PushedAt   int64            `json:"pushedAt"`
	Project    webhookProject   `json:"project"`
	Pusher     webhookPusher    `json:"pusher"`
	Refs       []webhookRefJSON `json:"refs"`
}

type webhookProject struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CloneURL string `json:"cloneUrl"`
}

type webhookPusher struct {
	Kind     string `json:"kind"` // owner = 主人本机；thread = orb（线程里的 agent）
	ThreadID string `json:"threadId,omitempty"`
}

type webhookRefJSON struct {
	Ref    string `json:"ref"`
	OldSha string `json:"oldSha"`
	NewSha string `json:"newSha"`
}

// queueWebhook 在项目设了 Webhook 时把这次推送放进投递队列；队列满时丢弃并记下。只读库，不阻塞推送。
func (s *Service) queueWebhook(ctx context.Context, prj db.Project, pusher egress.HostedPusher, updates []egress.RefUpdate, at time.Time) {
	if _, err := s.st.Q.GetProjectWebhook(ctx, prj.ID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("托管仓库：读取 Webhook", "project", prj.ID, "err", err)
		}
		return
	}
	ev := webhookEvent{prj: prj, pusher: pusher, updates: updates, at: at}
	s.hooks.mu.Lock()
	if s.hooks.q == nil {
		s.hooks.q = map[string]chan webhookEvent{}
	}
	q, ok := s.hooks.q[prj.ID]
	if !ok {
		// 与 Close 的 wg.Wait 有序：closed 置位之后不再起投递 goroutine
		s.mu.Lock()
		closed := s.closed
		if !closed {
			s.wg.Add(1)
		}
		s.mu.Unlock()
		if closed {
			s.hooks.mu.Unlock()
			return
		}
		q = make(chan webhookEvent, webhookQueueCap)
		s.hooks.q[prj.ID] = q
		go s.webhookWorker(prj.ID, q)
	}
	var queued bool
	select {
	case q <- ev:
		queued = true
	default:
	}
	s.hooks.mu.Unlock()
	if !queued {
		slog.Warn("托管仓库：Webhook 投递队列已满，丢弃这次推送事件", "project", prj.ID)
		s.recordWebhook(ctx, prj.ID, at, 0, new(webhookQueueFull))
	}
}

// webhookWorker 依次投递一个项目队列里的事件；队列空了就退出（在锁内删掉队列，之后的入队会再起一个）。
func (s *Service) webhookWorker(projectID string, q chan webhookEvent) {
	defer s.wg.Done()
	for {
		s.hooks.mu.Lock()
		select {
		case ev := <-q:
			s.hooks.mu.Unlock()
			if s.ctx.Err() != nil {
				continue // 关闭中：排空队列，不再发请求
			}
			s.deliverWebhook(ev)
		default:
			delete(s.hooks.q, projectID)
			s.hooks.mu.Unlock()
			return
		}
	}
}

// deliverWebhook 投递一次推送事件并记下结果，然后广播让打开着的设置页刷新。地址与密钥按投递时的行取：
// 排队期间被移除就不发。
func (s *Service) deliverWebhook(ev webhookEvent) {
	ctx, cancel := context.WithTimeout(s.ctx, cmp.Or(s.cfg.WebhookTimeout, webhookTimeout))
	defer cancel()
	id := ev.prj.ID
	hook, err := s.st.Q.GetProjectWebhook(ctx, id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("托管仓库：读取 Webhook", "project", id, "err", err)
		}
		return
	}
	now := s.cfg.Now()
	status, msg := s.postWebhook(ctx, hook, ev)
	s.recordWebhook(context.WithoutCancel(ctx), id, now, status, msg)
}

// postWebhook 发出请求，返回状态码（0 = 没拿到响应）与失败原因（成功为 nil）。
func (s *Service) postWebhook(ctx context.Context, hook db.ProjectWebhook, ev webhookEvent) (int, *string) {
	var key []byte
	if len(hook.SecretEnc) > 0 {
		k, err := s.openWebhookSecret(hook)
		if err != nil {
			slog.Warn("托管仓库：解密 Webhook 签名密钥", "project", hook.ProjectID, "err", err)
			return 0, new(webhookBadSecret)
		}
		key = k
	}
	p := webhookBody(ev, s.cfg.PublicURL)
	body, err := json.Marshal(p)
	if err != nil {
		return 0, new(err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.Url, bytes.NewReader(body))
	if err != nil {
		return 0, new(headRunes(err.Error(), errMessageMax))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Oryn-Webhook/"+version.Version)
	req.Header.Set("X-Oryn-Event", "push")
	req.Header.Set("X-Oryn-Delivery", p.DeliveryID)
	if key != nil {
		req.Header.Set("X-Oryn-Signature-256", "sha256="+WebhookSignature(key, body))
	}
	client := &http.Client{
		Transport:     http.DefaultTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, new(headRunes(err.Error(), errMessageMax))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webhookBodyReadMax))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, new(fmt.Sprintf("HTTP %d", resp.StatusCode))
}

func (s *Service) openWebhookSecret(hook db.ProjectWebhook) ([]byte, error) {
	if s.cfg.Box == nil {
		return nil, errors.New("没有密钥盒")
	}
	return s.cfg.Box.Open(WebhookAAD(hook.ProjectID), hook.SecretEnc)
}

// WebhookSignature 是 X-Oryn-Signature-256 的值（不含 sha256= 前缀）：hex(HMAC-SHA256(密钥, 原始请求体))。
func WebhookSignature(key, body []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// webhookBody 组装载荷：主人本机的推送 pusher.kind=owner；orb 的推送是 thread（解析不出唯一线程时不带 threadId）。
// refs 是全部变化的 ref，包括删除（newSha 全 0）。
func webhookBody(ev webhookEvent, publicURL string) webhookPayload {
	pusher := webhookPusher{Kind: "owner"}
	if ev.pusher.OrbID != "" {
		pusher = webhookPusher{Kind: "thread", ThreadID: ev.pusher.ThreadID}
	}
	refs := make([]webhookRefJSON, 0, len(ev.updates))
	for _, u := range ev.updates {
		refs = append(refs, webhookRefJSON{Ref: u.Ref, OldSha: u.Old, NewSha: u.New})
	}
	return webhookPayload{
		Event: "push", DeliveryID: ids.New("whd"), PushedAt: ev.at.UnixMilli(),
		Project: webhookProject{ID: ev.prj.ID, Name: ev.prj.Name, CloneURL: orbs.HostedCloneURL(publicURL, ev.prj.ID)},
		Pusher:  pusher, Refs: refs,
	}
}

// recordWebhook 写最近一次投递的结果并广播（设置页显示最近投递）。
func (s *Service) recordWebhook(ctx context.Context, projectID string, at time.Time, status int, msg *string) {
	if err := s.st.Q.SetProjectWebhookDelivery(ctx, db.SetProjectWebhookDeliveryParams{
		LastDeliveryAt: new(at.UnixMilli()), LastStatus: new(int64(status)), LastError: msg, ProjectID: projectID,
	}); err != nil {
		slog.Warn("托管仓库：记录 Webhook 投递", "project", projectID, "err", err)
	}
	s.changed(projectID)
}

// headRunes 截取 s 的前 n 个字符。
func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
