package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestProjectSnapshotsLsTable(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	now := time.UnixMilli(1_800_000_000_000)
	f.json("GET /api/v1/projects/prj_A/snapshots", 200, apigen.ProjectSnapshotList{Snapshots: []apigen.ProjectSnapshot{
		{ID: "oryn-snap-1", ProviderID: "prov_1", ProviderName: new("local"), Type: "medium", State: apigen.ProjectSnapshotStateReady,
			BaseCommit: new("abc1234def5678"), LineageAt: now.Add(-(2*time.Hour + 5*time.Minute)).UnixMilli(),
			SizeBytes: new(int64(3 << 30)), UseCount: 4},
		{ID: "oryn-snap-0", ProviderID: "prov_1", Type: "small", State: apigen.ProjectSnapshotStateRetired,
			RetireReason: new("manual"), LineageAt: now.Add(-90 * time.Second).UnixMilli()},
	}})
	var out bytes.Buffer
	require.NoError(t, projectSnapshotsLs(context.Background(), f.client(), "demo", false, now, &out))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"TYPE", "PROVIDER", "STATE", "REASON", "COMMIT", "AGE", "SIZE", "USES"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"medium", "local", "ready", "-", "abc1234", "2h5m", "3.0GiB", "4"}, strings.Fields(lines[1]))
	// 无 providerName 时回落到 id；无提交、无大小显示 "-"
	assert.Equal(t, []string{"small", "prov_1", "retired", "manual", "-", "1m30s", "-", "0"}, strings.Fields(lines[2]))

	out.Reset()
	require.NoError(t, projectSnapshotsLs(context.Background(), f.client(), "prj_A", true, now, &out))
	assert.Contains(t, out.String(), `"oryn-snap-1"`)

	_, err := resolveProject(context.Background(), f.client(), "nope")
	assert.ErrorContains(t, err, "不存在")
}

// S-P2-5：baseCommit 来自 orb（不可信），表格里不能出现控制字符（\x1b[2J\x1b[H 正好 7 字节，原先会原样打印）。
func TestProjectSnapshotsLsStripsControlChars(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	now := time.UnixMilli(1_800_000_000_000)
	f.json("GET /api/v1/projects/prj_A/snapshots", 200, apigen.ProjectSnapshotList{Snapshots: []apigen.ProjectSnapshot{
		{ID: "oryn-snap-1", ProviderID: "prov_1", ProviderName: new("lo\x1b]0;x\acal"), Type: "med\tium", State: apigen.ProjectSnapshotStateRetired,
			RetireReason: new("\u009b2Jquota"), BaseCommit: new("\x1b[2J\x1b[Habcdef"), LineageAt: now.UnixMilli()},
		{ID: "oryn-snap-2", ProviderID: "prov_1", Type: "small", State: apigen.ProjectSnapshotStateReady,
			BaseCommit: new("\x1b\x07\n"), LineageAt: now.UnixMilli()},
	}})
	var out bytes.Buffer
	require.NoError(t, projectSnapshotsLs(context.Background(), f.client(), "demo", false, now, &out))
	for _, r := range out.String() {
		if r != '\n' {
			assert.False(t, isTermControl(r), "输出含控制字符 %q: %q", r, out.String())
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"med", "ium", "lo", "]0;x", "cal", "retired", "2Jquota", "[2J", "[Ha", "0s", "-", "0"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"small", "prov_1", "ready", "-", "-", "0s", "-", "0"}, strings.Fields(lines[2]), "清理后为空的提交显示 -")
}

func TestProjectSnapshotsRmRequest(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	f.json("DELETE /api/v1/projects/prj_A/snapshots", 204, nil)
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, projectSnapshotsRm(ctx, f.client(), "demo", "", &out))
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	assert.Equal(t, "DELETE /api/v1/projects/prj_A/snapshots", last.Method+" "+last.Path)
	assert.Empty(t, last.RawQuery)
	assert.Empty(t, last.Body)
	assert.Contains(t, out.String(), "全部快照")

	require.NoError(t, projectSnapshotsRm(ctx, f.client(), "demo", "large docker", &out))
	reqs = f.requests()
	last = reqs[len(reqs)-1]
	assert.Equal(t, "DELETE /api/v1/projects/prj_A/snapshots", last.Method+" "+last.Path)
	assert.Equal(t, "type=large+docker", last.RawQuery)

	// 项目不存在：不发 DELETE
	n := len(f.requests())
	assert.Error(t, projectSnapshotsRm(ctx, f.client(), "nope", "", &out))
	reqs = f.requests()
	require.Len(t, reqs, n+1)
	assert.Equal(t, "GET", reqs[n].Method)
}

func TestFmtBytes(t *testing.T) {
	assert.Equal(t, "812B", fmtBytes(812))
	assert.Equal(t, "4.0KiB", fmtBytes(4096))
	assert.Equal(t, "1.5MiB", fmtBytes(3<<19))
	assert.Equal(t, "1.3GiB", fmtBytes(1_400_000_000))
}
