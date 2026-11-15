package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunArgs(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 0, run([]string{"--version"}, &out, &errb))
	assert.Contains(t, out.String(), "orbd ")

	assert.Equal(t, 2, run(nil, &out, &errb))
	assert.Equal(t, 2, run([]string{"bogus"}, &out, &errb))

	t.Setenv("ORYN_URL", "")
	t.Setenv("ORYN_ORB_ID", "")
	t.Setenv("ORYN_ORB_TOKEN", "")
	errb.Reset()
	assert.Equal(t, 2, run([]string{"run"}, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB_TOKEN")
}

// TestRunInvalidOrbCA：ORYN_ORB_CA_PEM 解析失败时 orbd 启动失败（不退回系统根）。
func TestRunInvalidOrbCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORYN_URL", "https://cp.invalid")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "oot_x")
	t.Setenv("ORYN_ORB_CA_PEM", "not a pem")
	var out, errb bytes.Buffer
	assert.Equal(t, 1, run([]string{"run"}, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB_CA_PEM")
}

// 不带 --runner 时 `orbd run` 不接受任何参数；runner 的参数与启动前置检查。
func TestRunRunnerFlags(t *testing.T) {
	geteuid = func() int { return 1000 } // 测试可能以 root 运行
	t.Cleanup(func() { geteuid = os.Geteuid })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORYN_URL", "http://127.0.0.1:1")
	t.Setenv("ORYN_RUNNER_TOKEN", "")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"run", "--dir", home}, "--dir requires --runner"},
		{[]string{"run", "--terminal", "--allow-root"}, "--allow-root, --terminal requires --runner"},
		{[]string{"run", "extra"}, `unexpected argument "extra"`},
		{[]string{"run", "--runner"}, "at least one --dir"},
		{[]string{"run", "--runner", "--dir", home}, "ORYN_RUNNER_TOKEN"},
		{[]string{"run", "--bogus"}, "flag provided but not defined"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		assert.Equal(t, 2, run(c.args, &out, &errb), "%v", c.args)
		assert.Contains(t, errb.String(), c.want, "%v", c.args)
	}

	tok := filepath.Join(home, "tok")
	require.NoError(t, os.WriteFile(tok, []byte("oot_notarunner\n"), 0o600))
	var out, errb bytes.Buffer
	assert.Equal(t, 2, run([]string{"run", "--runner", "--dir", home, "--token-file", tok}, &out, &errb))
	assert.Contains(t, errb.String(), `must start with "ort_"`)

	// root：没有 --allow-root 直接拒绝（在读令牌之前）。
	geteuid = func() int { return 0 }
	errb.Reset()
	assert.Equal(t, 2, run([]string{"run", "--runner", "--dir", home}, &out, &errb))
	assert.Contains(t, errb.String(), "--allow-root")
	errb.Reset()
	assert.Equal(t, 2, run([]string{"run", "--runner", "--allow-root", "--dir", home, "--token-file", tok}, &out, &errb))
	assert.Contains(t, errb.String(), "ort_", "with --allow-root startup proceeds to the token check")

	// 令牌正确但目录不存在：New 失败（退出码 1）。
	t.Setenv("ORYN_RUNNER_TOKEN", "ort_x")
	errb.Reset()
	assert.Equal(t, 1, run([]string{"run", "--runner", "--allow-root", "--dir", filepath.Join(home, "missing")}, &out, &errb))
	assert.Contains(t, errb.String(), "--dir")
}
