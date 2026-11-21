package egress

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostedFake 是记录型的托管仓库通道：off 为真时没有通道（srv 为 nil），err 非 nil 时查线程失败。
type hostedFake struct {
	mu     sync.Mutex
	off    bool
	err    error
	pusher HostedPusher
	seen   []hostedSeen
}

type hostedSeen struct {
	project, endpoint, auth string
	pusher                  HostedPusher
}

func (f *hostedFake) ServeHostedGit(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher HostedPusher) {
	f.mu.Lock()
	f.seen = append(f.seen, hostedSeen{project: projectID, endpoint: endpoint, auth: r.Header.Get("Authorization"), pusher: pusher})
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("hosted"))
}

func (f fakeR) HostedGit() (HostedGitServer, HostedPusher, error) {
	if f.hosted == nil || f.hosted.off {
		return nil, HostedPusher{}, nil
	}
	if f.hosted.err != nil {
		return nil, HostedPusher{}, f.hosted.err
	}
	return f.hosted, f.hosted.pusher, nil
}

func hostedPolicy() Policy {
	return Policy{HostedGit: &HostedGitRoute{Host: "oryn.test", Repo: "git/prj_a", ProjectID: "prj_A"}}
}

func hostedDo(t *testing.T, r fakeR, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	req.Header.Set("Authorization", "Basic b3J5bjpzZWNyZXQ=")
	rec := httptest.NewRecorder()
	NewHandler("orb-1", r).ServeHTTP(rec, req)
	return rec
}

func TestHostedGitRoute(t *testing.T) {
	hf := &hostedFake{pusher: HostedPusher{OrbID: "orb-1", ThreadID: "T-1"}}
	r := fakeR{pol: hostedPolicy(), hosted: hf, git: map[string]string{"oryn.test": "should-not-be-used"}}

	rec := hostedDo(t, r, http.MethodGet, "http://oryn.test/git/prj_A.git/info/refs?service=git-upload-pack")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "hosted", rec.Body.String())
	require.Len(t, hf.seen, 1)
	assert.Equal(t, hostedSeen{project: "prj_A", endpoint: "info/refs", pusher: HostedPusher{OrbID: "orb-1", ThreadID: "T-1"}}, hf.seen[0],
		"在本地服务、不注入令牌、不带入站的 Authorization")

	rec = hostedDo(t, r, http.MethodPost, "http://oryn.test/git/prj_A.git/git-receive-pack")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "git-receive-pack", hf.seen[1].endpoint)

	// 路径形式（M15 之前的 orbd）进同一分支。
	rec = hostedDo(t, r, http.MethodPost, "/git/oryn.test/git/prj_A.git/git-upload-pack")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "git-upload-pack", hf.seen[2].endpoint)

	rec = hostedDo(t, r, http.MethodPost, "http://oryn.test/git/prj_B.git/git-upload-pack")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), denyHostedOther)
	assert.Len(t, hf.seen, 3, "别的托管仓库不到本地服务")
}

func TestHostedGitReviewOnly(t *testing.T) {
	hf := &hostedFake{}
	gl := newGateFake()
	gl.thread = ThreadGates{ReviewOnly: true}
	r := fakeR{pol: hostedPolicy(), hosted: hf, gl: gl}

	rec := hostedDo(t, r, http.MethodPost, "http://oryn.test/git/prj_A.git/git-receive-pack")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), denyReviewOnly)
	rec = hostedDo(t, r, http.MethodGet, "http://oryn.test/git/prj_A.git/info/refs?service=git-receive-pack")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = hostedDo(t, r, http.MethodPost, "http://oryn.test/git/prj_A.git/git-upload-pack")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, hf.seen, 1)

	// LFS 上传同样算写操作，拒绝回 LFS JSON 错误体。
	req := httptest.NewRequest(http.MethodPost, "http://oryn.test/git/prj_A.git/info/lfs/objects/batch",
		strings.NewReader(`{"operation":"upload","objects":[]}`))
	w := httptest.NewRecorder()
	NewHandler("orb-1", r).ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, lfs.MediaType, w.Header().Get("Content-Type"))
}

