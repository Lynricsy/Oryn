package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeFsReply 是假 provider 对一次文件请求的回应。
type fakeFsReply struct {
	status int
	hdr    map[string]string
	body   string
}

// runFakeFsProvider 以 provider 令牌长轮询 /provider/v1/fs/claim，把认领到的请求交给 respond，再经 reply 回应。
// 收到的请求依次送入返回的通道。
func runFakeFsProvider(t *testing.T, baseURL, token string, respond func(apigen.ProviderFsRequest) fakeFsReply) <-chan apigen.ProviderFsRequest {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan apigen.ProviderFsRequest, 32)
	stopped := make(chan struct{})
	t.Cleanup(func() { cancel(); <-stopped })
	post := func(path string, hdr map[string]string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return http.DefaultClient.Do(req)
	}
	go func() {
		defer close(stopped)
		for ctx.Err() == nil {
			b, _ := json.Marshal(apigen.ProviderFsClaimRequest{InstanceID: "i1", WaitSeconds: 1})
			res, err := post("/provider/v1/fs/claim", map[string]string{"Content-Type": "application/json"}, bytes.NewReader(b))
			if err != nil {
				continue
			}
			var cr apigen.ProviderFsClaimResponse
			status := res.StatusCode
			if status == http.StatusOK {
				_ = json.NewDecoder(res.Body).Decode(&cr)
			}
			_ = res.Body.Close()
			if status != http.StatusOK {
				continue
			}
			seen <- cr.Request
			rp := respond(cr.Request)
			hdr := map[string]string{"X-Oryn-Status": strconv.Itoa(rp.status)}
			for k, v := range rp.hdr {
				hdr[k] = v
			}
			if res, err := post("/provider/v1/fs/"+cr.Request.ID+"/reply", hdr, bytes.NewReader([]byte(rp.body))); err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
			}
		}
	}()
	return seen
}

// get 以设备令牌发 GET，返回响应与响应体。
func (e *orbsEnv) get(path string, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.dev)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func (e *orbsEnv) createOrb(provider, state string, kind *string) string {
	e.t.Helper()
	id := ids.Orb()
	require.NoError(e.t, e.st.Q.CreateOrb(context.Background(), db.CreateOrbParams{
		ID: id, ProviderID: provider, Type: "small", State: state, StateChangedAt: 1, TokenHash: ids.Hash(ids.Token("oot")), CreatedAt: 1, Kind: kind,
	}))
	return id
}

func (e *orbsEnv) createThread(orbID string, cwd *string) string {
	e.t.Helper()
	id := ids.Thread()
	require.NoError(e.t, e.st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		Kind: "work", ID: id, OrbID: &orbID, Title: "t", TitleSource: "user", State: "idle", SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1, Cwd: cwd,
	}))
	return id
}

func offlineHello(offline bool) apigen.ProviderHelloRequest {
	return apigen.ProviderHelloRequest{
		InstanceID: "i1", Driver: apigen.Docker, Version: "test", DefaultType: "small",
		Types:        []apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}},
		Capabilities: apigen.ProviderCapabilities{OfflineFs: &offline},
	}
}

