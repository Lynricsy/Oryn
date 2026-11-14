package orbs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// registerHost 写入内置的 @host provider（同 coordhost.register）。
func registerHost(t *testing.T, st *store.Store) db.Provider {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: ids.New("prov"), TokenHash: ids.Hash(ids.Token("opv")), Version: "test",
		Capabilities: `{"freeze":true,"snapshot":false}`, OrbTypes: `[{"name":"host","cpus":0,"memoryMB":0}]`, CreatedAt: 1,
	}))
	p, err := st.Q.GetProviderByName(ctx, HostProviderName)
	require.NoError(t, err)
	return p
}

// H6：「一个真实 provider + @host」时缺省仍选中真实的那个；显式 / 项目默认 / orb_defaults 指向 @host 一律拒绝；
// 协调 orb 固定落在 @host、类型 host。
func TestResolveProviderReservesHost(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, nil)
	host := registerHost(t, b.st)

	p, err := b.svc.resolveProvider(ctx, "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, b.provID, p.ID, "唯一回退不计入 @host")

	_, err = b.svc.resolveProvider(ctx, HostProviderName, nil, nil)
	require.ErrorIs(t, err, ErrNoProvider)
	assert.Contains(t, err.Error(), "reserved")

	hostName := HostProviderName
	_, err = b.svc.resolveProvider(ctx, "", &db.Project{DefaultProvider: &hostName}, nil)
	require.ErrorIs(t, err, ErrNoProvider, "项目默认也不能是 @host")

	_, err = b.svc.resolveProvider(ctx, "", nil, &db.Orb{ProviderID: host.ID})
	require.ErrorIs(t, err, ErrNoProvider, "从协调 orb 派生（fork 连环境）同样拒绝")

	_, err = b.svc.Create(ctx, CreateParams{Provider: HostProviderName})
	require.ErrorIs(t, err, ErrNoProvider)

	orb, err := b.svc.Create(ctx, CreateParams{Coordinator: true})
	require.NoError(t, err)
	assert.True(t, orb.Coordinator)
	assert.Equal(t, host.ID, orb.ProviderID)
	assert.Equal(t, HostType, orb.Type)
	assert.Equal(t, []string{"spawn:"}, b.pending(orb.ID), "spawn 任务进 @host 的队列")
	view, err := View(ctx, b.st.Q, orb)
	require.NoError(t, err)
	require.NotNil(t, view.Coordinator)
	assert.True(t, *view.Coordinator)
}

func TestResolveProviderHostAsOrbDefault(t *testing.T) {
	b := newBare(t, func(c *Config) { c.Defaults.Provider = HostProviderName })
	registerHost(t, b.st)
	_, err := b.svc.resolveProvider(context.Background(), "", nil, nil)
	require.ErrorIs(t, err, ErrNoProvider)
}

func TestCreateCoordinatorWithoutHost(t *testing.T) {
	b := newBare(t, nil)
	_, err := b.svc.Create(context.Background(), CreateParams{Coordinator: true})
	require.ErrorIs(t, err, ErrNoProvider)
}

type coordHome map[string][]byte

func (h coordHome) LoadAll() (map[string][]byte, error) { return h, nil }
func (h coordHome) Modes() (map[string]os.FileMode, error) {
	return map[string]os.FileMode{"models.yml": 0o600}, nil
}

type coordVault struct{}

func (coordVault) Lookup(name string) (string, string, bool) {
	switch name {
	case "omp/models/anthropic/apiKey":
		return "sec1", "sk-ant-real", true
	case "omp/models/exa/apiKey":
		return "sec2", "exa-real", true
	}
	return "", "", false
}
func (coordVault) SecretByID(id string) (string, []string, bool) {
	switch id {
	case "sec1":
		return "sk-ant-real", []string{"api.anthropic.com"}, true
	case "sec2":
		return "exa-real", nil, true
	}
	return "", nil, false
}
func (coordVault) GitToken(string) (string, bool) { return "tok", true }
func (coordVault) EnvSecrets() (map[string]string, error) {
	return map[string]string{"NPM_TOKEN": "npm-real"}, nil
}
func (coordVault) GitHosts() ([]string, error) { return []string{"github.com"}, nil }

