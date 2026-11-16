package githubtest

import (
	"encoding/json"
	"io"
	"fmt"
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
	// Extra 由各步骤补充的状态（PR、issue、rollup 等）按 seedExtra 处理。
	Extra json.RawMessage `json:"extra,omitempty"`
}

// seedExtra 处理 SeedState 之外的种子数据；其它步骤在这里接上 PR / issue / rollup。nil = 没有。
var seedExtra func(s *Server, body []byte) error

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch r.Method + " " + r.URL.Path {
	case "POST /__fake/seed":
		var in SeedState
		if err := json.Unmarshal(body, &in); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.seed(in, body); err != nil {
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
		app := s.App
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"id": app.ID, "slug": app.Slug, "clientId": app.ClientID, "clientSecret": app.ClientSecret,
			"webhookSecret": app.WebhookSecret, "manifestCode": app.ManifestCode,
		})
	default:
		if extraControl != nil && extraControl(s, w, r, body) {
			return
		}
		fail(w, http.StatusNotFound, "unknown control endpoint")
	}
}

// extraControl 处理其它步骤补充的控制端点（如 /__fake/deliver）；返回是否已处理。nil = 没有。
var extraControl func(s *Server, w http.ResponseWriter, r *http.Request, body []byte) bool

// seed 整体替换用户、安装与仓库（及 seedExtra 处理的状态）；GitRoot 非空时为每个仓库建裸仓库。
func (s *Server) seed(in SeedState, raw []byte) error {
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
	s.Renamed = map[string]string{}
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
	if seedExtra != nil {
		return seedExtra(s, raw)
	}
	return nil
}
