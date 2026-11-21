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
	ID   string
	Name string
	Mode string
	// ProjectID 是项目级条目所属的项目；全局密钥为空串。
	ProjectID string
	// Secret 为假 = 项目级的普通环境变量（值可读）；全局密钥恒为真。
	Secret    bool
	Hosts     []string
	CreatedAt int64
	UpdatedAt int64
}

// ProjectEntry 是项目级条目；Value 只对不保密的条目填写（解密失败时为 nil 并记日志）。
type ProjectEntry struct {
	Meta
	Value *string
}

// Vault 是基于 secrets 表的密钥库；值以 Box 加密。附加数据：全局行 = 密钥名；
// 项目级条目 = ProjectAAD（project:<项目 id>:<名称>，不保密的再加 ":plain"）。
//
// 它同时实现 agentcfg.Resolver（Lookup）与 egress.Resolver 的 SecretByID / GitToken；这三者与占位符只解析全局行，
// 项目级条目经 ProjectEnv / ProjectGitToken 取用。
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
		old, err := q.GetSecretByName(ctx, name)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		hs := normalizeHosts(hosts)
		if hosts == nil && found {
			hs = normalizeHosts(decodeHosts(old.Hosts))
		}
		hj, _ := json.Marshal(hs)
		if found {
			err = q.UpdateSecret(ctx, db.UpdateSecretParams{
				ID: old.ID, Mode: mode, Secret: true, ValueEnc: sealed, Hosts: string(hj), UpdatedAt: now,
			})
		} else {
			err = q.InsertSecret(ctx, db.InsertSecretParams{
				ID: ids.New("sec"), Name: name, Mode: mode, Secret: true, ValueEnc: sealed, Hosts: string(hj), CreatedAt: now, UpdatedAt: now,
			})
		}
		if err != nil {
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

// metaOf 构造全局密钥的元数据（全局行恒为保密）。
func metaOf(id, name, mode, hosts string, created, updated int64) Meta {
	return Meta{ID: id, Name: name, Mode: mode, Secret: true, Hosts: decodeHosts(hosts), CreatedAt: created, UpdatedAt: updated}
}

// projectMetaOf 构造项目级条目的元数据。
func projectMetaOf(row db.Secret) Meta {
	m := Meta{ID: row.ID, Name: row.Name, Mode: row.Mode, Secret: row.Secret, Hosts: decodeHosts(row.Hosts), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.ProjectID != nil {
		m.ProjectID = *row.ProjectID
	}
	return m
}

// aadOf 是一行密文的附加数据：全局行 = 名称，项目行 = ProjectAAD。
func aadOf(row db.Secret) string {
	if row.ProjectID == nil {
		return row.Name
	}
	return ProjectAAD(*row.ProjectID, row.Name, row.Secret)
}

func (v *Vault) open(row db.Secret) (string, error) {
	b, err := v.box.Open(aadOf(row), row.ValueEnc)
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

// All 返回全部保密值的明文（键 → 值），即「已知密钥集合」：公开分享的密钥扫描、runner 无密钥校验与主人记忆脱敏共用。
// 全局密钥的键是名称；项目级保密条目的键是 <项目名>:<名称>（项目名唯一，全局名不含 ':'，不会撞键）。
// 不保密的项目值不在其中。解密失败的跳过并记日志。
func (v *Vault) All(ctx context.Context) (map[string]string, error) {
	rows, err := v.st.Q.ListSealedSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		key, aad := r.Name, r.Name
		if r.ProjectID != nil {
			prj := *r.ProjectID
			if r.ProjectName != nil {
				prj = *r.ProjectName
			}
			key = prj + ":" + r.Name
			aad = ProjectAAD(*r.ProjectID, r.Name, true)
		}
		b, err := v.box.Open(aad, r.ValueEnc)
		if err != nil {
			slog.Warn("密钥扫描：解密失败，跳过", "name", key, "err", err)
			continue
		}
		out[key] = string(b)
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

// gitHostOf 从密钥名 git/<host>/token 取出主机。
func gitHostOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "git/")
	if !ok {
		return "", false
	}
	host, ok := strings.CutSuffix(rest, "/token")
	if !ok || host == "" || strings.Contains(host, "/") {
		return "", false
	}
	return host, true
}

// GitHosts 返回配置了 git/<host>/token 密钥的主机列表（写 git insteadOf 用）。
func (v *Vault) GitHosts() ([]string, error) {
	ms, err := v.List(context.Background())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range ms {
		if host, ok := gitHostOf(m.Name); ok {
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

// PutProject 写入或覆盖项目级条目（阶段 ④；覆盖时保留原 id）。secret 为假的 env 条目之后可经 ListProject 读回值。
// 项目不存在（含写入期间被删除）时返回 ErrNoProject。
func (v *Vault) PutProject(ctx context.Context, projectID, name, mode, value string, secret bool) (Meta, error) {
	if err := ValidProjectEntry(name, mode, secret); err != nil {
		return Meta{}, err
	}
	if value == "" {
		return Meta{}, errors.New("值不能为空")
	}
	sealed, err := v.box.Seal(ProjectAAD(projectID, name, secret), []byte(value))
	if err != nil {
		return Meta{}, err
	}
	now := time.Now().UnixMilli()
	key := db.GetProjectSecretParams{ProjectID: projectID, Name: name}
	var row db.Secret
	err = v.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetProject(ctx, projectID); errors.Is(err, sql.ErrNoRows) {
			return ErrNoProject
		} else if err != nil {
			return err
		}
		old, err := q.GetProjectSecret(ctx, key)
		switch {
		case err == nil:
			err = q.UpdateSecret(ctx, db.UpdateSecretParams{
				ID: old.ID, Mode: mode, Secret: secret, ValueEnc: sealed, Hosts: old.Hosts, UpdatedAt: now,
			})
		case errors.Is(err, sql.ErrNoRows):
			err = q.InsertSecret(ctx, db.InsertSecretParams{
				ID: ids.New("sec"), ProjectID: &projectID, Name: name, Mode: mode, Secret: secret,
				ValueEnc: sealed, Hosts: "[]", CreatedAt: now, UpdatedAt: now,
			})
		}
		if err != nil {
			return err
		}
		row, err = q.GetProjectSecret(ctx, key)
		return err
	})
	if err != nil {
		return Meta{}, err
	}
	return projectMetaOf(row), nil
}

// ListProject 返回项目级条目（按名称排序）；只解密不保密的条目。
func (v *Vault) ListProject(ctx context.Context, projectID string) ([]ProjectEntry, error) {
	rows, err := v.st.Q.ListProjectSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectEntry, 0, len(rows))
	for _, r := range rows {
		e := ProjectEntry{Meta: projectMetaOf(r)}
		if !r.Secret {
			if val, err := v.open(r); err != nil {
				slog.Warn("解密项目环境变量失败", "project", projectID, "name", r.Name, "err", err)
			} else {
				e.Value = &val
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// DeleteProject 删除项目级条目；不存在时返回 false。
func (v *Vault) DeleteProject(ctx context.Context, projectID, name string) (bool, error) {
	n, err := v.st.Q.DeleteProjectSecret(ctx, db.DeleteProjectSecretParams{ProjectID: projectID, Name: name})
	return n > 0, err
}

// ProjectEnv 返回项目的 env 条目（保密与不保密都含；变量名 → 明文）；任一条解密失败即返回错误。
func (v *Vault) ProjectEnv(projectID string) (map[string]string, error) {
	rows, err := v.st.Q.ListProjectSecrets(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		if r.Mode != ModeEnv {
			continue
		}
		val, err := v.open(r)
		if err != nil {
			return nil, err
		}
		out[r.Name] = val
	}
	return out, nil
}

// ProjectGitToken 返回项目的 git/<host>/token：不存在时 found=false、err=nil；读库或解密失败返回 err，
// 调用方此时一律拒绝，不回落到（通常更宽的）全局令牌。
func (v *Vault) ProjectGitToken(projectID, host string) (tok string, found bool, err error) {
	row, err := v.st.Q.GetProjectSecret(context.Background(), db.GetProjectSecretParams{ProjectID: projectID, Name: GitTokenName(host)})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	tok, err = v.open(row)
	if err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// ProjectGitHosts 返回项目里有 git/<host>/token 条目的主机。
func (v *Vault) ProjectGitHosts(projectID string) ([]string, error) {
	rows, err := v.st.Q.ListProjectSecrets(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if host, ok := gitHostOf(r.Name); ok {
			out = append(out, host)
		}
	}
	return out, nil
}
