package jobs

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M11a 2.4（控制面侧）：hello 的 baseImageId 写进 providers.base_image_id（每次覆盖，缺省 = 空）；
// complete 的 result 存进 jobs.result，Wait 返回的行能读到；失败的尝试不留 result。
func TestBaseImageAndJobResult(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	h := hello("a")
	h.BaseImageID = new("sha256:img1")
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", h, nil))
	p, err := f.st.Q.GetProvider(ctx, f.prov.ID)
	require.NoError(t, err)
	assert.Equal(t, "sha256:img1", p.BaseImageID)
	// 旧 provider（不报 baseImageId）→ 空 = 未知
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	p, err = f.st.Q.GetProvider(ctx, f.prov.ID)
	require.NoError(t, err)
	assert.Empty(t, p.BaseImageID)

	claim := func() apigen.ProviderJob {
		var r apigen.ProviderClaimResponse
		require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a", WaitSeconds: 1}, &r))
		return r.Job
	}

	job, err := f.q.Enqueue(ctx, f.prov.ID, "", OpSnapshot, apigen.JobSnapshotParams{SnapshotID: "oryn-snap-x"})
	require.NoError(t, err)
	// 第一次尝试失败（带 result 也不保存），重排后成功
	j := claim()
	require.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/"+j.ID+"/complete", apigen.ProviderCompleteRequest{
		ClaimGen: j.ClaimGen, Ok: false, Error: new("boom"), Result: &apigen.JobResult{SnapshotBytes: new(int64(1))},
	}, nil))
	j = claim()
	require.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/"+j.ID+"/complete", apigen.ProviderCompleteRequest{
		ClaimGen: j.ClaimGen, Ok: true, Result: &apigen.JobResult{SnapshotBytes: new(int64(123456))},
	}, nil))
	done, err := f.q.Wait(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateSucceeded, done.State)
	require.NotNil(t, ResultOf(done).SnapshotBytes)
	assert.EqualValues(t, 123456, *ResultOf(done).SnapshotBytes)

	// 不带 result 的任务：jobs.result 为空，ResultOf 为零值
	job2, err := f.q.Enqueue(ctx, f.prov.ID, "", OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: "oryn-snap-x"})
	require.NoError(t, err)
	j = claim()
	require.Equal(t, job2.ID, j.ID)
	require.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/"+j.ID+"/complete", apigen.ProviderCompleteRequest{ClaimGen: j.ClaimGen, Ok: true}, nil))
	done, err = f.q.Wait(ctx, job2.ID)
	require.NoError(t, err)
	assert.Nil(t, done.Result)
	assert.Nil(t, ResultOf(done).SnapshotBytes)
}
