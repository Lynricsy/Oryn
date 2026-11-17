package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

)

func TestCreateKeyGenerate(t *testing.T) {
	e := newEnv(t)
	k := e.key(t, "my key")
	require.Equal(t, "ssh-ed25519", k.Type)
	require.True(t, strings.HasSuffix(k.PublicKey, " my key"), k.PublicKey)
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	require.NoError(t, err)
	require.Equal(t, "my key", comment)
	require.Equal(t, ssh.FingerprintSHA256(pub), k.Fingerprint)

	signer, err := e.svc.signerFor(context.Background(), k.ID)
	require.NoError(t, err)
	require.Equal(t, pub.Marshal(), signer.PublicKey().Marshal())

	_, err = e.svc.CreateKey(context.Background(), KeyInput{Name: "my key", Mode: "generate"})
	requireCode(t, err, "conflict", "钥匙名已存在")
	for _, bad := range []string{"", " ", ".x", "-x", "a/b", strings.Repeat("a", 65)} {
		_, err = e.svc.CreateKey(context.Background(), KeyInput{Name: bad, Mode: "generate"})
		requireCode(t, err, "invalid_request", "钥匙名")
	}
}

func TestImportKeyWithPassphrase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "orig", []byte("s3cret"))
	require.NoError(t, err)
	pemText := string(pem.EncodeToMemory(block))

	_, err = e.svc.CreateKey(ctx, KeyInput{Name: "imp", Mode: "import", PrivateKey: pemText})
	requireCode(t, err, "invalid_request", "这把私钥有口令")
	_, err = e.svc.CreateKey(ctx, KeyInput{Name: "imp", Mode: "import", PrivateKey: pemText, Passphrase: "nope"})
	requireCode(t, err, "invalid_request", "口令不对")
	_, err = e.svc.CreateKey(ctx, KeyInput{Name: "imp", Mode: "import", PrivateKey: "garbage"})
	requireCode(t, err, "invalid_request", "")

	k, err := e.svc.CreateKey(ctx, KeyInput{Name: "imp", Mode: "import", PrivateKey: pemText, Passphrase: "s3cret"})
	require.NoError(t, err)
	// 落库的是无口令格式：直接解析即可
	row, err := e.st.Q.GetSshKey(ctx, k.ID)
	require.NoError(t, err)
	plain, err := e.svc.cfg.Box.Open(keyAAD(k.ID), row.PrivateKeyEnc)
	require.NoError(t, err)
	signer, err := ssh.ParsePrivateKey(plain)
	require.NoError(t, err)
	want, err := ssh.NewPublicKey(priv.Public())
	require.NoError(t, err)
	require.Equal(t, want.Marshal(), signer.PublicKey().Marshal())
}

func TestRenameAndDeleteKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "a")
	other := e.key(t, "b")
	_, err := e.svc.RenameKey(ctx, k.ID, "b")
	requireCode(t, err, "conflict", "")
	r, err := e.svc.RenameKey(ctx, k.ID, "renamed")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(r.PublicKey, " renamed"))
	require.Equal(t, k.Fingerprint, r.Fingerprint)

	e.mustHost(t, hostIn("web", k.ID))
	err = e.svc.DeleteKey(ctx, k.ID)
	requireCode(t, err, "conflict", "钥匙正被 1 台主机使用")
	list, err := e.svc.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, list.Keys, 2)
	require.Equal(t, "b", list.Keys[0].Name)
	require.Equal(t, 0, list.Keys[0].HostCount)
	require.Equal(t, 1, list.Keys[1].HostCount)

	require.NoError(t, e.svc.DeleteKey(ctx, other.ID))
	requireCode(t, e.svc.DeleteKey(ctx, other.ID), "not_found", "")
}
