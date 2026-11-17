package api_test

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

// TestSSHAPIFsAndTest 对假目标机走一遍：测试连接、写文件、读回（含 Range）、If-Match 不符 412、删主目录 403。
func TestSSHAPIFsAndTest(t *testing.T) {
	e := newSSHEnv(t)
	c := h2DeviceToken(t, e)

	r := e.do("POST", "/api/v1/ssh/keys", c, map[string]any{"name": "k", "mode": "generate"})
	require.Equal(t, 201, r.status, string(r.body))
	var key apigen.SSHKey
	r.json(t, &key)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	require.NoError(t, err)
	d := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{AuthorizedKeys: []ssh.PublicKey{pub}})
	addr, port := d.Addr()
	r = e.do("POST", "/api/v1/ssh/hosts", c, map[string]any{
		"name": "fake", "address": addr, "port": port, "username": "tester", "authType": "key", "keyId": key.ID,
	})
	require.Equal(t, 201, r.status, string(r.body))
	var host apigen.SSHHost
	r.json(t, &host)
	base := "/api/v1/ssh/hosts/" + host.ID

	r = e.do("POST", base+"/test", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var tr apigen.SSHTestResult
	r.json(t, &tr)
	require.True(t, tr.Ok, string(r.body))
	require.Equal(t, d.HostKeyFingerprint(), *tr.HostKeyFingerprint)
	assert.Equal(t, 404, e.do("POST", "/api/v1/ssh/hosts/ssh_nope/test", c, nil).status)

	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	q := "?path=" + url.QueryEscape(p)

	r = e.do("PUT", base+"/fs/file"+q, c, []byte("hello world"))
	require.Equal(t, 200, r.status, string(r.body))
	var ent apigen.FsEntry
	r.json(t, &ent)
	require.NotNil(t, ent.Etag)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(b))

	raw := func(method, path string, body []byte, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+c.bearer)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, out
	}

	resp, body := raw("GET", base+"/fs/file"+q, nil, nil)
	require.Equal(t, 200, resp.StatusCode, string(body))
	assert.Equal(t, "hello world", string(body))
	assert.Equal(t, *ent.Etag, resp.Header.Get("ETag"))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	resp, body = raw("GET", base+"/fs/file"+q, nil, map[string]string{"Range": "bytes=6-10"})
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "world", string(body))

	resp, body = raw("PUT", base+"/fs/file"+q, []byte("x"), map[string]string{"If-Match": `"1-1"`})
	require.Equal(t, http.StatusPreconditionFailed, resp.StatusCode, string(body))
	assert.Contains(t, string(body), `"modified"`)
	resp, body = raw("PUT", base+"/fs/file"+q, []byte("second"), map[string]string{"If-Match": *ent.Etag})
	require.Equal(t, 200, resp.StatusCode, string(body))
	resp, body = raw("PUT", base+"/fs/file"+q+"&overwrite=false", []byte("x"), nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	assert.Contains(t, string(body), `"exists"`)

	r = e.do("GET", base+"/fs/file?path="+url.QueryEscape(dir), c, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "is_a_directory", r.code(t))
	r = e.do("GET", base+"/fs/stat?path=relative", c, nil)
	assert.Equal(t, 400, r.status)

	r = e.do("GET", base+"/fs/list?path="+url.QueryEscape(dir), c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list apigen.FsListing
	r.json(t, &list)
	require.Len(t, list.Entries, 1)
	assert.Equal(t, "a.txt", list.Entries[0].Name)

	r = e.do("POST", base+"/fs/mkdir", c, map[string]any{"path": filepath.Join(dir, "sub")})
	require.Equal(t, 200, r.status, string(r.body))
	r = e.do("POST", base+"/fs/rename", c, map[string]any{"from": p, "to": filepath.Join(dir, "sub", "b.txt")})
	require.Equal(t, 200, r.status, string(r.body))
	r = e.do("POST", base+"/fs/delete", c, map[string]any{"path": filepath.Join(dir, "sub")})
	require.Equal(t, 204, r.status, string(r.body))
	r = e.do("GET", base+"/fs/stat?path="+url.QueryEscape(filepath.Join(dir, "sub")), c, nil)
	assert.Equal(t, 404, r.status)

	r = e.do("GET", base+"/fs/roots", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var roots apigen.SSHFsRoots
	r.json(t, &roots)
	r = e.do("POST", base+"/fs/delete", c, map[string]any{"path": roots.Home})
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "protected_path", r.code(t))

	d.Close()
	r = e.do("POST", base+"/test", c, nil)
	require.Equal(t, 200, r.status)
	r.json(t, &tr)
	assert.False(t, tr.Ok)
	r = e.do("GET", "/api/v1/ssh/hosts/ssh_nope/fs/roots", c, nil)
	assert.Equal(t, 404, r.status)
}
