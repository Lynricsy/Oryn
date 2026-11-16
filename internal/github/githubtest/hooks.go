package githubtest

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Delivery 是 App webhook 投递日志里的一条。
type Delivery struct {
	ID          int64
	GUID        string
	Event       string
	Action      string
	Payload     json.RawMessage
	StatusCode  int
	Redelivery  bool
	DeliveredAt time.Time
}

// hookRoutes 登记投递日志、协作者权限、反应与评论端点（M14 阶段 4、7）。
func (s *Server) hookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/hook/deliveries", s.listDeliveries)
	mux.HandleFunc("GET /app/hook/deliveries/{id}", s.getDelivery)
	mux.HandleFunc("GET /repos/{owner}/{repo}/collaborators/{login}/permission", s.collaboratorPermission)
}

// Sign 按假 App 的 webhook secret 给 body 签名，返回 X-Hub-Signature-256 的值。
func (s *Server) Sign(body []byte) string {
	s.mu.Lock()
	secret := s.App.WebhookSecret
	s.mu.Unlock()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Deliver 签名后把一次事件真实 POST 到 targetURL，并记进投递日志；Unreachable 为真或请求失败时日志里记 502。
// 返回 targetURL 的响应状态码（没有发出或失败时为 0）与投递 guid。
func (s *Server) Deliver(targetURL, event string, payload any) (status int, guid string) {
	return s.DeliverGUID(targetURL, event, payload, "")
}

// DeliverGUID 同 Deliver；guid 非空时用它（模拟同一投递的重放）。
func (s *Server) DeliverGUID(targetURL, event string, payload any, guid string) (status int, outGUID string) {
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
	s.mu.Lock()
	unreachable := s.Unreachable
	s.mu.Unlock()
	code := http.StatusBadGateway
	if !unreachable {
		req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(body))
		if err != nil {
			panic(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "GitHub-Hookshot/fake")
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", guid)
		req.Header.Set("X-Hub-Signature-256", s.Sign(body))
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			status, code = resp.StatusCode, resp.StatusCode
		}
	}
	s.mu.Lock()
	s.seq++
	s.Deliveries = append(s.Deliveries, &Delivery{
		ID: 50000 + s.seq, GUID: guid, Event: event, Action: head.Action, Payload: body, StatusCode: code,
		DeliveredAt: time.Now().UTC().Truncate(time.Second),
	})
	s.mu.Unlock()
	return status, guid
}

func deliverySummary(d *Delivery) map[string]any {
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
		"installation_id": nil, "repository_id": nil,
	}
}

// listDeliveries 按 id 倒序列出投递日志；cursor 是上一页最后一条的 id（只返回更小的），下一页经 Link 头给出。
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.jwtOK(r) {
		fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 || per > 100 {
		per = 30
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	var all []*Delivery
	for i := len(s.Deliveries) - 1; i >= 0; i-- {
		if d := s.Deliveries[i]; cursor == 0 || d.ID < cursor {
			all = append(all, d)
		}
	}
	page := all[:min(per, len(all))]
	if len(all) > len(page) {
		q := r.URL.Query()
		q.Set("per_page", strconv.Itoa(per))
		q.Set("cursor", strconv.FormatInt(page[len(page)-1].ID, 10))
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?%s>; rel="next"`, s.baseURL(r), r.URL.Path, q.Encode()))
	}
	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		out = append(out, deliverySummary(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.jwtOK(r) {
		fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, d := range s.Deliveries {
		if d.ID != id {
			continue
		}
		out := deliverySummary(d)
		out["request"] = map[string]any{
			"headers": map[string]any{"X-GitHub-Event": d.Event, "X-GitHub-Delivery": d.GUID},
			"payload": d.Payload,
		}
		out["response"] = map[string]any{"headers": map[string]any{}, "payload": nil}
		writeJSON(w, http.StatusOK, out)
		return
	}
	fail(w, http.StatusNotFound, "Not Found")
}

// collaboratorPermission 按仓库的协作者权限表回答；表里没有的已知用户为 none，未知用户 404。
func (s *Server) collaboratorPermission(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, "/collaborators/"+r.PathValue("login")+"/permission")
		return
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	login := r.PathValue("login")
	perm, known := "", false
	for k, v := range rp.Permissions {
		if strings.EqualFold(k, login) {
			perm, known = v, true
		}
	}
	if !known {
		if _, ok := s.Users[strings.ToLower(login)]; !ok {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		perm = "none"
	}
	writeJSON(w, http.StatusOK, map[string]any{"permission": perm, "role_name": perm, "user": map[string]any{"login": login, "type": "User"}})
}
