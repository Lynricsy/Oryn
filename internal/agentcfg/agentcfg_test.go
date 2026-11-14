package agentcfg

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// fakeResolver：密钥名 → {id, 明文, 逗号分隔的 hosts}。
type fakeResolver map[string][3]string

func (f fakeResolver) Lookup(name string) (string, string, bool) {
	v, ok := f[name]
	return v[0], v[1], ok
}

func (f fakeResolver) SecretByID(id string) (string, []string, bool) {
	for _, v := range f {
		if v[0] == id {
			var hosts []string
			if v[2] != "" {
				hosts = strings.Split(v[2], ",")
			}
			return v[1], hosts, true
		}
	}
	return "", nil, false
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
		"omp/models/custom/apiKey":    {"llm.example.com", "alt.example.com"},
		"omp/models/anthropic/apiKey": {"api.anthropic.com"},
	}, res.SecretHosts, "provider 与模型 baseUrl 的主机都绑定；exa 无上游主机（环境变量降级），不绑定主机")
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
		"omp/models/custom/apiKey":    {"sec1", "sk-literal-custom-0123456789", "llm.example.com,alt.example.com"},
		"omp/models/anthropic/apiKey": {"sec2", "sk-ant-literal-abcdef", "api.anthropic.com"},
		"omp/models/exa/apiKey":       {"sec3", "exa-literal-xyz", ""},
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

// M9：RenderAt 把模型地址改写到给定代理地址（协调宿主的端口），Render 等价于 RenderAt(EgressAddr)。
func TestRenderAtEgressAddr(t *testing.T) {
	res, err := Import(map[string][]byte{"models.yml": []byte(sampleModels)})
	require.NoError(t, err)
	r := fakeResolver{
		"omp/models/custom/apiKey":    {"sec1", "sk-literal-custom-0123456789", "llm.example.com,alt.example.com"},
		"omp/models/anthropic/apiKey": {"sec2", "sk-ant-literal-abcdef", "api.anthropic.com"},
		"omp/models/exa/apiKey":       {"sec3", "exa-literal-xyz", ""},
	}
	at, err := RenderAt(res.Files, r, "127.0.0.1:9911")
	require.NoError(t, err)
	models := string(at.Files["models.yml"])
	assert.Contains(t, models, "http://127.0.0.1:9911/llm/custom/api/v1")
	assert.Contains(t, models, "http://127.0.0.1:9911/llm/custom.m0/v2")
	assert.Contains(t, models, "http://127.0.0.1:9911/llm/anthropic")
	assert.NotContains(t, models, "7811")

	def, err := Render(res.Files, r)
	require.NoError(t, err)
	viaAt, err := RenderAt(res.Files, r, EgressAddr)
	require.NoError(t, err)
	assert.Equal(t, def.Files, viaAt.Files)
	assert.Equal(t, def.Routes, at.Routes, "路由只与上游有关，与代理地址无关")
}

func TestExcluded(t *testing.T) {
	for p, want := range map[string]bool{
		"sessions/a.jsonl": true, "blobs/x": true, "agent.db": true, "agent.db-wal": true,
		"auth.json": true, "history.db": true, "config.yml": false, "skills/sessions/x": false,
		"skill-repositories/x/.git/objects/pack/p.pack": true, "extensions/a/node_modules/b/index.js": true,
		"ssh-keys/id_ed25519": true, "remote-pairing.json": true, "models.yml.bak.20260714-220351": true,
	} {
		assert.Equal(t, want, Excluded(p), p)
	}
}

func TestScanSecretLikeNoise(t *testing.T) {
	noisy := "a0162f1e3c4b5d6e7f8091a2b3c4d5e6f7081920 refs/heads/main\nsee https://github.com/owner/some-very-long-repository-name-here\nmax_completion_tokens_per_request_limit_value"
	assert.Empty(t, ScanSecretLike([]byte(noisy)), "git 哈希、URL、单词拼接不告警")
	assert.NotEmpty(t, ScanSecretLike([]byte("token: Xy7kP2mQ9vR4tL8nB3wE6sD1fG5hJ0aZ")))
	assert.NotEmpty(t, ScanSecretLike([]byte("sk-abcdefghijklmnop1234")))
}

