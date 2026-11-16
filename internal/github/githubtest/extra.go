package githubtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 本文件是 cmd/fakegithub 用到的扩展（M14 步骤 9.1）：git smart-HTTP（git http-backend）与只供验收脚本调用的控制端点。

// gitPathRe 匹配 smart-HTTP 端点：/<owner>/<repo>[.git]/<尾部>。
var gitPathRe = regexp.MustCompile(`^/([^/]+)/([^/]+?)(\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)

// serveExtra 处理 git 与 /__fake/ 控制端点；返回是否已处理。请求已由 handler 记进 Calls。
func (s *Server) serveExtra(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/__fake/") {
		s.serveControl(w, r)
		return true
	}
	m := gitPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil || s.GitRoot == "" {
		return false
	}
	s.serveGit(w, r, m[1], m[2], m[4])
	return true
}

// barePath 是仓库 full_name 对应的裸仓库目录（一律小写）。
func (s *Server) barePath(fullName string) string {
	owner, name, _ := strings.Cut(strings.ToLower(fullName), "/")
	return filepath.Join(s.GitRoot, owner, name+".git")
}

func (s *Server) serveGit(w http.ResponseWriter, r *http.Request, owner, name, tail string) {
	full := strings.ToLower(owner + "/" + name)
	s.mu.Lock()
	if to, ok := s.Renamed[full]; ok {
		full = strings.ToLower(to)
	}
	s.mu.Unlock()
	git, err := exec.LookPath("git")
	if err != nil {
		fail(w, http.StatusInternalServerError, "git not found")
		return
	}
	if _, err := os.Stat(s.barePath(full)); err != nil {
		fail(w, http.StatusNotFound, "Repository not found.")
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + full + ".git/" + tail
	r2.URL.RawPath = ""
	h := &cgi.Handler{
		Path: git,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + s.GitRoot, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=oryn"},
	}
	h.ServeHTTP(w, r2)
}

// initBare 在 GitRoot 下建一个裸仓库（已存在则不动），开放推送，并推入含 README.md 的初始提交到 main。
func (s *Server) initBare(fullName string) error {
	dir := s.barePath(fullName)
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	run := func(cwd string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fake", "GIT_AUTHOR_EMAIL=fake@example.com",
			"GIT_COMMITTER_NAME=fake", "GIT_COMMITTER_EMAIL=fake@example.com", "GIT_CONFIG_NOSYSTEM=1", "HOME="+os.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := run(filepath.Dir(dir), "init", "--bare", "-q", "-b", "main", dir); err != nil {
		return err
	}
	if err := run(dir, "config", "http.receivepack", "true"); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "fakegithub-seed")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# "+fullName+"\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "README.md"},
		{"commit", "-q", "-m", "initial commit"},
		{"push", "-q", dir, "main"},
	} {
		if err := run(work, args...); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- 控制端点

// SeedState 是 /__fake/seed 的请求体：整体替换可变状态。
type SeedState struct {
	Users []struct {
		ID          int64  `json:"id"`
		Login       string `json:"login"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		FailRefresh bool   `json:"failRefresh"`
	} `json:"users"`
	Installations []Installation `json:"installations"`
	Repos         []struct {
		ID             int64             `json:"id"`
		FullName       string            `json:"fullName"`
		OwnerID        int64             `json:"ownerId"`
		DefaultBranch  string            `json:"defaultBranch"`
		Private        bool              `json:"private"`
		Description    string            `json:"description"`
		InstallationID int64             `json:"installationId"`
		Permissions    map[string]string `json:"permissions"`
	} `json:"repos"`
	// Issues / Pulls / Comments 整体替换（JSON 键按字段名、不区分大小写，如 repoId、headSha、mergedAt）。
	Issues   []Issue   `json:"issues"`
	Pulls    []Pull    `json:"pulls"`
	Comments []Comment `json:"comments"`
	// Rollups 是提交 sha → statusCheckRollup 状态（SUCCESS / FAILURE / ERROR / PENDING / EXPECTED）。
	Rollups map[string]string `json:"rollups"`
	// Checks 是提交 sha → 检查与旧式提交状态。
	Checks map[string]struct {
		Runs     []CheckRun     `json:"runs"`
		Statuses []CommitStatus `json:"statuses"`
	} `json:"checks"`
}

