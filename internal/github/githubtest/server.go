// Package githubtest 是 GitHub 的进程内假服务（M14）：供 internal/github 及其它包的测试、以及 cmd/fakegithub 使用。
// 同一个地址同时充当 API 基址与网页基址。不得导入 internal/github。
package githubtest

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

// App 是假 GitHub 上唯一的 GitHub App。
type App struct {
	ID            int64
	Slug, Name    string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	OwnerLogin    string
	// ManifestCode 是 manifest 转换接口接受的 code。
	ManifestCode string
	// Key 是 App 私钥；JWT 用其公钥验签。
	Key *rsa.PrivateKey
}

// User 是一个 GitHub 用户。
type User struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
	// Type 缺省为 User。
	Type string
	// FailRefresh 为真时该用户的刷新请求返回 bad_refresh_token。
	FailRefresh bool
}

// Installation 是 App 的一个安装。
type Installation struct {
	ID                  int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
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
	// InstallationID 是覆盖该仓库的安装；0 = App 未安装到该仓库。
	InstallationID int64
	// Permissions 是协作者权限表：login → admin | write | read。
	Permissions map[string]string
}

// Call 是假服务收到的一次请求。
type Call struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   []byte
}

type scoped struct {
	login   string
	repoIDs []int64
}

// Server 是假 GitHub。导出字段是可变状态：测试修改前后用 Lock / Unlock 包住。
type Server struct {
	mu sync.Mutex
	hs *httptest.Server
	// base 是 NewHandlerOnly 的对外地址（hs 为 nil 时 URL() 取它）。
	base string
	// GitRoot 非空时以 git http-backend 服务 smart-HTTP（裸仓库在 <GitRoot>/<owner>/<repo>.git）。
	GitRoot string

	App           App
	Users         map[string]*User // login（小写）→ 用户
	Installations []*Installation
	Repos         []*Repo
	// Renamed 记录改名：旧 full_name（小写）→ 新 full_name；旧路径回 301。
	Renamed map[string]string
	// UserTokenTTL 是用户访问令牌寿命（秒）；0 = 不过期（响应不带 expires_in 与刷新令牌）。
	UserTokenTTL int64
	Calls        []Call

	seq           int64
	codes         map[string]string // 授权码 → login
	userTokens    map[string]string // 用户访问令牌 → login
	refreshTokens map[string]string // 刷新令牌 → login
	instTokens    map[string]int64  // 安装令牌 → 安装 id
	scopedTokens  map[string]scoped
}

var sharedKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

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
		App: App{
			ID: 1, Slug: "oryn-test", Name: "Oryn Test", ClientID: "Iv1.test", ClientSecret: "client-secret",
			WebhookSecret: "webhook-secret", OwnerLogin: "owner", ManifestCode: "manifest-code", Key: sharedKey(),
		},
		Users:         map[string]*User{},
		Renamed:       map[string]string{},
		UserTokenTTL:  28800,
		codes:         map[string]string{},
		userTokens:    map[string]string{},
		refreshTokens: map[string]string{},
		instTokens:    map[string]int64{},
		scopedTokens:  map[string]scoped{},
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

// AddInstallation 登记一个安装（仓库范围 all）。
func (s *Server) AddInstallation(id int64, account string) *Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := &Installation{ID: id, AccountLogin: account, AccountType: "User", RepositorySelection: "all"}
	s.Installations = append(s.Installations, in)
	return in
}

// RemoveInstallation 删除一个安装（相当于卸载 App）。
func (s *Server) RemoveInstallation(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, in := range s.Installations {
		if in.ID == id {
			s.Installations = append(s.Installations[:i], s.Installations[i+1:]...)
			return
		}
	}
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

// PrivateKeyPEM 返回 App 私钥的 PKCS#1 PEM（manifest 转换响应里的 pem）。
func (s *Server) PrivateKeyPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(s.App.Key)}))
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", s.manifestConversion)
	mux.HandleFunc("POST /login/oauth/access_token", s.oauthToken)
	mux.HandleFunc("GET /user", s.getUser)
	mux.HandleFunc("GET /app/installations", s.listAppInstallations)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.installationToken)
	mux.HandleFunc("POST /applications/{client}/token/scoped", s.scopedToken)
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", s.repoInstallation)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.getRepo)
	mux.HandleFunc("GET /user/installations", s.userInstallations)
	mux.HandleFunc("GET /user/installations/{id}/repositories", s.userInstallationRepos)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.Calls = append(s.Calls, Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: body})
		s.mu.Unlock()
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

// jwtOK 校验 App JWT（RS256、iss = App id、未过期）。调用方持锁。
func (s *Server) jwtOK(r *http.Request) bool {
	parts := strings.Split(bearer(r), ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&s.App.Key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return false
	}
	now := time.Now().Unix()
	return claims.Iss == strconv.FormatInt(s.App.ID, 10) && claims.Exp > now && claims.Iat <= now+60 && claims.Exp-claims.Iat <= 600
}

// userOf 返回用户令牌（含收窄令牌）对应的用户。调用方持锁。
func (s *Server) userOf(tok string) *User {
	if login, ok := s.userTokens[tok]; ok {
		return s.Users[login]
	}
	if sc, ok := s.scopedTokens[tok]; ok {
		return s.Users[sc.login]
	}
	return nil
}

