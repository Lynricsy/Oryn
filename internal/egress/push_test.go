package egress

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushR 是实现 PushRecorder 的 fakeR（M24）。
type pushR struct {
	fakeR
	mu  *sync.Mutex
	got *[]GitPush
}

func newPushR(f fakeR) pushR { return pushR{fakeR: f, mu: &sync.Mutex{}, got: &[]GitPush{}} }

func (p pushR) GitPushed(g GitPush) {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.got = append(*p.got, g)
}

func (p pushR) take() []GitPush {
	p.mu.Lock()
	defer p.mu.Unlock()
	g := *p.got
	*p.got = nil
	return g
}

func postPush(t *testing.T, url string, body []byte) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/x-git-receive-pack-request", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// 非 GitHub 主机（PAT）的推送：上游 200 才记录，命令取自请求体，请求体原样到达上游；上游 403 与 upload-pack 不记录。
func TestPushRecorderPATHost(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	var gotBody []byte
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(up.Close)
	old := http.DefaultTransport
	http.DefaultTransport = up.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })

	host := strings.TrimPrefix(up.URL, "https://")
	r := newPushR(fakeR{git: map[string]string{host: "tok"}, pol: Policy{GitRepos: map[string][]string{host: {"Acme/Widgets.git"}}}})
	srv := httptest.NewServer(NewHandler("orb-x", r))
	t.Cleanup(srv.Close)

	body := append(pkt(oidA+" "+oidB+" refs/heads/x\x00report-status", "0000"), "PACK"...)
	code, _ := postPush(t, srv.URL+"/git/"+host+"/acme/widgets.git/git-receive-pack", body)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, body, gotBody, "请求体原样到达上游")
	assert.Equal(t, []GitPush{{Host: host, Repo: "acme/widgets", Refs: []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/x"}}}}, r.take())

	status.Store(http.StatusForbidden)
	code, _ = postPush(t, srv.URL+"/git/"+host+"/acme/widgets.git/git-receive-pack", body)
	require.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, r.take(), "上游拒绝时不记录")

	status.Store(http.StatusOK)
	code, _ = postPush(t, srv.URL+"/git/"+host+"/acme/widgets.git/git-upload-pack", body)
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, r.take(), "upload-pack 不记录")
}

// github.com 开放时：门禁已解析的推送复用它的命令；门禁拒绝（ReviewOnly 回 ng）时不触达上游，也不记录。
func TestPushRecorderGitHubGate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
		_, _ = w.Write(pkt("unpack ok\n", "0000"))
	}))
	t.Cleanup(up.Close)
	gl := newGateFake()
	r := newPushR(fakeR{
		gh: "T0KEN", git: map[string]string{"github.com": "tok"}, rec: &writeRec{}, gl: gl,
		pol: Policy{GitHub: &GitHubAccess{APIOrigin: up.URL, GitOrigin: up.URL}},
	})
	srv := httptest.NewServer(NewHandler("orb-x", r))
	t.Cleanup(srv.Close)

	body := append(pkt(oidA+" "+oidB+" refs/heads/feat\x00report-status", "0000"), "PACK"...)
	code, out := postPush(t, srv.URL+"/git/github.com/Acme/Widgets.git/git-receive-pack", body)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []GitPush{{Host: "github.com", Repo: "acme/widgets", Refs: []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/feat"}}}}, r.take())

	gl.mu.Lock()
	gl.thread = ThreadGates{ReviewOnly: true}
	gl.mu.Unlock()
	code, out = postPush(t, srv.URL+"/git/github.com/acme/widgets.git/git-receive-pack", body)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, out, denyReviewOnly)
	assert.Empty(t, r.take(), "门禁拒绝时不记录")
}
