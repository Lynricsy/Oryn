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

// M13 4.6：`oryn library put` 把相对路径解析成绝对路径，带上调用方线程与可选的库内文件名经 orbd.sock 发 library.put；
// orbd 回的文件记录（scope=shared、source=orb）原样打印。
func TestLibraryPut(t *testing.T) {
	var got apigen.LinkCtlLibraryPutParams
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		require.Equal(t, orbsock.MethodLibraryPut, method)
		require.NoError(t, json.Unmarshal(params, &got))
		return apigen.AssistantFile{ID: "afl_01JX", Name: *got.Name, Size: 4, Scope: apigen.Shared,
			Source: apigen.AssistantFileSourceOrb, SourceThreadID: &got.ThreadID}, nil
	})
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	t.Setenv("ORYN_API_TOKEN", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out.csv"), []byte("a,b\n"), 0o644))

	af, err := libraryPut(context.Background(), "out.csv", "report.csv")
	require.NoError(t, err)
	assert.Equal(t, "T-caller", got.ThreadID)
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(filepath.Dir(got.Path))
	require.NoError(t, err)
	assert.Equal(t, real, gotDir, "相对路径按当前目录解析成绝对路径")
	assert.True(t, filepath.IsAbs(got.Path))
	assert.Equal(t, "out.csv", filepath.Base(got.Path))
	assert.Equal(t, apigen.Shared, af.Scope)
	assert.Equal(t, apigen.AssistantFileSourceOrb, af.Source)
	var out bytes.Buffer
	printLibraryFile(&out, af)
	assert.Equal(t, "afl_01JX\treport.csv\t4 字节\t库内共享\n", out.String())

	// 目录、不存在的文件、缺 ORYN_THREAD_ID：调用错误（退出码 2），不发请求
	n := len(f.reqs)
	for _, p := range []string{dir, filepath.Join(dir, "missing")} {
		_, err := libraryPut(context.Background(), p, "")
		var ec *exitCodeError
		require.ErrorAs(t, err, &ec, p)
		assert.Equal(t, 2, ec.code)
	}
	t.Setenv("ORYN_THREAD_ID", "")
	_, err = libraryPut(context.Background(), "out.csv", "")
	assert.ErrorContains(t, err, "ORYN_THREAD_ID")
	assert.Len(t, f.reqs, n)
}

// 不在 orb 内（或带着 API 令牌）时不可用。
func TestLibraryPutOutsideOrb(t *testing.T) {
	t.Setenv("ORYN_ORB", "")
	_, err := libraryPut(context.Background(), "x", "")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
}
