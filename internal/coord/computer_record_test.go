package coord

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeRecorder 在 fsMachines 的 mux 上扮演 orbd 的录屏路由：同时至多一个录制；stop 把视频写进电脑的文件表并回 next。
type fakeRecorder struct {
	mu        sync.Mutex
	recording bool
	next      orbsock.DesktopRecording
	video     string
}

func (r *fakeRecorder) install(m *compMachines) {
	conflict := func(w http.ResponseWriter, code string) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(apigen.Error{Code: code, Message: code})
	}
	m.mux.HandleFunc("POST /desktop/record/start", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.recording {
			conflict(w, "already_recording")
			return
		}
		r.recording = true
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.next.Path})
	})
	m.mux.HandleFunc("POST /desktop/record/stop", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.recording {
			conflict(w, "not_recording")
			return
		}
		r.recording = false
		m.mu.Lock()
		m.files[r.next.Path] = r.video
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(r.next)
	})
}

// recordDetails 把工具结果的 details 按 JSON 解出来（网页卡片看到的形状）。
func recordDetails(t *testing.T, r threads.HostToolResult) map[string]any {
	t.Helper()
	require.False(t, r.IsError, r.Text)
	raw, err := json.Marshal(r.Details)
	require.NoError(t, err)
	var d map[string]any
	require.NoError(t, json.Unmarshal(raw, &d))
	return d
}

// M25 3.4：computer_record start 经电脑上 orbd 的本机路由开始录制；stop 拿到录制结果，不超过 transferMax 时存进文件库
// （Source 为助手、本对话可见）并同步到协调宿主，超过时只给录制结果、文本说明视频留在电脑的哪个路径；409 码原样带出。
func TestComputerRecord(t *testing.T) {
	e := newEnv(t, true)
	lib, _, host := e.withLibrary()
	asst := e.assistant()
	m := newCompMachines()
	e.c.SetMachines(m)
	video := "\x00\x00\x00\x18ftypisom-video"
	rec := &fakeRecorder{video: video, next: orbsock.DesktopRecording{Path: "/home/u/recordings/20261122-101010.mp4",
		Bytes: int64(len(video)), DurationMs: 83_400, Width: 1280, Height: 800, StoppedBy: orbsock.RecordStoppedByAgent}}
	rec.install(m)

	assert.True(t, slices.ContainsFunc(computerToolDefs(), func(f apigen.OmpFrame) bool { return f["name"] == ToolComputerRecord }))
	assert.Equal(t, "not_recording", errCodeOf(t, e.call(asst, ToolComputerRecord, `{"action":"stop"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolComputerRecord, `{"action":"pause"}`, true)))
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(e.spaceChat(e.space("Alpha", "")), ToolComputerRecord, `{"action":"start"}`, true)))

	t.Run("start", func(t *testing.T) {
		touches := m.touches
		r := e.call(asst, ToolComputerRecord, `{"action":"start"}`, true)
		assert.Equal(t, map[string]any{"path": rec.next.Path}, recordDetails(t, r))
		assert.Contains(t, r.Text, rec.next.Path)
		assert.Equal(t, fsReq{orb: "orb_comp", method: "POST", path: "/desktop/record/start", query: map[string]string{}}, m.last())
		assert.Greater(t, m.touches, touches, "记 agent 活动")
		assert.Equal(t, "already_recording", errCodeOf(t, e.call(asst, ToolComputerRecord, `{"action":"start"}`, true)))
	})

	t.Run("stop saves into the library", func(t *testing.T) {
		r := e.call(asst, ToolComputerRecord, `{"action":"stop"}`, true)
		d := recordDetails(t, r)
		id, _ := d["libraryFileId"].(string)
		require.NotEmpty(t, id, r.Text)
		assert.Equal(t, map[string]any{"path": rec.next.Path, "bytes": float64(len(video)), "durationMs": 83400.0, "width": 1280.0,
			"height": 800.0, "truncated": false, "stoppedBy": "agent"}, d["recording"])
		f, err := lib.Get(e.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "20261122-101010.mp4", f.Name)
		assert.Equal(t, library.SourceAssistant, f.Source)
		assert.Equal(t, asst.ID, f.ThreadID)
		assert.Equal(t, library.ScopeConversation, f.Scope)
		assert.Equal(t, int64(len(video)), f.Size)
		assert.Equal(t, []string{deref(e.row(asst.ID).OrbID)}, host.syncs, "存库后同步到协调宿主")
		assert.Equal(t, fsReq{orb: "orb_comp", method: "GET", path: "/fs/file", query: map[string]string{"path": rec.next.Path}}, m.last())
		assert.Contains(t, r.Text, "1:23")
		assert.Contains(t, r.Text, id)
	})

	t.Run("stop over transferMax keeps the video on the computer", func(t *testing.T) {
		before, err := lib.List(e.ctx, library.Filter{})
		require.NoError(t, err)
		recordDetails(t, e.call(asst, ToolComputerRecord, `{"action":"start"}`, true))
		rec.next.StoppedBy, rec.next.Truncated = orbsock.RecordStoppedByLimit, true
		e.c.xferMax = 4
		t.Cleanup(func() { e.c.xferMax = 0 })
		r := e.call(asst, ToolComputerRecord, `{"action":"stop"}`, true)
		d := recordDetails(t, r)
		assert.NotContains(t, d, "libraryFileId")
		assert.Equal(t, "limit", d["recording"].(map[string]any)["stoppedBy"])
		assert.Equal(t, "POST /desktop/record/stop", m.last().method+" "+m.last().path, "不取文件")
		assert.Contains(t, r.Text, "stays on your computer at "+rec.next.Path)
		assert.Contains(t, r.Text, "10-minute limit")
		assert.Contains(t, r.Text, "may be incomplete")
		after, err := lib.List(e.ctx, library.Filter{})
		require.NoError(t, err)
		assert.Len(t, after, len(before), "超过上限不存")
	})
	assert.Empty(t, e.actions(), "电脑工具不写审计")
}
