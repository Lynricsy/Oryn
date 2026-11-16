package githubtest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s → %d", url, resp.StatusCode)
	}
}

// seed 一个仓库后可 clone / push；改名后旧地址照样可用、新地址指向同一个裸仓库。
func TestGitSmartHTTP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	s := New()
	t.Cleanup(s.Close)
	s.GitRoot = t.TempDir()
	post(t, s.URL()+"/__fake/seed", `{"repos":[{"id":42,"fullName":"Acme/Widgets","ownerId":900,"installationId":10}],"installations":[{"id":10,"accountLogin":"Acme"}]}`)

	work := filepath.Join(t.TempDir(), "w")
	gitCmd(t, filepath.Dir(work), "clone", "-q", s.URL()+"/acme/widgets.git", work)
	if _, err := os.Stat(filepath.Join(work, "README.md")); err != nil {
		t.Fatalf("clone 后缺 README.md：%v", err)
	}
	if len(s.CallsTo("GET", "/acme/widgets.git/info/refs")) == 0 {
		t.Fatal("Calls 里没有 /acme/widgets.git/info/refs")
	}

	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", "a.txt")
	gitCmd(t, work, "commit", "-q", "-m", "a")
	gitCmd(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")

	post(t, s.URL()+"/__fake/rename", `{"from":"acme/widgets","to":"acme/gadgets"}`)
	refs := gitCmd(t, work, "ls-remote", s.URL()+"/acme/gadgets")
	if !strings.Contains(refs, "refs/heads/feature") {
		t.Fatalf("改名后新地址看不到分支：%s", refs)
	}
	if refs := gitCmd(t, work, "ls-remote", "origin"); !strings.Contains(refs, "refs/heads/feature") {
		t.Fatalf("改名后旧地址不可用：%s", refs)
	}
}

// seed 的 PR / 评论 / rollup 字段按小驼峰键解析；不可达的投递只进投递日志，可达的真实 POST 并带签名，guid 可重放。
func TestSeedAndDeliverControl(t *testing.T) {
	s := New()
	t.Cleanup(s.Close)
	post(t, s.URL()+"/__fake/seed", `{"repos":[{"id":42,"fullName":"Acme/Widgets"}],
		"pulls":[{"repoId":42,"number":12,"title":"Fix","headSha":"abc","headRef":"fix","mergedAt":"2026-11-16T08:00:00Z"}],
		"comments":[{"repoId":42,"number":12,"author":"alice","body":"hi","path":"a.go","line":3}],
		"rollups":{"abc":"FAILURE"}}`)
	s.Lock()
	if len(s.Pulls) != 1 || s.Pulls[0].HeadSHA != "abc" || s.Pulls[0].Number != 12 || s.Pulls[0].MergedAt.IsZero() || s.Pulls[0].BaseRef != "main" {
		t.Fatalf("PR 种子解析错误：%+v", s.Pulls)
	}
	if len(s.Comments) != 1 || s.Comments[0].Line != 3 || s.Comments[0].ID == 0 || s.Rollups["abc"] != "FAILURE" {
		t.Fatalf("评论 / rollup 种子解析错误：%+v %v", s.Comments, s.Rollups)
	}
	s.Unlock()

	var got []string
	hook := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-GitHub-Event")+" "+r.Header.Get("X-GitHub-Delivery")+" "+r.Header.Get("X-Hub-Signature-256"))
		w.WriteHeader(http.StatusAccepted)
	})
	post(t, s.URL()+"/__fake/deliver", `{"target":"`+hook+`","event":"ping","payload":{"zen":"x"},"reachable":false,"guid":"g-1"}`)
	post(t, s.URL()+"/__fake/deliver", `{"target":"`+hook+`","event":"ping","payload":{"zen":"x"},"reachable":true,"guid":"g-1"}`)
	s.Lock()
	n := len(s.Deliveries)
	first := s.Deliveries[0].StatusCode
	s.Unlock()
	if n != 2 || first != http.StatusBadGateway {
		t.Fatalf("投递日志：%d 条，第一条状态 %d", n, first)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "ping g-1 sha256=") {
		t.Fatalf("可达投递：%v", got)
	}
}

func httptestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
