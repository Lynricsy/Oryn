package orbs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M21：runner 的无密钥投递校验——中央配置的 mcp.json 里形似密钥的串、config.yml 里等于某个已知密钥值的串：这两个
// 文件不出现在 runner 的 omphome.apply 参数里，orb 事件备注写出路径与原因（不含密钥）；models.yml（渲染后只有占位符）
// 与干净文件照常下发；orb 的 plan 仍原样带这些文件。
func TestRunnerApplyWithholdsSecretLikeFiles(t *testing.T) {
	const vaultValue = "vault-value-for-test"
	const keyLike = "sk-test0123456789abcdefXYZ"
	home := &memHome{files: map[string][]byte{
		"models.yml": []byte(modelsYml("p1", "https://a.example.com/v1")),
		"mcp.json":   []byte(`{"mcpServers":{"x":{"headers":{"Authorization":"Bearer ` + keyLike + `"}}}}`),
		"config.yml": []byte("note: " + vaultValue + "\n"),
		"AGENTS.md":  []byte("clean\n"),
	}}
	ds := &DefaultSetup{Home: home, Vault: modelVault{}, KnownSecrets: func(context.Context) (map[string]string, error) {
		return map[string]string{"db/token": vaultValue}, nil
	}}
	h := newHarnessRun(t, newFakeDriver(), ds, nil, false)
	ds.Store = h.st
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	var mu sync.Mutex
	var applied []apigen.LinkCtlOmpHomeApplyParams
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{FeatureOmpHomeApply}}
	fr.onReq = func(method string, params json.RawMessage) (any, error) {
		if method != string(apigen.MethodOmpHomeApply) {
			return nil, &link.CtlError{Code: "not_supported", Message: method}
		}
		var p apigen.LinkCtlOmpHomeApplyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		mu.Lock()
		applied = append(applied, p)
		mu.Unlock()
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	mu.Lock()
	require.Len(t, applied, 1)
	p := applied[0]
	mu.Unlock()
	paths := map[string]string{}
	for _, f := range p.OmpHome {
		b, err := base64.StdEncoding.DecodeString(f.ContentB64)
		require.NoError(t, err)
		paths[f.Path] = string(b)
	}
	assert.Contains(t, paths, "models.yml")
	assert.Contains(t, paths, "AGENTS.md")
	assert.NotContains(t, paths, "mcp.json")
	assert.NotContains(t, paths, "config.yml")
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	for _, s := range []string{vaultValue, keyLike, "real-key"} {
		assert.NotContains(t, string(raw), s)
		assert.NotContains(t, strings.Join(mapValues(paths), "\n"), s)
	}

	evs, err := h.st.Q.ListOrbEvents(ctx, r.OrbID)
	require.NoError(t, err)
	var note string
	for _, e := range evs {
		if strings.Contains(e.Detail, "runner config withheld") {
			note = e.Detail
		}
	}
	require.NotEmpty(t, note, "orb 事件里有扣下文件的备注")
	assert.Contains(t, note, `"path":"config.yml"`)
	assert.Contains(t, note, `"path":"mcp.json"`)
	assert.Contains(t, note, "db/token")
	assert.NotContains(t, note, vaultValue)
	assert.NotContains(t, note, keyLike)

	rep := h.svc.applyOrb(ctx, runnerOrbRow(t, h, r.OrbID))
	require.True(t, rep.OK, rep.Err)
	evs2, err := h.st.Q.ListOrbEvents(ctx, r.OrbID)
	require.NoError(t, err)
	assert.Len(t, evs2, len(evs))

	// orb：原样下发
	op, err := ds.Plan(ctx, db.Orb{ID: "orb-x"})
	require.NoError(t, err)
	assert.Empty(t, op.Withheld)
	got := map[string]bool{}
	for _, f := range op.Params.OmpHome {
		got[f.Path] = true
	}
	assert.True(t, got["mcp.json"] && got["config.yml"], "orb 的下发不变")
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func runnerOrbRow(t *testing.T, h *harness, id string) db.Orb {
	t.Helper()
	o, err := h.svc.Get(context.Background(), id)
	require.NoError(t, err)
	return o
}
