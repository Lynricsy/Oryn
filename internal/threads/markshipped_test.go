package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkShipped(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.thread(f.orb("ready"), "idle")

	require.NoError(t, f.s.MarkShipped(f.ctx, w.id, 1234))
	got, err := f.s.Get(f.ctx, w.id)
	require.NoError(t, err)
	require.NotNil(t, got.ShippedAt)
	assert.EqualValues(t, 1234, *got.ShippedAt)
	assert.Nil(t, got.ArchivedAt, "合并即交付不归档")

	// 已有 shipped_at 时不改写
	require.NoError(t, f.s.MarkShipped(f.ctx, w.id, 9999))
	got, err = f.s.Get(f.ctx, w.id)
	require.NoError(t, err)
	assert.EqualValues(t, 1234, *got.ShippedAt)

	assert.ErrorIs(t, f.s.MarkShipped(f.ctx, "T-missing", 1), ErrNotFound)
}
