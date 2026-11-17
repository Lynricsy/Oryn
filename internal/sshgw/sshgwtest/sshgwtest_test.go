package sshgwtest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func addr(f *FakeSSHD) string {
	h, p := f.Addr()
	return net.JoinHostPort(h, strconv.Itoa(p))
}

func dial(t *testing.T, f *FakeSSHD, user string, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", addr(f), &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.FixedHostKey(f.HostPublicKey()),
		Timeout:         5 * time.Second,
	})
}

func keyClient(t *testing.T) (ssh.Signer, *FakeSSHD, *ssh.Client) {
	t.Helper()
	key := RandomSigner(t)
	f := NewFakeSSHD(t, Options{AuthorizedKeys: []ssh.PublicKey{key.PublicKey()}, AllowDirectTCPIP: true})
	c, err := dial(t, f, "alice", ssh.PublicKeys(key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return key, f, c
}

func TestKeyLoginExec(t *testing.T) {
	_, f, c := keyClient(t)

	sess, err := c.NewSession()
	require.NoError(t, err)
	var out, errb bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &errb
	err = sess.Run("echo out; echo err >&2; exit 3")
	var ee *ssh.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 3, ee.ExitStatus())
	assert.Equal(t, "out\n", out.String())
	assert.Equal(t, "err\n", errb.String())

	// 标准输入接到进程。
	sess, err = c.NewSession()
	require.NoError(t, err)
	sess.Stdin = strings.NewReader("abc")
	got, err := sess.Output("cat")
	require.NoError(t, err)
	assert.Equal(t, "abc", string(got))

	// 全局请求（保活）得到 false 回复。
	ok, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
	require.NoError(t, err)
	assert.False(t, ok)

	// 未授权的钥匙被拒。
	_, err = dial(t, f, "alice", ssh.PublicKeys(RandomSigner(t)))
	require.Error(t, err)
}

func TestShell(t *testing.T) {
	_, _, c := keyClient(t)
	sess, err := c.NewSession()
	require.NoError(t, err)
	require.NoError(t, sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}))
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stdin = strings.NewReader("echo hi\necho e >&2\nexit 4\n")
	require.NoError(t, sess.Shell())
	require.NoError(t, sess.WindowChange(40, 100))
	err = sess.Wait()
	var ee *ssh.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 4, ee.ExitStatus())
	assert.Equal(t, "hi\ne\n", out.String())
}

func TestSignalKillsCommand(t *testing.T) {
	_, _, c := keyClient(t)
	sess, err := c.NewSession()
	require.NoError(t, err)
	require.NoError(t, sess.Start("sleep 30"))
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, sess.Signal(ssh.SIGKILL))
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL 后会话没有结束")
	}
}

func TestPasswordLogin(t *testing.T) {
	f := NewFakeSSHD(t, Options{Password: "s3cret", User: "bob"})
	c, err := dial(t, f, "bob", ssh.Password("s3cret"))
	require.NoError(t, err)
	out, err := mustSession(t, c).Output("echo ok")
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(out))
	_ = c.Close()

	_, err = dial(t, f, "bob", ssh.Password("wrong"))
	require.Error(t, err)
	_, err = dial(t, f, "mallory", ssh.Password("s3cret"))
	require.Error(t, err)
}

func TestKeyboardInteractiveLogin(t *testing.T) {
	f := NewFakeSSHD(t, Options{Password: "s3cret", KeyboardInteractive: true})
	answer := func(pw string) ssh.AuthMethod {
		return ssh.KeyboardInteractive(func(_, _ string, qs []string, echos []bool) ([]string, error) {
			if len(qs) != 1 || echos[0] {
				return nil, errors.New("意外的提示")
			}
			return []string{pw}, nil
		})
	}
	c, err := dial(t, f, "carol", answer("s3cret"))
	require.NoError(t, err)
	_ = c.Close()

	_, err = dial(t, f, "carol", answer("wrong"))
	require.Error(t, err)
	// 不提供 password 方法。
	_, err = dial(t, f, "carol", ssh.Password("s3cret"))
	require.Error(t, err)
}

