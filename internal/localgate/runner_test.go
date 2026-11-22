//go:build linux

package localgate

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain 让测试二进制兼任 `orynd schedule-gate` 子进程。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Command {
		os.Exit(RunChild(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// newRunner 用测试二进制作子进程；本机施加不了沙箱（内核太旧）时跳过。
func newRunner(t *testing.T, cfg Config) *Runner {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cfg.Command = []string{exe, Command}
	r, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Available(); err != nil && cfg.MaxABI == 0 {
		t.Skipf("local sandbox unavailable: %v", err)
	}
	return r
}

func run(r *Runner, command string, timeout time.Duration) (string, string, int, error) {
	return r.Run(context.Background(), command, "test gate", timeout)
}

// 工作目录是 gates/ 的副本：脚本可读可执行，写入不回流到数据目录；stdout / stderr 分开，非 0 退出不算错误。
func TestRunInGatesCopy(t *testing.T) {
	gates := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(gates, "data.txt"), []byte("seen-before\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gates, "check.sh"),
		[]byte("#!/bin/sh\ncat data.txt\necho new > data.txt\necho \"$HOME\" > \"$TMPDIR/x\"\necho '{\"wakeAgent\": false}'\necho warn >&2\nexit 3\n"), 0o755))
	r := newRunner(t, Config{GatesDir: gates})

	stdout, stderr, code, err := run(r, "./check.sh", 30*time.Second)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Equal(t, 3, code)
	assert.Equal(t, "seen-before\n{\"wakeAgent\": false}\n", stdout)
	assert.Equal(t, "warn\n", stderr)
	b, err := os.ReadFile(filepath.Join(gates, "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "seen-before\n", string(b), "沙箱里的写入不回流到数据目录")

	// 数据目录没有 gates/ 时工作目录为空
	r2 := newRunner(t, Config{GatesDir: filepath.Join(t.TempDir(), "missing")})
	stdout, _, code, err = run(r2, "ls -A; pwd", 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "gates", filepath.Base(filepath.Clean(stdout[:len(stdout)-1])))
}

// 沙箱外的文件读不到（即使权限位对所有人可读），回环上的服务直连不上、经代理也被拒（代理只放行公网）。
func TestRunIsolation(t *testing.T) {
	r := newRunner(t, Config{GatesDir: t.TempDir()})

	dir, err := os.MkdirTemp("", "oryn-secret-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o755))
	secret := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("s3cret"), 0o644))
	stdout, _, code, err := run(r, "cat "+secret, 30*time.Second)
	require.NoError(t, err)
	assert.NotEqual(t, 0, code)
	assert.NotContains(t, stdout, "s3cret")

	stdout, _, code, err = run(r, "cat /etc/shadow /etc/machine-id 2>/dev/null; ls /root 2>/dev/null", 30*time.Second)
	require.NoError(t, err)
	assert.NotEqual(t, 0, code)
	assert.Empty(t, stdout)

	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("internal")) }))
	t.Cleanup(srv.Close)
	stdout, _, code, err = run(r, "curl -s -m 5 --noproxy '*' "+srv.URL, 30*time.Second)
	require.NoError(t, err)
	assert.NotEqual(t, 0, code, "直连回环被 Landlock 拒绝")
	assert.NotContains(t, stdout, "internal")
	stdout, _, _, err = run(r, "curl -s -m 5 -o /dev/null -w '%{http_code}' "+srv.URL, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "403", stdout, "代理拒绝非公网目标")
}

// 超过执行上限时杀掉整个 PID 命名空间，返回超时错误而不是等命令自己结束。
func TestRunTimeout(t *testing.T) {
	r := newRunner(t, Config{GatesDir: t.TempDir()})
	start := time.Now()
	stdout, _, _, err := run(r, "echo started; sleep 30 & sleep 30", time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out after 1s")
	assert.Equal(t, "started\n", stdout, "超时前的输出保留")
	assert.Less(t, time.Since(start), 10*time.Second)
}

// procsWithArg 返回宿主上命令行里含参数 arg 的进程数（扫 /proc；僵尸进程的命令行为空，不计入）。
func procsWithArg(t *testing.T, arg string) int {
	t.Helper()
	paths, err := filepath.Glob("/proc/[0-9]*/cmdline")
	require.NoError(t, err)
	n := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if bytes.Contains(append([]byte{0}, b...), []byte("\x00"+arg+"\x00")) {
			n++
		}
	}
	return n
}

// 后台进程（含 setsid 脱离原进程组与会话的）不能活过闸门：超时与正常退出两种结束方式都要清理干净。
func TestRunKillsEscapedDescendants(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	r := newRunner(t, Config{GatesDir: t.TempDir()})
	for _, tc := range []struct {
		name, tail string
		timeout    time.Duration
		timedOut   bool
	}{
		{"timeout", "sleep 30", 2 * time.Second, true},
		{"normal exit", "sleep 1; exit 0", 30 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 每个子测试一个独一无二的 sleep 时长，用来在宿主 /proc 里认出这些后台进程
			arg := fmt.Sprintf("%d.%09d", 600+len(tc.name), time.Now().UnixNano()%1e9)
			command := fmt.Sprintf("setsid sleep %s </dev/null >/dev/null 2>&1 & "+
				"(setsid sh -c 'exec sleep %s' </dev/null >/dev/null 2>&1 &); %s", arg, arg, tc.tail)
			res := make(chan error, 1)
			go func() {
				_, _, _, err := run(r, command, tc.timeout)
				res <- err
			}()
			require.Eventually(t, func() bool { return procsWithArg(t, arg) == 2 }, 5*time.Second, 20*time.Millisecond,
				"后台进程先要跑起来，否则检查没有意义")
			err := <-res
			if tc.timedOut {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "timed out")
			} else {
				require.NoError(t, err)
			}
			assert.Eventually(t, func() bool { return procsWithArg(t, arg) == 0 }, 2*time.Second, 20*time.Millisecond,
				"闸门结束后不能留下后台进程")
		})
	}
}

// 内核的 Landlock 不支持 TCP 规则时不可用：预检与执行都报错，不在没有网络限制的情况下执行命令。
func TestUnavailableWithoutTCPRules(t *testing.T) {
	r := newRunner(t, Config{GatesDir: t.TempDir(), MaxABI: 3})
	require.Error(t, r.Available())
	marker := filepath.Join(t.TempDir(), "ran")
	_, _, _, err := run(r, "touch "+marker, 10*time.Second)
	require.Error(t, err)
	assert.NoFileExists(t, marker)
}
