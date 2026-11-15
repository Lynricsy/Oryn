package orbs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestPlanGitIdentityPrecedence：项目覆盖（两项齐全）> 所有者默认 > 不下发；半套项目覆盖被忽略。
func TestPlanGitIdentityPrecedence(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	d := &DefaultSetup{Store: st}

	project := func(name string, gitName, gitEmail *string) *string {
		id := ids.New("prj")
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{
			ID: id, Name: name, GitAuthorName: gitName, GitAuthorEmail: gitEmail, CreatedAt: 1, UpdatedAt: 1,
		}))
		return &id
	}
	full := project("full", new("Project Bot"), new("bot@project.example"))
	half := project("half", new("Only Name"), nil)
	plain := project("plain", nil, nil)
	plan := func(projectID *string) *apigen.GitIdentity {
		t.Helper()
		p, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: projectID})
		require.NoError(t, err)
		return p.Params.GitIdentity
	}

	// 尚无所有者：只有项目覆盖生效。
	assert.Nil(t, plan(nil))
	assert.Nil(t, plan(plain))
	assert.Nil(t, plan(half))
	assert.Equal(t, &apigen.GitIdentity{Name: "Project Bot", Email: "bot@project.example"}, plan(full))

	require.NoError(t, st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	assert.Nil(t, plan(nil), "所有者未设置默认值")

	n, err := st.Q.SetOwnerGitIdentity(ctx, db.SetOwnerGitIdentityParams{GitName: new("Owner"), GitEmail: new("owner@example.com")})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	owner := &apigen.GitIdentity{Name: "Owner", Email: "owner@example.com"}
	assert.Equal(t, owner, plan(nil))
	assert.Equal(t, owner, plan(plain))
	assert.Equal(t, owner, plan(half), "半套项目覆盖不生效，回落到所有者默认")
	assert.Equal(t, &apigen.GitIdentity{Name: "Project Bot", Email: "bot@project.example"}, plan(full))
}

// fakeProjects 是 ProjectConfigs 的假实现：按项目 id 返回固定内容，并记录被读过的项目。
type fakeProjects struct {
	mu    sync.Mutex
	files map[string]map[string][]byte
	modes map[string]map[string]os.FileMode
	err   error
	calls []string
}

func (f *fakeProjects) Load(id string) (map[string][]byte, map[string]os.FileMode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, nil, f.err
	}
	files := f.files[id]
	if files == nil {
		files = map[string][]byte{}
	}
	return files, f.modes[id], nil
}

func (f *fakeProjects) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// TestPlanProjectConfig（M12.2）：属于项目的 orb 收到按路径排序、原样（不经 Render）的项目层；项目没有文件时是
// 非 nil 的空清单；无项目、项目已删、没配存储与协调 orb 都不下发；读取失败让 Plan 失败。
func TestPlanProjectConfig(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	project := func(name string) string {
		id := ids.New("prj")
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: name, CreatedAt: 1, UpdatedAt: 1}))
		return id
	}
	full, empty := project("full"), project("empty")
	secretRef := "token: ${oryn:secret:x}\n"
	fp := &fakeProjects{
		files: map[string]map[string][]byte{full: {
			"rules/b.md": []byte("B"), "mcp.json": []byte(secretRef), "hooks/pre/run.sh": []byte("#!/bin/sh\n"),
		}},
		modes: map[string]map[string]os.FileMode{full: {"rules/b.md": 0o600, "hooks/pre/run.sh": 0o755}},
	}
	d := &DefaultSetup{Store: st, Projects: fp}
	plan := func(o db.Orb) *apigen.LinkCtlProjectConfig {
		t.Helper()
		p, err := d.Plan(ctx, o)
		require.NoError(t, err)
		return p.Params.ProjectConfig
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	got := plan(db.Orb{ID: "orb-a", ProjectID: &full})
	require.NotNil(t, got)
	assert.Equal(t, []apigen.LinkCtlOmpHomeFile{
		{Path: "hooks/pre/run.sh", Mode: 0o755, ContentB64: b64("#!/bin/sh\n")},
		{Path: "mcp.json", Mode: 0o644, ContentB64: b64(secretRef)},
		{Path: "rules/b.md", Mode: 0o600, ContentB64: b64("B")},
	}, got.Files, "按路径排序、权限缺省 0644、内容原样（不解析 ${oryn:secret:…}）")

	got = plan(db.Orb{ID: "orb-b", ProjectID: &empty})
	require.NotNil(t, got, "属于项目但没有文件：仍下发，orbd 据此清空镜像")
	require.NotNil(t, got.Files)
	assert.Empty(t, got.Files)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{"files":[]}`, string(raw))

	assert.Nil(t, plan(db.Orb{ID: "orb-c"}), "无项目")
	gone := "prj_missing"
	assert.Nil(t, plan(db.Orb{ID: "orb-d", ProjectID: &gone}), "项目已删除")
	assert.Nil(t, plan(db.Orb{ID: "orb-e", Coordinator: true, ProjectID: &full}), "协调宿主")
	assert.Equal(t, []string{full, empty}, fp.called(), "无项目 / 已删项目 / 协调宿主都不读项目层")

	p, err := (&DefaultSetup{Store: st}).Plan(ctx, db.Orb{ID: "orb-f", ProjectID: &full})
	require.NoError(t, err)
	assert.Nil(t, p.Params.ProjectConfig, "没配项目存储")

	fp.err = errors.New("disk on fire")
	_, err = d.Plan(ctx, db.Orb{ID: "orb-g", ProjectID: &full})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "读取项目 omp 配置")
	assert.ErrorIs(t, err, fp.err)
}
