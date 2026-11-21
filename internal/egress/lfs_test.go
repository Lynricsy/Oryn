package egress

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/lfs"
)

const lfsTok = "s3cr3t-gh-value"

var lfsOID = strings.Repeat("a", 64)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// lfsEnv 是 github.com 开放、git 上游指向假上游的出站代理；orb 侧经 proxy 客户端发代理形式请求。
type lfsEnv struct {
	h      *Handler
	up     *httptest.Server
	client *http.Client
	mu     sync.Mutex
	batch  func(w http.ResponseWriter, r *http.Request) // 上游对 batch 的应答
	// 上游记下的最近一次请求
	gotAuth, gotBody, gotCL, gotPath string
}

func newLFSEnv(t *testing.T, gl *gateFake) *lfsEnv {
	t.Helper()
	e := &lfsEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.gotAuth, e.gotBody, e.gotCL, e.gotPath = r.Header.Get("Authorization"), string(body), r.Header.Get("Content-Length"), r.URL.RequestURI()
		batch := e.batch
		e.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch") {
			batch(w, r)
			return
		}
		_, _ = io.WriteString(w, "object-content")
	}))
	t.Cleanup(e.up.Close)
	e.h = NewHandler("orb-x", fakeR{
		git: map[string]string{"github.com": lfsTok}, gl: gl,
		pol: Policy{GitHub: &GitHubAccess{GitOrigin: e.up.URL, APIOrigin: e.up.URL}},
	})
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	pu, _ := url.Parse(srv.URL)
	e.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	return e
}

func (e *lfsEnv) setBatch(f func(w http.ResponseWriter, r *http.Request)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batch = f
}

// jsonBatch 让上游以 200 回 objects。
func (e *lfsEnv) jsonBatch(objs ...lfs.ObjectResult) {
	e.setBatch(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", lfs.MediaType)
		_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Transfer: "basic", Objects: objs})
	})
}

func (e *lfsEnv) do(t *testing.T, method, target, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", lfs.MediaType)
	}
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const ghBatch = "http://github.com/acme/w.git/info/lfs/objects/batch"

func downloadBody() string {
	return `{"operation":"download","objects":[{"oid":"` + lfsOID + `","size":14}]}`
}

func decodeBatch(t *testing.T, body string) lfs.BatchResponse {
	t.Helper()
	var br lfs.BatchResponse
	require.NoError(t, json.Unmarshal([]byte(body), &br), body)
	return br
}

// 回显型上游（Gitea）：同主机的 action 改写成经代理的 http:// 地址并去掉回显的凭据；随后凭门票的 GET 注入令牌，
// 别的方法与过期的门票被拒。
func TestLFSBatchSameHostRewrite(t *testing.T) {
	e := newLFSEnv(t, nil)
	e.setBatch(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", lfs.MediaType)
		_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.ObjectResult{{
			OID: lfsOID, Size: 14,
			Actions: map[string]*lfs.Action{"download": {
				Href:   e.up.URL + "/media/" + lfsOID,
				Header: map[string]string{"Authorization": r.Header.Get("Authorization"), "Accept": "application/octet-stream"},
			}},
		}}})
	})
	resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "Basic "+b64("x-access-token:"+lfsTok), e.gotAuth)
	assert.NotContains(t, body, lfsTok)
	assert.NotContains(t, body, b64("x-access-token:"+lfsTok))
	br := decodeBatch(t, body)
	require.Len(t, br.Objects, 1)
	dl := br.Objects[0].Actions["download"]
	require.NotNil(t, dl)
	assert.Equal(t, "http://github.com/media/"+lfsOID, dl.Href)
	assert.Equal(t, map[string]string{"Accept": "application/octet-stream"}, dl.Header)
	assert.Positive(t, dl.ExpiresIn)

	resp, body = e.do(t, http.MethodGet, dl.Href, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "object-content", body)
	assert.Equal(t, "Basic "+b64("x-access-token:"+lfsTok), e.gotAuth, "门票请求注入主机令牌")
	assert.Equal(t, "/media/"+lfsOID, e.gotPath)

	resp, body = e.do(t, http.MethodPut, dl.Href, "x")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, body, denyProxyNotGit, "门票只对登记的方法有效")

	e.h.now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	resp, _ = e.do(t, http.MethodGet, dl.Href, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "门票过期")
}

