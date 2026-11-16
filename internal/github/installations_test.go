package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncInstallations(t *testing.T) {
	e := newEnv(t)
	assert.Equal(t, "github_not_connected", codeOf(e.s.SyncInstallations(e.ctx)))

	e.installApp()
	e.fake.AddInstallation(10, "acme")
	org := e.fake.AddInstallation(11, "widgets-org")
	e.fake.Lock()
	org.AccountType, org.RepositorySelection, org.Suspended = "Organization", "selected", true
	e.fake.Unlock()

	require.NoError(t, e.s.SyncInstallations(e.ctx))
	rows, err := e.st.Q.ListGithubInstallations(e.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "acme", rows[0].AccountLogin)
	assert.False(t, rows[0].Suspended)
	assert.Equal(t, "Organization", rows[1].AccountType)
	assert.Equal(t, "selected", rows[1].RepositorySelection)
	assert.True(t, rows[1].Suspended)
	assert.Positive(t, e.changes)

	e.fake.RemoveInstallation(10)
	require.NoError(t, e.s.SyncInstallations(e.ctx))
	rows, err = e.st.Q.ListGithubInstallations(e.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 11, rows[0].ID)

	e.fake.RemoveInstallation(11)
	require.NoError(t, e.s.SyncInstallations(e.ctx))
	rows, err = e.st.Q.ListGithubInstallations(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestSyncInstallationsPaged(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	for i := range 150 {
		e.fake.AddInstallation(int64(100+i), "acct")
	}
	require.NoError(t, e.s.SyncInstallations(e.ctx))
	rows, err := e.st.Q.ListGithubInstallations(e.ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 150)
	assert.Len(t, e.fake.CallsTo("GET", "/app/installations"), 2)
}
