package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// doResult 是后台 Do 调用的结果。
type doResult struct {
	rep *FSReply
	err error
}

func (f *fixture) doAsync(ctx context.Context, req apigen.ProviderFsRequest) <-chan doResult {
	ch := make(chan doResult, 1)
	go func() {
		rep, err := f.q.FSRelay().Do(ctx, f.prov.ID, req)
		ch <- doResult{rep, err}
	}()
	return ch
}

// claimFs 以 provider 身份长轮询认领一个文件请求。
func (f *fixture) claimFs(t *testing.T, instance string) (apigen.ProviderFsRequest, int) {
	t.Helper()
	var res apigen.ProviderFsClaimResponse
	code := f.post(t, "/provider/v1/fs/claim", apigen.ProviderFsClaimRequest{InstanceID: instance, WaitSeconds: 5}, &res)
	return res.Request, code
}

// replyFs 以 token 身份回应文件请求 id，返回 reply 请求的状态码。
func (f *fixture) replyFs(t *testing.T, token, id string, status int, hdr map[string]string, body io.Reader) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/provider/v1/fs/"+id+"/reply", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Oryn-Status", strconv.Itoa(status))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res.StatusCode
}

func (r *FSRelay) sizes() (pending, inflight int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.pending {
		pending += len(q)
	}
	return pending, len(r.inflight)
}

// 认领 → 回应：请求字段原样到达 provider；回应的状态码、选定头与请求体流式交给调用方；调用方 Done 后 reply 得 204。
func TestFSRelayClaimReplyStreamed(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	path, rng := "/home/user/a.txt", "bytes=0-9"
	done := f.doAsync(t.Context(), apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Read, Path: &path, Range: &rng})

	got, code := f.claimFs(t, "a")
	require.Equal(t, http.StatusOK, code)
	assert.True(t, strings.HasPrefix(got.ID, "fsr_"), got.ID)
	assert.Equal(t, apigen.Read, got.Op)
	assert.Equal(t, "orb-x", got.OrbID)
	assert.Equal(t, path, *got.Path)
	assert.Equal(t, rng, *got.Range)

	pr, pw := io.Pipe()
	replied := make(chan int, 1)
	go func() {
		replied <- f.replyFs(t, f.token, got.ID, http.StatusPartialContent, map[string]string{
			"Content-Type": "application/octet-stream", "Content-Range": "bytes 0-9/100", "ETag": `"1-100"`, "Set-Cookie": "x=1",
		}, pr)
	}()
	_, err := io.WriteString(pw, "hello")
	require.NoError(t, err)

	var res doResult
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Do 未返回")
	}
	require.NoError(t, res.err)
	rep := res.rep
	assert.Equal(t, http.StatusPartialContent, rep.Status)
	assert.Equal(t, "bytes 0-9/100", rep.Header.Get("Content-Range"))
	assert.Equal(t, `"1-100"`, rep.Header.Get("ETag"))
	assert.Empty(t, rep.Header.Get("Set-Cookie"))

	// 第一段在 provider 写完整个体之前就已可读（流式）
	buf := make([]byte, 5)
	_, err = io.ReadFull(rep.Body, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
	_, err = io.WriteString(pw, "world")
	require.NoError(t, err)
	require.NoError(t, pw.Close())
	rest, err := io.ReadAll(rep.Body)
	require.NoError(t, err)
	assert.Equal(t, "world", string(rest))

	select {
	case <-replied:
		t.Fatal("调用方 Done 之前 reply 不得返回")
	case <-time.After(50 * time.Millisecond):
	}
	rep.Done()
	rep.Done() // 重复调用无害
	assert.Equal(t, http.StatusNoContent, <-replied)
	p, inf := f.q.FSRelay().sizes()
	assert.Zero(t, p)
	assert.Zero(t, inf)

	// 同一请求不能回应两次
	assert.Equal(t, http.StatusNotFound, f.replyFs(t, f.token, got.ID, 200, nil, strings.NewReader("{}")))
}

