package githubtest

import (
	"bytes"
	"net/http"
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
