package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var keyNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]{0,63}$`)

// KeyInput 是新建钥匙的输入。
type KeyInput struct {
	Name       string
	Mode       string // generate | import
	PrivateKey string
	Passphrase string
}

func keyAAD(id string) string { return "ssh/key/" + id }

func validKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !keyNameRe.MatchString(name) {
		return "", invalid("钥匙名只能含字母、数字、空格与 _ . -，最长 64")
	}
	return name, nil
}

// authorizedLine 返回 authorized_keys 一行（注释为钥匙名）。
func authorizedLine(pub ssh.PublicKey, name string) string {
	return trimNL(string(ssh.MarshalAuthorizedKey(pub))) + " " + name
}

// CreateKey 生成 ed25519 钥匙或导入私钥。导入的私钥一律转存为无口令 OpenSSH 格式，口令不落库。
func (s *Service) CreateKey(ctx context.Context, in KeyInput) (apigen.SSHKey, error) {
	name, err := validKeyName(in.Name)
	if err != nil {
		return apigen.SSHKey{}, err
	}
	var raw any
	switch in.Mode {
	case string(apigen.SSHKeyCreateGenerate):
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return apigen.SSHKey{}, err
		}
		raw = priv
	case string(apigen.SSHKeyCreateImport):
		raw, err = parseImported([]byte(in.PrivateKey), []byte(in.Passphrase))
		if err != nil {
			return apigen.SSHKey{}, err
		}
	default:
		return apigen.SSHKey{}, invalid("mode 只能是 generate 或 import")
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return apigen.SSHKey{}, invalid("不支持这种私钥类型")
	}
	block, err := ssh.MarshalPrivateKey(raw, name)
	if err != nil {
		return apigen.SSHKey{}, invalid("不支持这种私钥类型")
	}
	plain := pem.EncodeToMemory(block)
	clear(block.Bytes)
	id := ids.SSHKey()
	enc, err := s.cfg.Box.Seal(keyAAD(id), plain)
	clear(plain)
	if err != nil {
		return apigen.SSHKey{}, err
	}
	pub := signer.PublicKey()
	row := db.InsertSshKeyParams{
		ID: id, Name: name, KeyType: pub.Type(), PublicKey: authorizedLine(pub, name),
		Fingerprint: ssh.FingerprintSHA256(pub), PrivateKeyEnc: enc, CreatedAt: s.now(),
	}
	if err := s.st.Q.InsertSshKey(ctx, row); err != nil {
		if isUniqueViolation(err) {
			return apigen.SSHKey{}, conflict("钥匙名已存在")
		}
		return apigen.SSHKey{}, err
	}
	s.cfg.Changed()
	return apigen.SSHKey{
		ID: id, Name: name, Type: row.KeyType, PublicKey: row.PublicKey, Fingerprint: row.Fingerprint,
		HostCount: 0, CreatedAt: row.CreatedAt,
	}, nil
}

// parseImported 解析导入的私钥；带口令的私钥要求一并给出口令。
func parseImported(pemBytes, passphrase []byte) (any, error) {
	defer clear(passphrase)
	if len(strings.TrimSpace(string(pemBytes))) == 0 {
		return nil, invalid("请粘贴私钥")
	}
	raw, err := ssh.ParseRawPrivateKey(pemBytes)
	if err == nil {
		return raw, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, invalid("无法解析私钥：%v", err)
	}
	if len(passphrase) == 0 {
		return nil, invalid("这把私钥有口令，请一并填写")
	}
	raw, err = ssh.ParseRawPrivateKeyWithPassphrase(pemBytes, passphrase)
	if errors.Is(err, x509.IncorrectPasswordError) {
		return nil, invalid("口令不对")
	}
	if err != nil {
		return nil, invalid("无法解开私钥：%v", err)
	}
	return raw, nil
}

// ListKeys 列出钥匙（不含私钥），每项带使用它的主机数。
func (s *Service) ListKeys(ctx context.Context) (apigen.SSHKeyList, error) {
	rows, err := s.st.Q.ListSshKeys(ctx)
	if err != nil {
		return apigen.SSHKeyList{}, err
	}
	out := apigen.SSHKeyList{Keys: make([]apigen.SSHKey, 0, len(rows))}
	for _, r := range rows {
		n, err := s.st.Q.CountSshHostsByKey(ctx, &r.ID)
		if err != nil {
			return apigen.SSHKeyList{}, err
		}
		out.Keys = append(out.Keys, apigen.SSHKey{
			ID: r.ID, Name: r.Name, Type: r.KeyType, PublicKey: r.PublicKey, Fingerprint: r.Fingerprint,
			HostCount: int(n), CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

func (s *Service) keyView(ctx context.Context, k db.SshKey) (apigen.SSHKey, error) {
	n, err := s.st.Q.CountSshHostsByKey(ctx, &k.ID)
	if err != nil {
		return apigen.SSHKey{}, err
	}
	return apigen.SSHKey{
		ID: k.ID, Name: k.Name, Type: k.KeyType, PublicKey: k.PublicKey, Fingerprint: k.Fingerprint,
		HostCount: int(n), CreatedAt: k.CreatedAt,
	}, nil
}

// RenameKey 改名，并同步改写公钥行的注释。
func (s *Service) RenameKey(ctx context.Context, id, name string) (apigen.SSHKey, error) {
	name, err := validKeyName(name)
	if err != nil {
		return apigen.SSHKey{}, err
	}
	k, err := s.st.Q.GetSshKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.SSHKey{}, notFound("钥匙不存在")
	}
	if err != nil {
		return apigen.SSHKey{}, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil {
		return apigen.SSHKey{}, err
	}
	k.Name, k.PublicKey = name, authorizedLine(pub, name)
	if _, err := s.st.Q.RenameSshKey(ctx, db.RenameSshKeyParams{Name: k.Name, PublicKey: k.PublicKey, ID: id}); err != nil {
		if isUniqueViolation(err) {
			return apigen.SSHKey{}, conflict("钥匙名已存在")
		}
		return apigen.SSHKey{}, err
	}
	s.cfg.Changed()
	return s.keyView(ctx, k)
}

// DeleteKey 删除钥匙；仍有主机使用时拒绝。
func (s *Service) DeleteKey(ctx context.Context, id string) error {
	n, err := s.st.Q.CountSshHostsByKey(ctx, &id)
	if err != nil {
		return err
	}
	if n > 0 {
		return conflict("钥匙正被 %d 台主机使用", n)
	}
	affected, err := s.st.Q.DeleteSshKey(ctx, id)
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound("钥匙不存在")
	}
	s.cfg.Changed()
	return nil
}

// signerFor 解开钥匙私钥。
func (s *Service) signerFor(ctx context.Context, keyID string) (ssh.Signer, error) {
	k, err := s.st.Q.GetSshKey(ctx, keyID)
	if err != nil {
		return nil, err
	}
	plain, err := s.cfg.Box.Open(keyAAD(k.ID), k.PrivateKeyEnc)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return ssh.ParsePrivateKey(plain)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
