package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestGitIdentityFlags(t *testing.T) {
	id, err := gitIdentityFlags("", "")
	require.NoError(t, err)
	assert.Nil(t, id, "都缺省 = 不涉及")

	id, err = gitIdentityFlags(" Ada ", "ada@example.com")
	require.NoError(t, err)
	assert.Equal(t, &apigen.GitIdentity{Name: "Ada", Email: "ada@example.com"}, id)

	for _, c := range [][2]string{{"Ada", ""}, {"", "ada@example.com"}, {"Ada", "nope"}, {"A\nda", "ada@example.com"}} {
		_, err := gitIdentityFlags(c[0], c[1])
		assert.Error(t, err, "%q", c)
	}
}