// 其它主机的 action：href 原样交给 orb 直连，上游的签名头保留，带已知令牌的头删掉。
func TestLFSBatchOtherHost(t *testing.T) {
	e := newLFSEnv(t, nil)
	e.jsonBatch(lfs.ObjectResult{OID: lfsOID, Size: 14, Actions: map[string]*lfs.Action{"download": {
		Href:   "https://objects.example/x?sig=1",
		Header: map[string]string{"X-Amz-Date": "20261121T000000Z", "X-Cred": "Basic " + b64("x-access-token:"+lfsTok)},
	}}})
	resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	dl := decodeBatch(t, body).Objects[0].Actions["download"]
	require.NotNil(t, dl)
	assert.Equal(t, "https://objects.example/x?sig=1", dl.Href)
	assert.Equal(t, map[string]string{"X-Amz-Date": "20261121T000000Z"}, dl.Header)
}

// GitLab 式上游在同主机 action 里新签发凭据：orb 拿不到它，经门票的 GET 仍可用。
func TestLFSBatchFreshCredentialStripped(t *testing.T) {
	e := newLFSEnv(t, nil)
	fresh := "Basic " + b64("u:fresh-cred-123")
	e.jsonBatch(lfs.ObjectResult{OID: lfsOID, Size: 14, Actions: map[string]*lfs.Action{"download": {
		Href: e.up.URL + "/acme/w.git/gitlab-lfs/objects/" + lfsOID, Header: map[string]string{"Authorization": fresh},
	}}})
	resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.NotContains(t, body, "fresh-cred-123")
	assert.NotContains(t, body, b64("u:fresh-cred-123"))
	dl := decodeBatch(t, body).Objects[0].Actions["download"]
	require.NotNil(t, dl)
	resp, body = e.do(t, http.MethodGet, dl.Href, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "Basic "+b64("x-access-token:"+lfsTok), e.gotAuth)
}

// 上游 gzip 时由 Transport 解压，改写读到的是明文。
func TestLFSBatchGzipUpstream(t *testing.T) {
	e := newLFSEnv(t, nil)
	e.setBatch(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.Header.Get("Accept-Encoding"), "gzip")
		w.Header().Set("Content-Type", lfs.MediaType)
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_ = json.NewEncoder(zw).Encode(lfs.BatchResponse{Objects: []lfs.ObjectResult{{OID: lfsOID, Size: 14,
			Actions: map[string]*lfs.Action{"download": {Href: e.up.URL + "/media/" + lfsOID}}}}})
		_ = zw.Close()
	})
	resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "http://github.com/media/"+lfsOID, decodeBatch(t, body).Objects[0].Actions["download"].Href)
}

// 失败关闭：认不出的 batch 响应换成 502 的 LFS 错误体，上游的内容不交给 orb。
func TestLFSBatchFailClosed(t *testing.T) {
	e := newLFSEnv(t, nil)
	big := `{"objects":[],"message":"sentinel` + strings.Repeat("x", lfsBatchRespMax) + `"}`
	for name, f := range map[string]func(w http.ResponseWriter, r *http.Request){
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json sentinel") },
		"too big":  func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, big) },
		"br": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, `{"objects":[],"message":"sentinel"}`)
		},
		"201": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"objects":[],"message":"sentinel"}`)
		},
	} {
		e.setBatch(f)
		resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, name)
		assert.Equal(t, lfs.MediaType, resp.Header.Get("Content-Type"), name)
		assert.NotContains(t, body, "sentinel", name)
		assert.Contains(t, body, `"message":"oryn: `, name)
	}
}

