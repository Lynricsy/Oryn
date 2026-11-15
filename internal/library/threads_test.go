package library

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func TestSandboxPath(t *testing.T) {
	f := File{ID: "afl_1", Name: "a b.txt", Scope: ScopeConversation, ThreadID: "T-1"}
	assert.Equal(t, "~/library/threads/T-1/afl_1/a b.txt", SandboxPath(f))
	f.Scope = ScopeShared
	assert.Equal(t, "~/library/shared/afl_1/a b.txt", SandboxPath(f))
}

// ThreadFiles 在调用方的事务里绑定，按顺序返回沙箱路径；业务错误转成 *threads.Error，出错时全部不绑定。
func TestThreadFilesBindTx(t *testing.T) {
	s, st, _, _ := newSvc(t)
	ctx := context.Background()
	tid, other := ids.Thread(), ids.Thread()
	mkThread(t, st, tid)
	mkThread(t, st, other)
	a, err := s.Upload(ctx, "a.txt", "", strings.NewReader("aaa"))
	require.NoError(t, err)
	b, err := s.Put(ctx, PutParams{Name: "b.md", ThreadID: other, Scope: ScopeShared, Source: SourceOrb, R: strings.NewReader("b")})
	require.NoError(t, err)
	mine, err := s.Upload(ctx, "c.txt", other, strings.NewReader("c"))
	require.NoError(t, err)

	tf := ThreadFiles{S: s}
	var got []threads.DeliveredAttachment
	require.NoError(t, st.Tx(ctx, func(q *db.Queries) error {
		var err error
		got, err = tf.BindTx(ctx, q, tid, []string{a.ID, b.ID})
		return err
	}))
	assert.Equal(t, []threads.DeliveredAttachment{
		{ID: a.ID, Name: "a.txt", Mime: a.Mime, Size: 3, Path: "~/library/threads/" + tid + "/" + a.ID + "/a.txt"},
		{ID: b.ID, Name: "b.md", Mime: b.Mime, Size: 1, Path: "~/library/shared/" + b.ID + "/b.md"},
	}, got)
	f, err := s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, f.Bound)
	assert.Equal(t, tid, f.ThreadID)

	fresh, err := s.Upload(ctx, "d.txt", "", strings.NewReader("d"))
	require.NoError(t, err)
	for id, code := range map[string]string{mine.ID: "invalid", "afl_00000000000000000000000000": "not_found"} {
		err := st.Tx(ctx, func(q *db.Queries) error {
			_, err := tf.BindTx(ctx, q, tid, []string{fresh.ID, id})
			return err
		})
		var te *threads.Error
		require.ErrorAs(t, err, &te, id)
		assert.Equal(t, code, te.Code, id)
	}
	f, err = s.Get(ctx, fresh.ID)
	require.NoError(t, err)
	assert.False(t, f.Bound, "出错时全部不绑定")
}
