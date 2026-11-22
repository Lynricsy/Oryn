package headtail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuffer(t *testing.T) {
	const half = 2048
	w := New(half)
	_, _ = w.Write([]byte(strings.Repeat("h", half)))
	_, _ = w.Write([]byte("MID"))
	_, _ = w.Write([]byte(strings.Repeat("t", half)))
	assert.Equal(t, strings.Repeat("h", half)+"\n…（省略 3 字节）…\n"+strings.Repeat("t", half), w.String())

	small := New(half)
	_, _ = small.Write([]byte("abc"))
	_, _ = small.Write([]byte(strings.Repeat("x", half)))
	assert.Equal(t, "abc"+strings.Repeat("x", half), small.String())
}
