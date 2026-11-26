package providerrt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/unixsock"
)

func TestRelayTarget(t *testing.T) {
	for in, want := range map[string]string{
		"http://172.18.0.1:7600":       "172.18.0.1:7600",
		"http://orynd.local":           "orynd.local:80",
		"ws://orynd.local/":            "orynd.local:80",
		"https://172.31.255.1:7643":    "172.31.255.1:7643",
		"https://oryn.example.com/x":   "oryn.example.com:443",
		"WSS://oryn.example.com":       "oryn.example.com:443",
		"https://[fd00::1]/":           "[fd00::1]:443",
		"http://[fd00::1]:7600/orb/v1": "[fd00::1]:7600",
	} {
		got, err := relayTarget(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "ftp://x", "http://", "://x", "172.18.0.1:7600"} {
		_, err := relayTarget(bad)
		assert.Error(t, err, bad)
	}
}

// echoServer 起一个 TCP 回显服务，返回地址。
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// roundTrip 经 c 发一行并读回显。
func roundTrip(t *testing.T, c net.Conn, s string) {
	t.Helper()
	require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	_, err := io.WriteString(c, s+"\n")
	require.NoError(t, err)
	line, err := bufio.NewReader(c).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, s+"\n", line)
}

// expectClosed 断言 c 很快读到 EOF（或连接被重置），而不是等到超时。
func expectClosed(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err := c.Read(make([]byte, 1))
	require.Error(t, err, msg)
	var ne net.Error
	assert.False(t, errors.As(err, &ne) && ne.Timeout(), "%s：等到超时也没关", msg)
}

// 中继转发、并发上限 relayMaxConns（第 9 条立即关闭）、Close 关掉在途连接；监听用 unixsock.Listen 的长路径。
func TestRelayForward(t *testing.T) {
	target := echoServer(t)
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 80))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	sock := filepath.Join(dir, "link.sock")
	ln, err := unixsock.Listen(sock, 0o600, -1)
	require.NoError(t, err)
	r := newLinkRelay(ln, target)
	t.Cleanup(r.Close)

	ctx := context.Background()
	var conns []net.Conn
	for i := range relayMaxConns {
		c, err := unixsock.Dial(ctx, sock)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		roundTrip(t, c, "hello-"+string(rune('a'+i)))
		conns = append(conns, c)
	}
	extra, err := unixsock.Dial(ctx, sock)
	require.NoError(t, err)
	defer extra.Close()
	expectClosed(t, extra, "第 9 条连接应被立即关闭")

	// 释放一条之后又能连。
	require.NoError(t, conns[0].Close())
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.conns) < relayMaxConns
	}, 5*time.Second, 10*time.Millisecond)
	again, err := unixsock.Dial(ctx, sock)
	require.NoError(t, err)
	defer again.Close()
	roundTrip(t, again, "again")

	// 目标拨不通：沙箱侧连接被关。
	dead := newLinkRelay(mustUnixListen(t), "127.0.0.1:1")
	defer dead.Close()
	dc, err := net.Dial("unix", dead.ln.Addr().String())
	require.NoError(t, err)
	defer dc.Close()
	expectClosed(t, dc, "目标拨不通时关闭")

	// Close 关监听与在途连接；socket 文件留着（unixsock.Listen 的约定）。
	r.Close()
	expectClosed(t, conns[1], "Close 后在途连接被关")
	_, err = unixsock.Dial(ctx, sock)
	assert.Error(t, err)
	assert.FileExists(t, sock)
}

func mustUnixListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "r.sock"))
	require.NoError(t, err)
	return ln
}

// relaySet：put 替换并关闭旧的；has 按目标比对；drop / closeAll 关闭。
func TestRelaySet(t *testing.T) {
	var s relaySet
	a := newLinkRelay(mustUnixListen(t), "127.0.0.1:1")
	s.put("orb-a", a)
	assert.True(t, s.has("orb-a", "127.0.0.1:1"))
	assert.False(t, s.has("orb-a", "127.0.0.1:2"))
	assert.False(t, s.has("orb-b", "127.0.0.1:1"))
	b := newLinkRelay(mustUnixListen(t), "127.0.0.1:2")
	s.put("orb-a", b)
	assert.True(t, a.isClosed(), "被替换的中继已关闭")
	assert.False(t, b.isClosed())
	c := newLinkRelay(mustUnixListen(t), "127.0.0.1:3")
	s.put("orb-c", c)
	s.drop("orb-a")
	assert.True(t, b.isClosed())
	s.drop("orb-a") // 幂等
	s.closeAll()
	assert.True(t, c.isClosed())
	assert.False(t, s.has("orb-c", "127.0.0.1:3"))
}

