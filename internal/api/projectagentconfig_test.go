package api_test

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

func h2Project(t *testing.T, e *h2Env, c h2Cred, name string) string {
	t.Helper()
	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": name})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct{ ID string }
	r.json(t, &p)
	require.NotEmpty(t, p.ID)
	return p.ID
}

func h2ListPaths(t *testing.T, e *h2Env, c h2Cred, url string) []string {
	t.Helper()
	r := e.do("GET", url, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list struct {
		Files []struct{ Path string }
	}
	r.json(t, &list)
	out := []string{}
	for _, f := range list.Files {
		out = append(out, f.Path)
	}
	return out
}

func h2Tar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// M12.2：项目级 omp 配置的 REST。
func TestProjectAgentConfigAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	a := h2Project(t, e, c, "a")
	b := h2Project(t, e, c, "b")
	base := "/api/v1/projects/" + a + "/agent-config"
	baseB := "/api/v1/projects/" + b + "/agent-config"
	const globalList = "/api/v1/agent-config/files"
	require.Equal(t, 200, e.do("PUT", "/api/v1/agent-config/files/rules%2Fg.md", c, []byte("# g")).status)

	// 未写过：空列表
	assert.Equal(t, []string{}, h2ListPaths(t, e, c, base+"/files"))

	r := e.do("PUT", base+"/files/rules%2Fa.md?mode=384", c, []byte("# a"))
	require.Equal(t, 200, r.status, string(r.body))
	var wr struct {
		File struct {
			Path string
			Mode int
		}
		ExtractedSecrets []string
		Warnings         []any
	}
	r.json(t, &wr)
	assert.Equal(t, "rules/a.md", wr.File.Path)
	assert.Equal(t, 0o600, wr.File.Mode)
	assert.Equal(t, []string{}, wr.ExtractedSecrets)
	assert.Equal(t, []string{"rules/a.md"}, h2ListPaths(t, e, c, base+"/files"))
	r = e.do("GET", base+"/files/rules%2Fa.md", c, nil)
	require.Equal(t, 200, r.status)
	assert.Equal(t, "# a", string(r.body))

	// 两个项目与全局互不可见
	assert.Equal(t, []string{}, h2ListPaths(t, e, c, baseB+"/files"))
	assert.Equal(t, 404, e.do("GET", baseB+"/files/rules%2Fa.md", c, nil).status)
	assert.Equal(t, []string{"rules/g.md"}, h2ListPaths(t, e, c, globalList))

	// 未知项目 404（五个接口都先确认项目存在）
	for _, ghost := range []string{"prj_00000000000000000000000000", "nope"} {
		gb := "/api/v1/projects/" + ghost + "/agent-config"
		for _, req := range []struct {
			method, path string
			body         any
		}{
			{"GET", gb + "/files", nil},
			{"GET", gb + "/files/rules%2Fa.md", nil},
			{"PUT", gb + "/files/rules%2Fa.md", []byte("x")},
			{"DELETE", gb + "/files/rules%2Fa.md", nil},
			{"POST", gb + "/import", h2Tar(t, map[string]string{"a.md": "x"})},
		} {
			r := e.do(req.method, req.path, c, req.body)
			assert.Equal(t, 404, r.status, req.method+" "+req.path)
			assert.Equal(t, "not_found", r.code(t), req.method+" "+req.path)
		}
	}
	_, err := os.Stat(filepath.Join(e.dataDir, agentcfg.ProjectsDirName, "nope"))
	assert.True(t, os.IsNotExist(err), "未知项目不建目录")

	// 400：路径穿越、运行时数据、models.yml、plans/、非映射或多文档 config.yml、文本路径含 NUL / 非 UTF-8、mode 越界
	for _, bad := range []struct {
		path string
		body string
	}{
		{"..%2Fx", "x"},
		{"rules%2F..%2F..%2Fx", "x"},
		{"auth.json", "{}"},
		{"models.yml", "providers: {}\n"},
		{"plans%2Fa.md", "x"},
		{"config.yml", "- a\n"},
		{"config.yml", "a: [\n"},
		{"config.yml", "theme: dark\n---\ntheme: light\n"},
		{"rules%2Fenv.md", "GITHUB_TOKEN=x\x00AWS_SECRET=y\x00"},
		{"AGENTS.md", "caf\xe9"},
		{"mcp.json", "{}\x00"},
		{"x?mode=999", "x"},
	} {
		r := e.do("PUT", base+"/files/"+bad.path, c, []byte(bad.body))
		assert.Equal(t, 400, r.status, bad.path+": "+string(r.body))
		assert.Equal(t, "invalid_request", r.code(t), bad.path)
	}
	assert.Equal(t, 400, e.do("GET", base+"/files/models.yml", c, nil).status)
	for _, bad := range []map[string]string{{"config.yml": "a: 1\n---\n"}, {"rules/env.md": "A=1\x00"}} {
		r := e.do("POST", base+"/import", c, h2Tar(t, bad))
		assert.Equal(t, 400, r.status, string(r.body))
		assert.Equal(t, "invalid_request", r.code(t))
	}
	// 全局层同一套内容校验
	for path, body := range map[string]string{"config.yml": "a: 1\n---\nb: 2\n", "rules%2Fenv.md": "A=1\x00"} {
		r := e.do("PUT", "/api/v1/agent-config/files/"+path, c, []byte(body))
		assert.Equal(t, 400, r.status, path+": "+string(r.body))
		assert.Equal(t, "invalid_request", r.code(t), path)
	}
	r = e.do("POST", "/api/v1/agent-config/import", c, h2Tar(t, map[string]string{"rules/g.md": "# g", "AGENTS.md": "\xff"}))
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Equal(t, 200, e.do("PUT", base+"/files/config.yml", c, []byte("theme: dark\n")).status)

	// 413：单文件超 1 MiB（PUT 与导入）
	big := strings.Repeat("x", agentcfg.ProjectMaxFileSize+1)
	r = e.do("PUT", base+"/files/big.md", c, []byte(big))
	assert.Equal(t, 413, r.status, string(r.body))
	assert.Equal(t, "invalid_request", r.code(t))
	r = e.do("POST", base+"/import", c, h2Tar(t, map[string]string{"big.md": big}))
	assert.Equal(t, 413, r.status, string(r.body))
	assert.Equal(t, []string{"config.yml", "rules/a.md"}, h2ListPaths(t, e, c, base+"/files"))

	// tar 导入整体替换本项目；被排除的路径静默跳过；坏 config.yml 400
	require.Equal(t, 200, e.do("PUT", baseB+"/files/rules%2Fb.md", c, []byte("# b")).status)
	r = e.do("POST", base+"/import", c, h2Tar(t, map[string]string{
		"rules/new.md": "new", "mcp.json": "{}", "models.yml": "providers: {}\n", "plans/x.md": "x",
	}))
	require.Equal(t, 200, r.status, string(r.body))
	var imp struct {
		Files            []struct{ Path string }
		ExtractedSecrets []string
	}
	r.json(t, &imp)
	require.Len(t, imp.Files, 2)
	assert.Equal(t, "mcp.json", imp.Files[0].Path)
	assert.Equal(t, "rules/new.md", imp.Files[1].Path)
	assert.Equal(t, []string{}, imp.ExtractedSecrets)
	assert.Equal(t, []string{"mcp.json", "rules/new.md"}, h2ListPaths(t, e, c, base+"/files"))
	assert.Equal(t, []string{"rules/b.md"}, h2ListPaths(t, e, c, baseB+"/files"))
	assert.Equal(t, 400, e.do("POST", base+"/import", c, h2Tar(t, map[string]string{"config.yml": "- a\n"})).status)
	assert.Equal(t, 400, e.do("POST", base+"/import", c, []byte("garbage")).status)
	assert.Equal(t, []string{"mcp.json", "rules/new.md"}, h2ListPaths(t, e, c, base+"/files"))

	// DELETE 204 再 404
	require.Equal(t, 204, e.do("DELETE", base+"/files/rules%2Fnew.md", c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", base+"/files/rules%2Fnew.md", c, nil).status)
	assert.Equal(t, 404, e.do("GET", base+"/files/rules%2Fnew.md", c, nil).status)

	// 删除项目后其配置目录不存在；另一个项目不受影响
	dirA := filepath.Join(e.dataDir, agentcfg.ProjectsDirName, a)
	require.DirExists(t, dirA)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+a, c, nil).status)
	_, err = os.Stat(dirA)
	assert.True(t, os.IsNotExist(err), "%v", err)
	assert.Equal(t, 404, e.do("GET", base+"/files", c, nil).status)
	assert.Equal(t, []string{"rules/b.md"}, h2ListPaths(t, e, c, baseB+"/files"))

	// 全局列表全程不变
	assert.Equal(t, []string{"rules/g.md"}, h2ListPaths(t, e, c, globalList))
}

