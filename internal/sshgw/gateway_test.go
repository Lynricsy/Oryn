package sshgw

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type gwEnv struct {
	*testEnv
	key    apigen.SSHKey
	target *sshgwtest.FakeSSHD
	host   apigen.SSHHost
	orb    db.Orb
	addr   string
}

// newGwEnv 起一台假目标机、登记主机 web，并在回环端口上为 orb 跑网关（每条连接一个 serveConn）。
func newGwEnv(t *testing.T, opts sshgwtest.Options, mod ...func(*apigen.SSHHostCreate)) *gwEnv {
	t.Helper()
	return newGwEnvCfg(t, func(*Config) {}, opts, mod...)
}

func newGwEnvCfg(t *testing.T, cfgMod func(*Config), opts sshgwtest.Options, mod ...func(*apigen.SSHHostCreate)) *gwEnv {
	t.Helper()
	e := &gwEnv{testEnv: newEnv(t, cfgMod)}
	e.key = e.testEnv.key(t, "k")
	opts.AuthorizedKeys = []ssh.PublicKey{authorizedKey(t, e.key)}
	e.target = sshgwtest.NewFakeSSHD(t, opts)
	e.host = e.fakeHost(t, "web", e.key, e.target, mod...)
	e.orb = db.Orb{ID: "orb-gw", Kind: "orb"}
	e.orbs.put(e.orb)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); _ = ln.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				orb, err := e.orbs.Get(ctx, e.orb.ID)
				if err != nil {
					_ = c.Close()
					return
				}
				e.svc.serveConn(ctx, orb, c)
			})
		}
	})
	e.addr = ln.Addr().String()
	return e
}

// connect 以登录名 user、无认证方法连网关；banner 收集服务端发来的横幅。
func (e *gwEnv) connect(t *testing.T, user string) (*ssh.Client, *strings.Builder, error) {
	t.Helper()
	var banner strings.Builder
	cfg := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.FixedHostKey(e.svc.signer.PublicKey()),
		BannerCallback:  func(m string) error { banner.WriteString(m); return nil },
		Timeout:         5 * time.Second,
	}
	c, err := ssh.Dial("tcp", e.addr, cfg)
	if err == nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, &banner, err
}

func (e *gwEnv) auditRows(t *testing.T) []apigen.SSHAuditEntry {
	t.Helper()
	l, err := e.svc.Audit(context.Background(), AuditQuery{})
	require.NoError(t, err)
	return l.Entries
}

func TestGatewayExec(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	c, _, err := e.connect(t, "web")
	require.NoError(t, err)

	sess, err := c.NewSession()
	require.NoError(t, err)
	var out, errOut strings.Builder
	sess.Stdout, sess.Stderr = &out, &errOut
	err = sess.Run("echo hi; echo oops >&2; exit 3")
	var ee *ssh.ExitError
	require.True(t, errors.As(err, &ee), "%v", err)
	require.Equal(t, 3, ee.ExitStatus())
	require.Equal(t, "hi\n", out.String())
	require.Equal(t, "oops\n", errOut.String())

	// stdin 透传
	sess, err = c.NewSession()
	require.NoError(t, err)
	sess.Stdin = strings.NewReader("ping\n")
	b, err := sess.Output("cat")
	require.NoError(t, err)
	require.Equal(t, "ping\n", string(b))

	require.Eventually(t, func() bool { return len(e.auditRows(t)) >= 3 }, 5*time.Second, 20*time.Millisecond)
	rows := e.auditRows(t)
	var execRow *apigen.SSHAuditEntry
	for i := range rows {
		if rows[i].Kind == "exec" && rows[i].Detail == "echo hi; echo oops >&2; exit 3" {
			execRow = &rows[i]
		}
	}
	require.NotNil(t, execRow)
	require.Equal(t, apigen.SSHAuditEntrySource("orb"), execRow.Source)
	require.Equal(t, 3, *execRow.ExitCode)
	require.False(t, execRow.Ok)
	require.Equal(t, e.orb.ID, *execRow.OrbID)
	require.Equal(t, "web", execRow.HostName)
	require.NotNil(t, execRow.DurationMs)
	require.Equal(t, "connect", string(rows[len(rows)-1].Kind))
	require.True(t, rows[len(rows)-1].Ok)
}

func TestGatewayRejects(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	_, banner, err := e.connect(t, "nope")
	require.Error(t, err)
	require.Contains(t, banner.String(), `oryn: 主机 "nope" 未登记，或对这台 orb 不可见`)

	// 全自动 orb 连未放开的主机
	e.orb.Automation = true
	e.orbs.put(e.orb)
	_, banner, err = e.connect(t, "web")
	require.Error(t, err)
	require.Contains(t, banner.String(), "不允许全自动线程使用")
	_, err = e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AllowAutomation: new(true)}})
	require.NoError(t, err)
	_, _, err = e.connect(t, "web")
	require.NoError(t, err)

	// 上游拨号失败的原因也送到 orb
	_, err = e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Port: new(1)}})
	require.NoError(t, err)
	_, banner, err = e.connect(t, "web")
	require.Error(t, err)
	require.Contains(t, banner.String(), "连不上 127.0.0.1:1")

	kinds := map[string]int{}
	for _, r := range e.auditRows(t) {
		if !r.Ok {
			kinds[string(r.Kind)]++
		}
	}
	require.Equal(t, map[string]int{"deny": 2, "connect": 1}, kinds)
}

