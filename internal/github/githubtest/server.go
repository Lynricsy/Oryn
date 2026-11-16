// Package githubtest 是 GitHub 的进程内假服务（M14 / M15）：供 internal/github 及其它包的测试、以及 cmd/fakegithub 使用。
// 同一个地址同时充当 API 基址与网页基址。不得导入 internal/github。
package githubtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OAuthApp 是假 GitHub 上登记的 OAuth App（只有 client 凭据）。
type OAuthApp struct {
	ClientID     string
	ClientSecret string
}

// DefaultScopes 是假服务默认授予 OAuth App 令牌的 scope（等于 Oryn 请求的七项）。
const DefaultScopes = "repo, workflow, read:org, admin:repo_hook, gist, write:packages, user:email"

// User 是一个 GitHub 用户。
type User struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
	// Type 缺省为 User。
	Type string
}

// Repo 是一个仓库。
type Repo struct {
	ID            int64
	FullName      string
	OwnerID       int64
	DefaultBranch string
	Private       bool
	Description   string
	PushedAt      time.Time
	// Permissions 是协作者权限表：login → admin | write | read。表里的用户与 owner 段同名的用户算作与仓库有关联
	// （出现在 GET /user/repos 里）。
	Permissions map[string]string
	// Restricted 模拟 OAuth App 的组织限制：org_restricted（403 + 访问限制 message）或 sso_required（403 + X-GitHub-SSO）；
	// 只作用于用户令牌对仓库与仓库 hook 的请求。空串 = 不限制。
	Restricted string
	// Refs 是 git ref → 提交 sha，键形如 heads/main、tags/v1（GET …/git/ref/{ref}）。
	Refs map[string]string
}

// Call 是假服务收到的一次请求。
type Call struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   []byte
}

// Server 是假 GitHub。导出字段是可变状态：测试修改前后用 Lock / Unlock 包住。
type Server struct {
	mu sync.Mutex
	hs *httptest.Server
	// base 是 NewHandlerOnly 的对外地址（hs 为 nil 时 URL() 取它）。
	base string
	// GitRoot 非空时以 git http-backend 服务 smart-HTTP（裸仓库在 <GitRoot>/<owner>/<repo>.git）。
	GitRoot string

	// OAuthApp 是假 GitHub 上登记的 OAuth App；令牌端点只接受它的 client 凭据。
	OAuthApp OAuthApp
	// Scopes 是 OAuth App 授权时实际授予的 scope（`X-OAuth-Scopes` 格式 `a, b, c`），签发时随令牌固定。
	Scopes string
	Users  map[string]*User // login（小写）→ 用户
	Repos  []*Repo
	// Renamed 记录改名：旧 full_name（小写）→ 新 full_name；旧路径回 301。
	Renamed map[string]string
	Calls   []Call
	// Issues / Pulls / Comments / Reviews 是各仓库的 issue、PR、评论与评审；Rollups、CheckRuns、Statuses 按提交 sha 记 CI 结果（见 issues.go）。
	Issues    []*Issue
	Pulls     []*Pull
	Comments  []*Comment
	Reviews   []*Review
	Rollups   map[string]string
	CheckRuns map[string][]CheckRun
	Statuses  map[string][]CommitStatus
	// Unreachable 为真时仓库 hook 投递不真的发请求，只在投递日志里记 502。
	Unreachable bool
	// Fail 注入失败："METHOD /path" → 状态码；命中的请求直接回该状态码（仍记进 Calls）。
	Fail map[string]int

	// RepoHooks / RepoDeliveries 是仓库 webhook 与它们的投递日志（M15，见 repohooks.go）；
	// DeliveriesUsed / DeliveriesReset 是投递日志接口的独立额度（上限 500 次/小时，304 不计数）。
	RepoHooks       []*RepoHook
	RepoDeliveries  []*RepoDelivery
	DeliveriesUsed  int
	DeliveriesReset time.Time

	// IssueEvents / Runs 是 issue 时间线事件与 Actions 运行（M15 REST 轮询，见 watch.go）；CoreRemaining 非 0 时
	// 轮询用到的接口带 X-RateLimit-Remaining（以及 CoreReset 对应的 X-RateLimit-Reset），模拟核心额度。
	IssueEvents   []*IssueEvent
	Runs          []*WorkflowRun
	CoreRemaining int
	CoreReset     time.Time

	// Reactions 是评论上的反应（M15 问题自动关闭，见 tasks.go）。
	Reactions []*Reaction

	seq         int64
	codes       map[string]string // 授权码 → login
	userTokens  map[string]string // 用户访问令牌 → login
	tokenScopes map[string]string // 用户访问令牌 → 授予的 scope
}

