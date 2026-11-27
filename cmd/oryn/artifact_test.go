package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

// runArtifactPull 以给定参数跑一次 `oryn artifact pull`，返回标准输出；跑完把标志复位。
func runArtifactPull(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := artifactPullCmd
	defer func() {
		for _, name := range []string{"to", "version", "path", "overwrite", "json"} {
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

// M31 C3：`oryn artifact pull <id> --to <路径>` 循环 pull_artifact，第二页起钉住第一页的版本与路径，写 <to>.oryn-part
// 核对大小与 sha256 后改名；to 已存在且没给 --overwrite 时是调用错误；校验不过时不留文件。
func TestArtifactPull(t *testing.T) {
	content := []byte("PK\x03\x04 这是一个分三页取回的文件")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	var got []map[string]any
	corrupt := false
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		var p apigen.LinkCtlArtifactCallParams
		require.NoError(t, json.Unmarshal(params, &p))
		require.Equal(t, "pull_artifact", p.Op)
		got = append(got, p.Params)
		off := 0
		if c, ok := p.Params["cursor"].(string); ok {
			off, _ = strconv.Atoi(c)
		}
		end := min(off+16, len(content))
		next := ""
		if end < len(content) {
			next = strconv.Itoa(end)
		}
		data := content[off:end]
		if corrupt && off > 0 {
			data = append([]byte{data[0] ^ 0xff}, data[1:]...)
		}
		page, _ := json.Marshal(map[string]any{"id": "art_X", "version": 3, "path": "document.docx", "size": len(content), "sha256": sha,
			"offset": off, "data": base64.StdEncoding.EncodeToString(data), "next": next})
		return apigen.LinkCtlArtifactCallResult{Text: string(page)}, nil
	})
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	t.Setenv("ORYN_API_TOKEN", "")
	dir := t.TempDir()
	to := filepath.Join(dir, "out.docx")

	out, err := runArtifactPull(t, "art_X", "--to", to, "--json")
	require.NoError(t, err)
	var res artifactPullResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, artifactPullResult{ID: "art_X", Version: 3, Path: "document.docx", To: to, Size: int64(len(content)), Sha256: sha}, res)
	b, err := os.ReadFile(to)
	require.NoError(t, err)
	assert.Equal(t, content, b)
	assert.NoFileExists(t, to+".oryn-part")
	require.Len(t, got, 3)
	assert.Equal(t, map[string]any{"id": "art_X"}, got[0])
	assert.Equal(t, map[string]any{"id": "art_X", "version": float64(3), "path": "document.docx", "cursor": "16"}, got[1], "钉住第一页的版本与路径")

	// 已存在且没给 --overwrite：调用错误，不发请求
	n := len(f.reqs)
	_, err = runArtifactPull(t, "art_X", "--to", to)
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
	assert.ErrorContains(t, err, "--overwrite")
	assert.Len(t, f.reqs, n)
	out, err = runArtifactPull(t, "art_X", "--to", to, "--overwrite", "--version", "3", "--path", "document.docx")
	require.NoError(t, err)
	assert.Contains(t, out, "已取回 art_X 版本 3 的 document.docx")
	assert.Equal(t, map[string]any{"id": "art_X", "version": float64(3), "path": "document.docx"}, got[3])

	// sha256 对不上（第二页起每页首字节被改）：报错，不留目标文件与 .oryn-part
	corrupt = true
	bad := filepath.Join(dir, "bad.docx")
	_, err = runArtifactPull(t, "art_X", "--to", bad)
	assert.ErrorContains(t, err, "不一致")
	assert.NoFileExists(t, bad)
	assert.NoFileExists(t, bad+".oryn-part")

	_, err = runArtifactPull(t, "art_X")
	require.ErrorAs(t, err, &ec)
	assert.ErrorContains(t, err, "--to")
}

// 评审回归：<to>.oryn-part 独占创建。已存在（残片或另一次取回在写）时取回失败，不覆盖、不删除它，目标文件不变；
// 两个并发取回同一目标只有一个成功，失败的那个不破坏对方的临时文件。
func TestArtifactPullExclusivePart(t *testing.T) {
	content := []byte("PK\x03\x04 并发取回的文件")
	sum := sha256.Sum256(content)
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	startFakeOrbd(t, func(context.Context, string, json.RawMessage, *orbsock.Responder) (any, error) {
		first.Do(func() { close(entered); <-release })
		page, _ := json.Marshal(map[string]any{"id": "art_X", "version": 1, "path": "document.docx", "size": len(content),
			"sha256": hex.EncodeToString(sum[:]), "offset": 0, "data": base64.StdEncoding.EncodeToString(content), "next": ""})
		return apigen.LinkCtlArtifactCallResult{Text: string(page)}, nil
	})
	releaseAll := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseAll) // 先于假 orbd 的清理放行被挡住的请求
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	t.Setenv("ORYN_API_TOKEN", "")
	ctx := context.Background()
	to := filepath.Join(t.TempDir(), "out.docx")
	part := to + ".oryn-part"

	require.NoError(t, os.WriteFile(to, []byte("原文件"), 0o644))
	require.NoError(t, os.WriteFile(part, []byte("残片"), 0o644))
	_, err := artifactPull(ctx, "art_X", to, "", 0, true)
	assert.ErrorContains(t, err, ".oryn-part 已存在")
	assertFile(t, to, "原文件")
	assertFile(t, part, "残片")
	require.NoError(t, os.Remove(part))

	errA := make(chan error, 1)
	go func() {
		_, err := artifactPull(ctx, "art_X", to, "", 0, true)
		errA <- err
	}()
	<-entered // A 已独占创建临时文件，正等第一页
	_, err = artifactPull(ctx, "art_X", to, "", 0, true)
	assert.ErrorContains(t, err, ".oryn-part 已存在")
	assert.FileExists(t, part, "失败的一方不删对方的临时文件")
	releaseAll()
	require.NoError(t, <-errA)
	assertFile(t, to, string(content))
	assert.NoFileExists(t, part)
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(b))
}
