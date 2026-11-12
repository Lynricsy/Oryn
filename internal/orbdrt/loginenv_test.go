package orbdrt

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestLoginEnvAppliesProfile(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export FROM_PROFILE=yes\nPATH=\"$HOME/tools:$PATH\"\nunset DROP_ME\n"), 0o644))
	base := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin", "KEEP": "1", "DROP_ME": "x"}
	env := loginEnv(context.Background(), home, base)
	assert.Equal(t, "yes", env["FROM_PROFILE"])
	assert.True(t, strings.HasPrefix(env["PATH"], home+"/tools:"), env["PATH"])
	assert.Equal(t, "1", env["KEEP"])
	assert.NotContains(t, env, "DROP_ME", "登录 shell 里删掉的变量不带出")
	assert.NotContains(t, env, "SHLVL")
}

// 前一个 setup 脚本写进 ~/.profile 的环境，对后一个脚本可见（agent 同理在启动时取登录环境）。
func TestSetupScriptsSeeProfileWrittenByEarlierScript(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	project := `echo 'export ORYN_TEST_MARK=from-profile' >> "$HOME/.profile"`
	workdir := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, ".oryn"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workdir, ".oryn", "setup"),
		[]byte("#!/usr/bin/env bash\necho \"mark=${ORYN_TEST_MARK:-unset}\" > \"$HOME/mark.txt\"\n"), 0o755))
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: &project}}, &res))
	require.True(t, res.Ok, "%v", res.SetupError)
	b, err := os.ReadFile(filepath.Join(rt.home, "mark.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mark=from-profile\n", string(b))
}

// profile 里留下持有 stdout 的后台进程时，不得卡住（agent 启动与 setup 锁都在它之后）；
// 环境照常求出，后台进程被清理。
func TestLoginEnvBackgroundChildDoesNotHang(t *testing.T) {
	home := t.TempDir()
	pidFile := filepath.Join(home, "bg.pid")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export FROM_PROFILE=yes\nsleep 60 &\necho $! > \"$HOME/bg.pid\"\n"), 0o644))
	base := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin"}
	start := time.Now()
	env := loginEnv(context.Background(), home, base)
	assert.Less(t, time.Since(start), 5*time.Second, "后台进程持有 stdout 时不应等到它退出")
	assert.Equal(t, "yes", env["FROM_PROFILE"], "bash 已正常退出，环境仍可用")

	b, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 20*time.Millisecond,
		"profile 留下的后台进程应被杀掉")
}

// profile 卡住（前台阻塞）时按超时放弃并回退原环境，不拖住调用方。
func TestLoginEnvHungProfileTimesOut(t *testing.T) {
	old := loginEnvTimeout
	loginEnvTimeout = 300 * time.Millisecond
	t.Cleanup(func() { loginEnvTimeout = old })
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte("sleep 60 &\nsleep 60\n"), 0o644))
	base := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin", "KEEP": "1"}
	start := time.Now()
	env := loginEnv(context.Background(), home, base)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Equal(t, base, env, "超时回退原环境")
}
