package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestProjectSecretsMigration 覆盖 0050（M24 阶段 ④）：重建 secrets 后全局行的 id 与密文原样保留、默认保密；
// 两条部分唯一索引、保密位的 CHECK、删项目级联；带作用域的快照戳；Down 丢掉项目行并恢复 UNIQUE(name)，再 Up 照常。
func TestProjectSecretsMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	exec := func(q string, args ...any) error {
		_, err := s.DB.ExecContext(ctx, q, args...)
		return err
	}
	ins := func(id, project, name, mode string, secret bool) error {
		var pid *string
		if project != "" {
			pid = &project
		}
		return s.Q.InsertSecret(ctx, db.InsertSecretParams{ID: id, ProjectID: pid, Name: name, Mode: mode, Secret: secret,
			ValueEnc: []byte(id), Hosts: "[]", CreatedAt: 1, UpdatedAt: 2})
	}

	downTo(ctx, t, p, 49)
	require.NoError(t, exec(`INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
		VALUES ('sec_A', 'FOO', 'env', x'00', '[]', 1, 1), ('sec_B', 'git/x.example/token', 'proxy', x'0102', '["x.example"]', 1, 3)`))
	_, err := p.Up(ctx)
	require.NoError(t, err)

	type row struct {
		ID, Hosts string
		Enc       []byte
		Secret    bool
		Project   *string
	}
	// cols 是 secret 与 project_id 两列的取法：Down 之后表里没有这两列，用常量代替。
	readRows := func(cols string) []row {
		rs, err := s.DB.QueryContext(ctx, `SELECT id, hosts, value_enc, `+cols+` FROM secrets ORDER BY id`)
		require.NoError(t, err)
		defer rs.Close()
		var out []row
		for rs.Next() {
			var r row
			require.NoError(t, rs.Scan(&r.ID, &r.Hosts, &r.Enc, &r.Secret, &r.Project))
			out = append(out, r)
		}
		require.NoError(t, rs.Err())
		return out
	}
	globals := []row{
		{ID: "sec_A", Hosts: "[]", Enc: []byte{0}, Secret: true},
		{ID: "sec_B", Hosts: `["x.example"]`, Enc: []byte{1, 2}, Secret: true},
	}
	assert.Equal(t, globals, readRows("secret, project_id"), "全局行原样搬回，默认保密")

	for _, q := range []string{
		`INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES ('prj_a', 'a', 0, 1, 1)`,
		`INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES ('prj_b', 'b', 0, 1, 1)`,
	} {
		require.NoError(t, exec(q))
	}
	require.Error(t, ins("sec_dup", "", "FOO", "env", true), "全局同名")
	require.NoError(t, ins("sec_pa", "prj_a", "FOO", "env", false), "项目与全局同名")
	require.Error(t, ins("sec_pa2", "prj_a", "FOO", "env", true), "同项目同名")
	require.NoError(t, ins("sec_pb", "prj_b", "FOO", "env", true), "两个项目同名")
	require.NoError(t, ins("sec_pt", "prj_a", "git/x.example/token", "proxy", true))
	require.Error(t, ins("sec_plain", "", "BAR", "env", false), "全局行必须保密")
	require.Error(t, ins("sec_pp", "prj_a", "git/y.example/token", "proxy", false), "proxy 条目必须保密")

	// 全局查询只看全局行
	g, err := s.Q.GetSecretByName(ctx, "FOO")
	require.NoError(t, err)
	assert.Equal(t, "sec_A", g.ID)
	_, err = s.Q.GetSecret(ctx, "sec_pa")
	require.Error(t, err, "占位符解析不到项目行")
	list, err := s.Q.ListSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// 快照戳：全局 env 与本项目的 env 条目（带 project: 前缀），不含别的项目、不含项目的 proxy 条目
	names := func(pid string) []string {
		st, err := s.Q.ListConfigSecretStamps(ctx, pid)
		require.NoError(t, err)
		var out []string
		for _, r := range st {
			out = append(out, r.Name)
		}
		return out
	}
	assert.Equal(t, []string{"FOO"}, names(""))
	assert.Equal(t, []string{"FOO", "project:FOO"}, names("prj_a"))
	assert.Equal(t, []string{"FOO", "project:FOO"}, names("prj_b"))
	sealed, err := s.Q.ListSealedSecrets(ctx)
	require.NoError(t, err)
	var sealedIDs []string
	for _, r := range sealed {
		sealedIDs = append(sealedIDs, r.ID)
	}
	assert.Equal(t, []string{"sec_A", "sec_B", "sec_pt", "sec_pb"}, sealedIDs, "全局在前、按项目名再按名称；不保密的不在内")

	require.NoError(t, exec(`DELETE FROM projects WHERE id = 'prj_b'`))
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM secrets WHERE project_id = 'prj_b'`), "删项目级联删条目")

	downTo(ctx, t, p, 49)
	assert.Equal(t, globals, readRows("1, NULL"), "Down 丢掉项目行，全局行原样保留")
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM pragma_table_info('secrets') WHERE name IN ('project_id', 'secret')`))
	require.Error(t, exec(`INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
		VALUES ('sec_C', 'FOO', 'env', x'00', '[]', 1, 1)`), "UNIQUE(name) 已恢复")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, ins("sec_pa", "prj_a", "FOO", "env", false))
	assert.Equal(t, 3, queryInt(ctx, t, s, `SELECT COUNT(*) FROM secrets`))
}