// process 驱动的沙箱：spawn 建中继（经它连得到 ORYN_URL 的 host:port）、给 orbd 加环境变量、记下网络模式；
// set_network 的三种情形；delete 关中继、删目录。
func TestProcessSandbox(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "fake-orbd")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho \"$ORYN_SANDBOX $ORYN_LINK_VIA\" > \"$HOME/sandbox-env\"\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))
	p := NewProcess(filepath.Join(root, "data"), bin, config.Docker{})
	p.StopTimeout = 3 * time.Second
	t.Cleanup(p.Close)
	assert.Equal(t, new(true), p.Info().Capabilities.Sandbox)
	ctx := context.Background()
	target := echoServer(t)
	spawn := func(orbID string, network apigen.SandboxNetwork) Job {
		return Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
			Env:     apigen.JobSpawnEnv{OrynURL: "http://" + target, OrynOrbToken: "oot_" + orbID},
			Sandbox: &apigen.JobSpawnSandbox{Network: network},
		}}
	}
	setNet := func(orbID, network string) error {
		return execErr(p, ctx, Job{Op: "set_network", OrbID: orbID, Network: network}, io.Discard)
	}
	readFile := func(orbID, name string) string {
		b, err := os.ReadFile(filepath.Join(p.orbDir(orbID), name))
		require.NoError(t, err)
		return string(b)
	}

	require.NoError(t, execErr(p, ctx, spawn("sbx-none", apigen.SandboxNetworkNone), io.Discard))
	sock := filepath.Join(p.orbDir("sbx-none"), "link", "link.sock")
	eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(p.HomeDir("sbx-none"), "sandbox-env"))
		return string(b) == "1 unix:"+sock+"\n"
	}, "orbd 拿到 ORYN_SANDBOX 与 ORYN_LINK_VIA")
	c, err := unixsock.Dial(ctx, sock)
	require.NoError(t, err)
	roundTrip(t, c, "via relay")
	_ = c.Close()
	assert.Equal(t, "none", readFile("sbx-none", "created-network"))
	assert.Equal(t, "none", readFile("sbx-none", "network"))
	// 再次 spawn（已在运行）沿用中继，不改记录。
	require.NoError(t, execErr(p, ctx, spawn("sbx-none", apigen.SandboxNetworkNone), io.Discard))
	c, err = unixsock.Dial(ctx, sock)
	require.NoError(t, err)
	roundTrip(t, c, "still")
	_ = c.Close()

	err = setNet("sbx-none", "egress")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "network_unavailable:"), err.Error())
	require.NoError(t, setNet("sbx-none", "none"))

	require.NoError(t, execErr(p, ctx, spawn("sbx-egress", apigen.SandboxNetworkEgress), io.Discard))
	require.NoError(t, setNet("sbx-egress", "none"))
	assert.Equal(t, "none", readFile("sbx-egress", "network"))
	assert.Equal(t, "egress", readFile("sbx-egress", "created-network"))
	require.NoError(t, setNet("sbx-egress", "egress"))
	assert.Equal(t, "egress", readFile("sbx-egress", "network"))

	// 普通 orb 不能 set_network。
	require.NoError(t, execErr(p, ctx, Job{Op: "spawn", OrbID: "orb-plain", Spawn: &apigen.JobSpawnParams{Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_x"}}}, io.Discard))
	assert.ErrorContains(t, setNet("orb-plain", "none"), "不是沙箱")
	assert.NoDirExists(t, filepath.Join(p.orbDir("orb-plain"), "link"))

	require.NoError(t, execErr(p, ctx, Job{Op: "delete", OrbID: "sbx-none"}, io.Discard))
	assert.NoDirExists(t, p.orbDir("sbx-none"))
	assert.False(t, p.relays.has("sbx-none", target))
	assert.True(t, p.relays.has("sbx-egress", target))
}

// sandboxLink 是测试 link 服务端收到的一条已 hello 的 link。
type sandboxLink struct {
	sess  *link.Session
	token string
	hello apigen.LinkCtlHelloParams
}

// serveSandboxLinks 返回一个最小的 link 服务端：接受任意令牌，收到 hello 后把 link 送进返回的 channel，
// 之后保持会话直到对端断开或测试结束。
func serveSandboxLinks(t *testing.T) (http.Handler, <-chan sandboxLink) {
	t.Helper()
	links := make(chan sandboxLink, 8)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != link.Path {
			http.Error(w, `{"code":"not_found"}`, http.StatusNotFound)
			return
		}
		var token string
		sess, err := link.Accept(w, req, func(_ *http.Request, tok string) error { token = tok; return nil })
		if err != nil {
			return
		}
		defer sess.Close()
		hctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		first, err := sess.AcceptStream(hctx)
		cancel()
		if err != nil || first.Header.T != link.StreamCtl {
			return
		}
		var once sync.Once
		ctl := link.NewCtl(first, link.CtlHandlers{
			OnNotify: func(_ context.Context, method string, params json.RawMessage) {
				if method != "hello" {
					return
				}
				once.Do(func() {
					var h apigen.LinkCtlHelloParams
					_ = json.Unmarshal(params, &h)
					select {
					case links <- sandboxLink{sess: sess, token: token, hello: h}:
					default:
					}
				})
			},
		})
		select {
		case <-ctl.Done():
		case <-done:
		}
	}), links
}

// waitSandboxLink 等一条 hello 带 sandbox 特性的 link。
func waitSandboxLink(t *testing.T, links <-chan sandboxLink, wait time.Duration) sandboxLink {
	t.Helper()
	select {
	case l := <-links:
		require.True(t, slices.Contains(l.hello.Features, "sandbox"), "hello 特性 %v 不含 sandbox", l.hello.Features)
		return l
	case <-time.After(wait):
		t.Fatalf("%s 内没有收到沙箱 orbd 的 hello", wait)
		return sandboxLink{}
	}
}

// sandboxExec 经 link 的 fs 流向沙箱 orbd 发 POST /sandbox/exec，返回合并输出与退出码。
func sandboxExec(t *testing.T, ctx context.Context, sess *link.Session, command string) (string, int) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return sess.Open(ctx, link.StreamHeader{T: link.StreamFS})
		},
	}}
	body, err := json.Marshal(apigen.OrbSandboxExecRequest{Command: command, TimeoutSec: 60})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://orb/sandbox/exec", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /sandbox/exec: %d %s", resp.StatusCode, b)
	}
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var line struct {
			S    string `json:"s"`
			D    string `json:"d"`
			Exit *int   `json:"exit"`
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line), sc.Text())
		if line.Exit != nil {
			return out.String(), *line.Exit
		}
		out.WriteString(line.D)
	}
	t.Fatalf("POST /sandbox/exec 没有 exit 行（%v）：%s", sc.Err(), out.String())
	return "", -1
}