func (s *Server) installation(id int64) *Installation {
	for _, in := range s.Installations {
		if in.ID == id {
			return in
		}
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
		"id": rp.ID, "name": name, "full_name": rp.FullName, "private": rp.Private, "default_branch": rp.DefaultBranch,
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

// paginate 按 per_page / page 截取 [lo, hi)，还有下一页时写 Link 头。
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
	if hi < n {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page+1))
		q.Set("per_page", strconv.Itoa(per))
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?%s>; rel="next"`, s.baseURL(r), r.URL.Path, q.Encode()))
	}
	return lo, hi
}

func (s *Server) manifestConversion(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.PathValue("code") != s.App.ManifestCode {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": s.App.ID, "slug": s.App.Slug, "name": s.App.Name, "client_id": s.App.ClientID, "client_secret": s.App.ClientSecret,
		"webhook_secret": s.App.WebhookSecret, "pem": s.PrivateKeyPEM(), "owner": map[string]any{"login": s.App.OwnerLogin},
		"html_url": s.baseURL(r) + "/apps/" + s.App.Slug,
	})
}

// issueUserToken 给 login 签发一对新令牌。调用方持锁。
func (s *Server) issueUserToken(login string) map[string]any {
	tok := s.nextToken("ghu")
	s.userTokens[tok] = login
	out := map[string]any{"access_token": tok, "token_type": "bearer", "scope": ""}
	if s.UserTokenTTL > 0 {
		ref := s.nextToken("ghr")
		s.refreshTokens[ref] = login
		out["expires_in"] = s.UserTokenTTL
		out["refresh_token"] = ref
		out["refresh_token_expires_in"] = 15897600
	}
	return out
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.PostForm.Get("client_id") != s.App.ClientID || r.PostForm.Get("client_secret") != s.App.ClientSecret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	if r.PostForm.Get("grant_type") == "refresh_token" {
		ref := r.PostForm.Get("refresh_token")
		login, ok := s.refreshTokens[ref]
		if !ok || s.Users[login] == nil || s.Users[login].FailRefresh {
			writeJSON(w, http.StatusOK, map[string]string{"error": "bad_refresh_token"})
			return
		}
		delete(s.refreshTokens, ref) // 刷新令牌一次性
		writeJSON(w, http.StatusOK, s.issueUserToken(login))
		return
	}
	code := r.PostForm.Get("code")
	login, ok := s.codes[code]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	delete(s.codes, code)
	writeJSON(w, http.StatusOK, s.issueUserToken(login))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userOf(bearer(r))
	if u == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
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

func installationJSON(in *Installation) map[string]any {
	var suspended any
	if in.Suspended {
		suspended = "2026-01-01T00:00:00Z"
	}
	typ := in.AccountType
	if typ == "" {
		typ = "User"
	}
	return map[string]any{
		"id": in.ID, "account": map[string]any{"login": in.AccountLogin, "type": typ},
		"repository_selection": in.RepositorySelection, "suspended_at": suspended,
	}
}

func (s *Server) listAppInstallations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.jwtOK(r) {
		fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	lo, hi := s.paginate(w, r, len(s.Installations))
	out := []map[string]any{}
	for _, in := range s.Installations[lo:hi] {
		out = append(out, installationJSON(in))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) installationToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.jwtOK(r) {
		fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	in := s.installation(id)
	if in == nil || in.Suspended {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	tok := s.nextToken("ghs_inst")
	s.instTokens[tok] = id
	writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

func (s *Server) scopedToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, pass, ok := r.BasicAuth()
	if !ok || r.PathValue("client") != s.App.ClientID || user != s.App.ClientID || pass != s.App.ClientSecret {
		fail(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	var in struct {
		AccessToken   string  `json:"access_token"`
		TargetID      int64   `json:"target_id"`
		RepositoryIDs []int64 `json:"repository_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusUnprocessableEntity, "Invalid request")
		return
	}
	login, ok := s.userTokens[in.AccessToken]
	if !ok {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	tok := s.nextToken("ghu_scoped")
	s.scopedTokens[tok] = scoped{login: login, repoIDs: in.RepositoryIDs}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": tok, "expires_at": time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
		"user": map[string]any{"login": s.Users[login].Login, "id": s.Users[login].ID},
	})
}

func (s *Server) redirectRepo(w http.ResponseWriter, r *http.Request, to, tail string) {
	http.Redirect(w, r, "/repos/"+to+tail, http.StatusMovedPermanently)
}

func (s *Server) repoInstallation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.jwtOK(r) {
		fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, "/installation")
		return
	}
	if rp == nil || rp.InstallationID == 0 || s.installation(rp.InstallationID) == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	in := s.installation(rp.InstallationID)
	writeJSON(w, http.StatusOK, map[string]any{"id": in.ID, "account": map[string]any{"login": in.AccountLogin}})
}

// canRead 报告令牌能否读该仓库：覆盖它的安装令牌、用户令牌，或包含它的收窄令牌。调用方持锁。
func (s *Server) canRead(tok string, rp *Repo) bool {
	if id, ok := s.instTokens[tok]; ok {
		return id == rp.InstallationID && s.installation(id) != nil
	}
	if _, ok := s.userTokens[tok]; ok {
		return true
	}
	if sc, ok := s.scopedTokens[tok]; ok {
		for _, id := range sc.repoIDs {
			if id == rp.ID {
				return true
			}
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
	writeJSON(w, http.StatusOK, s.repoJSON(r, rp))
}

func (s *Server) userInstallations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userOf(bearer(r)) == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	lo, hi := s.paginate(w, r, len(s.Installations))
	out := []map[string]any{}
	for _, in := range s.Installations[lo:hi] {
		out = append(out, installationJSON(in))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(s.Installations), "installations": out})
}

func (s *Server) userInstallationRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userOf(bearer(r)) == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if s.installation(id) == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	var repos []*Repo
	for _, rp := range s.Repos {
		if rp.InstallationID == id {
			repos = append(repos, rp)
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	lo, hi := s.paginate(w, r, len(repos))
	out := []map[string]any{}
	for _, rp := range repos[lo:hi] {
		out = append(out, s.repoJSON(r, rp))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": out})
}