func TestOfflineFsRelay(t *testing.T) {
	e := newOrbsEnv(t)
	require.Equal(t, http.StatusOK, e.do("POST", "/provider/v1/hello", e.provT, offlineHello(true), nil))
	orb := e.createOrb("prov_1", orbs.StateStopped, nil)
	cwd := "/home/user/workspace/proj"
	thread := e.createThread(orb, &cwd)

	seen := runFakeFsProvider(t, e.srv.URL, e.provT, func(req apigen.ProviderFsRequest) fakeFsReply {
		switch req.Op {
		case apigen.Roots:
			return fakeFsReply{200, map[string]string{"Content-Type": "application/json"}, `{"home":"/home/user","workdir":"/home/user/workspace/proj"}`}
		case apigen.List:
			// 恶意的 Content-Type / Set-Cookie 不得到达浏览器
			return fakeFsReply{200, map[string]string{"Content-Type": "text/html", "Set-Cookie": "x=1"}, `{"path":"/home/user","entries":[],"truncated":false}`}
		case apigen.Stat:
			if *req.Path == "/home/user/redirect" {
				return fakeFsReply{302, map[string]string{"Location": "https://evil.example/"}, ""}
			}
			return fakeFsReply{503, map[string]string{"Content-Type": "application/json", "Retry-After": "2"}, `{"code":"offline_indexing","message":"indexing: 12034 entries scanned"}`}
		default:
			return fakeFsReply{206, map[string]string{"Content-Type": "text/html", "Content-Range": "bytes 0-3/10", "ETag": `"1-10"`}, "abcd"}
		}
	})
	base := "/api/v1/orbs/" + orb + "/fs/"

	// roots：工作目录候选 = 线程 cwd + 两个缺省目录
	res, body := e.get(base+"roots?offline=1&thread="+thread, nil)
	require.Equal(t, http.StatusOK, res.StatusCode, string(body))
	req := <-seen
	assert.Equal(t, apigen.Roots, req.Op)
	assert.Equal(t, orb, req.OrbID)
	assert.Equal(t, []string{cwd, "/home/user/workspace/repo", "/home/user/workspace"}, req.Workdirs)
	assert.JSONEq(t, `{"home":"/home/user","workdir":"/home/user/workspace/proj"}`, string(body))

	// list：JSON 原样转发，套 JSON 响应头
	res, body = e.get(base+"list?offline=1&path=~/x", nil)
	require.Equal(t, http.StatusOK, res.StatusCode, string(body))
	req = <-seen
	assert.Equal(t, apigen.List, req.Op)
	assert.Equal(t, "~/x", *req.Path)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Empty(t, res.Header.Get("Set-Cookie"))
	assert.JSONEq(t, `{"path":"/home/user","entries":[],"truncated":false}`, string(body))

	// read：206 + Content-Range 透传，附件 / 沙箱 / nosniff，音视频按扩展名给媒体类型
	res, body = e.get(base+"file?offline=1&path=/home/user/v.mp4", map[string]string{"Range": "bytes=0-3"})
	require.Equal(t, http.StatusPartialContent, res.StatusCode, string(body))
	req = <-seen
	assert.Equal(t, apigen.Read, req.Op)
	assert.Equal(t, "/home/user/v.mp4", *req.Path)
	require.NotNil(t, req.Range)
	assert.Equal(t, "bytes=0-3", *req.Range)
	assert.Equal(t, "video/mp4", res.Header.Get("Content-Type"))
	assert.Contains(t, res.Header.Get("Content-Disposition"), "attachment")
	assert.Equal(t, "sandbox", res.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "bytes 0-3/10", res.Header.Get("Content-Range"))
	assert.Equal(t, `"1-10"`, res.Header.Get("ETag"))
	assert.Equal(t, "abcd", string(body))

	// 索引中：503 offline_indexing 带 Retry-After
	res, body = e.get(base+"stat?offline=1&path=/home/user/a", nil)
	assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	assert.Equal(t, "2", res.Header.Get("Retry-After"))
	assert.Equal(t, "offline_indexing", errCodeOf(t, body))
	<-seen

	// 白名单外的状态码 → 502 orbd_bad_response
	res, body = e.get(base+"stat?offline=1&path=/home/user/redirect", nil)
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
	assert.Equal(t, "orbd_bad_response", errCodeOf(t, body))
	assert.Empty(t, res.Header.Get("Location"))
	<-seen

	// thread 不属于该 orb → 400（与在线路由相同）
	res, body = e.get(base+"list?offline=1&path=~&thread="+ids.Thread(), nil)
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
	assert.Equal(t, "invalid_request", errCodeOf(t, body))

	// 非停机的 orb：与不带 offline 完全相同（没有 link 的后台请求 → 409 orb_not_ready），不经 provider
	ready := e.createOrb("prov_1", orbs.StateReady, nil)
	res, body = e.get("/api/v1/orbs/"+ready+"/fs/list?offline=1&bg=1&path=~", nil)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "orb_not_ready", errCodeOf(t, body))
	// 不存在的 orb 同样照旧 404
	res, _ = e.get("/api/v1/orbs/orb-nope/fs/list?offline=1&path=~", nil)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	select {
	case r := <-seen:
		t.Fatalf("不应转给 provider: %+v", r)
	default:
	}
}

func TestOfflineFsUnsupported(t *testing.T) {
	e := newOrbsEnv(t)
	ctx := context.Background()
	require.Equal(t, http.StatusOK, e.do("POST", "/provider/v1/hello", e.provT, offlineHello(true), nil))

	// runner 的 orb → 409 offline_unsupported
	runner := e.createOrb("prov_1", orbs.StateStopped, new(orbs.KindRunner))
	cwd := "/home/u/repo"
	thread := e.createThread(runner, &cwd)
	res, body := e.get("/api/v1/orbs/"+runner+"/fs/list?offline=1&path=~&thread="+thread, nil)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "offline_unsupported", errCodeOf(t, body))

	// provider 未声明 offlineFs → 409 offline_unsupported
	oldT := ids.Token("opv")
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_old", Name: "old", TokenHash: ids.Hash(oldT), CreatedAt: 1}))
	require.Equal(t, http.StatusOK, e.do("POST", "/provider/v1/hello", oldT, offlineHello(false), nil))
	old := e.createOrb("prov_old", orbs.StateStopped, nil)
	res, body = e.get("/api/v1/orbs/"+old+"/fs/roots?offline=1", nil)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "offline_unsupported", errCodeOf(t, body))

	// 支持但不在线（租约已释放）→ 503 provider_unavailable
	offT := ids.Token("opv")
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_off", Name: "off", TokenHash: ids.Hash(offT), CreatedAt: 1}))
	require.Equal(t, http.StatusOK, e.do("POST", "/provider/v1/hello", offT, offlineHello(true), nil))
	require.Equal(t, http.StatusNoContent, e.do("POST", "/provider/v1/bye", offT, map[string]string{"instanceId": "i1"}, nil))
	off := e.createOrb("prov_off", orbs.StateStopped, nil)
	res, body = e.get("/api/v1/orbs/"+off+"/fs/file?offline=1&path=/home/user/a", nil)
	assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	assert.Equal(t, "provider_unavailable", errCodeOf(t, body))
}

// 在线但没有认领循环：认领超时 → 503 provider_unavailable。
func TestOfflineFsClaimTimeout(t *testing.T) {
	e := newOrbsEnv(t)
	require.Equal(t, http.StatusOK, e.do("POST", "/provider/v1/hello", e.provT, offlineHello(true), nil))
	e.svc.Jobs().FSRelay().ClaimTimeout = 50 * time.Millisecond
	orb := e.createOrb("prov_1", orbs.StateStopped, nil)
	res, body := e.get("/api/v1/orbs/"+orb+"/fs/stat?offline=1&path=~", nil)
	assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	assert.Equal(t, "provider_unavailable", errCodeOf(t, body))
}

func errCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var e apigen.Error
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e.Code
}
