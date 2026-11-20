package sshgw

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	orbID := e.orb.ID // 测试会把 e.orb 换成同 ID 的 runner：接入协程只读这份拷贝，每条连接按 ID 取当前行
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); _ = ln.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				orb, err := e.orbs.Get(ctx, orbID)
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

// runnerThreadRow 在库里建 runner orb（若还没有）与它的一条工作线程（属于 project，可为 nil）。
func (e *gwEnv) runnerThreadRow(t *testing.T, orbID, id string, project *string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.st.Q.GetProvider(ctx, "prv-r"); err != nil {
		require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prv-r", Name: "r", TokenHash: "h", CreatedAt: 1}))
	}
	if _, err := e.st.Q.GetOrb(ctx, orbID); err != nil {
		require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{
			ID: orbID, ProviderID: "prv-r", Type: "small", State: "ready", TokenHash: "th-" + orbID, CreatedAt: 1,
		}))
	}
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: id, ProjectID: project, OrbID: &orbID, State: "idle", StateChangedAt: 1, SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1,
		Kind: "work", TitleSource: "auto",
	}))
}

// threadSigner 生成一把线程密钥，返回签名器与 authorized_keys 单行。
func threadSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return s, string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}

// connectKey 以登录名 user、只用公钥 signer 连网关。
func (e *gwEnv) connectKey(t *testing.T, user string, signer ssh.Signer) (*ssh.Client, *strings.Builder, error) {
	t.Helper()
	var banner strings.Builder
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
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

// M21：runner 的流只接受登记过的线程公钥；两个项目的线程各自只连得上全局与本项目主机，审计记在认出的线程上。
func TestGatewayRunnerThreadKeys(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	pa, pb := e.project(t, "prj_a"), e.project(t, "prj_b")
	e.fakeHost(t, "pa-only", e.key, e.target, func(in *apigen.SSHHostCreate) { in.ProjectID = &pa })
	pbHost := e.fakeHost(t, "pb-only", e.key, e.target, func(in *apigen.SSHHostCreate) { in.ProjectID = &pb })
	e.orb = db.Orb{ID: "orb-gw", Kind: "runner"}
	e.orbs.put(e.orb)
	e.runnerThreadRow(t, e.orb.ID, "T-a", &pa)
	e.runnerThreadRow(t, e.orb.ID, "T-b", &pb)
	e.runnerThreadRow(t, "orb-other", "T-c", &pa)
	ka, pubA := threadSigner(t)
	kb, pubB := threadSigner(t)
	kc, pubC := threadSigner(t)
	kd, _ := threadSigner(t)
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA))
	require.NoError(t, e.svc.RegisterThreadKey("T-b", e.orb.ID, pubB))
	require.NoError(t, e.svc.RegisterThreadKey("T-c", "orb-other", pubC))

	// 项目 a 的线程：全局与本项目主机可连，别的项目的主机拒绝
	c, _, err := e.connectKey(t, "web", ka)
	require.NoError(t, err)
	require.Equal(t, "global\n", runOn(t, c, "echo global"))
	c, _, err = e.connectKey(t, "pa-only", ka)
	require.NoError(t, err)
	require.Equal(t, "a\n", runOn(t, c, "echo a"))
	_, banner, err := e.connectKey(t, "pb-only", ka)
	require.Error(t, err)
	require.Contains(t, banner.String(), `oryn: 主机 "pb-only" 未登记，或对这个线程不可见`)
	// 项目 b 的线程反之
	c, _, err = e.connectKey(t, "pb-only", kb)
	require.NoError(t, err)
	require.Equal(t, "b\n", runOn(t, c, "echo b"))
	_, banner, err = e.connectKey(t, "pa-only", kb)
	require.Error(t, err)
	require.Contains(t, banner.String(), "对这个线程不可见")

	// 无公钥、未登记的公钥、登记在别的 runner 上的线程的公钥：一律拒绝，不碰主机
	_, _, err = e.connect(t, "web")
	require.Error(t, err)
	_, _, err = e.connectKey(t, "web", kd)
	require.Error(t, err)
	_, _, err = e.connectKey(t, "pa-only", kc)
	require.Error(t, err)
	// 登记表与线程行不一致（公钥登记在这台 runner、线程行却在别的 orb 上）同样拒绝
	require.NoError(t, e.svc.RegisterThreadKey("T-c", e.orb.ID, pubC))
	_, _, err = e.connectKey(t, "pa-only", kc)
	require.Error(t, err)
	// 归档的线程：公钥即刻失效（登记保留，取消归档后 agent 不必重启）
	archive := func(at *int64) {
		_, err := e.st.Q.UpdateThreadMeta(context.Background(), db.UpdateThreadMetaParams{TitleSource: "auto", ArchivedAt: at, UpdatedAt: 2, ID: "T-b"})
		require.NoError(t, err)
	}
	archive(new(int64(2)))
	_, _, err = e.connectKey(t, "pb-only", kb)
	require.Error(t, err)
	archive(nil)
	_, _, err = e.connectKey(t, "pb-only", kb)
	require.NoError(t, err)

	// 全自动：只用放开的主机
	e.orb.Automation = true
	e.orbs.put(e.orb)
	_, banner, err = e.connectKey(t, "pb-only", kb)
	require.Error(t, err)
	require.Contains(t, banner.String(), "不允许全自动线程使用")
	_, err = e.svc.UpdateHost(context.Background(), pbHost.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AllowAutomation: new(true)}})
	require.NoError(t, err)
	_, _, err = e.connectKey(t, "pb-only", kb)
	require.NoError(t, err)
	_, _, err = e.connectKey(t, "web", kb)
	require.Error(t, err)
	e.orb.Automation = false
	e.orbs.put(e.orb)

	// 注销后失效；同一线程登记新公钥后旧公钥失效
	e.svc.DropThreadKey("T-a")
	_, _, err = e.connectKey(t, "web", ka)
	require.Error(t, err)
	ka2, pubA2 := threadSigner(t)
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA))
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA2))
	_, _, err = e.connectKey(t, "web", ka)
	require.Error(t, err)
	_, _, err = e.connectKey(t, "web", ka2)
	require.NoError(t, err)
	// runner 离线：整台的登记清掉
	e.svc.DropOrbKeys(e.orb.ID)
	_, _, err = e.connectKey(t, "web", ka2)
	require.Error(t, err)
	_, _, err = e.connectKey(t, "pb-only", kb)
	require.Error(t, err)

	require.Eventually(t, func() bool {
		for _, r := range e.auditRows(t) {
			if r.Kind == "exec" && r.Detail == "echo a" {
				return r.OrbID != nil && *r.OrbID == e.orb.ID && r.ThreadID != nil && *r.ThreadID == "T-a"
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}

// M21：网关记着每条 runner 连接经哪把公钥认证。同值重复登记（崩溃自重启、幂等 agent.start）不动已有连接；同一线程登记
// 新公钥（新一代监督器）时经旧公钥认证的连接被关闭，再连只能用新公钥认证；注销线程 / 整台 runner 时已建立的连接关闭。
func TestGatewayRunnerKeyConnsClosed(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	e.orb = db.Orb{ID: "orb-gw", Kind: "runner"}
	e.orbs.put(e.orb)
	e.runnerThreadRow(t, e.orb.ID, "T-a", nil)
	ka, pubA := threadSigner(t)
	ka2, pubA2 := threadSigner(t)
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA))
	closed := func(c *ssh.Client) bool {
		t.Helper()
		done := make(chan struct{})
		go func() { _ = c.Wait(); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}
	connects := func() int {
		n := 0
		for _, r := range e.auditRows(t) {
			if r.Kind == "connect" && r.Ok && r.ThreadID != nil && *r.ThreadID == "T-a" {
				n++
			}
		}
		return n
	}

	c1, _, err := e.connectKey(t, "web", ka)
	require.NoError(t, err)
	require.Equal(t, "1\n", runOn(t, c1, "echo 1"))
	// 崩溃自重启：同一把公钥再登记，连接保留
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA))
	require.Equal(t, "2\n", runOn(t, c1, "echo 2"))

	// 新一代监督器：旧公钥的连接被关闭，旧公钥再连被拒，新公钥认证成功
	before := connects()
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA2))
	require.True(t, closed(c1), "经旧公钥认证的连接被关闭")
	_, _, err = e.connectKey(t, "web", ka)
	require.Error(t, err)
	c2, _, err := e.connectKey(t, "web", ka2)
	require.NoError(t, err)
	require.Equal(t, "3\n", runOn(t, c2, "echo 3"))
	require.Equal(t, before+1, connects(), "网关以新公钥认证了一次")

	// 注销线程：已建立的连接关闭
	e.svc.DropThreadKey("T-a")
	require.True(t, closed(c2))

	// 注销整台 runner：同样关闭
	require.NoError(t, e.svc.RegisterThreadKey("T-a", e.orb.ID, pubA2))
	c3, _, err := e.connectKey(t, "web", ka2)
	require.NoError(t, err)
	e.svc.DropOrbKeys(e.orb.ID)
	require.True(t, closed(c3))
	e.svc.keys.mu.Lock()
	require.Empty(t, e.svc.keys.conns, "连接退出后从公钥下摘掉")
	e.svc.keys.mu.Unlock()
}

