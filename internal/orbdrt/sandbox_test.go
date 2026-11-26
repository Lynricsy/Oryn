package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

func startSandbox(t *testing.T) (*fakeCP, *Runtime) {
	t.Helper()
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.Sandbox = true })
	return cp, rt
}

// M30 2.2：精简模式的 hello 特性恰为 C6 所列；没有 egress、orbd.sock、agent 资产；本机 API 只有 /fs/* 与 /sandbox/*；
// ctl 只认 shutdown；控制面打开的非 fs 流被关掉。
func TestSandboxModeSurface(t *testing.T) {
	cp, rt := startSandbox(t)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.Equal(t, []string{"fs", "fs-tar", "metrics", "sandbox"}, h.Features)
	assert.Empty(t, rt.EgressAddr())
	assert.Nil(t, rt.sshLn)
	assert.Nil(t, rt.box)
	assert.Nil(t, rt.desk)
	assert.Nil(t, rt.comp)

	o := newOrbHTTP(t, cp, rt)
	resp, b := o.do(http.MethodGet, "/git/status", nil, nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodPost, "/box/exec", nil, nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	require.NoError(t, os.WriteFile(filepath.Join(rt.home, "a.txt"), []byte("x"), 0o600))
	resp, b = o.do(http.MethodGet, "/fs/stat", pathQ("~/a.txt"), nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(b))

	for _, m := range []string{"setup.run", "agent.start", string(apigen.MethodComputerStart), string(apigen.MethodOmpHomeApply)} {
		err := cp.call(t, m, map[string]any{}, nil)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce, m)
		assert.Equal(t, link.CodeMethodNotFound, ce.Code, m)
	}

	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamPortal, Port: 22})
	require.NoError(t, err)
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = st.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)

	// orbd.sock 与 agent 资产都不会出现（Run 已在跑：给异步启动的部分留一点时间）。
	assert.Never(t, func() bool {
		_, err := os.Stat(orbsock.Path(rt.home))
		return err == nil
	}, 300*time.Millisecond, 20*time.Millisecond)
	assert.NoDirExists(t, filepath.Join(rt.home, ".oryn", "core"))

	require.NoError(t, cp.call(t, "shutdown", apigen.LinkCtlShutdownParams{GraceSeconds: 1}, nil))
}

func TestSandboxModeExclusive(t *testing.T) {
	base := Config{URL: "http://cp", OrbID: "orb-x", Token: testToken, Home: t.TempDir(), Sandbox: true}
	runner := base
	runner.Runner = &RunnerConfig{}
	_, err := New(runner)
	assert.ErrorContains(t, err, "sandbox")
	coord := base
	coord.Coordinator = true
	coord.Connect = func(context.Context) (*link.Session, error) { return nil, errors.New("unused") }
	_, err = New(coord)
	assert.ErrorContains(t, err, "sandbox")
}
