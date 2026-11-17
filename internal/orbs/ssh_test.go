package orbs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type fakeSSH struct {
	cfg *apigen.LinkCtlSSHConfig
	err error
}

func (f fakeSSH) OrbConfig(context.Context, db.Orb) (*apigen.LinkCtlSSHConfig, error) { return f.cfg, f.err }

var testSSHConfig = &apigen.LinkCtlSSHConfig{HostKey: "ssh-ed25519 AAAA", Hosts: []apigen.LinkCtlSSHHost{{Name: "web", Description: "root@h:22"}}}

// setup.run 带上 SSH 配置；中央配置里残留的 ssh.json 不再下发。
func TestPlanSSH(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	home := &memHome{files: map[string][]byte{"config.yml": []byte("a: 1\n"), "ssh.json": []byte(`{"hosts":{"x":{"host":"10.0.0.1"}}}`)}}
	d := &DefaultSetup{Store: st, Home: home, Vault: modelVault{}, SSH: fakeSSH{cfg: testSSHConfig}}
	p, err := d.Plan(ctx, db.Orb{ID: "orb-a"})
	require.NoError(t, err)
	assert.Equal(t, testSSHConfig, p.Params.SSH)
	var paths []string
	for _, f := range p.Params.OmpHome {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"config.yml"}, paths)

	d.SSH = fakeSSH{err: errors.New("db down")}
	_, err = d.Plan(ctx, db.Orb{ID: "orb-a"})
	require.ErrorContains(t, err, "读取 SSH 主机")

	d.SSH = nil
	p, err = d.Plan(ctx, db.Orb{ID: "orb-a"})
	require.NoError(t, err)
	assert.Nil(t, p.Params.SSH)
}

// sshSetup 在 fakeSetup 的参数上带 SSH 配置。
type sshSetup struct{ fakeSetup }

func (s sshSetup) Plan(ctx context.Context, o db.Orb) (SetupPlan, error) {
	p, err := s.fakeSetup.Plan(ctx, o)
	p.Params.SSH = testSSHConfig
	return p, err
}

// SSH 主机的摘要并进这一层参与比较：变了 Changed 里有 ssh.json；omphome.apply 带上 SSH 配置。
func TestApplySSHState(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureOmpHomeApply}
	h := newHarness(t, drv, sshSetup{fakeSetup{upstream: "https://up.example.com"}}, nil)
	h.quiet.Store(false)
	ctx := context.Background()
	state := []byte("v1")
	var layers []string
	home := &memHome{files: map[string][]byte{"config.yml": []byte("a: 1\n")}}
	require.NoError(t, h.svc.EnableOmpApply(OmpApplyConfig{Home: home, Dir: t.TempDir(), SSHState: func(p string) ([]byte, error) {
		layers = append(layers, p)
		return state, nil
	}}))
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)

	rep, err := h.svc.ApplyOmpHome(ctx)
	require.NoError(t, err)
	assert.Empty(t, rep.Changed, "基线已含 SSH 摘要")
	state = []byte("v2")
	rep, err = h.svc.ApplyOmpHome(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"ssh.json"}, rep.Changed)
	assert.Equal(t, []string{"", "", ""}, layers)

	raw, ok := drv.orbd(orb.ID).call(string(apigen.MethodOmpHomeApply))
	require.True(t, ok)
	var params apigen.LinkCtlOmpHomeApplyParams
	require.NoError(t, json.Unmarshal(raw, &params))
	assert.Equal(t, testSSHConfig, params.SSH)
	for _, f := range params.OmpHome {
		assert.NotEqual(t, "ssh.json", f.Path, "摘要不下发")
	}
}