// M1：models.yml 不能把任意密钥经 apiKey 送进 orb 或送往任意上游。
func TestSecretRefPolicy(t *testing.T) {
	r := fakeResolver{
		"omp/models/x/apiKey":      {"own", "OWN", ""},
		"omp/models/y/apiKey":      {"oy", "OY", "api.y.com"},
		"git/github.com/token":     {"gh", "GHTOKEN", ""},
		"other/free":               {"free", "FREE", ""},
		"other/bound":              {"bound", "BOUND", "api.y.com"},
		"omp/models/custom/apiKey": {"c", "C", "llm.example.com"},
	}
	bad := map[string]string{
		"apiKey 引用别的 provider 的密钥": "providers:\n  x:\n    apiKey: \"${oryn:secret:omp/models/y/apiKey}\"\n",
		"apiKey 引用任意密钥（环境变量降级）":    "providers:\n  x:\n    apiKey: \"${oryn:secret:other/free}\"\n",
		"apiKey 部分引用":              "providers:\n  x:\n    apiKey: \"k-${oryn:secret:omp/models/x/apiKey}\"\n",
		"引用 git 密钥":                "providers:\n  x:\n    apiKey: \"${oryn:secret:git/github.com/token}\"\n",
		"providers 之外引用 git 密钥":    "foo: \"${oryn:secret:git/github.com/token}\"\n",
		"其它引用未绑定主机":                "providers:\n  custom:\n    baseUrl: https://llm.example.com\n    apiKey: \"${oryn:secret:omp/models/custom/apiKey}\"\n    headers:\n      X-Extra: \"${oryn:secret:other/free}\"\n",
		"绑定主机的密钥不能环境变量降级":          "providers:\n  y:\n    apiKey: \"${oryn:secret:omp/models/y/apiKey}\"\n",
	}
	for name, yml := range bad {
		_, err := Render(map[string][]byte{ModelsFile: []byte(yml)}, r)
		assert.Error(t, err, name)
	}
	_, err := Import(map[string][]byte{ModelsFile: []byte(bad["apiKey 引用别的 provider 的密钥"])})
	assert.Error(t, err, "导入（PUT）时同样拒绝")
	_, err = Import(map[string][]byte{ModelsFile: []byte(bad["引用 git 密钥"])})
	assert.Error(t, err)

	// 合法：自己的密钥（未绑定主机）可环境变量降级；绑定主机的其它引用只挂到主机匹配的路由。
	ok := "providers:\n  x:\n    apiKey: \"${oryn:secret:omp/models/x/apiKey}\"\n" +
		"  custom:\n    baseUrl: https://llm.example.com\n    apiKey: \"${oryn:secret:omp/models/custom/apiKey}\"\n" +
		"    headers:\n      X-Extra: \"${oryn:secret:other/bound}\"\n    models:\n      - id: m\n        baseUrl: https://api.y.com/v1\n"
	out, err := Render(map[string][]byte{ModelsFile: []byte(ok)}, r)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ORYN_MODEL_KEY_X": "OWN"}, out.Env)
	assert.Equal(t, egress.Route{Origin: "https://llm.example.com", SecretIDs: []string{"c"}}, out.Routes["custom"])
	assert.Equal(t, egress.Route{Origin: "https://api.y.com", SecretIDs: []string{"c"}, HostBoundIDs: []string{"bound"}}, out.Routes["custom.m0"])
}

// OD5：provider 的模型条目 baseUrl 在另一台主机上：导入绑定的主机含它，经 egress 的请求能用上真实密钥。
func TestModelBaseURLOnOtherHostAuthenticates(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	// 同一上游，provider 用 127.0.0.1、模型条目用 localhost：两个不同的主机名。
	yml := fmt.Sprintf("providers:\n  custom:\n    baseUrl: http://127.0.0.1:%s/api\n    apiKey: sk-real-key-0123456789\n"+
		"    models:\n      - id: m\n        baseUrl: http://localhost:%s/v2\n", u.Port(), u.Port())
	imp, err := Import(map[string][]byte{ModelsFile: []byte(yml)})
	require.NoError(t, err)
	name := ModelKeySecretName("custom")
	r := fakeResolver{name: {"sid", imp.Secrets[name], strings.Join(imp.SecretHosts[name], ",")}}
	out, err := Render(imp.Files, r)
	require.NoError(t, err)

	h := egress.NewHandler("orb-x", policyResolver{fakeResolver: r, pol: egress.Policy{Routes: out.Routes}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/llm/custom.m0/v2/chat", nil)
	req.Header.Set("Authorization", "Bearer "+Placeholder("sid"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "Bearer sk-real-key-0123456789", got)
}

type policyResolver struct {
	fakeResolver
	pol egress.Policy
}

func (p policyResolver) Policy() egress.Policy          { return p.pol }
func (p policyResolver) GitToken(string) (string, bool) { return "", false }
