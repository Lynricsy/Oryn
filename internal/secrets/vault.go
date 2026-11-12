package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Meta 是密钥元数据（不含值）。
type Meta struct {
	ID        string
	Name      string
	Mode      string
	Hosts     []string
	CreatedAt int64
	UpdatedAt int64
}

// Vault 是基于 secrets 表的密钥库；值以 Box 加密（附加数据 = 密钥名）。
//
// 它同时实现 agentcfg.Resolver（Lookup）与 egress.Resolver 的 SecretByID / GitToken。
type Vault struct {
	st  *store.Store
	box *Box
}

// NewVault 构造密钥库。
func NewVault(st *store.Store, box *Box) *Vault { return &Vault{st: st, box: box} }

// GitTokenName 是 git 主机访问令牌的密钥名。
func GitTokenName(host string) string { return "git/" + strings.ToLower(host) + "/token" }

func normalizeHosts(hosts []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

func decodeHosts(s string) []string {
	var hs []string
	_ = json.Unmarshal([]byte(s), &hs)
	return hs
}

// Put 写入或覆盖密钥（覆盖时保留原 id，已下发的占位符继续有效）。
// hosts 为 nil 时保留已有记录的 hosts（新建则为空）；传空切片表示清空。
func (v *Vault) Put(ctx context.Context, name, mode, value string, hosts []string) (Meta, error) {
	if err := ValidName(name, mode); err != nil {
		return Meta{}, err
	}
	if value == "" {
		return Meta{}, errors.New("密钥值不能为空")
	}
	sealed, err := v.box.Seal(name, []byte(value))
	if err != nil {
		return Meta{}, err
	}
	now := time.Now().UnixMilli()
	var row db.Secret
	err = v.st.Tx(ctx, func(q *db.Queries) error {
		hs := normalizeHosts(hosts)
		if hosts == nil {
			old, err := q.GetSecretByName(ctx, name)
			switch {
			case err == nil:
				hs = normalizeHosts(decodeHosts(old.Hosts))
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		hj, _ := json.Marshal(hs)
		if err := q.UpsertSecret(ctx, db.UpsertSecretParams{
			ID: ids.New("sec"), Name: name, Mode: mode, ValueEnc: sealed, Hosts: string(hj), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		row, err = q.GetSecretByName(ctx, name)
		return err
	})
	if err != nil {
		return Meta{}, err
	}
	return metaOf(row.ID, row.Name, row.Mode, row.Hosts, row.CreatedAt, row.UpdatedAt), nil
}

func metaOf(id, name, mode, hosts string, created, updated int64) Meta {
	return Meta{ID: id, Name: name, Mode: mode, Hosts: decodeHosts(hosts), CreatedAt: created, UpdatedAt: updated}
}

func (v *Vault) open(row db.Secret) (string, error) {
	b, err := v.box.Open(row.Name, row.ValueEnc)
	if err != nil {
		return "", fmt.Errorf("解密密钥 %s: %w", row.Name, err)
	}
	return string(b), nil
}

// Get 按名称读取密钥值；不存在时 ok=false。
func (v *Vault) Get(ctx context.Context, name string) (string, Meta, bool, error) {
	row, err := v.st.Q.GetSecretByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Meta{}, false, nil
	}
	if err != nil {
		return "", Meta{}, false, err
	}
	val, err := v.open(row)
	if err != nil {
		return "", Meta{}, false, err
	}
	return val, metaOf(row.ID, row.Name, row.Mode, row.Hosts, row.CreatedAt, row.UpdatedAt), true, nil
}

// List 返回全部密钥的元数据（按名称排序，不解密）。
func (v *Vault) List(ctx context.Context) ([]Meta, error) {
	rows, err := v.st.Q.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(rows))
	for _, r := range rows {
		out = append(out, metaOf(r.ID, r.Name, r.Mode, r.Hosts, r.CreatedAt, r.UpdatedAt))
	}
	return out, nil
}

// Delete 删除密钥；不存在时返回 false。
func (v *Vault) Delete(ctx context.Context, name string) (bool, error) {
	n, err := v.st.Q.DeleteSecretByName(ctx, name)
	return n > 0, err
}

// Lookup 实现 agentcfg.Resolver：按名称返回 id 与明文。
func (v *Vault) Lookup(name string) (string, string, bool) {
	val, m, ok, err := v.Get(context.Background(), name)
	if err != nil {
		slog.Warn("读取密钥失败", "name", name, "err", err)
		return "", "", false
	}
	return m.ID, val, ok
}

// SecretByID 实现 egress.Resolver：按 id 返回明文与允许的上游主机。
func (v *Vault) SecretByID(id string) (string, []string, bool) {
	row, err := v.st.Q.GetSecret(context.Background(), id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("读取密钥失败", "id", id, "err", err)
		}
		return "", nil, false
	}
	val, err := v.open(row)
	if err != nil {
		slog.Warn("解密密钥失败", "id", id, "err", err)
		return "", nil, false
	}
	return val, decodeHosts(row.Hosts), true
}

// GitToken 实现 egress.Resolver：返回密钥 git/<host>/token。
func (v *Vault) GitToken(host string) (string, bool) {
	_, val, ok := v.Lookup(GitTokenName(host))
	return val, ok
}

// GitHosts 返回配置了 git/<host>/token 密钥的主机列表（写 git insteadOf 用）。
func (v *Vault) GitHosts() ([]string, error) {
	ms, err := v.List(context.Background())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range ms {
		rest, ok := strings.CutPrefix(m.Name, "git/")
		if !ok {
			continue
		}
		if host, ok := strings.CutSuffix(rest, "/token"); ok && host != "" && !strings.Contains(host, "/") {
			out = append(out, host)
		}
	}
	return out, nil
}

// EnvSecrets 返回 mode=env 的全部密钥（变量名 = 密钥名 → 明文），注入 agent 进程。
func (v *Vault) EnvSecrets() (map[string]string, error) {
	ctx := context.Background()
	ms, err := v.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, m := range ms {
		if m.Mode != ModeEnv {
			continue
		}
		val, _, ok, err := v.Get(ctx, m.Name)
		if err != nil {
			return nil, err
		}
		if ok {
			out[m.Name] = val
		}
	}
	return out, nil
}
