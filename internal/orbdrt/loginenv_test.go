package orbdrt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestLoginEnvAppliesProfile(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export FROM_PROFILE=yes\nPATH=\"$HOME/tools:$PATH\"\nunset DROP_ME\n"), 0o644))
	base := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin", "KEEP": "1", "DROP_ME": "x"}
	env := loginEnv(context.Background(), home, base)
	assert.Equal(t, "yes", env["FROM_PROFILE"])
	assert.True(t, strings.HasPrefix(env["PATH"], home+"/tools:"), env["PATH"])
	assert.Equal(t, "1", env["KEEP"])
	assert.NotContains(t, env, "DROP_ME", "登录 shell 里删掉的变量不带出")
	assert.NotContains(t, env, "SHLVL")
}

// 前一个 setup 脚本写进 ~/.profile 的环境，对后一个脚本可见（agent 同理在启动时取登录环境）。
func TestSetupScriptsSeeProfileWrittenByEarlierScript(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	project := `echo 'export ORYN_TEST_MARK=from-profile' >> "$HOME/.profile"`
	workdir := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, ".oryn"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workdir, ".oryn", "setup"),
		[]byte("#!/usr/bin/env bash\necho \"mark=${ORYN_TEST_MARK:-unset}\" > \"$HOME/mark.txt\"\n"), 0o755))
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: &project}}, &res))
	require.True(t, res.Ok, "%v", res.SetupError)
	b, err := os.ReadFile(filepath.Join(rt.home, "mark.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mark=from-profile\n", string(b))
}
