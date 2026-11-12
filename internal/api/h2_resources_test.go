package api_test

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func h2DeviceToken(t *testing.T, e *h2Env) h2Cred {
	t.Helper()
	_, tok, err := auth.CreateDevice(context.Background(), e.st, "t", "cli", "")
	require.NoError(t, err)
	return h2Cred{bearer: tok}
}

func TestProvidersAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 400, e.do("POST", "/api/v1/providers", c, map[string]string{"name": "Bad Name"}).status)
	r := e.do("POST", "/api/v1/providers", c, map[string]string{"name": "local"})
	require.Equal(t, 201, r.status, string(r.body))
	var created struct {
		Token    string
		Provider struct {
			ID       string
			Online   bool
			OrbTypes []any
		}
	}
	r.json(t, &created)
	assert.Regexp(t, `^opv_`, created.Token)
	assert.False(t, created.Provider.Online)
	assert.NotNil(t, created.Provider.OrbTypes)
	assert.Equal(t, 409, e.do("POST", "/api/v1/providers", c, map[string]string{"name": "local"}).status)

	// 模拟 hello 后的租约
	lease := time.Now().Add(45 * time.Second).UnixMilli()
	require.NoError(t, e.st.Q.UpdateProviderHello(context.Background(), db.UpdateProviderHelloParams{
		Driver: "docker", Capabilities: `{"freeze":true,"snapshot":true}`,
		OrbTypes: `[{"name":"medium","cpus":4,"memoryMB":8192}]`, DefaultType: "medium",
		InstanceID: new("i-1"), LeaseUntil: &lease, ID: created.Provider.ID,
	}))
	r = e.do("GET", "/api/v1/providers", c, nil)
	require.Equal(t, 200, r.status)
	var list struct {
		Providers []struct {
			Online       bool
			Driver       string
			DefaultType  string
			Capabilities struct{ Freeze bool }
			OrbTypes     []struct {
				Name     string
				MemoryMB int
			}
		}
	}
	r.json(t, &list)
	require.Len(t, list.Providers, 1)
	p := list.Providers[0]
	assert.True(t, p.Online)
	assert.Equal(t, "docker", p.Driver)
	assert.True(t, p.Capabilities.Freeze)
	assert.Equal(t, 8192, p.OrbTypes[0].MemoryMB)

	require.Equal(t, 204, e.do("DELETE", "/api/v1/providers/"+created.Provider.ID, c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/providers/"+created.Provider.ID, c, nil).status)
}

func TestProjectsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 400, e.do("POST", "/api/v1/projects", c, map[string]any{"name": " "}).status)
	assert.Equal(t, 400, e.do("POST", "/api/v1/projects", c, map[string]any{"name": "x", "defaultProvider": "ghost"}).status)
	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "oryn", "repoUrl": "https://github.com/Lynricsy/Oryn.git", "baseBranch": "main", "shallowClone": true})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct {
		ID, Name, RepoURL, BaseBranch string
		ShallowClone                  bool
		ArchivedAt                    *int64
	}
	r.json(t, &p)
	assert.Equal(t, "main", p.BaseBranch)
	assert.True(t, p.ShallowClone)
	assert.Equal(t, 409, e.do("POST", "/api/v1/projects", c, map[string]any{"name": "oryn"}).status)

	r = e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"baseBranch": "", "archived": true})
	require.Equal(t, 200, r.status, string(r.body))
	p.BaseBranch = ""
	r.json(t, &p)
	assert.Empty(t, p.BaseBranch)
	assert.NotNil(t, p.ArchivedAt)
	assert.Equal(t, "https://github.com/Lynricsy/Oryn.git", p.RepoURL)

	var list struct{ Projects []struct{ ID string } }
	e.do("GET", "/api/v1/projects", c, nil).json(t, &list)
	assert.Empty(t, list.Projects, "缺省只列未归档")
	e.do("GET", "/api/v1/projects?archived=true", c, nil).json(t, &list)
	assert.Len(t, list.Projects, 1)

	assert.Equal(t, 200, e.do("GET", "/api/v1/projects/"+p.ID, c, nil).status)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+p.ID, c, nil).status)
	assert.Equal(t, 404, e.do("GET", "/api/v1/projects/"+p.ID, c, nil).status)
	assert.Equal(t, 404, e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"name": "y"}).status)
}

func TestSecretsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 400, e.do("PUT", "/api/v1/secrets/lower", c, map[string]any{"value": "v", "mode": "env"}).status)
	assert.Equal(t, 400, e.do("PUT", "/api/v1/secrets/X", c, map[string]any{"value": "v", "mode": "bogus"}).status)
	r := e.do("PUT", "/api/v1/secrets/git%2Fgithub.com%2Ftoken", c, map[string]any{"value": "ghp_secret", "mode": "proxy", "hosts": []string{"github.com"}})
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), "ghp_secret")
	tok, ok := e.vault.GitToken("github.com")
	require.True(t, ok)
	assert.Equal(t, "ghp_secret", tok)

	r = e.do("GET", "/api/v1/secrets", c, nil)
	require.Equal(t, 200, r.status)
	assert.Contains(t, string(r.body), `"git/github.com/token"`)
	assert.NotContains(t, string(r.body), "ghp_secret")

	require.Equal(t, 204, e.do("DELETE", "/api/v1/secrets/git%2Fgithub.com%2Ftoken", c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/secrets/git%2Fgithub.com%2Ftoken", c, nil).status)
}

func TestAgentConfigAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	models := []byte("providers:\n  anthropic:\n    apiKey: sk-ant-literal-0123456789abcdef\n")
	r := e.do("PUT", "/api/v1/agent-config/files/models.yml", c, models)
	require.Equal(t, 200, r.status, string(r.body))
	var wr struct {
		File struct {
			Path string
			Mode int
		}
		ExtractedSecrets []string
		Warnings         []any
	}
	r.json(t, &wr)
	assert.Equal(t, []string{"omp/models/anthropic/apiKey"}, wr.ExtractedSecrets)
	assert.Equal(t, 0o644, wr.File.Mode)
	assert.NotNil(t, wr.Warnings)

	r = e.do("GET", "/api/v1/agent-config/files/models.yml", c, nil)
	require.Equal(t, 200, r.status)
	assert.NotContains(t, string(r.body), "sk-ant-literal")
	assert.Contains(t, string(r.body), "${oryn:secret:omp/models/anthropic/apiKey}")

	assert.Equal(t, 200, e.do("PUT", "/api/v1/agent-config/files/skills%2Fa%2FSKILL.md?mode=384", c, []byte("# a")).status)
	assert.Equal(t, 400, e.do("PUT", "/api/v1/agent-config/files/..%2Fescape", c, []byte("x")).status)
	assert.Equal(t, 400, e.do("PUT", "/api/v1/agent-config/files/auth.json", c, []byte("{}")).status)
	assert.Equal(t, 400, e.do("PUT", "/api/v1/agent-config/files/x?mode=999", c, []byte("x")).status)
	assert.Equal(t, 404, e.do("GET", "/api/v1/agent-config/files/missing.md", c, nil).status)

	var list struct {
		Files []struct {
			Path string
			Mode int
		}
	}
	e.do("GET", "/api/v1/agent-config/files", c, nil).json(t, &list)
	require.Len(t, list.Files, 2)
	assert.Equal(t, "skills/a/SKILL.md", list.Files[1].Path)
	assert.Equal(t, 0o600, list.Files[1].Mode)

	// tar 导入整体替换
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("theme: dark\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "config.yml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, _ = tw.Write(body)
	require.NoError(t, tw.Close())
	r = e.do("POST", "/api/v1/agent-config/import", c, buf.Bytes())
	require.Equal(t, 200, r.status, string(r.body))
	var imp struct {
		Files []struct{ Path string }
	}
	r.json(t, &imp)
	require.Len(t, imp.Files, 1)
	assert.Equal(t, "config.yml", imp.Files[0].Path)
	assert.Equal(t, 400, e.do("POST", "/api/v1/agent-config/import", c, []byte("garbage")).status)

	require.Equal(t, 204, e.do("DELETE", "/api/v1/agent-config/files/config.yml", c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/agent-config/files/config.yml", c, nil).status)
}
