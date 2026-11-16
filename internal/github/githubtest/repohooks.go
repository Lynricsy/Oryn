package githubtest

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 本文件是仓库 webhook 的假实现（M15 步骤 1.2 / 1.3）：增删改查、投递日志（ETag / 304、独立额度）与签名投递。
// 行为取自 docs/plan-m15.md 附录 B：GET 返回的 config.secret 打码为 ********；投递日志接口单独计额
// （X-RateLimit-Resource: webhook_deliveries，500 次/小时），304 不计数。

// DeliveriesLimit 是投递日志接口每小时的额度。
const DeliveriesLimit = 500

// maskedSecret 是 GET hook 时 config.secret 的打码值。
const maskedSecret = "********"

// RepoHook 是一个仓库 webhook。
type RepoHook struct {
	ID          int64
	RepoID      int64
	Name        string
	Active      bool
	Events      []string
	URL         string
	ContentType string
	Secret      string
	InsecureSSL string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RepoDelivery 是仓库 webhook 投递日志里的一条。
type RepoDelivery struct {
	ID          int64
	HookID      int64
	RepoID      int64
	GUID        string
	Event       string
	Action      string
	Payload     json.RawMessage
	StatusCode  int
	Redelivery  bool
	DeliveredAt time.Time
}

func (s *Server) repoHookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /repos/{owner}/{repo}/hooks", s.createRepoHook)
	mux.HandleFunc("GET /repos/{owner}/{repo}/hooks", s.listRepoHooks)
	mux.HandleFunc("GET /repos/{owner}/{repo}/hooks/{id}", s.getRepoHook)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/hooks/{id}", s.updateRepoHook)
	mux.HandleFunc("DELETE /repos/{owner}/{repo}/hooks/{id}", s.deleteRepoHook)
	mux.HandleFunc("GET /repos/{owner}/{repo}/hooks/{id}/deliveries", s.listRepoHookDeliveries)
	mux.HandleFunc("GET /repos/{owner}/{repo}/hooks/{id}/deliveries/{delivery}", s.getRepoHookDelivery)
}

// permOf 返回 login 在仓库上的权限（admin | write | read）；表里没有时为 read。调用方持锁。
func (s *Server) permOf(rp *Repo, login string) string {
	for k, v := range rp.Permissions {
		if strings.EqualFold(k, login) {
			return v
		}
	}
	return "read"
}

// permissionsJSON 是 GET /repos/{o}/{r} 里用户令牌看到的 permissions 对象。
func permissionsJSON(perm string) map[string]bool {
	admin := perm == "admin"
	push := admin || perm == "write" || perm == "maintain"
	return map[string]bool{"admin": admin, "maintain": admin || perm == "maintain", "push": push, "triage": push, "pull": true}
}

// restricted 按 Repo.Restricted 对用户令牌回 403；返回是否已写出响应。调用方持锁。
func (s *Server) restricted(w http.ResponseWriter, r *http.Request, rp *Repo) bool {
	if s.userOf(bearer(r)) == nil {
		return false
	}
	switch rp.Restricted {
	case "org_restricted":
		fail(w, http.StatusForbidden, "Although you appear to have the correct authorization credentials, the `"+
			strings.Split(rp.FullName, "/")[0]+"` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited.")
		return true
	case "sso_required":
		w.Header().Set("X-GitHub-SSO", "required; url="+s.baseURL(r)+"/orgs/"+strings.Split(rp.FullName, "/")[0]+"/sso?authorization_request=fake")
		fail(w, http.StatusForbidden, "Resource protected by organization SAML enforcement. You must grant your OAuth token access to this organization.")
		return true
	}
	return false
}

// hookAdmin 找出请求路径上的仓库，并要求用户令牌对它有 admin 权限（GitHub 对非 admin 回 404）；失败时已写出响应。调用方持锁。
func (s *Server) hookAdmin(w http.ResponseWriter, r *http.Request) *Repo {
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, strings.TrimPrefix(r.URL.Path, "/repos/"+r.PathValue("owner")+"/"+r.PathValue("repo")))
		return nil
	}
	u := s.userOf(bearer(r))
	if u == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return nil
	}
	if rp == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return nil
	}
	if s.restricted(w, r, rp) {
		return nil
	}
	if s.permOf(rp, u.Login) != "admin" {
		fail(w, http.StatusNotFound, "Not Found")
		return nil
	}
	return rp
}

