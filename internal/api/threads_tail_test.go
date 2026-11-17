package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19：`entries?before=` 向前翻页按 seq 升序、翻到头 hasMore=false；after 与 before 同给回 400。
func TestThreadEntriesBefore(t *testing.T) {
	e := newThreadsEnv(t)
	ctx := context.Background()
	const id = "T-before"
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "seeded", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	for i := range 30 {
		_, err := e.st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: id, File: "s.jsonl", EntryID: fmt.Sprintf("e%02d", i), Type: "message", Payload: []byte(`{"type":"message"}`)})
		require.NoError(t, err)
	}

	code, body := e.do("GET", "/api/v1/threads/"+id+"/entries?before=21&limit=15", nil)
	require.Equal(t, 200, code, string(body))
	var page threads.EntryPage
	require.NoError(t, json.Unmarshal(body, &page))
	require.Len(t, page.Entries, 15)
	assert.EqualValues(t, 6, page.Entries[0].Seq)
	assert.EqualValues(t, 20, page.Entries[14].Seq)
	assert.True(t, page.HasMore)
	assert.EqualValues(t, 30, page.LastSeq)

	code, body = e.do("GET", "/api/v1/threads/"+id+"/entries?before=6&limit=15", nil)
	require.Equal(t, 200, code, string(body))
	page = threads.EntryPage{}
	require.NoError(t, json.Unmarshal(body, &page))
	require.Len(t, page.Entries, 5)
	assert.EqualValues(t, 1, page.Entries[0].Seq)
	assert.False(t, page.HasMore)

	code, _ = e.do("GET", "/api/v1/threads/"+id+"/entries?before=6&after=1", nil)
	assert.Equal(t, 400, code)
}
