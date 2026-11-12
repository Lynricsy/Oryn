package agentcfg

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const sampleModels = `# 用户注释
providers:
  custom:
    baseUrl: https://llm.example.com/api/v1
    apiKey: sk-literal-custom-0123456789
    models:
      - id: m1
        baseUrl: https://alt.example.com/v2
  anthropic:
    apiKey: sk-ant-literal-abcdef
  envform:
    apiKey: MY_PROVIDER_KEY
  cmdform:
    apiKey: "!pass show key"
  exa:
    apiKey: exa-literal-xyz
`

type fakeResolver map[string][2]string

func (f fakeResolver) Lookup(name string) (string, string, bool) {
	v, ok := f[name]
	return v[0], v[1], ok
}

func TestImportAndRender(t *testing.T) {
	res, err := Import(map[string][]byte{
		"models.yml":        []byte(sampleModels),
		"AGENTS.md":         []byte("token ghp_abcdefghijklmnopqrstuvwxyz0123"),
		"sessions/x.jsonl":  []byte("ignored"),
		"history.db":        []byte("ignored"),
		"skills/a/SKILL.md": []byte("hello"),
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"omp/models/custom/apiKey":    "sk-literal-custom-0123456789",
		"omp/models/anthropic/apiKey": "sk-ant-literal-abcdef",
		"omp/models/exa/apiKey":       "exa-literal-xyz",
	}, res.Secrets)
	assert.Equal(t, map[string][]string{
		"omp/models/custom/apiKey":    {"llm.example.com"},
		"omp/models/anthropic/apiKey": {"api.anthropic.com"},
	}, res.SecretHosts, "exa 无上游主机（环境变量降级），不绑定主机")
	assert.NotContains(t, res.Files, "sessions/x.jsonl")
	assert.NotContains(t, res.Files, "history.db")
	assert.Contains(t, res.Files, "skills/a/SKILL.md")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "AGENTS.md")
	tmpl := string(res.Files["models.yml"])
	assert.NotContains(t, tmpl, "sk-literal")
	assert.Contains(t, tmpl, "# 用户注释")
	assert.Contains(t, tmpl, "MY_PROVIDER_KEY")
	assert.Contains(t, tmpl, "!pass show key")

	r := fakeResolver{
		"omp/models/custom/apiKey":    {"sec1", "sk-literal-custom-0123456789"},
		"omp/models/anthropic/apiKey": {"sec2", "sk-ant-literal-abcdef"},
		"omp/models/exa/apiKey":       {"sec3", "exa-literal-xyz"},
	}
	out, err := Render(res.Files, r)
	require.NoError(t, err)
	var doc struct {
		Providers map[string]struct {
			BaseURL string `yaml:"baseUrl"`
			APIKey  string `yaml:"apiKey"`
			Models  []struct {
				BaseURL string `yaml:"baseUrl"`
			} `yaml:"models"`
		} `yaml:"providers"`
	}
	require.NoError(t, yaml.Unmarshal(out.Files["models.yml"], &doc))
	p := doc.Providers
	assert.Equal(t, "oryn-ph-sec1", p["custom"].APIKey)
	assert.Equal(t, "http://127.0.0.1:7811/llm/custom/api/v1", p["custom"].BaseURL)
	assert.Equal(t, "http://127.0.0.1:7811/llm/custom.m0/v2", p["custom"].Models[0].BaseURL)
	assert.Equal(t, "oryn-ph-sec2", p["anthropic"].APIKey)
	assert.Equal(t, "http://127.0.0.1:7811/llm/anthropic", p["anthropic"].BaseURL)
	assert.Equal(t, "MY_PROVIDER_KEY", p["envform"].APIKey)
	assert.Equal(t, "!pass show key", p["cmdform"].APIKey)
	assert.Equal(t, "ORYN_MODEL_KEY_EXA", p["exa"].APIKey)
	assert.Equal(t, map[string]egress.Route{
		"custom":    {Origin: "https://llm.example.com", SecretIDs: []string{"sec1"}},
		"custom.m0": {Origin: "https://alt.example.com", SecretIDs: []string{"sec1"}},
		"anthropic": {Origin: "https://api.anthropic.com", SecretIDs: []string{"sec2"}},
	}, out.Routes)
	assert.Equal(t, map[string]string{"ORYN_MODEL_KEY_EXA": "exa-literal-xyz"}, out.Env)
	assert.Len(t, out.Warnings, 1)
	assert.NotContains(t, string(out.Files["models.yml"]), "sk-literal")
	assert.NotContains(t, string(out.Files["models.yml"]), "sk-ant-literal")

	_, err = Render(res.Files, fakeResolver{})
	assert.Error(t, err, "缺失密钥应报错")
}

func TestExcluded(t *testing.T) {
	for p, want := range map[string]bool{
		"sessions/a.jsonl": true, "blobs/x": true, "agent.db": true, "agent.db-wal": true,
		"auth.json": true, "history.db": true, "config.yml": false, "skills/sessions/x": false,
		"ssh-keys/id_ed25519": true, "remote-pairing.json": true, "models.yml.bak.20260714-220351": true,
	} {
		assert.Equal(t, want, Excluded(p), p)
	}
}
