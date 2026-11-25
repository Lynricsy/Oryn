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

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// runDesignEject 以给定参数跑一次 `oryn design eject`，返回标准输出；跑完把标志复位。
func runDesignEject(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := designEjectCmd
	defer func() {
		for _, name := range []string{"to", "version", "json"} {
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
	if err := cmd.Args(cmd, cmd.Flags().Args()); err != nil {
		return "", err
	}
	err := cmd.RunE(cmd, cmd.Flags().Args())
	return out.String(), err
}

// M29 D3：`oryn design eject <id> --to <dir> [--version <n>] [--json]` 校验参数、把相对路径转绝对路径后经
// orbd.sock 发 design.eject；默认打印目录/版本/文件数与下一步 git + oryn boxes deploy 提示；--json 输出结果 JSON；
// 控制口错误码原样透传且退出码为 2。
func TestDesignEject(t *testing.T) {
	var got []orbsock.DesignEjectParams
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		require.Equal(t, orbsock.MethodDesignEject, method)
		var p orbsock.DesignEjectParams
		require.NoError(t, json.Unmarshal(params, &p))
		got = append(got, p)
		if p.ID == "art_fail" {
			return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "target directory %q is not empty", p.To)
		}
		ver := p.Version
		if ver == 0 {
			ver = 3
		}
		return orbsock.DesignEjectResult{ID: p.ID, Version: ver, Dir: p.To, Files: 14}, nil
	})
	t.Setenv("ORYN_THREAD_ID", "T-design")
	t.Setenv("ORYN_API_TOKEN", "")

	// 相对路径应先转绝对路径再发给 orbd.sock
	work := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(work))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	out, err := runDesignEject(t, "art_demo", "--to", "./ejected-app")
	require.NoError(t, err)
	wantDir := filepath.Join(work, "ejected-app")
	require.Len(t, got, 1)
	assert.Equal(t, orbsock.DesignEjectParams{
		ThreadID: "T-design",
		ID:       "art_demo",
		Version:  0,
		To:       wantDir,
	}, got[0])
	assert.Contains(t, out, "art_demo")
	assert.Contains(t, out, "版本 3")
	assert.Contains(t, out, "14 个文件")
	assert.Contains(t, out, wantDir)
	assert.Contains(t, out, "cd "+wantDir+" && git init && git add -A && git commit -m \"init\" && oryn boxes deploy")

	// --version + --json + 绝对路径
	absDir := filepath.Join(work, "my app v2")
	out, err = runDesignEject(t, "art_demo", "--to", absDir, "--version", "2", "--json")
	require.NoError(t, err)
	var res orbsock.DesignEjectResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, orbsock.DesignEjectResult{ID: "art_demo", Version: 2, Dir: absDir, Files: 14}, res)
	require.Len(t, got, 2)
	assert.Equal(t, 2, got[1].Version)
	assert.Equal(t, absDir, got[1].To)

	// 含空格路径在文本提示中加单引号
	out, err = runDesignEject(t, "art_demo", "--to", absDir)
	require.NoError(t, err)
	assert.Contains(t, out, "cd '"+absDir+"' && git init")

	// 控制口错误透传与退出码 2
	_, err = runDesignEject(t, "art_fail", "--to", "./nonempty")
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeInvalidParams, se.Code)
	assert.Contains(t, se.Message, "is not empty")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)

	// 参数校验失败不发控制口请求
	n := len(f.reqs)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"缺 --to", []string{"art_demo"}},
		{"空 --to", []string{"art_demo", "--to", "   "}},
		{"空 id", []string{"   ", "--to", "./out"}},
		{"负 --version", []string{"art_demo", "--to", "./out", "--version", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runDesignEject(t, tc.args...)
			var ec *exitCodeError
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, 2, ec.code)
		})
	}
	assert.Len(t, f.reqs, n)

	// 缺 ORYN_THREAD_ID 不发请求
	t.Setenv("ORYN_THREAD_ID", "")
	_, err = runDesignEject(t, "art_demo", "--to", "./out")
	assert.ErrorContains(t, err, "ORYN_THREAD_ID")
	assert.Len(t, f.reqs, n)
}

// 不在 orb 内（或带着 API 令牌）时不可用。
func TestDesignEjectOutsideOrb(t *testing.T) {
	t.Setenv("ORYN_ORB", "")
	_, err := designEject(context.Background(), "art_demo", "/tmp/out", 0)
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)

	t.Setenv("ORYN_ORB", "1")
	t.Setenv("ORYN_API_TOKEN", "tok_secret")
	_, err = designEject(context.Background(), "art_demo", "/tmp/out", 0)
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
}