// 同主机但端口不同、路径不规范的 href：该对象没有 actions、error 422；未知名字的 action 丢掉，同对象的 download 不受影响。
func TestLFSBatchActionChecks(t *testing.T) {
	e := newLFSEnv(t, nil)
	u, _ := url.Parse(e.up.URL)
	port := "1"
	if u.Port() == port {
		port = "2"
	}
	e.jsonBatch(
		lfs.ObjectResult{OID: lfsOID, Size: 1, Actions: map[string]*lfs.Action{"download": {Href: "http://" + u.Hostname() + ":" + port + "/x"}}},
		lfs.ObjectResult{OID: lfsOID, Size: 2, Actions: map[string]*lfs.Action{
			"download": {Href: e.up.URL + "/ok"}, "sync": {Href: e.up.URL + "/sync"},
		}},
		lfs.ObjectResult{OID: lfsOID, Size: 3, Actions: map[string]*lfs.Action{"upload": {Href: e.up.URL + "/a/../b"}}},
		lfs.ObjectResult{OID: lfsOID, Size: 4, Actions: map[string]*lfs.Action{"download": {Href: "https://u:p@objects.example/x"}}},
		lfs.ObjectResult{OID: lfsOID, Size: 5, Actions: map[string]*lfs.Action{"download": {Href: e.up.URL + "/old", ExpiresIn: -1, ExpiresAt: "2000-01-01T00:00:00Z"}}},
	)
	resp, body := e.do(t, http.MethodPost, ghBatch, downloadBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	objs := decodeBatch(t, body).Objects
	require.Len(t, objs, 5)
	for _, i := range []int{0, 2, 3, 4} {
		assert.Nil(t, objs[i].Actions, i)
		require.NotNil(t, objs[i].Error, i)
		assert.Equal(t, http.StatusUnprocessableEntity, objs[i].Error.Code, i)
	}
	assert.Nil(t, objs[1].Error)
	assert.Len(t, objs[1].Actions, 1)
	assert.Equal(t, "http://github.com/ok", objs[1].Actions["download"].Href)
}

// 请求体规范化：上游收到的就是代理据以判断读写的那一份。
func TestLFSBatchRequestNormalized(t *testing.T) {
	e := newLFSEnv(t, nil)
	e.jsonBatch()
	resp, body := e.do(t, http.MethodPost, ghBatch, `{"operation":"upload","OPERATION":"download","transfers":["tus"],"objects":[],"x":1}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	want := `{"operation":"download","transfers":["basic"],"objects":[]}`
	assert.Equal(t, want, e.gotBody)
	assert.Equal(t, strconv.Itoa(len(want)), e.gotCL)
	assert.JSONEq(t, `{"transfer":"basic","objects":[]}`, body, "objects 为空时仍输出空数组")
}

// 只读评审线程：LFS 上传被拒（JSON 错误体），下载照常。
func TestLFSReviewOnly(t *testing.T) {
	gl := newGateFake()
	gl.thread = ThreadGates{ReviewOnly: true}
	e := newLFSEnv(t, gl)
	e.jsonBatch()
	resp, body := e.do(t, http.MethodPost, ghBatch, `{"operation":"upload","objects":[]}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, lfs.MediaType, resp.Header.Get("Content-Type"))
	var m struct{ Message string }
	require.NoError(t, json.Unmarshal([]byte(body), &m))
	assert.Equal(t, "oryn: "+denyReviewOnly, m.Message)
	resp, body = e.do(t, http.MethodPost, ghBatch, `{"operation":"download","objects":[]}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode, body)
}

// 分类与放行：PAT 主机上的读写判定、请求体校验与门票不遮蔽已识别的端点。
func TestLFSClassify(t *testing.T) {
	req := func(method, target, body string, hdr ...string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return r
	}
	const base = "http://git.example/a/b.git/info/lfs/"
	objs := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `{"oid":"` + lfsOID + `","size":1}`
		}
		return `{"operation":"download","objects":[` + strings.Join(parts, ",") + `]}`
	}
	ro := NewHandler("orb-x", fakeR{git: map[string]string{"git.example": "t"}})
	for _, c := range []struct {
		r    *http.Request
		want int
	}{
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[]}`), 200},
		{req("POST", base+"objects/batch", `{"operation":"upload","objects":[]}`), 403},
		{req("POST", base+"objects/batch", `{`), 422},
		{req("POST", base+"objects/batch", `{"operation":"verify","objects":[]}`), 422},
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[{"oid":"../x","size":1}]}`), 422},
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[{"oid":"`+lfsOID+`","size":-1}]}`), 422},
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[],"hash_algo":"sha512"}`), 422},
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[],"x":"`+strings.Repeat("y", lfs.BatchBodyMax)+`"}`), 413},
		{req("POST", base+"objects/batch", objs(lfs.BatchMaxObjects+1)), 413},
		{req("POST", base+"objects/batch", objs(2)), 200},
		{req("POST", base+"objects/batch", `{"operation":"download","objects":[]}`, "Content-Encoding", "gzip"), 415},
		{req("GET", base+"locks", ""), 200},
		{req("POST", base+"locks", `{}`), 403},
		{req("POST", base+"locks/verify", `{}`), 403},
		{req("POST", base+"locks/L-1/unlock", `{}`), 403},
		{req("POST", base+"locks/a%2Fb/unlock", `{}`), 403},
		{req("GET", base+"objects/batch", ""), 403},
		{req("DELETE", base+"locks", ""), 403},
		{req("POST", "http://git.example/a.git/info/lfs/objects/batch", `{"operation":"download","objects":[]}`), 403},
		{req("POST", "/git/git.example/a/b.git/info/lfs/objects/batch", `{"operation":"download","objects":[]}`), 200},
		{req("GET", "/git/git.example/a/b.git/info/lfs/objects/batch", ""), 404},
	} {
		tg, st := ro.resolve(c.r)
		assert.Equal(t, c.want, st, "%s %s", c.r.Method, c.r.URL)
		if st != 200 && st != 404 && c.r.URL.Host != "" && strings.Contains(c.r.URL.Path, "/info/lfs/") {
			assert.True(t, tg.lfs || tg.deny == denyProxyNotGit, "%s %s", c.r.Method, c.r.URL)
		}
	}
	tg, st := ro.resolve(req("GET", base+"objects/batch", ""))
	require.Equal(t, http.StatusForbidden, st)
	assert.Equal(t, denyProxyNotGit, tg.deny)
	tg, st = ro.resolve(req("POST", base+"objects/batch", `{"operation":"upload","objects":[]}`))
	require.Equal(t, http.StatusForbidden, st)
	assert.True(t, tg.lfs)
	assert.Equal(t, denyGitReadOnly, tg.deny)
	tg, st = ro.resolve(req("POST", base+"objects/batch", `{"operation":"download","objects":[]}`))
	require.Equal(t, http.StatusOK, st)
	assert.True(t, tg.lfs && tg.lfsBatch && tg.stripEncoding)
	assert.Equal(t, "https://git.example/a/b.git/info/lfs/objects/batch", tg.url.String())

	// 有项目仓库：只放行项目仓库（读写）。
	proj := NewHandler("orb-x", fakeR{git: map[string]string{"git.example": "t"}, pol: Policy{GitRepos: map[string][]string{"git.example": {"a/b"}}}})
	_, st = proj.resolve(req("POST", "http://git.example/c/d.git/info/lfs/objects/batch", `{"operation":"download","objects":[]}`))
	assert.Equal(t, http.StatusForbidden, st)
	_, st = proj.resolve(req("POST", base+"objects/batch", `{"operation":"upload","objects":[]}`))
	assert.Equal(t, http.StatusOK, st)

	// 门票不遮蔽已识别的端点：同形的推送路径照普通推送判定（不是项目仓库 → 403），而不是凭门票放行。
	upstream, _ := url.Parse("https://git.example/other/repo.git/git-receive-pack")
	proj.lfsTickets = map[string]lfsTicket{"POST http://git.example/other/repo.git/git-receive-pack": {
		upstream: upstream, host: "git.example", repo: "a/b", write: true, expires: time.Now().Add(time.Hour),
	}}
	tg, st = proj.resolve(req("POST", "http://git.example/other/repo.git/git-receive-pack", ""))
	assert.Equal(t, http.StatusForbidden, st)
	assert.Equal(t, denyGitOtherRepo, tg.deny)
	assert.Nil(t, tg.url)
}

