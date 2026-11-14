package threads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nopOrbs 是只用于直接调用入库路径的 Orbs 替身。
type nopOrbs struct{}

func (nopOrbs) Create(context.Context, OrbCreate) (db.Orb, error) { return db.Orb{}, nil }
func (nopOrbs) Wake(context.Context, string) error                { return nil }
func (nopOrbs) Snapshot(context.Context, string) (string, error)  { return "", nil }
func (nopOrbs) Ctl(string) (*link.Ctl, bool)                      { return nil, false }
func (nopOrbs) HandleRequest(string, func(context.Context, string, json.RawMessage) (any, error)) {
}
func (nopOrbs) HandleNotify(string, func(context.Context, string, json.RawMessage)) {}
func (nopOrbs) HandleStream(string, func(context.Context, string, link.StreamHeader, *link.Stream)) {
}
func (nopOrbs) Subscribe(func(OrbEvent))                     {}
func (nopOrbs) TouchUser(context.Context, string)            {}
func (nopOrbs) TouchAgent(context.Context, string)           {}
func (nopOrbs) SetQuiescence(func(string, func() bool) bool) {}
func (nopOrbs) Kick(string, string)                          {}
func (nopOrbs) HasFeature(string, string) bool               { return false }

// fork_from 在途时出现的新主会话文件（其产物）不得作为源线程的新会话入库，也不得移动源线程的叶子；
// 同期出现的其它新文件在 fork 结束后补入库。
func TestForkFromProductNotIngested(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := New(Config{DataDir: t.TempDir()}, st, nopOrbs{})
	t.Cleanup(s.Close)
	const id, orb = "T-fork-src", "orb-fork-src"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "", TitleSource: "auto", State: StateIdle, SessionDir: "~/.oryn/threads/" + id}))
	th, err := s.get(ctx, id)
	require.NoError(t, err)
	th.mu.Lock()
	th.orbID = orb
	th.mu.Unlock()

	appendLines := func(file string, base int64, lines ...string) apigen.LinkCtlEntriesAppendResult {
		end := base
		for _, l := range lines {
			end += int64(len(l) + 1)
		}
		raw, _ := json.Marshal(apigen.LinkCtlEntriesAppendParams{ThreadID: id, File: file, BaseOffset: base, EndOffset: end, Lines: lines})
		res, err := s.onAppend(ctx, orb, raw)
		require.NoError(t, err)
		return res.(apigen.LinkCtlEntriesAppendResult)
	}
	u1 := `{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"q","timestamp":1}}`
	a1 := `{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[],"timestamp":2}}`
	require.True(t, appendLines("main.jsonl", 0, u1, a1).Accepted)

	th.mu.Lock()
	th.forking++
	th.mu.Unlock()
	// 产物（与源分支同 id 的副本）与另一个无关的新文件
	r := appendLines("fork.jsonl", 0, u1)
	assert.True(t, r.Accepted)
	r = appendLines("other.jsonl", 0, `{"type":"message","id":"o1","parentId":null,"message":{"role":"user","content":"x","timestamp":3}}`)
	assert.True(t, r.Accepted)
	th.mu.Lock()
	assert.Equal(t, "a1", *th.leaf, "暂存期间叶子不动")
	s.endForkLocked(ctx, th, "fork.jsonl")
	th.mu.Unlock()

	rows, err := st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: 0, Limit: 100})
	require.NoError(t, err)
	files := map[string]int{}
	for _, e := range rows {
		files[e.File]++
	}
	assert.Equal(t, map[string]int{"main.jsonl": 2, "other.jsonl": 1}, files)
	// 产物此后的追加被丢弃（游标停在暂存末尾）
	assert.True(t, appendLines("fork.jsonl", int64(len(u1)+1), a1).Accepted)
	rows, err = st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: 0, Limit: 100})
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}