// H6：协调 orb 的 setup 参数只有 models.yml 与 config.yml、模型地址换成协调宿主的代理端口、
// 没有 env 模式密钥 / 环境变量降级的密钥 / git 主机，egress 拒绝 git。
func TestPlanCoordinator(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	repo := "https://github.com/owner/repo"
	prj := ids.New("prj")
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: prj, Name: "p", RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))

	models := "providers:\n  anthropic:\n    apiKey: ${oryn:secret:omp/models/anthropic/apiKey}\n  exa:\n    apiKey: ${oryn:secret:omp/models/exa/apiKey}\n"
	d := &DefaultSetup{
		Store: st, Vault: coordVault{}, CoordinatorEgressAddr: "127.0.0.1:9911",
		Home: coordHome{
			"models.yml": []byte(models), "config.yml": []byte("theme: dark\n"),
			"mcp.json": []byte("{}"), "AGENTS.md": []byte("x"), "skills/a/SKILL.md": []byte("x"),
			"extensions/e.ts": []byte("x"), "agents/a.md": []byte("x"), "commands/c.md": []byte("x"),
		},
	}
	plan, err := d.Plan(ctx, db.Orb{ID: "orb-c", Coordinator: true, ProjectID: &prj})
	require.NoError(t, err)

	paths := []string{}
	files := map[string]string{}
	for _, f := range plan.Params.OmpHome {
		paths = append(paths, f.Path)
		b, err := base64.StdEncoding.DecodeString(f.ContentB64)
		require.NoError(t, err)
		files[f.Path] = string(b)
	}
	assert.Equal(t, []string{"config.yml", "models.yml"}, paths)
	assert.Contains(t, files["models.yml"], "http://127.0.0.1:9911/llm/anthropic")
	assert.NotContains(t, files["models.yml"], "sk-ant-real")
	assert.NotContains(t, files["models.yml"], "7811")
	assert.Empty(t, plan.Params.Env, "env 模式密钥与降级密钥都不下发")
	assert.Empty(t, plan.Params.GitHosts)
	assert.Nil(t, plan.Params.Repo, "不取项目")
	assert.Nil(t, plan.Params.Scripts.Setup)
	assert.True(t, plan.Egress.NoGit)
	assert.Contains(t, plan.Egress.Routes, "anthropic")

	// 对照：同样的配置给普通 orb 时 7811、git 主机与 env 密钥都在
	normal, err := d.Plan(ctx, db.Orb{ID: "orb-n", ProjectID: &prj})
	require.NoError(t, err)
	assert.False(t, normal.Egress.NoGit)
	assert.NotEmpty(t, normal.Params.Env)
	assert.Greater(t, len(normal.Params.OmpHome), 2)
}

// H6：ServeConn 只服务处于可接 link 状态的协调 orb；成功时走与 WebSocket link 相同的 hello / setup 流程，
// HasFeature 反映 hello 的特性表，对端断开后返回。
func TestServeConn(t *testing.T) {
	ctx := context.Background()
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	host := registerHost(t, h.st)
	mk := func(state string, coordinator bool, prov string) string {
		id := ids.Orb()
		require.NoError(t, h.st.Q.CreateOrb(ctx, db.CreateOrbParams{
			ID: id, ProviderID: prov, Type: "host", State: state, StateChangedAt: 1,
			TokenHash: ids.Hash(orbToken(h.svc.cfg.TokenKey, id, 0)), CreatedAt: 1, Coordinator: coordinator,
		}))
		return id
	}
	closed := func(c net.Conn) bool {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := c.Read(make([]byte, 1))
		return err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
	}

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"普通 orb", mk(StateConnecting, false, h.provID)},
		{"协调 orb 已删除", mk(StateDeleted, true, host.ID)},
		{"协调 orb 已冻结", mk(StateFrozen, true, host.ID)},
		{"不存在", "orb-nope"},
	} {
		a, b := net.Pipe()
		err := h.svc.ServeConn(ctx, tc.id, a)
		assert.Error(t, err, tc.name)
		assert.True(t, closed(b), "%s：conn 被关闭", tc.name)
	}

	id := mk(StateConnecting, true, host.ID)
	a, b := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- h.svc.ServeConn(ctx, id, a) }()
	sess, err := link.NewConnSession(b, true)
	require.NoError(t, err)
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(t, err)
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "setup.run" || method == "resume.run" {
			return apigen.LinkCtlSetupRunResult{Ok: true}, nil
		}
		return nil, link.ErrMethodNotFound
	}})
	assert.False(t, h.svc.HasFeature(id, FeatureCoordinator), "hello 之前没有特性")
	require.NoError(t, ctl.Notify("hello", apigen.LinkCtlHelloParams{
		OrbdVersion: "dev", AgentVersion: "omp/test", Os: "linux", Arch: "amd64",
		Threads: []apigen.LinkCtlHelloThread{}, Features: []string{FeatureCoordinator},
	}))
	h.waitState(id, StateReady, 10*time.Second)
	assert.True(t, h.svc.HasFeature(id, FeatureCoordinator))
	assert.False(t, h.svc.HasFeature(id, "threads"))
	assert.False(t, h.svc.HasFeature("orb-other", FeatureCoordinator))

	require.NoError(t, sess.Close())
	select {
	case err := <-served:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("对端断开后 ServeConn 没有返回")
	}
	require.Eventually(t, func() bool { return !h.svc.HasFeature(id, FeatureCoordinator) }, 5*time.Second, 20*time.Millisecond)
}