// 验收：无项目 orb 上传 → 403，LFS JSON 错误体。
func TestLFSUploadDeniedJSON(t *testing.T) {
	h := NewHandler("orb-x", fakeR{git: map[string]string{"git.example": "t"}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	pu, _ := url.Parse(srv.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Post("http://git.example/a/b.git/info/lfs/objects/batch", lfs.MediaType, bytes.NewReader([]byte(`{"operation":"upload","objects":[]}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, lfs.MediaType, resp.Header.Get("Content-Type"))
	assert.Equal(t, `{"message":"oryn: `+denyGitReadOnly+`"}`, string(body))
}

// 门票超过上限时先删最早到期的。
func TestLFSTicketCap(t *testing.T) {
	h := NewHandler("orb-x", fakeR{})
	now := time.Now()
	tks := make(map[string]lfsTicket, lfsTicketMax)
	for i := range lfsTicketMax {
		tks["GET http://h/"+strconv.Itoa(i)] = lfsTicket{expires: now.Add(time.Hour + time.Duration(i)*time.Second)}
	}
	h.addTickets(tks, now)
	require.Len(t, h.lfsTickets, lfsTicketMax)
	h.addTickets(map[string]lfsTicket{"GET http://h/new": {expires: now.Add(2 * time.Hour)}}, now)
	assert.Len(t, h.lfsTickets, lfsTicketMax)
	_, ok := h.lfsTickets["GET http://h/0"]
	assert.False(t, ok, "最早到期的被删")
	_, ok = h.lfsTickets["GET http://h/new"]
	assert.True(t, ok)
}