func TestGatewayForwarding(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	e := newGwEnv(t, sshgwtest.Options{AllowDirectTCPIP: true})
	c, _, err := e.connect(t, "web")
	require.NoError(t, err)
	_, err = c.Dial("tcp", echo.Addr().String())
	var oe *ssh.OpenChannelError
	require.True(t, errors.As(err, &oe), "%v", err)
	require.Equal(t, ssh.Prohibited, oe.Reason)
	require.Contains(t, oe.Message, "未开启端口转发")

	_, err = e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AllowForward: new(true)}})
	require.NoError(t, err)
	c, _, err = e.connect(t, "web")
	require.NoError(t, err)
	fc, err := c.Dial("tcp", echo.Addr().String())
	require.NoError(t, err)
	_, err = fc.Write([]byte("marco\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(fc).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "marco\n", line)
	_ = fc.Close()
	require.Equal(t, 1, e.target.DirectTCPIPCount())

	// 上游发起的通道一律被拒
	require.Error(t, e.target.SendForwardedTCPIP())

	// 远程转发不支持
	_, err = c.Listen("tcp", "127.0.0.1:0")
	require.Error(t, err)
}

func TestGatewaySFTP(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	c, _, err := e.connect(t, "web")
	require.NoError(t, err)
	sc, err := sftp.NewClient(c)
	require.NoError(t, err)
	defer sc.Close()
	dir := t.TempDir()
	f, err := sc.Create(dir + "/a.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	ents, err := sc.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, ents, 1)
	require.Equal(t, "a.txt", ents[0].Name())
}

func TestGatewayInvalidateDisconnects(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	c, _, err := e.connect(t, "web")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	_, err = e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("x")}})
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("改主机后网关会话没有断开")
	}
}

// 拨号上游期间主机被改：连接在登记时作废，不会带着旧配置存活。
func TestGatewayInvalidateDuringDial(t *testing.T) {
	var gate struct {
		armed   chan struct{}
		entered chan struct{}
		release chan struct{}
	}
	gate.armed, gate.entered, gate.release = make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := newGwEnvCfg(t, func(c *Config) {
		c.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			select {
			case <-gate.armed:
				close(gate.entered)
				<-gate.release
			default:
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}, sshgwtest.Options{})
	close(gate.armed)
	type result struct {
		banner string
		err    error
	}
	res := make(chan result, 1)
	go func() {
		_, banner, err := e.connect(t, "web")
		res <- result{banner.String(), err}
	}()
	<-gate.entered
	_, err := e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("x")}})
	require.NoError(t, err)
	close(gate.release)
	r := <-res
	require.Error(t, r.err)
	require.Contains(t, r.banner, "主机配置刚被修改")
}

func TestGatewayConcurrentExecs(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	c, _, err := e.connect(t, "web")
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			sess, err := c.NewSession()
			if err != nil {
				errs <- fmt.Errorf("session %d open: %w", i, err)
				return
			}
			defer sess.Close()
			out, err := sess.Output(fmt.Sprintf("sleep 0.0%d; echo %d", i%10, i))
			if err != nil {
				errs <- fmt.Errorf("session %d run: %w", i, err)
				return
			}
			if string(out) != fmt.Sprintf("%d\n", i) {
				errs <- fmt.Errorf("session %d got %q", i, out)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// 真 OpenSSH 客户端（Core 的用法：BatchMode、accept-new）经网关执行命令；拒绝原因打到标准错误。
func TestGatewayOpenSSHClient(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("没有 ssh 可执行文件")
	}
	e := newGwEnv(t, sshgwtest.Options{})
	host, port, err := net.SplitHostPort(e.addr)
	require.NoError(t, err)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(kh, []byte(fmt.Sprintf("[%s]:%s %s oryn-ssh-gateway\n", host, port, e.svc.gatewayPublicKey())), 0o600))
	run := func(user, cmd string) (string, string, int) {
		c := exec.Command("ssh", "-F", "/dev/null", "-o", "UserKnownHostsFile="+kh, "-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=accept-new", "-p", port, user+"@"+host, cmd)
		var out, errOut strings.Builder
		c.Stdout, c.Stderr = &out, &errOut
		err := c.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			require.NoError(t, err)
		}
		return out.String(), errOut.String(), code
	}
	out, _, code := run("web", "echo hi; exit 3")
	require.Equal(t, "hi\n", out)
	require.Equal(t, 3, code)
	_, stderr, code := run("nope", "true")
	require.Equal(t, 255, code)
	require.Contains(t, stderr, "未登记")
}
