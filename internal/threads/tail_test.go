package threads

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// seedEntries 直接在库里建一个无 orb 的线程与 n 条条目（seq 1..n）。
func (f *fixture) seedEntries(id string, n int) {
	f.t.Helper()
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "seeded", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	for i := range n {
		_, err := f.st.Q.UpsertEntry(f.ctx, db.UpsertEntryParams{ThreadID: id, File: "s.jsonl", EntryID: fmt.Sprintf("e%04d", i), Type: "message", Payload: []byte(`{"type":"message"}`)})
		require.NoError(f.t, err)
	}
}

// M19：向前翻页按 seq 升序返回、翻到头 hasMore=false；TailStart 给出最后 n 条之前的 seq。
func TestEntriesBeforeAndTailStart(t *testing.T) {
	f := newFixture(t, Config{})
	f.seedEntries("T-tail", 1000)

	start, err := f.s.TailStart(f.ctx, "T-tail", 100)
	require.NoError(t, err)
	assert.EqualValues(t, 900, start)
	start, err = f.s.TailStart(f.ctx, "T-tail", 2000)
	require.NoError(t, err)
	assert.EqualValues(t, 0, start, "不足 n 条不开窗")

	before := int64(901)
	next := int64(900)
	pages := 0
	for {
		page, err := f.s.EntriesBefore(f.ctx, "T-tail", before, 500)
		require.NoError(t, err)
		require.NotEmpty(t, page.Entries)
		assert.EqualValues(t, 1000, page.LastSeq)
		for i := len(page.Entries) - 1; i >= 0; i-- {
			require.Equal(t, next, page.Entries[i].Seq, "页内升序、页间无缺口")
			next--
		}
		pages++
		before = page.Entries[0].Seq
		if !page.HasMore {
			break
		}
	}
	assert.Equal(t, 2, pages)
	assert.EqualValues(t, 0, next)

	page, err := f.s.EntriesBefore(f.ctx, "T-tail", 1, 500)
	require.NoError(t, err)
	assert.Empty(t, page.Entries)
	assert.False(t, page.HasMore)

	_, err = f.s.EntriesBefore(f.ctx, "T-missing", 10, 0)
	assert.ErrorIs(t, err, ErrNotFound)
}
