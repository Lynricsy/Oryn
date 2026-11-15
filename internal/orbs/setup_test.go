package orbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
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

// TestPlanDeliveryBudget：全局层与项目层随同一条 setup.run 下发。写入接口按 agentcfg.DeliveryBudget 接受的最大组合
// （此前单层就能下发的 64 MiB 全局 + 填到预算为止的项目层）编码成 ctl 帧后不超过 link.MaxCtlLineBytes；绕过写入接口
// 存下的超限配置（升级前留下的）让 Plan 报出说明两层大小的错误，而不是在 ctl 写入时以 line too long 让 orb failed。
func TestPlanDeliveryBudget(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	global, err := agentcfg.NewHome(dataDir)
	require.NoError(t, err)
	projects, err := agentcfg.NewProjectHomes(dataDir)
	require.NoError(t, err)
	id := ids.New("prj")
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: "big", CreatedAt: 1, UpdatedAt: 1}))

	// 全局 4×16 MiB 直接落盘（省掉写入接口对 64 MiB 的密钥扫描；预算按磁盘计，效果相同）
	big := bytes.Repeat([]byte("g"), agentcfg.MaxFileSize)
	gdir := filepath.Join(global.Dir(), "g")
	require.NoError(t, os.MkdirAll(gdir, 0o700))
	for i := range 4 {
		require.NoError(t, os.WriteFile(filepath.Join(gdir, fmt.Sprintf("%d.md", i)), big, 0o644))
	}
	// 经写入接口把项目层填到预算为止
	h, err := projects.For(id)
	require.NoError(t, err)
	mb := bytes.Repeat([]byte("p"), agentcfg.ProjectMaxFileSize)
	n := 0
	for ; ; n++ {
		if _, err := h.Put(ctx, fmt.Sprintf("rules/%d.md", n), mb, 0, nil); err != nil {
			require.ErrorIs(t, err, agentcfg.ErrTooLarge)
			break
		}
	}
	require.Positive(t, n, "预算内至少放得下一个项目文件")

	d := &DefaultSetup{Store: st, Home: global, Projects: projects}
	orb := db.Orb{ID: "orb-a", ProjectID: &id}
	plan, err := d.Plan(ctx, orb)
	require.NoError(t, err)
	require.Len(t, plan.Params.OmpHome, 4)
	require.Len(t, plan.Params.ProjectConfig.Files, n)
	raw, err := json.Marshal(plan.Params)
	require.NoError(t, err)
	envelope, err := json.Marshal(link.CtlMessage{ID: strings.Repeat("9", 64), Method: string(apigen.MethodSetupRun), Params: json.RawMessage("{}")})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(envelope)-2+len(raw)+1, link.MaxCtlLineBytes, "ctl 帧（含换行）放得进单行上限")

	// 升级前存下的超限全局配置：Plan 报出可读的错误
	require.NoError(t, os.WriteFile(filepath.Join(gdir, "4.md"), big, 0o644))
	_, err = d.Plan(ctx, orb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "超过单次下发上限")
	assert.Contains(t, err.Error(), "全局")
}