// DeliverRequest 是 /__fake/deliver 的请求体。
type DeliverRequest struct {
	Target    string          `json:"target"`
	Event     string          `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	Reachable bool            `json:"reachable"`
	GUID      string          `json:"guid"`
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch r.Method + " " + r.URL.Path {
	case "POST /__fake/seed":
		var in SeedState
		if err := json.Unmarshal(body, &in); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.seed(in); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "POST /__fake/rename":
		var in struct{ From, To string }
		if err := json.Unmarshal(body, &in); err != nil || in.From == "" || in.To == "" {
			fail(w, http.StatusBadRequest, "from and to are required")
			return
		}
		if s.GitRoot != "" {
			if err := os.MkdirAll(filepath.Dir(s.barePath(in.To)), 0o755); err == nil {
				_ = os.Rename(s.barePath(in.From), s.barePath(in.To))
			}
		}
		s.Rename(in.From, in.To)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "POST /__fake/oauth":
		var in struct {
			Code        string `json:"code"`
			Login       string `json:"login"`
			ExpiresIn   *int64 `json:"expiresIn"`
			FailRefresh bool   `json:"failRefresh"`
			// Scopes 覆盖此后 OAuth App 令牌授予的 scope（`a, b, c`）；缺省不变。
			Scopes *string `json:"scopes"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Code == "" || in.Login == "" {
			fail(w, http.StatusBadRequest, "code and login are required")
			return
		}
		s.mu.Lock()
		u, ok := s.Users[strings.ToLower(in.Login)]
		if ok {
			u.FailRefresh = in.FailRefresh
			if in.ExpiresIn != nil {
				s.UserTokenTTL = *in.ExpiresIn
			}
			if in.Scopes != nil {
				s.Scopes = *in.Scopes
			}
		}
		s.mu.Unlock()
		if !ok {
			fail(w, http.StatusNotFound, "unknown user")
			return
		}
		s.GrantCode(in.Code, in.Login)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "GET /__fake/calls":
		s.mu.Lock()
		out := make([]map[string]any, 0, len(s.Calls))
		for _, c := range s.Calls {
			if strings.HasPrefix(c.Path, "/__fake/") {
				continue
			}
			out = append(out, map[string]any{"method": c.Method, "path": c.Path, "query": c.Query, "auth": c.Auth, "body": string(c.Body)})
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	case "DELETE /__fake/calls":
		s.ResetCalls()
		w.WriteHeader(http.StatusNoContent)
	case "GET /__fake/app":
		s.mu.Lock()
		app, oa := s.App, s.OAuthApp
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"id": app.ID, "slug": app.Slug, "clientId": app.ClientID, "clientSecret": app.ClientSecret,
			"webhookSecret": app.WebhookSecret, "manifestCode": app.ManifestCode,
			"oauthClientId": oa.ClientID, "oauthClientSecret": oa.ClientSecret,
		})
	case "POST /__fake/deliver":
		var in DeliverRequest
		if err := json.Unmarshal(body, &in); err != nil || in.Event == "" || len(in.Payload) == 0 || (in.Reachable && in.Target == "") {
			fail(w, http.StatusBadRequest, "event and payload are required (target when reachable)")
			return
		}
		status, guid := s.deliverReach(in)
		writeJSON(w, http.StatusOK, map[string]any{"guid": guid, "status": status})
	default:
		fail(w, http.StatusNotFound, "unknown control endpoint")
	}
}

// deliverReach 按 reachable 投递一次：不可达时只记投递日志（status_code 502）。
func (s *Server) deliverReach(in DeliverRequest) (int, string) {
	s.mu.Lock()
	prev := s.Unreachable
	s.Unreachable = !in.Reachable
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.Unreachable = prev
		s.mu.Unlock()
	}()
	return s.DeliverGUID(in.Target, in.Event, in.Payload, in.GUID)
}

// seed 整体替换用户、安装、仓库、issue / PR / 评论与 CI 状态（改名记录保留）；GitRoot 非空时为每个仓库建裸仓库。
func (s *Server) seed(in SeedState) error {
	s.mu.Lock()
	s.Users = map[string]*User{}
	for _, u := range in.Users {
		s.Users[strings.ToLower(u.Login)] = &User{ID: u.ID, Login: u.Login, Name: u.Name, Type: u.Type, FailRefresh: u.FailRefresh,
			AvatarURL: fmt.Sprintf("%s/avatars/%s", s.URL(), u.Login)}
	}
	s.Installations = nil
	for _, in := range in.Installations {
		v := in
		if v.AccountType == "" {
			v.AccountType = "User"
		}
		if v.RepositorySelection == "" {
			v.RepositorySelection = "all"
		}
		s.Installations = append(s.Installations, &v)
	}
	s.Repos = nil
	for i := range in.Issues {
		fillIssue(&in.Issues[i])
	}
	for i := range in.Pulls {
		fillIssue(&in.Pulls[i].Issue)
		if in.Pulls[i].BaseRef == "" {
			in.Pulls[i].BaseRef = "main"
		}
	}
	s.Issues, s.Pulls, s.Comments = nil, nil, nil
	for i := range in.Issues {
		s.Issues = append(s.Issues, &in.Issues[i])
	}
	for i := range in.Pulls {
		s.Pulls = append(s.Pulls, &in.Pulls[i])
	}
	for i := range in.Comments {
		c := &in.Comments[i]
		if c.ID == 0 {
			s.seq++
			c.ID = 1000 + s.seq
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now().UTC().Truncate(time.Second)
		}
		s.Comments = append(s.Comments, c)
	}
	s.Rollups = map[string]string{}
	for k, v := range in.Rollups {
		s.Rollups[k] = v
	}
	s.CheckRuns, s.Statuses = map[string][]CheckRun{}, map[string][]CommitStatus{}
	for sha, c := range in.Checks {
		s.CheckRuns[sha], s.Statuses[sha] = c.Runs, c.Statuses
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i, rp := range in.Repos {
		br := rp.DefaultBranch
		if br == "" {
			br = "main"
		}
		s.Repos = append(s.Repos, &Repo{ID: rp.ID, FullName: rp.FullName, OwnerID: rp.OwnerID, DefaultBranch: br, Private: rp.Private,
			Description: rp.Description, InstallationID: rp.InstallationID, Permissions: rp.Permissions,
			PushedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	s.mu.Unlock()
	if s.GitRoot != "" {
		for _, rp := range in.Repos {
			if err := s.initBare(rp.FullName); err != nil {
				return err
			}
		}
	}
	return nil
}