// repoHook 取路径上的 hook；不属于该仓库时 404（已写出响应）。调用方持锁。
func (s *Server) repoHook(w http.ResponseWriter, r *http.Request, rp *Repo) *RepoHook {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, h := range s.RepoHooks {
		if h.ID == id && h.RepoID == rp.ID {
			return h
		}
	}
	fail(w, http.StatusNotFound, "Not Found")
	return nil
}

func (s *Server) repoHookJSON(r *http.Request, rp *Repo, h *RepoHook) map[string]any {
	cfg := map[string]any{"url": h.URL, "content_type": h.ContentType, "insecure_ssl": h.InsecureSSL}
	if h.Secret != "" {
		cfg["secret"] = maskedSecret
	}
	base := fmt.Sprintf("%s/repos/%s/hooks/%d", s.baseURL(r), rp.FullName, h.ID)
	return map[string]any{
		"type": "Repository", "id": h.ID, "name": h.Name, "active": h.Active, "events": h.Events, "config": cfg,
		"updated_at": h.UpdatedAt.Format(time.RFC3339), "created_at": h.CreatedAt.Format(time.RFC3339),
		"url": base, "test_url": base + "/test", "ping_url": base + "/pings", "deliveries_url": base + "/deliveries",
		"last_response": map[string]any{"code": nil, "status": "unused", "message": nil},
	}
}

// hookInput 是创建 / 修改 hook 的请求体；config 整体替换（与 GitHub 一致：PATCH 不带 secret 即清空 secret）。
type hookInput struct {
	Name   *string   `json:"name"`
	Active *bool     `json:"active"`
	Events *[]string `json:"events"`
	Config *struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		Secret      string `json:"secret"`
		InsecureSSL any    `json:"insecure_ssl"`
	} `json:"config"`
}

func (in hookInput) apply(h *RepoHook) {
	if in.Active != nil {
		h.Active = *in.Active
	}
	if in.Events != nil {
		h.Events = slices.Clone(*in.Events)
	}
	if c := in.Config; c != nil {
		h.URL, h.ContentType, h.Secret = c.URL, c.ContentType, c.Secret
		h.InsecureSSL = fmt.Sprint(c.InsecureSSL)
		if c.InsecureSSL == nil {
			h.InsecureSSL = "0"
		}
		if h.ContentType == "" {
			h.ContentType = "form"
		}
	}
}

func (s *Server) createRepoHook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	var in hookInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Config == nil || in.Config.URL == "" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	if in.Name != nil && *in.Name != "web" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	for _, h := range s.RepoHooks {
		if h.RepoID == rp.ID && h.URL == in.Config.URL {
			fail(w, http.StatusUnprocessableEntity, "Validation Failed: Hook already exists on this repository")
			return
		}
	}
	s.seq++
	now := time.Now().UTC().Truncate(time.Second)
	h := &RepoHook{ID: 600000 + s.seq, RepoID: rp.ID, Name: "web", Active: true, Events: []string{"push"}, CreatedAt: now, UpdatedAt: now}
	in.apply(h)
	s.RepoHooks = append(s.RepoHooks, h)
	writeJSON(w, http.StatusCreated, s.repoHookJSON(r, rp, h))
}

func (s *Server) listRepoHooks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	var hooks []*RepoHook
	for _, h := range s.RepoHooks {
		if h.RepoID == rp.ID {
			hooks = append(hooks, h)
		}
	}
	lo, hi := s.paginate(w, r, len(hooks))
	out := []map[string]any{}
	for _, h := range hooks[lo:hi] {
		out = append(out, s.repoHookJSON(r, rp, h))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRepoHook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	if h := s.repoHook(w, r, rp); h != nil {
		writeJSON(w, http.StatusOK, s.repoHookJSON(r, rp, h))
	}
}

func (s *Server) updateRepoHook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	h := s.repoHook(w, r, rp)
	if h == nil {
		return
	}
	var in hookInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Config != nil && in.Config.URL == "") {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	in.apply(h)
	h.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	writeJSON(w, http.StatusOK, s.repoHookJSON(r, rp, h))
}

func (s *Server) deleteRepoHook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	h := s.repoHook(w, r, rp)
	if h == nil {
		return
	}
	s.RepoHooks = slices.DeleteFunc(s.RepoHooks, func(x *RepoHook) bool { return x == h })
	w.WriteHeader(http.StatusNoContent)
}

