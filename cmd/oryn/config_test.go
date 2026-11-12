package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullTarget(t *testing.T) {
	dir := t.TempDir()

	dst, renamed, err := pullTarget(dir, "models.yml", false)
	require.NoError(t, err)
	assert.True(t, renamed)
	assert.Equal(t, filepath.Join(dir, "models.oryn.yml"), dst)

	dst, renamed, err = pullTarget(dir, "models.yml", true)
	require.NoError(t, err)
	assert.False(t, renamed)
	assert.Equal(t, filepath.Join(dir, "models.yml"), dst)

	dst, _, err = pullTarget(dir, "skills/a/SKILL.md", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "skills", "a", "SKILL.md"), dst)

	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", "..", "", "sessions/a.jsonl"} {
		_, _, err := pullTarget(dir, bad, false)
		assert.Error(t, err, bad)
	}
}

func TestPushExclusions(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"config.yml", "extensions/omp-notify/index.ts", "extensions/keep.ts", "omp-notify.json", "sessions/x.jsonl"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".orynignore"), []byte("# 通知插件\nextensions/omp-notify/\n"), 0o644))
	ignore, err := readOrynIgnore(dir)
	require.NoError(t, err)
	files, err := collectPushFiles(dir, append(ignore, "omp-notify*"))
	require.NoError(t, err)
	var rels []string
	for _, f := range files {
		rels = append(rels, f.rel)
	}
	assert.Equal(t, []string{"config.yml", "extensions/keep.ts"}, rels)
}
