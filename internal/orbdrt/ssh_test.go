package orbdrt

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

const testGatewayKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOqN0E7cCRw3rN3q0pBWyJLsrX5mYv7hY6zGJxT0Hn3m"

func sshRuntime(t *testing.T) *Runtime {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return &Runtime{home: t.TempDir(), log: slog.Default(), sshLn: ln}
}

func sshCfg(hosts ...apigen.LinkCtlSSHHost) *apigen.LinkCtlSSHConfig {
	return &apigen.LinkCtlSSHConfig{HostKey: testGatewayKey, Hosts: hosts}
}

func TestWriteSSHFiles(t *testing.T) {
	r := sshRuntime(t)
	port := r.sshPort()
	require.NotZero(t, port)
	require.NoError(t, r.writeSSH(sshCfg(
		apigen.LinkCtlSSHHost{Name: "web", Description: "root@10.0.0.1:22 — 前端 [prod]", Compat: new(true)},
		apigen.LinkCtlSSHHost{Name: "db", Description: "pg@10.0.0.2:2200"},
		apigen.LinkCtlSSHHost{Name: "-oProxyCommand=x", Description: "注入"},
	)))

	assert.Equal(t, fmt.Sprintf(`{
  "hosts": {
    "db": {
      "host": "127.0.0.1",
      "port": %d,
      "username": "db",
      "description": "pg@10.0.0.2:2200"
    },
    "web": {
      "host": "127.0.0.1",
      "port": %d,
      "username": "web",
      "description": "root@10.0.0.1:22 — 前端 [prod]",
      "compat": true
    }
  }
}
`, port, port), readFile(t, filepath.Join(r.home, ".omp", "agent", "ssh.json")))
	assert.Equal(t, fmt.Sprintf("# 由 Oryn 生成，每次下发都会重写\n\nHost db\n  HostName 127.0.0.1\n  Port %d\n  User db\n\nHost web\n  HostName 127.0.0.1\n  Port %d\n  User web\n", port, port),
		readFile(t, filepath.Join(r.home, ".oryn", "ssh", "config")))
	assert.Equal(t, "Include ~/.oryn/ssh/config\n", readFile(t, filepath.Join(r.home, ".ssh", "config")))
	assert.Equal(t, fmt.Sprintf("[127.0.0.1]:%d %s oryn-ssh-gateway\n", port, testGatewayKey),
		readFile(t, filepath.Join(r.home, ".ssh", "known_hosts")))
	for _, p := range []string{".omp/agent/ssh.json", ".oryn/ssh/config", ".ssh/config", ".ssh/known_hosts"} {
		st, err := os.Stat(filepath.Join(r.home, p))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), p)
	}
	for _, d := range []string{".oryn/ssh", ".ssh"} {
		st, err := os.Stat(filepath.Join(r.home, d))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), st.Mode().Perm(), d)
	}

	// 没有主机：空目录
	require.NoError(t, r.writeSSH(sshCfg()))
	assert.Equal(t, "{\n  \"hosts\": {}\n}\n", readFile(t, filepath.Join(r.home, ".omp", "agent", "ssh.json")))

	// 网关公钥不合规：拒绝
	require.Error(t, r.writeSSH(&apigen.LinkCtlSSHConfig{HostKey: "ssh-ed25519 AAAA\nHost *", Hosts: nil}))
}