// deliveriesQuota 写投递日志接口的额度头；charge 为真时先计一次。调用方持锁。
func (s *Server) deliveriesQuota(w http.ResponseWriter, charge bool) {
	now := time.Now()
	if s.DeliveriesReset.IsZero() || !now.Before(s.DeliveriesReset) {
		s.DeliveriesReset, s.DeliveriesUsed = now.Add(time.Hour).Truncate(time.Second), 0
	}
	if charge {
		s.DeliveriesUsed++
	}
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(DeliveriesLimit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(max(DeliveriesLimit-s.DeliveriesUsed, 0)))
	h.Set("X-RateLimit-Used", strconv.Itoa(s.DeliveriesUsed))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(s.DeliveriesReset.Unix(), 10))
	h.Set("X-RateLimit-Resource", "webhook_deliveries")
}

func repoDeliverySummary(d *RepoDelivery) map[string]any {
	st := "OK"
	if d.StatusCode < 200 || d.StatusCode > 299 {
		st = "failed"
	}
	var action any
	if d.Action != "" {
		action = d.Action
	}
	return map[string]any{
		"id": d.ID, "guid": d.GUID, "delivered_at": d.DeliveredAt.Format(time.RFC3339), "redelivery": d.Redelivery,
		"duration": 0.1, "status": st, "status_code": d.StatusCode, "event": d.Event, "action": action,
		"installation_id": nil, "repository_id": d.RepoID,
	}
}