func TestHostedGitFailClosed(t *testing.T) {
	const u = "http://oryn.test/git/prj_A.git/git-upload-pack"
	cases := []struct {
		name   string
		r      fakeR
		status int
	}{
		{"协调宿主 NoGit", fakeR{pol: Policy{NoGit: true, HostedGit: hostedPolicy().HostedGit}, hosted: &hostedFake{}}, http.StatusForbidden},
		{"没有通道", fakeR{pol: hostedPolicy(), hosted: &hostedFake{off: true}}, http.StatusForbidden},
		{"查线程失败", fakeR{pol: hostedPolicy(), hosted: &hostedFake{err: errors.New("db down")}}, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := hostedDo(t, c.r, http.MethodPost, u)
			assert.Equal(t, c.status, rec.Code, rec.Body.String())
			assert.Empty(t, c.r.hosted.seen)
		})
	}
}

// 没有托管路由时同一主机照旧按普通 git 主机处理（无令牌 → 404，不会到本地服务）。
func TestHostedGitNoRoute(t *testing.T) {
	hf := &hostedFake{}
	rec := hostedDo(t, fakeR{hosted: hf}, http.MethodPost, "http://oryn.test/git/prj_A.git/git-upload-pack")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, hf.seen)
}

// 托管仓库的 LFS 对象传输（步骤 3.14）：info/lfs/objects/<oid> 只在托管主机上认，GET 读、PUT 写，在本地服务。
func TestHostedGitLFSObjects(t *testing.T) {
	oid := strings.Repeat("ab", 32)
	obj := "http://oryn.test/git/prj_A.git/info/lfs/objects/" + oid
	hf := &hostedFake{pusher: HostedPusher{OrbID: "orb-1", ThreadID: "T-1"}}
	r := fakeR{pol: hostedPolicy(), hosted: hf}

	rec := hostedDo(t, r, http.MethodPut, obj)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, hf.seen, 1)
	assert.Equal(t, hostedSeen{project: "prj_A", endpoint: "info/lfs/objects/" + oid, pusher: hf.pusher}, hf.seen[0],
		"本地服务，入站的 Authorization 被去掉")

	rec = hostedDo(t, r, http.MethodGet, obj)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, hf.seen, 2)

	// 别的托管仓库。
	rec = hostedDo(t, r, http.MethodPut, "http://oryn.test/git/prj_B.git/info/lfs/objects/"+oid)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, lfs.MediaType, rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), denyHostedOther)

	// 不合法的 oid、别的方法都不认（落到门票查找，未命中）。
	rec = hostedDo(t, r, http.MethodGet, "http://oryn.test/git/prj_A.git/info/lfs/objects/"+strings.Repeat("AB", 32))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), denyProxyNotGit)
	rec = hostedDo(t, r, http.MethodDelete, obj)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), denyProxyNotGit)
	assert.Len(t, hf.seen, 2)

	// 只读评审：PUT 拒绝并回 LFS JSON 错误体，GET 照常。
	gl := newGateFake()
	gl.thread = ThreadGates{ReviewOnly: true}
	ro := fakeR{pol: hostedPolicy(), hosted: hf, gl: gl}
	rec = hostedDo(t, ro, http.MethodPut, obj)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, lfs.MediaType, rec.Header().Get("Content-Type"))
	var body struct{ Message string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "oryn: "+denyReviewOnly, body.Message)
	rec = hostedDo(t, ro, http.MethodGet, obj)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, hf.seen, 3)

	// 非托管主机上同样的路径不认：外部上游的传输地址只走门票。
	other := fakeR{pol: hostedPolicy(), hosted: hf, git: map[string]string{"git.example": "tok"}}
	rec = hostedDo(t, other, http.MethodGet, "http://git.example/a/b.git/info/lfs/objects/"+oid)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), denyProxyNotGit)
	assert.Len(t, hf.seen, 3)
}