func TestWriteSSHIdempotentAndPortChange(t *testing.T) {
	r := sshRuntime(t)
	dot := filepath.Join(r.home, ".ssh")
	require.NoError(t, os.MkdirAll(dot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dot, "config"), []byte("Host gh\n  User git\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dot, "known_hosts"), []byte("github.com ssh-ed25519 AAAAgh\n"), 0o600))
	// Core 的远端探测缓存：描述变了的主机要清掉
	cache := filepath.Join(r.home, ".omp", "remote-host")
	require.NoError(t, os.MkdirAll(cache, 0o755))

	cfg := sshCfg(apigen.LinkCtlSSHHost{Name: "web", Description: "a"}, apigen.LinkCtlSSHHost{Name: "keep", Description: "k"})
	require.NoError(t, r.writeSSH(cfg))
	for _, n := range []string{"web", "keep", "gone"} {
		require.NoError(t, os.WriteFile(filepath.Join(cache, n+".json"), []byte("{}"), 0o644))
	}
	require.NoError(t, r.writeSSH(cfg))
	assert.FileExists(t, filepath.Join(cache, "web.json"), "描述没变不清缓存")

	cfg.Hosts[0].Description = "b"
	require.NoError(t, r.writeSSH(cfg))
	assert.NoFileExists(t, filepath.Join(cache, "web.json"))
	assert.FileExists(t, filepath.Join(cache, "keep.json"))
	assert.FileExists(t, filepath.Join(cache, "gone.json"), "不在旧目录里的名字不碰")

	assert.Equal(t, "Include ~/.oryn/ssh/config\nHost gh\n  User git\n", readFile(t, filepath.Join(dot, "config")))
	st, err := os.Stat(filepath.Join(dot, "config"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), st.Mode().Perm(), "沿用原文件权限")
	oldPort := r.sshPort()
	assert.Equal(t, fmt.Sprintf("github.com ssh-ed25519 AAAAgh\n[127.0.0.1]:%d %s oryn-ssh-gateway\n", oldPort, testGatewayKey),
		readFile(t, filepath.Join(dot, "known_hosts")))

	// 端口变了：旧条目被替换
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	r.sshLn = ln
	require.NoError(t, r.writeSSH(cfg))
	kh := readFile(t, filepath.Join(dot, "known_hosts"))
	assert.Equal(t, fmt.Sprintf("github.com ssh-ed25519 AAAAgh\n[127.0.0.1]:%d %s oryn-ssh-gateway\n", r.sshPort(), testGatewayKey), kh)
	assert.NotContains(t, kh, fmt.Sprintf(":%d ", oldPort))
}

func TestWriteSSHSequence(t *testing.T) {
	r := sshRuntime(t)
	older, newer := r.nextOmpSeq(), r.nextOmpSeq()
	_, _, err := r.writeOmpConfigAt(newer, nil, nil, false, sshCfg(apigen.LinkCtlSSHHost{Name: "new", Description: "n"}))
	require.NoError(t, err)
	_, _, err = r.writeOmpConfigAt(older, nil, nil, false, sshCfg(apigen.LinkCtlSSHHost{Name: "old", Description: "o"}))
	require.NoError(t, err)
	assert.Contains(t, readFile(t, filepath.Join(r.home, ".omp", "agent", "ssh.json")), `"new"`, "编号更旧的写入被跳过")
	// ssh 缺省 = 不动
	_, _, err = r.writeOmpConfigAt(r.nextOmpSeq(), nil, nil, false, nil)
	require.NoError(t, err)
	assert.Contains(t, readFile(t, filepath.Join(r.home, ".omp", "agent", "ssh.json")), `"new"`)

	// 没有监听（runner / 协调宿主 / 监听失败）：不写
	r2 := &Runtime{home: t.TempDir(), log: slog.Default()}
	require.NoError(t, r2.writeSSH(sshCfg(apigen.LinkCtlSSHHost{Name: "x", Description: "x"})))
	assert.NoFileExists(t, filepath.Join(r2.home, ".omp", "agent", "ssh.json"))
}

// orb 内连到网关端口：控制面一侧收到 t=ssh 的流，两个方向的字节都能到达。
func TestSSHPortBridgesToLink(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	require.NotNil(t, rt.sshLn)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	assert.Contains(t, string(raw), `"ssh"`)

	c, err := net.Dial("tcp", rt.sshLn.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write([]byte("SSH-2.0-test\r\n"))
	require.NoError(t, err)
	var st *link.Stream
	select {
	case st = <-cp.sshs:
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到 ssh 流")
	}
	defer st.Close()
	assert.Equal(t, link.StreamSSH, st.Header.T)
	line, err := bufio.NewReader(st).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "SSH-2.0-test\r\n", line)
	_, err = st.Write([]byte("SSH-2.0-Oryn\r\n"))
	require.NoError(t, err)
	got := make([]byte, len("SSH-2.0-Oryn\r\n"))
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadFull(c, got)
	require.NoError(t, err)
	assert.Equal(t, "SSH-2.0-Oryn\r\n", string(got))

	// 控制面关流：orb 内的连接读到 EOF
	require.NoError(t, st.Close())
	_, err = c.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestCoordinatorHasNoSSHPort(t *testing.T) {
	rt, _, _, _ := startCoordinator(t)
	assert.Nil(t, rt.sshLn)
}
