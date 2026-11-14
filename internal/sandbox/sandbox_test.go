//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv 携带助手进程的参数（JSON）；非空即表示当前进程是助手。
const helperEnv = "ORYN_SANDBOX_HELPER"

// helperArgs 是父测试交给助手的参数。
type helperArgs struct {
	Home     string   `json:"home"`
	Port     uint16   `json:"port"`
	DenyRead []string `json:"denyRead"`
	DenyDial []string `json:"denyDial"`
	Outside  string   `json:"outside"`
	UID      int      `json:"uid"`
	GID      int      `json:"gid"`
}

// helperResult 是助手打印到 stdout 的最后一行。
type helperResult struct {
	Report Report  `json:"report"`
	Checks []Check `json:"checks"`
	Err    string  `json:"err,omitempty"`
}

// RequireSandbox 报告环境是否要求沙箱测试必须真正运行（CI 设置 ORYN_REQUIRE_SANDBOX=1）。
func requireSandbox() bool { return os.Getenv("ORYN_REQUIRE_SANDBOX") == "1" }

// skipUnlessSandbox 在内核不满足条件时跳过；ORYN_REQUIRE_SANDBOX=1 下改为失败。
func skipUnlessSandbox(t *testing.T) int {
	t.Helper()
	abi, err := Probe()
	if abi >= MinABI {
		return abi
	}
	if requireSandbox() {
		t.Fatalf("landlock ABI %d (%v) < %d but ORYN_REQUIRE_SANDBOX=1", abi, err, MinABI)
	}
	t.Skipf("landlock unavailable (abi %d, %v)", abi, err)
	return 0
}

// TestHelperSandbox 只在被 TestSandboxApply 以子进程方式重新执行时运行：降权、施加沙箱、自检，打印结果。
func TestHelperSandbox(t *testing.T) {
	raw := os.Getenv(helperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var a helperArgs
	res := helperResult{}
	emit := func() {
		b, _ := json.Marshal(res)
		_, _ = os.Stdout.Write(append(append([]byte("\nRESULT "), b...), '\n'))
		os.Exit(0)
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		res.Err = err.Error()
		emit()
	}
	execs, err := ExecPaths("/usr/bin/true")
	if err != nil {
		res.Err = err.Error()
		emit()
	}
	if err := DropPrivileges(a.UID, a.GID); err != nil {
		res.Err = err.Error()
		emit()
	}
	rep, err := Apply(Policy{
		ReadWrite:     []string{a.Home},
		Exec:          execs,
		ReadOnlyDirs:  []string{"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/proc"},
		ReadOnlyFiles: []string{"/etc/ld.so.cache"},
		TCPPort:       a.Port,
	})
	if err != nil {
		res.Err = err.Error()
		emit()
	}
	res.Report = rep
	res.Checks = Checks(context.Background(), CheckOptions{
		Home:      a.Home,
		DenyRead:  a.DenyRead,
		DenyWrite: []string{filepath.Join(a.Outside, "written"), filepath.Join(filepath.Dir(a.Home), "escape")},
		DenyDial:  a.DenyDial,
		Exec:      []string{"/usr/bin/true"},
		Port:      a.Port,
	})
	emit()
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return uint16(port)
}

// TestSandboxApply 在真实内核上验证沙箱：白名单外读写、执行、联网、建套接字、命名空间、挂载、ptrace、
// clone3、提权全部失败；home 内读写与跨目录改名、白名单程序、白名单端口成功；进程状态符合预期。
// 以 root 运行时先降权到 nobody（65534）。
func TestSandboxApply(t *testing.T) {
	skipUnlessSandbox(t)

	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		uid, gid = 65534, 65534
	}
	// home 与「秘密」都放在 /tmp 下自建的目录里：降权后的 nobody 要能进入（t.TempDir 的父目录是 0700）。
	home, err := os.MkdirTemp("", "oryn-sbx-home-")
	require.NoError(t, err)
	outside, err := os.MkdirTemp("", "oryn-sbx-outside-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home); _ = os.RemoveAll(outside) })
	require.NoError(t, os.Chmod(outside, 0o777)) // DAC 放行，只有 Landlock 能挡住
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("s3cret"), 0o644))
	if uid >= 0 {
		require.NoError(t, os.Chown(home, uid, gid))
	}

	// 一个真实在监听的本机端口：沙箱内必须连不上（只有 Port 放行）。
	other, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	args, err := json.Marshal(helperArgs{
		Home: home, Port: freePort(t), Outside: outside, UID: uid, GID: gid,
		DenyRead: []string{secret, "/etc/hostname"},
		DenyDial: []string{other.Addr().String(), "1.1.1.1:443"},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperSandbox$", "-test.v=false")
	cmd.Env = append(os.Environ(), helperEnv+"="+string(args))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "helper failed: %s\n%s", out, stderr.String())

	var res helperResult
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if b, ok := bytes.CutPrefix(sc.Bytes(), []byte("RESULT ")); ok {
			require.NoError(t, json.Unmarshal(b, &res))
			found = true
		}
	}
	require.True(t, found, "no RESULT line in helper output: %s", out)
	require.Empty(t, res.Err)
	assert.True(t, res.Report.Seccomp)
	assert.GreaterOrEqual(t, res.Report.LandlockABI, MinABI)
	if uid >= 0 {
		assert.Equal(t, uid, res.Report.UID)
	}
	names := map[string]bool{}
	for _, c := range res.Checks {
		names[c.Name] = true
		assert.True(t, c.OK, "check %q failed: %s", c.Name, c.Detail)
		t.Logf("%-40s ok=%v %s", c.Name, c.OK, c.Detail)
	}
	for _, want := range []string{
		"read " + secret, "readdir /", "exec /bin/sh", "exec copy in home", "tcp " + other.Addr().String(), "udp socket",
		"unix socket", "netlink socket", "unshare(CLONE_NEWUSER)", "mount", "clone3",
		"write and rename in home", "exec /usr/bin/true", "status NoNewPrivs", "status Seccomp", "status CapEff",
		"status CapBnd",
	} {
		assert.True(t, names[want], "missing check %q", want)
	}
	// 秘密文件未被改动、home 之外没有写出文件。
	_, err = os.Stat(filepath.Join(outside, "written"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	t.Logf("sandbox report: %+v; %d checks passed", res.Report, len(res.Checks))
}

func TestExecPaths(t *testing.T) {
	ps, err := ExecPaths("/usr/bin/true")
	if err != nil {
		t.Skipf("no /usr/bin/true: %v", err)
	}
	require.NotEmpty(t, ps)
	for _, p := range ps {
		assert.True(t, filepath.IsAbs(p), p)
		_, err := os.Stat(p)
		assert.NoError(t, err)
	}
	// 当前测试二进制自身：Go 二进制可能是静态的（只有一项）或带解释器（两项）。
	self, err := ExecPaths(os.Args[0])
	require.NoError(t, err)
	assert.NotEmpty(t, self)
	assert.LessOrEqual(t, len(self), 2, strconv.Itoa(len(self)))
}

func TestProbe(t *testing.T) {
	abi := skipUnlessSandbox(t)
	assert.GreaterOrEqual(t, abi, MinABI)
}
