package orbdrt

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 录屏（M25 计划 3.1）：fakedesk 扮演 ffmpeg，写一个以 ftyp 开头的小文件，读到 stdin 的 q 后退出。

// tuneRecorder 在录制开始前改录制器的上限、等待时长与保留数（零值不改）。
func tuneRecorder(e *deskEnv, limit, grace time.Duration, keep int) *deskRecorder {
	rr := e.rt.desk.recorder()
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if limit > 0 {
		rr.limit = limit
	}
	if grace > 0 {
		rr.grace = grace
	}
	if keep > 0 {
		rr.keep = keep
	}
	return rr
}

func assertRecordingFile(t *testing.T, rec orbsock.DesktopRecording) {
	t.Helper()
	b, err := os.ReadFile(rec.Path)
	require.NoError(t, err)
	assert.Positive(t, rec.Bytes)
	assert.Equal(t, int64(len(b)), rec.Bytes)
	assert.Equal(t, "ftyp", string(b[:4]))
	assert.Positive(t, rec.Width)
	assert.Positive(t, rec.Height)
	assert.Zero(t, rec.Width%2, "even width for yuv420p")
	assert.Zero(t, rec.Height%2, "even height for yuv420p")
	assert.False(t, rec.Truncated)
}

// TestDeskRecordStartStop：本机路由走完 start → stop；重复 start 回 409；没在录时 stop 回 409。
func TestDeskRecordStartStop(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)

	resp, b := o.do(http.MethodPost, "/desktop/record/stop", nil, nil, nil)
	assert.Equal(t, "not_recording", o.errCode(resp, b, http.StatusConflict))

	var started orbsock.DesktopRecordStartResult
	o.json(http.MethodPost, "/desktop/record/start", nil, nil, http.StatusOK, &started)
	assert.Equal(t, e.home("recordings"), filepath.Dir(started.Path))
	assert.Regexp(t, recordNameRe, filepath.Base(started.Path))
	assert.True(t, e.rt.desk.recorder().active())

	resp, b = o.do(http.MethodPost, "/desktop/record/start", nil, nil, nil)
	assert.Equal(t, "already_recording", o.errCode(resp, b, http.StatusConflict))

	var rec orbsock.DesktopRecording
	o.json(http.MethodPost, "/desktop/record/stop", nil, nil, http.StatusOK, &rec)
	assert.Equal(t, started.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByAgent, rec.StoppedBy)
	assertRecordingFile(t, rec)
	assert.False(t, e.rt.desk.recorder().active())

	resp, b = o.do(http.MethodPost, "/desktop/record/stop", nil, nil, nil)
	assert.Equal(t, "not_recording", o.errCode(resp, b, http.StatusConflict), "an agent stop is not handed out twice")
}

// TestDeskRecordSock：orbd.sock 方法与本机路由共用实现。
func TestDeskRecordSock(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))

	raw, err := sockCall(t, e.rt, orbsock.MethodDesktopRecordStart, nil, nil)
	require.NoError(t, err)
	var started orbsock.DesktopRecordStartResult
	require.NoError(t, json.Unmarshal(raw, &started))

	_, err = sockCall(t, e.rt, orbsock.MethodDesktopRecordStart, nil, nil)
	assert.Equal(t, orbsock.CodeAlreadyRecording, sockErrCode(err))

	raw, err = sockCall(t, e.rt, orbsock.MethodDesktopRecordStop, nil, nil)
	require.NoError(t, err)
	var rec orbsock.DesktopRecording
	require.NoError(t, json.Unmarshal(raw, &rec))
	assert.Equal(t, started.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByAgent, rec.StoppedBy)
	assertRecordingFile(t, rec)

	_, err = sockCall(t, e.rt, orbsock.MethodDesktopRecordStop, nil, nil)
	assert.Equal(t, orbsock.CodeNotRecording, sockErrCode(err))
}

// TestDeskRecordLimit：到上限自动停止，下一次 stop 取走 StoppedBy=limit 的结果，再下一次回 409。
func TestDeskRecordLimit(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	rr := tuneRecorder(e, 200*time.Millisecond, 0, 0)

	var started orbsock.DesktopRecordStartResult
	o.json(http.MethodPost, "/desktop/record/start", nil, nil, http.StatusOK, &started)
	require.Eventually(t, func() bool { return !rr.active() }, 10*time.Second, 10*time.Millisecond)

	var rec orbsock.DesktopRecording
	o.json(http.MethodPost, "/desktop/record/stop", nil, nil, http.StatusOK, &rec)
	assert.Equal(t, started.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByLimit, rec.StoppedBy)
	assert.GreaterOrEqual(t, rec.DurationMs, int64(200))
	assertRecordingFile(t, rec)

	resp, b := o.do(http.MethodPost, "/desktop/record/stop", nil, nil, nil)
	assert.Equal(t, "not_recording", o.errCode(resp, b, http.StatusConflict), "the limit result is handed out once")
}

// TestDeskRecordDisplayLost：录制中显示失效，按 StoppedBy=error 收尾。
func TestDeskRecordDisplayLost(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	rr := tuneRecorder(e, 0, 2*time.Second, 0)

	started, err := rr.start(t.Context())
	require.NoError(t, err)
	require.NoError(t, syscall.Kill(e.xvncPid(), syscall.SIGKILL))
	e.wait("display lost", displayIs(false))
	require.Eventually(t, func() bool { return !rr.active() }, 10*time.Second, 10*time.Millisecond)

	rec, err := rr.stop(t.Context())
	require.NoError(t, err)
	assert.Equal(t, started.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByError, rec.StoppedBy)
	assertRecordingFile(t, rec)
}

// TestDeskRecordKeep：第 21 个录制产生后最旧的那个被删除；不是录制器写的文件不动。
func TestDeskRecordKeep(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	dir := e.home("recordings")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	var old []string
	for i := range recordKeep {
		p := filepath.Join(dir, fmt.Sprintf("20200101-0000%02d.mp4", i))
		require.NoError(t, os.WriteFile(p, []byte("ftyp old"), 0o644))
		old = append(old, p)
	}
	other := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(other, []byte("keep me"), 0o644))

	rr := e.rt.desk.recorder()
	started, err := rr.start(t.Context())
	require.NoError(t, err)
	rec, err := rr.stop(t.Context())
	require.NoError(t, err)
	assert.Equal(t, started.Path, rec.Path)

	_, err = os.Stat(old[0])
	assert.ErrorIs(t, err, os.ErrNotExist, "the oldest recording is removed")
	for _, p := range append(old[1:], rec.Path, other) {
		_, err := os.Stat(p)
		assert.NoError(t, err, p)
	}
}

// TestDeskRecordNaming：同一秒内的录制加序号，保留策略按时间戳与序号排序。
func TestDeskRecordNaming(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 22, 10, 11, 12, 0, time.FixedZone("UTC+8", 8*3600))
	var paths []string
	for range 3 {
		p, err := newRecordingPath(dir, now)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, nil, 0o644))
		paths = append(paths, filepath.Base(p))
	}
	assert.Equal(t, []string{"20261122-021112.mp4", "20261122-021112-2.mp4", "20261122-021112-3.mp4"}, paths)

	pruneRecordings(dir, 2, slog.New(slog.DiscardHandler))
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var left []string
	for _, e := range ents {
		left = append(left, e.Name())
	}
	assert.ElementsMatch(t, []string{"20261122-021112-2.mp4", "20261122-021112-3.mp4"}, left)
}
