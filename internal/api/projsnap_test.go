package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M11a 2.7：项目的 snapshots / warmOrbs 读写与校验，快照列表与清除（200 / 204 / 404），项目与 provider 删除时退役快照。
func TestProjectSnapshotsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	ctx := context.Background()

	// 缺省：快照开、预热 0；warmOrbs 越界 400
	assert.Equal(t, 400, e.do("POST", "/api/v1/projects", c, map[string]any{"name": "x", "warmOrbs": 4}).status)
	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "app"})
	require.Equal(t, 201, r.status, string(r.body))
	var p apigen.Project
	r.json(t, &p)
	assert.True(t, p.Snapshots)
	assert.Equal(t, 0, p.WarmOrbs)
	assert.Equal(t, 400, e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"warmOrbs": 4}).status)
	assert.Equal(t, 400, e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"warmOrbs": -1}).status)
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"warmOrbs": 2, "snapshots": false})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &p)
	assert.False(t, p.Snapshots)
	assert.Equal(t, 2, p.WarmOrbs)
	// 不带这两项的 PATCH 不改动它们
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"name": "app2"})
	require.Equal(t, 200, r.status)
	r.json(t, &p)
	assert.False(t, p.Snapshots)
	assert.Equal(t, 2, p.WarmOrbs)
	r = e.do("POST", "/api/v1/projects", c, map[string]any{"name": "warm", "warmOrbs": 3, "snapshots": false})
	require.Equal(t, 201, r.status, string(r.body))
	var p2 apigen.Project
	r.json(t, &p2)
	assert.Equal(t, 3, p2.WarmOrbs)
	assert.False(t, p2.Snapshots)

	// 快照行：两种类型各一份 ready + 一份 building
	provID := ids.New("prov")
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "local", TokenHash: ids.Hash(provID), CreatedAt: 1}))
	mk := func(typ string, publish bool, created int64) string {
		id := ids.Snapshot()
		_, err := e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{
			ID: id, ProjectID: p.ID, ProviderID: provID, Type: typ, ConfigHash: "h", LineageAt: 1, CreatedAt: created,
		})
		require.NoError(t, err)
		if publish {
			_, err = e.st.Q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: created, SizeBytes: new(int64(42)), BaseCommit: "abc1234def", ID: id})
			require.NoError(t, err)
		}
		return id
	}
	small := mk("small", true, 10)
	large := mk("large", true, 20)
	building := mk("small", false, 30)

	assert.Equal(t, 404, e.do("GET", "/api/v1/projects/prj_nope/snapshots", c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/projects/prj_nope/snapshots", c, nil).status)
	r = e.do("GET", "/api/v1/projects/"+p.ID+"/snapshots", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list apigen.ProjectSnapshotList
	r.json(t, &list)
	require.Len(t, list.Snapshots, 3)
	assert.Equal(t, building, list.Snapshots[0].ID, "按 createdAt 倒序")
	assert.Equal(t, apigen.ProjectSnapshotStateBuilding, list.Snapshots[0].State)
	assert.Nil(t, list.Snapshots[0].BaseCommit)
	assert.Equal(t, small, list.Snapshots[2].ID)
	assert.Equal(t, "local", *list.Snapshots[2].ProviderName)
	assert.Equal(t, "abc1234def", *list.Snapshots[2].BaseCommit)
	assert.EqualValues(t, 42, *list.Snapshots[2].SizeBytes)

	// 按类型清除：只退役该类型的 ready（manual）
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+p.ID+"/snapshots?type=small", c, nil).status)
	get := func(id string) db.ProjectSnapshot {
		row, err := e.st.Q.GetProjectSnapshot(ctx, id)
		require.NoError(t, err)
		return row
	}
	assert.Equal(t, "retired", get(small).State)
	assert.Equal(t, "manual", *get(small).RetireReason)
	assert.Equal(t, "ready", get(large).State)
	assert.Equal(t, "building", get(building).State)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+p.ID+"/snapshots", c, nil).status)
	assert.Equal(t, "retired", get(large).State)

	// provider 删除（仍被快照引用 → 停用）时退役它的快照；项目删除时同样
	again := mk("medium", true, 40)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/providers/"+provID, c, nil).status)
	assert.Equal(t, "provider_removed", *get(again).RetireReason)
	prov, err := e.st.Q.GetProvider(ctx, provID)
	require.NoError(t, err)
	assert.NotNil(t, prov.DisabledAt)

	_, err = e.st.DB.Exec(`UPDATE providers SET disabled_at = NULL WHERE id = ?`, provID)
	require.NoError(t, err)
	last := mk("tiny", true, 50)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+p.ID, c, nil).status)
	row := get(last)
	assert.Equal(t, "project_deleted", *row.RetireReason)
	assert.Nil(t, row.ProjectID, "项目删除后行留给回收器")
}

// executor.kind = orb 指向预热池 orb → 409 orb_is_pooled。
func TestThreadOnPooledOrb409(t *testing.T) {
	e := newThreadsEnv(t)
	ctx := context.Background()
	provs, err := e.st.Q.ListProviders(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, provs)
	orbID := ids.Orb()
	require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orbID, ProviderID: provs[0].ID, Type: "medium", Image: "x",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash(orbID), CreatedAt: 1, PoolState: new("idle")}))
	code, body := e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "orb", "orbId": orbID}})
	assert.Equal(t, 409, code, string(body))
	assert.Contains(t, string(body), "orb_is_pooled")
}