// listRepoHookDeliveries 按 id 倒序列出 hook 的投递日志：带 ETag，If-None-Match 命中时回 304 且不计额度；
// cursor 是上一页最后一条的 id（只返回更小的），下一页经 Link 头给出。
func (s *Server) listRepoHookDeliveries(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	h := s.repoHook(w, r, rp)
	if h == nil {
		return
	}
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 || per > 100 {
		per = 30
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	var all []*RepoDelivery
	for i := len(s.RepoDeliveries) - 1; i >= 0; i-- {
		if d := s.RepoDeliveries[i]; d.HookID == h.ID && (cursor == 0 || d.ID < cursor) {
			all = append(all, d)
		}
	}
	page := all[:min(per, len(all))]
	sum := sha256.New()
	fmt.Fprintf(sum, "%d|%d|%d", h.ID, cursor, per)
	for _, d := range page {
		fmt.Fprintf(sum, "|%d", d.ID)
	}
	etag := `W/"` + hex.EncodeToString(sum.Sum(nil))[:32] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		s.deliveriesQuota(w, false)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.deliveriesQuota(w, true)
	if len(all) > len(page) {
		q := r.URL.Query()
		q.Set("per_page", strconv.Itoa(per))
		q.Set("cursor", strconv.FormatInt(page[len(page)-1].ID, 10))
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?%s>; rel="next"`, s.baseURL(r), r.URL.Path, q.Encode()))
	}
	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		out = append(out, repoDeliverySummary(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRepoHookDelivery(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.hookAdmin(w, r)
	if rp == nil {
		return
	}
	h := s.repoHook(w, r, rp)
	if h == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("delivery"), 10, 64)
	for _, d := range s.RepoDeliveries {
		if d.ID != id || d.HookID != h.ID {
			continue
		}
		s.deliveriesQuota(w, true)
		out := repoDeliverySummary(d)
		out["request"] = map[string]any{
			"headers": map[string]any{
				"X-GitHub-Event": d.Event, "X-GitHub-Delivery": d.GUID, "X-GitHub-Hook-ID": strconv.FormatInt(h.ID, 10),
				"X-GitHub-Hook-Installation-Target-ID":   strconv.FormatInt(rp.ID, 10),
				"X-GitHub-Hook-Installation-Target-Type": "repository",
				"Content-Type":                           "application/json",
			},
			"payload": d.Payload,
		}
		out["response"] = map[string]any{"headers": map[string]any{}, "payload": nil}
		writeJSON(w, http.StatusOK, out)
		return
	}
	fail(w, http.StatusNotFound, "Not Found")
}

// SignHook 按 hook 的 secret 给 body 签名，返回 X-Hub-Signature-256 的值。
func SignHook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// HookOf 返回仓库上 config.url 等于 url 的 hook 的副本；url 为空时取第一个；没有时 ok 为假。
func (s *Server) HookOf(fullName, url string) (RepoHook, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rp := range s.Repos {
		if !strings.EqualFold(rp.FullName, fullName) {
			continue
		}
		for _, h := range s.RepoHooks {
			if h.RepoID == rp.ID && (url == "" || h.URL == url) {
				c := *h
				c.Events = slices.Clone(h.Events)
				return c, true
			}
		}
	}
	return RepoHook{}, false
}

// AddRepoHook 直接在仓库上登记一个 hook（模拟手工建的或别处建的 hook），返回其 id。
func (s *Server) AddRepoHook(fullName string, h RepoHook) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rp := range s.Repos {
		if strings.EqualFold(rp.FullName, fullName) {
			s.seq++
			h.ID, h.RepoID = 600000+s.seq, rp.ID
			if h.Name == "" {
				h.Name = "web"
			}
			now := time.Now().UTC().Truncate(time.Second)
			h.CreatedAt, h.UpdatedAt = now, now
			s.RepoHooks = append(s.RepoHooks, &h)
			return h.ID
		}
	}
	panic("githubtest: AddRepoHook: unknown repo " + fullName)
}

// DeliverHook 以仓库 hook 的身份投递一次事件：对仓库上每个 active、且订阅了该事件（或 `*`）的 hook，
// 按 hook 的 secret 签名，带 X-GitHub-Hook-ID 与 X-GitHub-Hook-Installation-Target-ID 真实 POST 到 hook 的 url，
// 并记进该 hook 的投递日志；Unreachable 为真或请求失败时日志里记 502。
// 返回第一个 hook 的响应状态码（没有发出或失败时为 0）与投递 guid（同一事件的各 hook 共用）；没有匹配的 hook 时 guid 为空。
func (s *Server) DeliverHook(fullName, event string, payload any) (status int, guid string) {
	return s.DeliverHookGUID(fullName, event, payload, "")
}

// DeliverHookGUID 同 DeliverHook；guid 非空时用它（模拟同一投递的重放）。
func (s *Server) DeliverHookGUID(fullName, event string, payload any, guid string) (status int, outGUID string) {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	if guid == "" {
		guid = newGUID()
	}
	var head struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(body, &head)
	type target struct {
		hook   RepoHook
		repoID int64
	}
	var targets []target
	s.mu.Lock()
	unreachable := s.Unreachable
	for _, rp := range s.Repos {
		if !strings.EqualFold(rp.FullName, fullName) {
			continue
		}
		for _, h := range s.RepoHooks {
			if h.RepoID == rp.ID && h.Active && (event == "ping" || slices.Contains(h.Events, event) || slices.Contains(h.Events, "*")) {
				targets = append(targets, target{hook: *h, repoID: rp.ID})
			}
		}
	}
	s.mu.Unlock()
	if len(targets) == 0 {
		return 0, ""
	}
	for i, t := range targets {
		code := http.StatusBadGateway
		got := 0
		if !unreachable {
			req, err := http.NewRequest(http.MethodPost, t.hook.URL, bytes.NewReader(body))
			if err != nil {
				panic(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "GitHub-Hookshot/fake")
			req.Header.Set("X-GitHub-Event", event)
			req.Header.Set("X-GitHub-Delivery", guid)
			req.Header.Set("X-GitHub-Hook-ID", strconv.FormatInt(t.hook.ID, 10))
			req.Header.Set("X-GitHub-Hook-Installation-Target-ID", strconv.FormatInt(t.repoID, 10))
			req.Header.Set("X-GitHub-Hook-Installation-Target-Type", "repository")
			if t.hook.Secret != "" {
				req.Header.Set("X-Hub-Signature-256", SignHook(t.hook.Secret, body))
			}
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
				got, code = resp.StatusCode, resp.StatusCode
			}
		}
		if i == 0 {
			status = got
		}
		s.mu.Lock()
		s.seq++
		s.RepoDeliveries = append(s.RepoDeliveries, &RepoDelivery{
			ID: 700000 + s.seq, HookID: t.hook.ID, RepoID: t.repoID, GUID: guid, Event: event, Action: head.Action,
			Payload: body, StatusCode: code, DeliveredAt: time.Now().UTC().Truncate(time.Second),
		})
		s.mu.Unlock()
	}
	return status, guid
}

// LastRepoDelivery 返回仓库 hook 投递日志里最新一条的 id（没有时为 0）。
func (s *Server) LastRepoDelivery() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.RepoDeliveries); n > 0 {
		return s.RepoDeliveries[n-1].ID
	}
	return 0
}
