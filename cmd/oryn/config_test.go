package main

import (
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
