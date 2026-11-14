//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// tfoEnv 非空表示当前进程是 TestSeccompBlocksFastOpen 的子进程（seccomp 过滤器装上后无法卸下）。
const tfoEnv = "ORYN_SANDBOX_TFO_HELPER"

// TestHelperSeccompTFO 只在子进程里运行：先建回环监听，只装 seccomp 主过滤器（不施加 Landlock，
// 排除新内核上 Landlock 自己挡住 TFO 的干扰），再逐项尝试，按「名称 -> 错误文本（空 = 成功）」输出 JSON。
func TestHelperSeccompTFO(t *testing.T) {
	if os.Getenv(tfoEnv) == "" {
		t.Skip("helper process only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	addr := ln.Addr().String()
	sa, family, err := sockaddrOf(addr)
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	if err := loadDenyFilter(); err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	sock := func() int {
		fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			fmt.Println(err)
			os.Exit(3)
		}
		return fd
	}
	res := map[string]string{}
	record := func(name string, err error) {
		res[name] = ""
		if err != nil {
			res[name] = err.Error()
		}
	}
	record("sendto", fastOpenSendto(addr))
	fd := sock()
	_, err = unix.SendmsgN(fd, []byte("tfo"), nil, sa, unix.MSG_FASTOPEN)
	record("sendmsg", err)
	unix.Close(fd)
	// msgvec = NULL、vlen = 0：过滤器在内核处理参数之前判定，放行时返回 0。
	fd = sock()
	_, _, e := unix.Syscall6(unix.SYS_SENDMMSG, uintptr(fd), 0, 0, unix.MSG_FASTOPEN, 0, 0)
	if e != 0 {
		record("sendmmsg", e)
	} else {
		record("sendmmsg", nil)
	}
	unix.Close(fd)
	// 对照：已连接的套接字上不带 MSG_FASTOPEN 的发送照常可用
	fd = sock()
	if err := unix.Connect(fd, sa); err != nil {
		record("control connect", err)
	} else {
		record("control sendto", unix.Sendto(fd, []byte("x"), 0, nil))
		_, err = unix.SendmsgN(fd, []byte("x"), nil, nil, 0)
		record("control sendmsg", err)
	}
	unix.Close(fd)
	out, _ := json.Marshal(res)
	fmt.Println("RESULT " + string(out))
	os.Exit(0)
}

// 较旧内核上 TFO 的发送路径绕过 Landlock 的 connect 钩子，seccomp 必须独立挡住它（CI 的 ABI 8 内核上曾连到白名单外端口）。
func TestSeccompBlocksFastOpen(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSeccompTFO$", "-test.v=false")
	cmd.Env = append(os.Environ(), tfoEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var res map[string]string
	for _, l := range strings.Split(string(out), "\n") {
		if raw, ok := strings.CutPrefix(l, "RESULT "); ok {
			require.NoError(t, json.Unmarshal([]byte(raw), &res))
		}
	}
	require.NotNil(t, res, "%s", out)
	for _, n := range []string{"sendto", "sendmsg", "sendmmsg"} {
		assert.Contains(t, res[n], unix.EPERM.Error(), "%s 带 MSG_FASTOPEN 必须被 seccomp 拒绝", n)
	}
	for _, n := range []string{"control sendto", "control sendmsg"} {
		v, ok := res[n]
		assert.True(t, ok, "%s 没有执行：%v", n, res)
		assert.Empty(t, v, "%s 不带 MSG_FASTOPEN 应照常可用", n)
	}
}
