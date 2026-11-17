package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// authorizedKey 返回钥匙的公钥，供假目标机授权。
func authorizedKey(t *testing.T, k apigen.SSHKey) ssh.PublicKey {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	require.NoError(t, err)
	return pub
}

// fakeHost 在假目标机上登记一台钥匙登录的主机。
func (e *testEnv) fakeHost(t *testing.T, name string, k apigen.SSHKey, d *sshgwtest.FakeSSHD, mod ...func(*apigen.SSHHostCreate)) apigen.SSHHost {
	t.Helper()
	addr, port := d.Addr()
	in := apigen.SSHHostCreate{Name: name, Address: addr, Port: &port, Username: "tester", AuthType: apigen.SSHAuthKey, KeyID: &k.ID}
	for _, m := range mod {
		m(&in)
	}
	return e.mustHost(t, in)
}

func (e *testEnv) row(t *testing.T, id string) db.SshHost {
	t.Helper()
	h, err := e.st.Q.GetSshHost(context.Background(), id)
	require.NoError(t, err)
	return h
}

func runOn(t *testing.T, c *ssh.Client, cmd string) string {
	t.Helper()
	sess, err := c.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	require.NoError(t, err)
	return string(out)
}

func TestDialKeyPinsHostKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	d := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{AuthorizedKeys: []ssh.PublicKey{authorizedKey(t, k)}})
	h := e.fakeHost(t, "web", k, d)

	up, err := e.svc.dial(ctx, e.row(t, h.ID))
	require.NoError(t, err)
	require.Equal(t, "hi\n", runOn(t, up.client, "echo hi"))
	require.Equal(t, []string{h.ID}, up.hostIDs)
	up.Close()

	got, err := e.svc.GetHost(ctx, h.ID)
	require.NoError(t, err)
	require.NotNil(t, got.HostKey)
	require.Equal(t, d.HostKeyFingerprint(), got.HostKey.Fingerprint)

	// 指纹变了：拒绝，文案带两个指纹
	old := d.HostKeyFingerprint()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	d.SetHostKey(signer)
	_, err = e.svc.dial(ctx, e.row(t, h.ID))
	var mm *HostKeyMismatchError
	require.True(t, errors.As(err, &mm), "%v", err)
	require.Contains(t, err.Error(), old)
	require.Contains(t, err.Error(), d.HostKeyFingerprint())
	require.Contains(t, err.Error(), "指纹变了")

	// 重置后重新固定
	_, err = e.svc.ResetHostKey(ctx, h.ID)
	require.NoError(t, err)
	up, err = e.svc.dial(ctx, e.row(t, h.ID))
	require.NoError(t, err)
	up.Close()
	got, err = e.svc.GetHost(ctx, h.ID)
	require.NoError(t, err)
	require.Equal(t, d.HostKeyFingerprint(), got.HostKey.Fingerprint)
}

func TestDialPasswordAndKeyboardInteractive(t *testing.T) {
	for _, ki := range []bool{false, true} {
		t.Run(map[bool]string{false: "password", true: "keyboard-interactive"}[ki], func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			d := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{Password: "hunter2", KeyboardInteractive: ki})
			addr, port := d.Addr()
			h := e.mustHost(t, apigen.SSHHostCreate{
				Name: "pw", Address: addr, Port: &port, Username: "u", AuthType: apigen.SSHAuthPassword, Password: new("hunter2"),
			})
			up, err := e.svc.dial(ctx, e.row(t, h.ID))
			require.NoError(t, err)
			require.Equal(t, "ok\n", runOn(t, up.client, "echo ok"))
			up.Close()

			_, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Password: new("wrong")}})
			require.NoError(t, err)
			_, err = e.svc.dial(ctx, e.row(t, h.ID))
			require.Error(t, err)
			require.Contains(t, err.Error(), "登录 u@")
		})
	}
}

func TestDialThroughJumps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	auth := []ssh.PublicKey{authorizedKey(t, k)}
	b1 := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{AuthorizedKeys: auth, AllowDirectTCPIP: true})
	b2 := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{AuthorizedKeys: auth, AllowDirectTCPIP: true})
	target := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{AuthorizedKeys: auth})
	h1 := e.fakeHost(t, "b1", k, b1)
	h2 := e.fakeHost(t, "b2", k, b2, func(in *apigen.SSHHostCreate) { in.JumpHostID = &h1.ID })
	ht := e.fakeHost(t, "t", k, target, func(in *apigen.SSHHostCreate) { in.JumpHostID = &h2.ID })

	up, err := e.svc.dial(ctx, e.row(t, ht.ID))
	require.NoError(t, err)
	require.Equal(t, "deep\n", runOn(t, up.client, "echo deep"))
	require.Equal(t, []string{h1.ID, h2.ID, ht.ID}, up.hostIDs)
	require.Len(t, up.jumps, 2)
	up.Close()
	require.Equal(t, 1, b1.DirectTCPIPCount())
	require.Equal(t, 1, b2.DirectTCPIPCount())
	for _, id := range []string{h1.ID, h2.ID, ht.ID} {
		require.NotNil(t, e.row(t, id).HostKey, "链上每一跳都固定指纹")
	}

	// 数据在校验之后被改：跳板停用
	_, err = e.st.DB.ExecContext(ctx, `UPDATE ssh_hosts SET enabled = 0 WHERE id = ?`, h1.ID)
	require.NoError(t, err)
	_, err = e.svc.dial(ctx, e.row(t, ht.ID))
	require.ErrorContains(t, err, `跳板 "b1" 已停用`)

	// 跳板上出错时带前缀
	_, err = e.st.DB.ExecContext(ctx, `UPDATE ssh_hosts SET enabled = 1, port = 1 WHERE id = ?`, h1.ID)
	require.NoError(t, err)
	_, err = e.svc.dial(ctx, e.row(t, ht.ID))
	require.ErrorContains(t, err, `跳板 "b1"：连不上 127.0.0.1:1`)
}

func TestDialHandshakeTimeout(t *testing.T) {
	e := newEnv(t)
	k := e.key(t, "k")
	d := sshgwtest.NewFakeSSHD(t, sshgwtest.Options{Silent: true})
	h := e.fakeHost(t, "mute", k, d)
	start := time.Now()
	_, err := e.svc.dial(context.Background(), e.row(t, h.ID))
	require.ErrorContains(t, err, "连不上")
	require.ErrorContains(t, err, "握手超时")
	require.Less(t, time.Since(start), connectTimeout+2*time.Second)
}