// M21：runner 线程项目层的 ssh.json 只含该线程可用的本项目主机，端口引用 ORYN_SSH_PORT；orb 线程、无项目线程没有。
func TestRunnerProjectSSH(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()
	pa, pb := e.project(t, "prj_a"), e.project(t, "prj_b")
	e.fakeHost(t, "pa-only", e.key, e.target, func(in *apigen.SSHHostCreate) { in.ProjectID = &pa; in.Description = new("db") })
	e.fakeHost(t, "pa-auto", e.key, e.target, func(in *apigen.SSHHostCreate) { in.ProjectID = &pa; in.AllowAutomation = new(true) })
	e.fakeHost(t, "pb-only", e.key, e.target, func(in *apigen.SSHHostCreate) { in.ProjectID = &pb })
	runner := db.Orb{ID: "orb-r", Kind: "runner"}
	e.orbs.put(runner)
	e.runnerThreadRow(t, runner.ID, "T-a", &pa)
	e.runnerThreadRow(t, runner.ID, "T-none", nil)

	b, err := e.svc.RunnerProjectSSH(ctx, "T-a")
	require.NoError(t, err)
	var got struct {
		Hosts map[string]map[string]any `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Hosts, 2, "全局 web 与别的项目的主机都不在项目层里")
	addr, port := e.target.Addr()
	require.Equal(t, map[string]any{
		"host": "127.0.0.1", "port": RunnerSSHPortRef, "username": "pa-only",
		"description": fmt.Sprintf("tester@%s:%d — db", addr, port),
	}, got.Hosts["pa-only"])

	runner.Automation = true
	e.orbs.put(runner)
	b, err = e.svc.RunnerProjectSSH(ctx, "T-a")
	require.NoError(t, err)
	got.Hosts = nil
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Hosts, 1)
	require.Contains(t, got.Hosts, "pa-auto")

	b, err = e.svc.RunnerProjectSSH(ctx, "T-none")
	require.NoError(t, err)
	require.Nil(t, b)
	e.orbs.put(db.Orb{ID: "orb-gw2", Kind: "orb"})
	e.runnerThreadRow(t, "orb-gw2", "T-orb", &pa)
	e.orbs.put(db.Orb{ID: "orb-gw2", Kind: "orb", ProjectID: &pa})
	b, err = e.svc.RunnerProjectSSH(ctx, "T-orb")
	require.NoError(t, err)
	require.Nil(t, b, "orb 的项目主机经 setup / omphome.apply 下发")
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
