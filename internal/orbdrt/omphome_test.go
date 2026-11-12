package orbdrt

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 配置中移除的文件（如扩展）在下一次 setup/resume 时从 orb 删除；omp 自己写的文件不受影响。
func TestWriteOmpHomePrunesRemovedFiles(t *testing.T) {
	r := &Runtime{home: t.TempDir()}
	f := func(p string) apigen.LinkCtlOmpHomeFile {
		return apigen.LinkCtlOmpHomeFile{Path: p, Mode: 0o644, ContentB64: base64.StdEncoding.EncodeToString([]byte(p))}
	}
	base := filepath.Join(r.home, ".omp", "agent")
	require.NoError(t, r.writeOmpHome([]apigen.LinkCtlOmpHomeFile{f("config.yml"), f("extensions/notify/index.ts")}))
	require.NoError(t, os.WriteFile(filepath.Join(base, "omp-owned.json"), []byte("x"), 0o644))

	require.NoError(t, r.writeOmpHome([]apigen.LinkCtlOmpHomeFile{f("config.yml")}))
	assert.FileExists(t, filepath.Join(base, "config.yml"))
	assert.NoFileExists(t, filepath.Join(base, "extensions/notify/index.ts"))
	assert.FileExists(t, filepath.Join(base, "omp-owned.json"), "不在清单中的文件不删")
}