// New 起一个假 GitHub；URL() 同时作为 APIURL 与 WebURL。
func New() *Server {
	s := newServer()
	s.hs = httptest.NewServer(s.handler())
	return s
}

// NewHandlerOnly 构造不自带监听的假 GitHub（cmd/fakegithub 用）：base 是对外地址（无结尾斜杠），
// 由调用方用 Handler() 起 http.Server。
func NewHandlerOnly(base string) *Server {
	s := newServer()
	s.base = strings.TrimRight(base, "/")
	return s
}

// Handler 返回假服务的 HTTP 处理器（含请求记录）。
func (s *Server) Handler() http.Handler { return s.handler() }

func newServer() *Server {
	return &Server{
		OAuthApp:    OAuthApp{ClientID: "Ov23.test", ClientSecret: "oauth-secret"},
		Scopes:      DefaultScopes,
		Users:       map[string]*User{},
		Renamed:     map[string]string{},
		codes:       map[string]string{},
		userTokens:  map[string]string{},
		tokenScopes: map[string]string{},
	}
}

// URL 是假服务的基址（无结尾斜杠）。
func (s *Server) URL() string {
	if s.hs == nil {
		return s.base
	}
	return s.hs.URL
}

// Close 关闭假服务。
func (s *Server) Close() {
	if s.hs != nil {
		s.hs.Close()
	}
}

// Lock / Unlock 保护导出的可变状态。
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// AddUser 登记一个用户。
func (s *Server) AddUser(id int64, login, name string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := &User{ID: id, Login: login, Name: name, AvatarURL: fmt.Sprintf("%s/avatars/%s", s.URL(), login)}
	s.Users[strings.ToLower(login)] = u
	return u
}

// AddRepo 登记一个仓库。
func (s *Server) AddRepo(r Repo) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	if r.PushedAt.IsZero() {
		r.PushedAt = time.Now().UTC().Truncate(time.Second)
	}
	p := &r
	s.Repos = append(s.Repos, p)
	return p
}

// Rename 把仓库 from 改名为 to；旧路径此后回 301。
func (s *Server) Rename(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Repos {
		if strings.EqualFold(r.FullName, from) {
			r.FullName = to
			s.Renamed[strings.ToLower(from)] = to
			return
		}
	}
}

// GrantCode 登记一个可兑换为 login 用户令牌的 OAuth 授权码（一次性）。
func (s *Server) GrantCode(code, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = strings.ToLower(login)
}

// CallsTo 返回方法与路径都匹配的请求；path 以 `*` 结尾时按前缀匹配。
func (s *Server) CallsTo(method, path string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.Calls {
		if c.Method != method {
			continue
		}
		if p, ok := strings.CutSuffix(path, "*"); ok && strings.HasPrefix(c.Path, p) || c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// ResetCalls 清空请求记录。
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", s.oauthToken)
	mux.HandleFunc("GET /user", s.getUser)
	mux.HandleFunc("GET /user/repos", s.userRepos)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.getRepo)
	s.repoHookRoutes(mux)
	s.issueRoutes(mux)
	s.hookRoutes(mux)
	s.watchRoutes(mux)
	s.refRoutes(mux)
	s.taskRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.Calls = append(s.Calls, Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: body})
		code := s.Fail[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		if code != 0 {
			fail(w, code, "injected failure")
			return
		}
		if s.serveExtra(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *Server) nextToken(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s_%d", prefix, s.seq)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return t
	}
	if t, ok := strings.CutPrefix(h, "token "); ok {
		return t
	}
	return ""
}

// userOf 返回用户令牌对应的用户。调用方持锁。
func (s *Server) userOf(tok string) *User {
	if login, ok := s.userTokens[tok]; ok {
		return s.Users[login]
	}
	return nil
}

// lookupRepo 按 owner/repo 查仓库；命中改名记录时返回新 full_name（调用方回 301）。调用方持锁。
func (s *Server) lookupRepo(owner, name string) (*Repo, string) {
	full := owner + "/" + name
	for _, r := range s.Repos {
		if strings.EqualFold(r.FullName, full) {
			return r, ""
		}
	}
	if to, ok := s.Renamed[strings.ToLower(full)]; ok {
		return nil, to
	}
	return nil, ""
}

func (s *Server) baseURL(r *http.Request) string { return "http://" + r.Host }

func (s *Server) repoJSON(r *http.Request, rp *Repo) map[string]any {
	owner, name, _ := strings.Cut(rp.FullName, "/")
	m := map[string]any{
		"id": rp.ID, "node_id": RepoNodeID(rp.ID), "name": name, "full_name": rp.FullName, "private": rp.Private, "default_branch": rp.DefaultBranch,
		"owner":    map[string]any{"login": owner, "id": rp.OwnerID},
		"html_url": s.baseURL(r) + "/" + rp.FullName, "description": nil, "pushed_at": nil,
	}
	if rp.Description != "" {
		m["description"] = rp.Description
	}
	if !rp.PushedAt.IsZero() {
		m["pushed_at"] = rp.PushedAt.UTC().Format(time.RFC3339)
	}
	return m
}