// netInterfaces 返回 /proc/net/dev 里的接口名（排序）。
func netInterfaces(t *testing.T, procNetDev string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(procNetDev, "\n") {
		name, _, ok := strings.Cut(line, ":")
		if ok && !strings.Contains(name, "|") {
			names = append(names, strings.TrimSpace(name))
		}
	}
	slices.Sort(names)
	return names
}

// process 驱动起真 orbd（精简模式）：orbd 经 unix 中继连上测试里的 link 服务端，hello 带 sandbox。
// 依赖 orbd 的 ORYN_SANDBOX / ORYN_LINK_VIA（计划 2.1、2.2）。
func TestProcessSandboxOrbd(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "orbd")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/orbd").CombinedOutput(); err != nil {
		t.Fatalf("构建 orbd: %v\n%s", err, out)
	}
	handler, links := serveSandboxLinks(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	p := NewProcess(filepath.Join(root, "data"), bin, config.Docker{})
	p.StopTimeout = 5 * time.Second
	t.Cleanup(p.Close)
	ctx := context.Background()
	const orbID = "orb-sbxproc"
	require.NoError(t, execErr(p, ctx, Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
		Env:     apigen.JobSpawnEnv{OrynURL: srv.URL, OrynOrbID: orbID, OrynOrbToken: "oot_sbxproc"},
		Sandbox: &apigen.JobSpawnSandbox{Network: apigen.SandboxNetworkNone},
	}}, io.Discard))
	l := waitSandboxLink(t, links, 30*time.Second)
	assert.Equal(t, "oot_sbxproc", l.token)
	assert.Equal(t, []string{"fs", "fs-tar", "metrics", "sandbox"}, l.hello.Features)
}
