package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunArgs(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 0, run([]string{"--version"}, &out, &errb))
	assert.Contains(t, out.String(), "orbd ")

	assert.Equal(t, 2, run(nil, &out, &errb))
	assert.Equal(t, 2, run([]string{"bogus"}, &out, &errb))

	t.Setenv("ORYN_URL", "")
	t.Setenv("ORYN_ORB_ID", "")
	t.Setenv("ORYN_ORB_TOKEN", "")
	errb.Reset()
	assert.Equal(t, 2, run([]string{"run"}, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB_TOKEN")
}