// paginate 按 per_page / page 截取 [lo, hi)，写 Link 头：与 GitHub 一样，有下一页时给 next 与 last，不是第一页时给 prev 与 first。
func (s *Server) paginate(w http.ResponseWriter, r *http.Request, n int) (lo, hi int) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 || per > 100 {
		per = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	lo = min((page-1)*per, n)
	hi = min(lo+per, n)
	pages := max((n+per-1)/per, 1)
	link := func(p int, rel string) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		q.Set("per_page", strconv.Itoa(per))
		return fmt.Sprintf(`<%s%s?%s>; rel="%s"`, s.baseURL(r), r.URL.Path, q.Encode(), rel)
	}
	var rels []string
	if page > 1 {
		rels = append(rels, link(page-1, "prev"), link(1, "first"))
	}
	if hi < n {
		rels = append(rels, link(page+1, "next"), link(pages, "last"))
	}
	if len(rels) > 0 {
		w.Header().Set("Link", strings.Join(rels, ", "))
	}
	return lo, hi
}

// issueOAuthToken 给 login 签发 OAuth App 令牌：不过期、无刷新令牌，scope 取 s.Scopes 并随令牌固定。调用方持锁。
func (s *Server) issueOAuthToken(login string) map[string]any {
	tok := s.nextToken("gho")
	s.userTokens[tok] = login
	s.tokenScopes[tok] = s.Scopes
	return map[string]any{"access_token": tok, "token_type": "bearer", "scope": strings.ReplaceAll(s.Scopes, " ", "")}
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.PostForm.Get("client_id") != s.OAuthApp.ClientID || r.PostForm.Get("client_secret") != s.OAuthApp.ClientSecret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	code := r.PostForm.Get("code")
	login, ok := s.codes[code]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	delete(s.codes, code)
	writeJSON(w, http.StatusOK, s.issueOAuthToken(login))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := bearer(r)
	u := s.userOf(tok)
	if u == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if scopes, ok := s.tokenScopes[tok]; ok {
		w.Header().Set("X-OAuth-Scopes", scopes)
	}
	typ := u.Type
	if typ == "" {
		typ = "User"
	}
	var name any
	if u.Name != "" {
		name = u.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "login": u.Login, "name": name, "avatar_url": u.AvatarURL, "type": typ})
}

func (s *Server) redirectRepo(w http.ResponseWriter, r *http.Request, to, tail string) {
	http.Redirect(w, r, "/repos/"+to+tail, http.StatusMovedPermanently)
}

// canRead 报告令牌能否读该仓库：任何用户令牌都能读（假服务不区分公开与私有）。调用方持锁。
func (s *Server) canRead(tok string, _ *Repo) bool {
	_, ok := s.userTokens[tok]
	return ok
}

// affiliated 报告 login 与仓库有没有关联（GET /user/repos 的 owner / collaborator / organization_member）：
// owner 段同名，或出现在协作者权限表里。调用方持锁。
func affiliated(rp *Repo, login string) bool {
	owner, _, _ := strings.Cut(rp.FullName, "/")
	if strings.EqualFold(owner, login) {
		return true
	}
	for k := range rp.Permissions {
		if strings.EqualFold(k, login) {
			return true
		}
	}
	return false
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, "")
		return
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	if s.restricted(w, r, rp) {
		return
	}
	out := s.repoJSON(r, rp)
	if u := s.userOf(bearer(r)); u != nil {
		out["permissions"] = permissionsJSON(s.permOf(rp, u.Login))
	}
	writeJSON(w, http.StatusOK, out)
}

// userRepos 是 GET /user/repos：令牌用户有关联的仓库，sort=pushed 时按最近推送倒序（否则按 id），分页。
func (s *Server) userRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userOf(bearer(r))
	if u == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	var repos []*Repo
	for _, rp := range s.Repos {
		if affiliated(rp, u.Login) {
			repos = append(repos, rp)
		}
	}
	if r.URL.Query().Get("sort") == "pushed" {
		sort.SliceStable(repos, func(i, j int) bool { return repos[i].PushedAt.After(repos[j].PushedAt) })
	} else {
		sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	}
	lo, hi := s.paginate(w, r, len(repos))
	out := []map[string]any{}
	for _, rp := range repos[lo:hi] {
		m := s.repoJSON(r, rp)
		m["permissions"] = permissionsJSON(s.permOf(rp, u.Login))
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}
