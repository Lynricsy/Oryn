package orbdrt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M14 1.5：orbd.sock 的 artifact.call 原样转成 ctl artifact.call（source 规范化成 Clean 后的绝对路径）；
// 转发期间控制面能经 library.fetch 取回每个 source，之前与之后都不能；相对路径、目录、符号链接、缺失、
// 超过 20 MiB 与非字符串的 source 被拒且不转发；控制面的错误码与文案原样回到 sock。
func TestArtifactCallForward(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	ctl := cp.ctlNow(t)
	dir := filepath.Join(rt.home, "workspace", "site")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	page := filepath.Join(dir, "index.html")
	logo := filepath.Join(dir, "logo.svg")
	require.NoError(t, os.WriteFile(page, []byte("<h1>hi</h1>"), 0o644))
	require.NoError(t, os.WriteFile(logo, []byte("<svg/>"), 0o644))
	htmlTmp := filepath.Join(dir, "html-param")
	require.NoError(t, os.WriteFile(htmlTmp, []byte("<p>html</p>"), 0o600))

	type call struct {
		params  apigen.LinkCtlArtifactCallParams
		fetched map[string][]byte
		err     error
	}
	calls := make(chan call, 4)
	var ctlErr atomic.Pointer[link.CtlError]
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodArtifactCall) {
			return nil, false, nil
		}
		if e := ctlErr.Load(); e != nil {
			return nil, true, e
		}
		c := call{fetched: map[string][]byte{}}
		_ = json.Unmarshal(params, &c.params)
		var srcs []string
		if s, ok := c.params.Params["htmlSource"].(string); ok {
			srcs = append(srcs, s)
		}
		for _, f := range c.params.Params["files"].([]any) {
			if s, ok := f.(map[string]any)["source"].(string); ok {
				srcs = append(srcs, s)
			}
		}
		for i, src := range srcs {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var res apigen.LinkCtlLibraryFetchResult
			c.err = ctl.Call(ctx, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: fmt.Sprintf("art-%d", i), Path: src}, &res)
			cancel()
			if c.err != nil {
				break
			}
			st := <-cp.libs
			if c.fetched[src], c.err = io.ReadAll(st); c.err != nil {
				break
			}
		}
		calls <- c
		if c.err != nil {
			return nil, true, c.err
		}
		return apigen.LinkCtlArtifactCallResult{Text: `{"id":"art_X","version":1}`, Details: map[string]any{"op": c.params.Op, "id": "art_X"}}, true, nil
	}
	sock := orbsock.Path(rt.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "early", Path: page}, nil)
	assert.Equal(t, "not_found", ctlCode(err), "转发之前不能取回")

	params := map[string]any{
		"title":      "报告",
		"note":       "首版",
		"htmlSource": dir + "/../site/html-param",
		"files": []any{
			map[string]any{"path": "index.html", "source": dir + "/./index.html"},
			map[string]any{"path": "logo.svg", "source": logo},
			map[string]any{"path": "a.css", "content": "h1{color:red}"},
			map[string]any{"path": "old.css", "remove": true},
		},
	}
	raw, err := orbsock.Call(ctx, sock, orbsock.MethodArtifactCall,
		apigen.LinkCtlArtifactCallParams{ThreadID: libThread, Op: "create_artifact", Params: params}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlArtifactCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Equal(t, `{"id":"art_X","version":1}`, res.Text)
	assert.Equal(t, "create_artifact", res.Details["op"])

	c := <-calls
	require.NoError(t, c.err)
	assert.Equal(t, libThread, c.params.ThreadID)
	assert.Equal(t, "create_artifact", c.params.Op)
	want := map[string]any{
		"title":      "报告",
		"note":       "首版",
		"htmlSource": htmlTmp,
		"files": []any{
			map[string]any{"path": "index.html", "source": page},
			map[string]any{"path": "logo.svg", "source": logo},
			map[string]any{"path": "a.css", "content": "h1{color:red}"},
			map[string]any{"path": "old.css", "remove": true},
		},
	}
	assert.Equal(t, want, c.params.Params, "参数原样到达，只有 htmlSource 与 source 被 Clean")
	assert.Equal(t, map[string][]byte{htmlTmp: []byte("<p>html</p>"), page: []byte("<h1>hi</h1>"), logo: []byte("<svg/>")}, c.fetched)

	for _, p := range []string{htmlTmp, page, logo} {
		err = cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "late", Path: p}, nil)
		assert.Equal(t, "not_found", ctlCode(err), "转发结束后不能再取回 %s", p)
	}
	rt.lib.mu.Lock()
	assert.Empty(t, rt.lib.staged, "登记全部撤销")
	rt.lib.mu.Unlock()

	// 不合法的 source：invalid_params，不转发
	sym := filepath.Join(dir, "link.html")
	require.NoError(t, os.Symlink(page, sym))
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(artifactSourceMaxBytes+1))
	require.NoError(t, f.Close())
	for name, src := range map[string]any{
		"relative":   "workspace/site/index.html",
		"dir":        dir,
		"symlink":    sym,
		"missing":    filepath.Join(dir, "missing.html"),
		"too large":  big,
		"not string": 42,
	} {
		_, err := orbsock.Call(ctx, sock, orbsock.MethodArtifactCall, apigen.LinkCtlArtifactCallParams{ThreadID: libThread, Op: "update_artifact",
			Params: map[string]any{"id": "art_X", "files": []any{map[string]any{"path": "x.html", "source": src}}}}, nil)
		var se *orbsock.Error
		require.ErrorAs(t, err, &se, name)
		assert.Equal(t, orbsock.CodeInvalidParams, se.Code, name)
	}
	for name, src := range map[string]any{"html relative": "html-param", "html not string": true, "html dir": dir} {
		_, err := orbsock.Call(ctx, sock, orbsock.MethodArtifactCall, apigen.LinkCtlArtifactCallParams{ThreadID: libThread, Op: "create_artifact",
			Params: map[string]any{"title": "x", "htmlSource": src}}, nil)
		var se *orbsock.Error
		require.ErrorAs(t, err, &se, name)
		assert.Equal(t, orbsock.CodeInvalidParams, se.Code, name)
		assert.Contains(t, se.Message, "htmlSource", name)
	}
	assert.Empty(t, calls, "被拒的请求没有转发")

	// 控制面的错误码与文案原样回到 sock
	conflict := &link.CtlError{Code: "conflict", Message: "art_X 现在是版本 7，index.html 在你的 baseVersion 之后被改过。"}
	ctlErr.Store(conflict)
	_, err = orbsock.Call(ctx, sock, orbsock.MethodArtifactCall, apigen.LinkCtlArtifactCallParams{ThreadID: libThread, Op: "update_artifact",
		Params: map[string]any{"id": "art_X", "baseVersion": 1, "files": []any{map[string]any{"path": "index.html", "source": page}}}}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "conflict", se.Code)
	assert.Equal(t, conflict.Message, se.Message)
	rt.lib.mu.Lock()
	assert.Empty(t, rt.lib.staged, "出错时也撤销登记")
	rt.lib.mu.Unlock()
}