// 已知长度的回应体带 Content-Length；没有请求时长轮询到期回 204；租约被别的实例持有 → 409。
func TestFSRelayClaimEmptyAndLease(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	assert.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/fs/claim", apigen.ProviderFsClaimRequest{InstanceID: "a", WaitSeconds: 0}, nil))
	assert.Equal(t, http.StatusConflict, f.post(t, "/provider/v1/fs/claim", apigen.ProviderFsClaimRequest{InstanceID: "b", WaitSeconds: 0}, nil))
	assert.Equal(t, http.StatusBadRequest, f.post(t, "/provider/v1/fs/claim", apigen.ProviderFsClaimRequest{WaitSeconds: 0}, nil))

	done := f.doAsync(t.Context(), apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots, Workdirs: []string{"/home/user/workspace"}})
	got, code := f.claimFs(t, "a")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/home/user/workspace"}, got.Workdirs)
	replied := make(chan int, 1)
	go func() {
		replied <- f.replyFs(t, f.token, got.ID, http.StatusServiceUnavailable, map[string]string{"Content-Type": "application/json", "Retry-After": "2"}, strings.NewReader(`{"code":"offline_indexing"}`))
	}()
	res := <-done
	require.NoError(t, res.err)
	assert.Equal(t, http.StatusServiceUnavailable, res.rep.Status)
	assert.Equal(t, "2", res.rep.Header.Get("Retry-After"))
	assert.Equal(t, "27", res.rep.Header.Get("Content-Length"))
	b, err := io.ReadAll(res.rep.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"code":"offline_indexing"}`, string(b))
	res.rep.Done()
	assert.Equal(t, http.StatusNoContent, <-replied)
}

// 入队后无人认领：ClaimTimeout 后 ErrProviderUnavailable，请求移出队列（之后的认领拿不到它）。
func TestFSRelayClaimTimeout(t *testing.T) {
	f := newFixture(t)
	f.q.FSRelay().ClaimTimeout = 50 * time.Millisecond
	_, err := f.q.FSRelay().Do(t.Context(), f.prov.ID, apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots})
	require.ErrorIs(t, err, ErrProviderUnavailable)
	p, inf := f.q.FSRelay().sizes()
	assert.Zero(t, p)
	assert.Zero(t, inf)
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	assert.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/fs/claim", apigen.ProviderFsClaimRequest{InstanceID: "a", WaitSeconds: 0}, nil))
}

// 用户请求在认领前结束：请求移出队列，Do 返回 ctx 的错误。
func TestFSRelayCancelBeforeClaim(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := f.doAsync(ctx, apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots})
	require.Eventually(t, func() bool { p, _ := f.q.FSRelay().sizes(); return p == 1 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	res := <-done
	require.ErrorIs(t, res.err, context.Canceled)
	p, inf := f.q.FSRelay().sizes()
	assert.Zero(t, p)
	assert.Zero(t, inf)
	_, ok := f.q.FSRelay().Claim(t.Context(), f.prov.ID, 0)
	assert.False(t, ok)
}

// 认领后未按时回应：Do 返回 ErrFSReplyTimeout，迟到的 reply 得 404；用户取消同理。
func TestFSRelayLateReply(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	f.q.FSRelay().ReplyTimeout = 50 * time.Millisecond
	done := f.doAsync(t.Context(), apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots})
	got, code := f.claimFs(t, "a")
	require.Equal(t, http.StatusOK, code)
	require.ErrorIs(t, (<-done).err, ErrFSReplyTimeout)
	assert.Equal(t, http.StatusNotFound, f.replyFs(t, f.token, got.ID, 200, nil, strings.NewReader("{}")))

	f.q.FSRelay().ReplyTimeout = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	done = f.doAsync(ctx, apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots})
	got, code = f.claimFs(t, "a")
	require.Equal(t, http.StatusOK, code)
	cancel()
	require.ErrorIs(t, (<-done).err, context.Canceled)
	assert.Equal(t, http.StatusNotFound, f.replyFs(t, f.token, got.ID, 200, nil, strings.NewReader("{}")))
	p, inf := f.q.FSRelay().sizes()
	assert.Zero(t, p)
	assert.Zero(t, inf)
	assert.Equal(t, http.StatusNotFound, f.replyFs(t, f.token, "fsr_nope", 200, nil, strings.NewReader("{}")))
}

// 别的 provider 不能回应（也认领不到）不属于自己的请求。
func TestFSRelayReplyFromOtherProvider(t *testing.T) {
	f := newFixture(t)
	other := ids.Token("opv")
	require.NoError(t, f.st.Q.CreateProvider(t.Context(), db.CreateProviderParams{ID: ids.New("prov"), Name: "other", TokenHash: ids.Hash(other), CreatedAt: 1}))
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	done := f.doAsync(t.Context(), apigen.ProviderFsRequest{OrbID: "orb-x", Op: apigen.Roots})
	got, code := f.claimFs(t, "a")
	require.Equal(t, http.StatusOK, code)

	assert.Equal(t, http.StatusNotFound, f.replyFs(t, other, got.ID, 200, nil, strings.NewReader("{}")))
	assert.Equal(t, http.StatusUnauthorized, f.replyFs(t, "opv_bogus", got.ID, 200, nil, strings.NewReader("{}")))

	replied := make(chan int, 1)
	go func() { replied <- f.replyFs(t, f.token, got.ID, 200, nil, strings.NewReader("{}")) }()
	res := <-done
	require.NoError(t, res.err)
	_, _ = io.Copy(io.Discard, res.rep.Body)
	res.rep.Done()
	assert.Equal(t, http.StatusNoContent, <-replied)
}

// provider 的 reply 请求结束后（调用方仍未读完），响应体读取失败而不是读到被回收的请求体。
func TestFSRelayReplyBodyClosed(t *testing.T) {
	b := &fsReplyBody{r: strings.NewReader("abc")}
	b.close()
	_, err := b.Read(make([]byte, 3))
	assert.True(t, errors.Is(err, errFSReplyClosed))
}