// 认证与 CSRF 同全局 agent-config：未登录 401；Cookie 变更缺 CSRF 403；Cookie + CSRF 或设备 Bearer 通过。
func TestProjectAgentConfigAuth(t *testing.T) {
	e := newH2Env(t)
	dev := h2DeviceToken(t, e)
	id := h2Project(t, e, dev, "p")
	base := "/api/v1/projects/" + id + "/agent-config"
	web := e.webSession()
	tarBody := h2Tar(t, map[string]string{"rules/a.md": "a"})

	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"GET", base + "/files", nil},
		{"GET", base + "/files/rules%2Fa.md", nil},
		{"PUT", base + "/files/rules%2Fa.md", []byte("a")},
		{"DELETE", base + "/files/rules%2Fa.md", nil},
		{"POST", base + "/import", tarBody},
	} {
		assert.Equal(t, 401, e.do(req.method, req.path, h2Cred{}, req.body).status, req.method+" "+req.path)
	}
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"PUT", base + "/files/rules%2Fa.md", []byte("a")},
		{"DELETE", base + "/files/rules%2Fa.md", nil},
		{"POST", base + "/import", tarBody},
	} {
		assert.Equal(t, 403, e.do(req.method, req.path, h2Cred{cookie: web}, req.body).status, req.method+" "+req.path)
	}
	assert.Equal(t, []string{}, h2ListPaths(t, e, h2Cred{cookie: web}, base+"/files"), "GET 不需要 CSRF")

	webCSRF := h2Cred{cookie: web, csrf: true}
	require.Equal(t, 200, e.do("PUT", base+"/files/rules%2Fa.md", webCSRF, []byte("a")).status)
	require.Equal(t, 200, e.do("POST", base+"/import", webCSRF, tarBody).status)
	require.Equal(t, 204, e.do("DELETE", base+"/files/rules%2Fa.md", webCSRF, nil).status)
	require.Equal(t, 200, e.do("PUT", base+"/files/rules%2Fb.md", dev, []byte("b")).status)
	assert.Equal(t, []string{"rules/b.md"}, h2ListPaths(t, e, dev, base+"/files"))
}