func TestSFTP(t *testing.T) {
	_, _, c := keyClient(t)
	sc, err := sftp.NewClient(c)
	require.NoError(t, err)
	defer sc.Close()

	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	w, err := sc.Create(p)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	r, err := sc.Open(p)
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, "hello", string(got))

	entries, err := sc.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a.txt", entries[0].Name())
}

func TestDirectTCPIP(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	_, f, c := keyClient(t)
	conn, err := c.Dial("tcp", echo.Addr().String())
	require.NoError(t, err)
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))
	require.NoError(t, conn.Close())
	assert.Equal(t, 1, f.DirectTCPIPCount())

	// 未开启时被拒。
	key := RandomSigner(t)
	g := NewFakeSSHD(t, Options{AuthorizedKeys: []ssh.PublicKey{key.PublicKey()}})
	gc, err := dial(t, g, "x", ssh.PublicKeys(key))
	require.NoError(t, err)
	defer gc.Close()
	_, err = gc.Dial("tcp", echo.Addr().String())
	var oe *ssh.OpenChannelError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, ssh.Prohibited, oe.Reason)
	assert.Equal(t, 0, g.DirectTCPIPCount())
}

func TestSilentHandshakeHangs(t *testing.T) {
	f := NewFakeSSHD(t, Options{Silent: true, Password: "x"})
	nc, err := net.Dial("tcp", addr(f))
	require.NoError(t, err)
	defer nc.Close()
	require.NoError(t, nc.SetDeadline(time.Now().Add(300*time.Millisecond)))
	start := time.Now()
	_, _, _, err = ssh.NewClientConn(nc, addr(f), &ssh.ClientConfig{
		User:            "x",
		Auth:            []ssh.AuthMethod{ssh.Password("x")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestSetHostKey(t *testing.T) {
	key := RandomSigner(t)
	f := NewFakeSSHD(t, Options{AuthorizedKeys: []ssh.PublicKey{key.PublicKey()}})
	old := f.HostPublicKey()
	assert.Equal(t, ssh.FingerprintSHA256(old), f.HostKeyFingerprint())
	c, err := dial(t, f, "x", ssh.PublicKeys(key))
	require.NoError(t, err)
	defer c.Close()

	f.SetHostKey(RandomSigner(t))
	assert.NotEqual(t, ssh.FingerprintSHA256(old), f.HostKeyFingerprint())
	// 用旧密钥固定的客户端连不上。
	_, err = ssh.Dial("tcp", addr(f), &ssh.ClientConfig{
		User: "x", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(old),
	})
	require.Error(t, err)
	// 已有连接不受影响。
	out, err := mustSession(t, c).Output("echo still")
	require.NoError(t, err)
	assert.Equal(t, "still\n", string(out))
}

func TestSendForwardedTCPIP(t *testing.T) {
	_, f, c := keyClient(t)
	// ssh.Client 默认拒绝未登记的通道类型。
	err := f.SendForwardedTCPIP()
	var oe *ssh.OpenChannelError
	require.ErrorAs(t, err, &oe)

	// 登记之后被接受。
	chans := c.HandleChannelOpen("forwarded-tcpip")
	go func() {
		for nc := range chans {
			ch, reqs, err := nc.Accept()
			if err == nil {
				go ssh.DiscardRequests(reqs)
				_ = ch.Close()
			}
		}
	}()
	require.NoError(t, f.SendForwardedTCPIP())
}

func TestCloseIdempotent(t *testing.T) {
	_, f, _ := keyClient(t)
	f.Close()
	f.Close()
	_, err := net.DialTimeout("tcp", addr(f), time.Second)
	require.Error(t, err)
}

func mustSession(t *testing.T, c *ssh.Client) *ssh.Session {
	t.Helper()
	s, err := c.NewSession()
	require.NoError(t, err)
	return s
}
