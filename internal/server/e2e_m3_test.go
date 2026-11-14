package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M3 §3.3：经 orb link 的 fs 流反向代理 orbd 本机 API——process 驱动 + 真实 orbd + fakeomp。

// orbdRequest 是 orbd 在 ORYN_ORBD_REQUEST_LOG 中为每个本机 API 请求追加的一行。
type orbdRequest struct {
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	RawQuery string              `json:"rawQuery"`
	Header   map[string][]string `json:"header"`
}

func readOrbdRequests(t *testing.T, file string) []orbdRequest {
	t.Helper()
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	var out []orbdRequest
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var r orbdRequest
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		out = append(out, r)
	}
	return out
}

// orbReq 发一个带设备令牌、伪造 Cookie 与 CSRF 头的请求（Bearer 优先鉴权；后两者必须被代理剥掉）。
func (s *stack) orbReq(method, path string, body io.Reader, hdr map[string]string) *http.Response {
	s.t.Helper()
	req, err := http.NewRequest(method, s.http.URL+path, body)
	require.NoError(s.t, err)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Cookie", auth.CookieName+"=secret-session")
	req.Header.Set(auth.CSRFHeader, "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

// okBody 读出响应体并断言 200（失败时带上响应体）。
func okBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b := readAll(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", b)
	return b
}

func errCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(readAll(t, resp), &e))
	return e.Code
}

func TestE2EM3OrbFiles(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "orbd-requests.jsonl")
	t.Setenv("ORYN_ORBD_REQUEST_LOG", logFile)
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})

	var prj struct {
		ID string `json:"id"`
	}
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "m3", "baseBranch": "trunk"}, &prj, 201)
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "hi", "projectId": prj.ID,
		"executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	base := "/api/v1/orbs/" + th.OrbID

	// mkdir → PUT（3MiB 随机字节，文件名含非 ASCII 与空格）→ GET 字节一致、附件头齐全 → Range → list 可见
	resp := s.orbReq("POST", base+"/fs/mkdir", strings.NewReader(`{"path":"~/workspace/m3"}`), map[string]string{"Content-Type": "application/json"})
	okBody(t, resp)
	data := make([]byte, 3<<20)
	_, _ = rand.Read(data)
	file := "~/workspace/m3/报告 1.bin"
	q := "?path=" + url.QueryEscape(file)
	resp = s.orbReq("PUT", base+"/fs/file"+q, bytes.NewReader(data), map[string]string{"Content-Type": "application/octet-stream"})
	body := okBody(t, resp)
	var entry struct {
		Size int64  `json:"size"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(body, &entry))
	assert.Equal(t, int64(len(data)), entry.Size)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	resp = s.orbReq("GET", base+"/fs/file"+q, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, bytes.Equal(data, readAll(t, resp)), "downloaded bytes differ")
	assert.Equal(t, "attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A%201.bin", resp.Header.Get("Content-Disposition"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Empty(t, resp.Header.Values("Set-Cookie"))

	resp = s.orbReq("GET", base+"/fs/file"+q, nil, map[string]string{"Range": "bytes=2-5"})
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, data[2:6], readAll(t, resp))

	resp = s.orbReq("GET", base+"/fs/list?path="+url.QueryEscape("~/workspace/m3"), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(readAll(t, resp), &listing))
	require.Len(t, listing.Entries, 1)
	assert.Equal(t, "报告 1.bin", listing.Entries[0].Name)

	// 声明的 Content-Length 超过 512MiB：不读请求体、不碰 orbd，直接 413 too_large
	conn, err := net.Dial("tcp", s.http.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "PUT %s/fs/file?path=%s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n",
		base, url.QueryEscape("~/workspace/m3/big.bin"), s.token, api.MaxFileBytes+1)
	require.NoError(t, err)
	big, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, big.StatusCode)
	assert.Equal(t, "too_large", errCode(t, big))

	// git 路由：query 无 base 时补上项目的 base_branch
	resp = s.orbReq("GET", base+"/git/status?bg=1", nil, nil)
	okBody(t, resp)

	// 冻结后：bg=1 → 409 orb_not_ready 且 orb 仍 frozen；不带 bg → 唤醒并在 25s 内 200
	orbState := func() string {
		var o struct {
			State string `json:"state"`
		}
		s.rest("GET", base, nil, &o, 200)
		return o.State
	}
	s.rest("POST", base+"/pause", map[string]string{"mode": "freeze"}, nil, 202)
	require.Eventually(t, func() bool { return orbState() == "frozen" }, 30*time.Second, 50*time.Millisecond)
	resp = s.orbReq("GET", base+"/fs/list?path=~&bg=1", nil, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "orb_not_ready", errCode(t, resp))
	assert.Equal(t, "frozen", orbState())

	start := time.Now()
	resp = s.orbReq("GET", base+"/fs/list?path=~", nil, nil)
	okBody(t, resp)
	assert.Less(t, time.Since(start), 25*time.Second)
	// resuming 时 link 已 hello 即可用（usableState），请求可能先于 resume.run 收尾返回：ready 是随后到达的终态
	require.Eventually(t, func() bool { return orbState() == "ready" }, 30*time.Second, 50*time.Millisecond)

	// 到达 orbd 的请求：没有 Cookie / Authorization / CSRF / X-Forwarded-*，bg 已剥掉，git 带上了基线
	reqs := readOrbdRequests(t, logFile)
	require.NotEmpty(t, reqs)
	var sawPut, sawStatus bool
	for _, r := range reqs {
		for k := range r.Header {
			ck := http.CanonicalHeaderKey(k)
			assert.NotContains(t, []string{"Cookie", "Authorization", "X-Oryn-Csrf", "Forwarded", "X-Real-Ip"}, ck, "%s %s", r.Method, r.Path)
			assert.False(t, strings.HasPrefix(ck, "X-Forwarded-"), "%s %s: %s", r.Method, r.Path, ck)
		}
		vals, err := url.ParseQuery(r.RawQuery)
		require.NoError(t, err)
		assert.False(t, vals.Has("bg"), "bg forwarded: %s?%s", r.Path, r.RawQuery)
		sawPut = sawPut || (r.Method == "PUT" && r.Path == "/fs/file")
		if r.Path == "/git/status" {
			sawStatus = true
			assert.Equal(t, "trunk", vals.Get("base"))
		}
	}
	assert.True(t, sawPut)
	assert.True(t, sawStatus)

	// 不存在的 orb → 404
	resp = s.orbReq("GET", "/api/v1/orbs/orb-00000000000000000000000000/fs/roots", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errCode(t, resp))
}
