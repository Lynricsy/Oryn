package boxcfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDocExamples(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want Config
	}{
		"single app": {
			yaml: "build: pnpm install --frozen-lockfile && pnpm build\nsource: false\nartifacts: [build, package.json]\nservices:\n  web:\n    command: node build\n",
			want: Config{Build: "pnpm install --frozen-lockfile && pnpm build", Artifacts: []string{"build", "package.json"},
				Services: []Service{{Name: "web", Command: "node build"}}},
		},
		"runtime artifact": {
			yaml: "build: |\n  mkdir -p runtime\nsource: false\nartifacts: [runtime, server.mjs]\nservices:\n  web:\n    command: ./runtime/node server.mjs\n",
			want: Config{Build: "mkdir -p runtime\n", Artifacts: []string{"runtime", "server.mjs"},
				Services: []Service{{Name: "web", Command: "./runtime/node server.mjs"}}},
		},
		"monorepo include": {
			yaml: "build: pnpm install --frozen-lockfile && pnpm --filter crm build\ninclude:\n  - ../packages/ui\nservices:\n  web:\n    command: node build\n",
			want: Config{Build: "pnpm install --frozen-lockfile && pnpm --filter crm build", Source: true, Include: []string{"../packages/ui"},
				Services: []Service{{Name: "web", Command: "node build"}}},
		},
		"cwd inside artifact, order kept": {
			yaml: "source: false\nartifacts: [dist]\nservices:\n  web:\n    command: ./fakeapp\n    cwd: dist\n  worker:\n    command: ./w\n    cwd: ./dist/bin\n",
			want: Config{Artifacts: []string{"dist"}, Services: []Service{
				{Name: "web", Command: "./fakeapp", Cwd: "dist"}, {Name: "worker", Command: "./w", Cwd: "dist/bin"}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, warns, err := Parse([]byte(tc.yaml))
			require.NoError(t, err)
			assert.Empty(t, warns)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseWarnings(t *testing.T) {
	got, warns, err := Parse([]byte("artifacts: [dist]\nfoo: 1\nservices:\n  web:\n    command: x\n"))
	require.NoError(t, err)
	assert.True(t, got.Source)
	assert.Nil(t, got.Artifacts)
	assert.Len(t, warns, 2)
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no services":         "build: x\n",
		"services not map":    "services: [web]\n",
		"too many services":   "services:\n  a: {command: x}\n  b: {command: x}\n  c: {command: x}\n  d: {command: x}\n  e: {command: x}\n",
		"bad name":            "services:\n  Web: {command: x}\n",
		"double dash name":    "services:\n  a--b: {command: x}\n",
		"long name":           "services:\n  abcdefghijk: {command: x}\n",
		"empty command":       "services:\n  web: {command: ''}\n",
		"absolute cwd":        "services:\n  web: {command: x, cwd: /tmp}\n",
		"cwd escapes":         "services:\n  web: {command: x, cwd: ../x}\n",
		"artifact escapes":    "source: false\nartifacts: [../x]\nservices:\n  web: {command: x}\n",
		"absolute include":    "include: [/etc]\nservices:\n  web: {command: x}\n",
		"no artifacts":        "source: false\nservices:\n  web: {command: x}\n",
		"cwd outside":         "source: false\nartifacts: [dist]\nservices:\n  web: {command: x, cwd: src}\n",
		"cwd prefix not dir":  "source: false\nartifacts: [dist]\nservices:\n  web: {command: x, cwd: dist2}\n",
		"not yaml":            "services: [\n",
		"top level not a map": "- a\n",
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Parse([]byte(y))
			assert.Error(t, err)
		})
	}
}

func TestFind(t *testing.T) {
	repo := t.TempDir()
	for _, f := range []string{".oryn/box.yaml", "crm/.amp/box.yaml", "crm/src/x"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, f), nil, 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "status"), 0o755))
	root, file, err := Find(repo, filepath.Join(repo, "crm", "src"))
	require.NoError(t, err)
	assert.Equal(t, "crm", root)
	assert.Equal(t, filepath.Join(repo, "crm/.amp/box.yaml"), file)
	root, _, err = Find(repo, filepath.Join(repo, "status"))
	require.NoError(t, err)
	assert.Equal(t, "", root)

	empty := t.TempDir()
	_, _, err = Find(empty, empty)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = Find(repo, t.TempDir())
	assert.Error(t, err)
}

func TestIncludeDirs(t *testing.T) {
	got, err := IncludeDirs("crm", []string{"../packages/ui", "../shared"})
	require.NoError(t, err)
	assert.Equal(t, []string{"packages/ui", "shared"}, got)
	_, err = IncludeDirs("crm", []string{"../../x"})
	assert.Error(t, err)
	_, err = IncludeDirs("", []string{"../x"})
	assert.Error(t, err)
}

func TestTargets(t *testing.T) {
	repo := t.TempDir()
	got, err := LoadTargets(repo)
	require.NoError(t, err)
	assert.Empty(t, got.Targets)
	got.Targets["status"] = Target{On: "b1", Name: "status", Root: "status"}
	got.Targets["crm"] = Target{On: "b1", Name: "tiny-crm", Root: "crm", Private: true}
	require.NoError(t, SaveTargets(repo, got))
	data, err := os.ReadFile(filepath.Join(repo, TargetsFile))
	require.NoError(t, err)
	assert.Equal(t, "{\n\t\"targets\": {\n\t\t\"crm\": {\n\t\t\t\"on\": \"b1\",\n\t\t\t\"name\": \"tiny-crm\",\n\t\t\t\"root\": \"crm\",\n\t\t\t\"private\": true\n\t\t},\n\t\t\"status\": {\n\t\t\t\"on\": \"b1\",\n\t\t\t\"name\": \"status\",\n\t\t\t\"root\": \"status\"\n\t\t}\n\t}\n}\n", string(data))
	back, err := LoadTargets(repo)
	require.NoError(t, err)
	assert.Equal(t, got, back)
}

func TestTargetKey(t *testing.T) {
	assert.Equal(t, "default", TargetKey(""))
	assert.Equal(t, "crm", TargetKey("crm"))
	assert.Equal(t, "web", TargetKey("apps/web"))
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Acme/Wiki.git":                  "github.com/acme/wiki",
		"https://user:tok@GitHub.com/Acme/Wiki.git":     "github.com/acme/wiki",
		"https://github.com/acme/wiki/":                 "github.com/acme/wiki",
		"ssh://git@gitlab.example.com:2222/g/sub/r.git": "gitlab.example.com/g/sub/r",
		"github.com:acme/wiki":                          "github.com/acme/wiki",
	} {
		assert.Equal(t, want, NormalizeRepo(in), in)
	}
}