// gatedBody 是请求体：第一次 Read 时关闭 started，等 release 关闭后才交出内容。
type gatedBody struct {
	started, release chan struct{}
	r                io.Reader
	once             sync.Once
}

func (b *gatedBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.r.Read(p)
}

// 项目删除与在途写入竞争：PUT / 导入已确认项目存在并拿到配置根、正在读请求体时项目被删，之后的写入返回 404，
// 已删项目的配置目录不会复活。请求带 Expect: 100-continue，客户端要等 handler 开始读请求体才发送它，所以 started
// 关闭即表示 handler 已越过项目存在检查。
func TestProjectAgentConfigWriteRacesDelete(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	client := &http.Client{Transport: &http.Transport{ExpectContinueTimeout: time.Hour}}
	t.Cleanup(client.CloseIdleConnections)
	for _, tc := range []struct {
		name, method, path, ct string
		body                   []byte
	}{
		{"put", "PUT", "/files/rules%2Fa.md", "application/octet-stream", []byte("# a")},
		{"import", "POST", "/import", "application/x-tar", h2Tar(t, map[string]string{"rules/a.md": "# a"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := h2Project(t, e, c, tc.name)
			body := &gatedBody{started: make(chan struct{}), release: make(chan struct{}), r: bytes.NewReader(tc.body)}
			req, err := http.NewRequest(tc.method, e.srv.URL+"/api/v1/projects/"+id+"/agent-config"+tc.path, body)
			require.NoError(t, err)
			req.ContentLength = int64(len(tc.body))
			req.Header.Set("Content-Type", tc.ct)
			req.Header.Set("Expect", "100-continue")
			req.Header.Set("Authorization", "Bearer "+c.bearer)
			type result struct {
				status int
				body   string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := client.Do(req)
				if err != nil {
					done <- result{err: err}
					return
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				done <- result{status: resp.StatusCode, body: string(b), err: err}
			}()
			select {
			case <-body.started:
			case r := <-done:
				t.Fatalf("handler 没读请求体就应答了：%d %s %v", r.status, r.body, r.err)
			}
			require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+id, c, nil).status)
			close(body.release)
			res := <-done
			require.NoError(t, res.err)
			assert.Equal(t, 404, res.status, res.body)
			assert.Contains(t, res.body, "not_found")
			_, err = os.Lstat(filepath.Join(e.dataDir, agentcfg.ProjectsDirName, id))
			assert.True(t, os.IsNotExist(err), "已删项目的配置目录不能复活：%v", err)
		})
	}
}
