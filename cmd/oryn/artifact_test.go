package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// runArtifactCall 以给定参数跑一次 `oryn artifact call`，返回标准输出；跑完把标志复位。
func runArtifactCall(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := artifactCallCmd
	defer func() {
		for _, name := range []string{"params", "params-file", "json"} {
			f := cmd.Flags().Lookup(name)
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
		cmd.SetOut(nil)
	}()
	require.NoError(t, cmd.Flags().Parse(args))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, cmd.Flags().Args())
	return out.String(), err
}

// M14 1.5：`oryn artifact call <op>` 带上调用方线程把参数原样经 orbd.sock 发 artifact.call；默认打印 text，
// --json 打印整个结果；--params-file 读文件；控制口的错误码原样带出，退出码 2。
func TestArtifactCall(t *testing.T) {
	var got []apigen.LinkCtlArtifactCallParams
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		require.Equal(t, orbsock.MethodArtifactCall, method)
		var p apigen.LinkCtlArtifactCallParams
		require.NoError(t, json.Unmarshal(params, &p))
		got = append(got, p)
		if p.Op == "update_artifact" {
			return nil, orbsock.Errorf("conflict", "art_X 现在是版本 7")
		}
		return apigen.LinkCtlArtifactCallResult{Text: `{"id":"art_X","version":1}`, Details: map[string]any{"op": p.Op, "id": "art_X"}}, nil
	})
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	t.Setenv("ORYN_API_TOKEN", "")

	out, err := runArtifactCall(t, "create_artifact", "--params", `{"title":"报告","html":"<h1>hi</h1>"}`)
	require.NoError(t, err)
	assert.Equal(t, "{\"id\":\"art_X\",\"version\":1}\n", out)
	require.Len(t, got, 1)
	assert.Equal(t, apigen.LinkCtlArtifactCallParams{ThreadID: "T-caller", Op: "create_artifact",
		Params: map[string]any{"title": "报告", "html": "<h1>hi</h1>"}}, got[0])

	file := filepath.Join(t.TempDir(), "p.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"id":"art_X"}`), 0o644))
	out, err = runArtifactCall(t, "read_artifact", "--params-file", file, "--json")
	require.NoError(t, err)
	var res apigen.LinkCtlArtifactCallResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, "read_artifact", res.Details["op"])
	assert.Equal(t, map[string]any{"id": "art_X"}, got[1].Params)

	out, err = runArtifactCall(t, "list_artifacts")
	require.NoError(t, err)
	assert.NotEmpty(t, out)
	assert.Equal(t, map[string]any{}, got[2].Params, "不给参数时是空对象")

	_, err = runArtifactCall(t, "update_artifact", "--params", `{"id":"art_X"}`)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "conflict", se.Code)
	assert.EqualError(t, err, "conflict: art_X 现在是版本 7")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)

	// 参数不是 JSON 对象、两种参数同时给、缺 ORYN_THREAD_ID：调用错误（退出码 2），不发请求
	n := len(f.reqs)
	for _, args := range [][]string{
		{"create_artifact", "--params", `[1]`},
		{"create_artifact", "--params", `null`},
		{"create_artifact", "--params", `{`},
		{"create_artifact", "--params", `{}`, "--params-file", file},
	} {
		_, err := runArtifactCall(t, args...)
		require.ErrorAs(t, err, &ec, args)
		assert.Equal(t, 2, ec.code, args)
	}
	t.Setenv("ORYN_THREAD_ID", "")
	_, err = runArtifactCall(t, "list_artifacts")
	assert.ErrorContains(t, err, "ORYN_THREAD_ID")
	assert.Len(t, f.reqs, n)
}

// 不在 orb 内（或带着 API 令牌）时不可用。
func TestArtifactCallOutsideOrb(t *testing.T) {
	t.Setenv("ORYN_ORB", "")
	_, err := artifactCall(context.Background(), "list_artifacts", map[string]any{})
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
}
