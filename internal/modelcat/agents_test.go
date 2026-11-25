package modelcat

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

func TestBundledAgentsUnpackAndCache(t *testing.T) {
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)

	var calls atomic.Int32
	cat := New(Config{
		AgentsRun: func(_ context.Context, dir string) error {
			calls.Add(1)
			files := map[string]string{
				"scout.md":    "---\nname: scout\ndescription: Fast scout\nmodel: [\"@smol\"]\nthinkingLevel: medium\ntools: [read, grep]\n---\nInvestigate fast.\n",
				"reviewer.md": "---\nname: reviewer\ndescription: Reviewer\nmodel: \"@slow\"\n---\nFind real bugs.\n",
				"noname.md":   "---\ndescription: skip me\n---\n",
				"notes.txt":   "---\nname: ignored\n---\n",
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					return err
				}
			}
			return nil
		},
	}, home, nil)

	first, err := cat.BundledAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, "reviewer", first[0].Name)
	assert.Equal(t, "bundled", first[0].Source)
	assert.Equal(t, []string{"@slow"}, first[0].Model)
	assert.Equal(t, "Find real bugs.", first[0].SystemPrompt)

	assert.Equal(t, "scout", first[1].Name)
	assert.Equal(t, "medium", first[1].ThinkingLevel)
	assert.Equal(t, []string{"read", "grep"}, first[1].Tools)
	assert.Equal(t, int32(1), calls.Load())

	// 改写返回值不影响缓存，且二次调用命中缓存
	first[1].Tools[0] = "mutated"
	second, err := cat.BundledAgents(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "命中内存缓存")
	assert.Equal(t, []string{"read", "grep"}, second[1].Tools, "返回副本隔离缓存")
}

func TestBundledAgentsEmptyOmpPath(t *testing.T) {
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	cat := New(Config{}, home, nil)
	got, err := cat.BundledAgents(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got)
}
