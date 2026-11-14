package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M8 §3 验证：process 驱动 + 真实 orbd + fakeomp + fakedesk（Xtigervnc / openbox / chromium 的替身）。

// enableDesktop 让本测试启动的 orbd 启用桌面：ORYN_DESKTOP=1、CDP 前门与 egress 随机端口、X 锁目录在临时目录（不碰宿主机的 /tmp/.X1-lock）、
// fakedesk 的符号链接放到 PATH 前面。
func enableDesktop(t *testing.T) {
	t.Setenv("ORYN_DESKTOP", "1")
	t.Setenv("ORYN_DESKTOP_CDP_ADDR", "127.0.0.1:0")
	t.Setenv("ORYN_DESKTOP_X11_TMPDIR", t.TempDir())
	// 同一宿主上同时跑多台 process 驱动的 orbd：egress 不能都监听固定的 7811
	t.Setenv("ORYN_EGRESS_ADDR", "127.0.0.1:0")
	t.Setenv("PATH", filepath.Join(binDir, "desk")+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// dumpOrbdLogsOnFailure 在测试失败时打印各 orbd 的日志（排查用）。
func dumpOrbdLogsOnFailure(s *stack) {
	s.t.Cleanup(func() {
		if !s.t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(s.drv.Root, "orbs", "*", "orbd.log"))
		for _, l := range logs {
			b, _ := os.ReadFile(l)
			s.t.Logf("==== %s ====\n%s", l, b)
		}
	})
}

func desktopIdle() config.Idle {
	return config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)}
}

// newThread 建线程并等到 idle 且已有 orb。
func (s *stack) newThread(body map[string]any) threadView {
	s.t.Helper()
	body["executor"] = map[string]any{"kind": "new_orb", "provider": "local"}
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", body, &created, 201)
	return s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
}

func (s *stack) orb(id string) apigen.Orb {
	s.t.Helper()
	var o apigen.Orb
	s.rest("GET", "/api/v1/orbs/"+id, nil, &o, 200)
	return o
}

func (s *stack) waitOrb(id string, cond func(apigen.Orb) bool, msg string) apigen.Orb {
	s.t.Helper()
	var o apigen.Orb
	require.Eventually(s.t, func() bool {
		o = s.orb(id)
		return cond(o)
	}, 60*time.Second, 50*time.Millisecond, msg)
	return o
}

func profileStatus(o apigen.Orb) apigen.OrbDesktopProfileStatus {
	if o.Desktop == nil || o.Desktop.Profile == nil {
		return ""
	}
	return o.Desktop.Profile.Status
}

func (s *stack) browserProfile(id string) apigen.BrowserProfile {
	s.t.Helper()
	var list apigen.BrowserProfileList
	s.rest("GET", "/api/v1/browser-profiles", nil, &list, 200)
	for _, p := range list.Profiles {
		if p.ID == id {
			return p
		}
	}
	s.t.Fatalf("Profile %s 不存在", id)
	return apigen.BrowserProfile{}
}

// profileDir 是 orb 的 Chromium user-data-dir。
func (s *stack) profileDir(orbID string) string {
	return filepath.Join(s.drv.HomeDir(orbID), ".oryn", "desktop", "profile")
}

// browserPid 在 /proc 里找 user-data-dir 为该 orb 的假 chromium 进程；没有时为 0。
func (s *stack) browserPid(orbID string) int {
	want := "--user-data-dir=" + s.profileDir(orbID)
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) > 0 && filepath.Base(args[0]) == "chromium" && strings.Contains(string(raw), want) {
			return pid
		}
	}
	return 0
}

func (s *stack) startBrowser(orbID string) int {
	s.t.Helper()
	var o apigen.Orb
	s.rest("POST", "/api/v1/orbs/"+orbID+"/desktop/browser/start", nil, &o, 200)
	require.NotNil(s.t, o.Desktop)
	require.Equal(s.t, apigen.OrbBrowserStatusRunning, o.Desktop.Browser)
	pid := s.browserPid(orbID)
	require.NotZero(s.t, pid, "假浏览器在运行")
	return pid
}

func (s *stack) freeze(th threadView) {
	s.t.Helper()
	s.rest("POST", "/api/v1/orbs/"+th.OrbID+"/pause", apigen.OrbPauseRequest{Mode: apigen.Freeze}, nil, 202)
	s.waitOrb(th.OrbID, func(o apigen.Orb) bool { return o.State == apigen.OrbStateFrozen }, "冻结")
}

func (s *stack) wake(th threadView) {
	s.t.Helper()
	s.rest("POST", "/api/v1/threads/"+th.ID+"/wake", nil, nil, 202)
	s.waitOrb(th.OrbID, func(o apigen.Orb) bool { return o.State == apigen.OrbStateReady }, "唤醒")
}

// 计划步骤 3「验证」e2e 1–7：Profile 装载、删除前保存、跨 orb 恢复、独占冲突与接管、冻结前保存与唤醒连续性、
// 桌面 WS、不带 Profile 的线程。
func TestFullStackDesktopProfiles(t *testing.T) {
	enableDesktop(t)
	s := newStack(t, desktopIdle())
	dumpOrbdLogsOnFailure(s)
	ctx := context.Background()
	mark := "MARK-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	var p apigen.BrowserProfile
	s.rest("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: "work"}, &p, 201)

	// 1. 带 Profile 建线程 → orb ready 时 attached，本地标记是该 Profile
	t1 := s.newThread(map[string]any{"prompt": "one", "title": "线程一", "browserProfileId": p.ID})
	o1 := s.waitOrb(t1.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusAttached }, "attached")
	assert.True(t, o1.Desktop.DisplayReady)
	raw, err := os.ReadFile(filepath.Join(s.drv.HomeDir(t1.OrbID), ".oryn", "desktop", "profile.json"))
	require.NoError(t, err)
	var marker struct{ OrbID, ProfileID string }
	require.NoError(t, json.Unmarshal(raw, &marker))
	assert.Equal(t, p.ID, marker.ProfileID)
	assert.Equal(t, t1.OrbID, marker.OrbID)

	// 2. 往 Profile 目录写带标记串的文件、启动浏览器；删除 orb → 关闭式保存为修订 1，租约释放，落盘不含明文
	require.NoError(t, os.MkdirAll(filepath.Join(s.profileDir(t1.OrbID), "Default"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.profileDir(t1.OrbID), "Default", "marker.txt"), []byte(mark), 0o600))
	s.startBrowser(t1.OrbID)
	s.rest("DELETE", "/api/v1/orbs/"+t1.OrbID, nil, nil, 202)
	s.waitOrb(t1.OrbID, func(o apigen.Orb) bool { return o.State == apigen.OrbStateDeleted }, "删除")
	require.Eventually(t, func() bool { return s.browserProfile(p.ID).Holder == nil }, 30*time.Second, 50*time.Millisecond)
	bp := s.browserProfile(p.ID)
	assert.EqualValues(t, 1, bp.Revision)
	assert.Positive(t, bp.SizeBytes)
	archiveDir := filepath.Join(s.cfg.DataDir, "browser-profiles", p.ID)
	files, err := os.ReadDir(archiveDir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	enc, err := os.ReadFile(filepath.Join(archiveDir, files[0].Name()))
	require.NoError(t, err)
	assert.Equal(t, "OSS1", string(enc[:4]))
	assert.NotContains(t, string(enc), mark)

	// 3. 再带它建线程：新 orb 的 Profile 目录里有那个文件
	t2 := s.newThread(map[string]any{"prompt": "two", "title": "线程二", "browserProfileId": p.ID})
	s.waitOrb(t2.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusAttached }, "t2 attached")
	got, err := os.ReadFile(filepath.Join(s.profileDir(t2.OrbID), "Default", "marker.txt"))
	require.NoError(t, err)
	assert.Equal(t, mark, string(got))

	// 4. 第二台 orb 运行时再带它建线程 → 409 ProfileInUseConflict；只读 → 201 且不取租约；接管 → 201，原 orb 降级只读
	var conflict apigen.ProfileInUseConflict
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "three", "browserProfileId": p.ID, "executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &conflict, 409)
	assert.Equal(t, apigen.ProfileInUseConflictCodeProfileInUse, conflict.Code)
	assert.Equal(t, t2.OrbID, conflict.Holder.OrbID)
	require.NotNil(t, conflict.Holder.ThreadTitle)
	assert.Equal(t, "线程二", *conflict.Holder.ThreadTitle)
	assert.Positive(t, conflict.Holder.Since)

	tr := s.newThread(map[string]any{"prompt": "ro", "title": "只读", "browserProfileId": p.ID, "profileMode": "readonly"})
	or := s.waitOrb(tr.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusReadonly }, "readonly")
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonRequested, *or.Desktop.Profile.ReadonlyReason)
	got, err = os.ReadFile(filepath.Join(s.profileDir(tr.OrbID), "Default", "marker.txt"))
	require.NoError(t, err)
	assert.Equal(t, mark, string(got), "只读副本装载最近一次保存的修订")
	assert.Equal(t, t2.OrbID, s.browserProfile(p.ID).Holder.OrbID)

	t3 := s.newThread(map[string]any{"prompt": "three", "title": "线程三", "browserProfileId": p.ID, "profileMode": "takeover"})
	s.waitOrb(t3.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusAttached }, "t3 attached")
	o2 := s.waitOrb(t2.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusReadonly }, "t2 降级只读")
	require.NotNil(t, o2.Desktop.Profile.TakenOverBy)
	assert.Equal(t, "线程三", *o2.Desktop.Profile.TakenOverBy.ThreadTitle)
	assert.Equal(t, t3.OrbID, s.browserProfile(p.ID).Holder.OrbID)
	_, err = os.Stat(filepath.Join(s.profileDir(t2.OrbID), "Default", "marker.txt"))
	assert.NoError(t, err, "被接管的一方本地目录不动")
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(s.drv.HomeDir(t2.OrbID), ".oryn", "desktop", "profile.json"))
		return err == nil && strings.Contains(string(raw), `"readonly"`)
	}, 10*time.Second, 50*time.Millisecond, "原 orbd 收到 profile.mode")

	// 5. 第三台：启动浏览器、改点东西，冻结 → 修订前进、租约释放；唤醒 → attached，浏览器 pid 不变
	pid := s.startBrowser(t3.OrbID)
	require.NoError(t, os.WriteFile(filepath.Join(s.profileDir(t3.OrbID), "Default", "t3.txt"), []byte("t3"), 0o600))
	s.freeze(t3)
	require.Eventually(t, func() bool { return s.browserProfile(p.ID).Holder == nil }, 30*time.Second, 50*time.Millisecond)
	assert.EqualValues(t, 2, s.browserProfile(p.ID).Revision)
	s.wake(t3)
	o3 := s.waitOrb(t3.OrbID, func(o apigen.Orb) bool { return profileStatus(o) == apigen.OrbDesktopProfileStatusAttached }, "唤醒后 attached")
	assert.Equal(t, apigen.OrbBrowserStatusRunning, o3.Desktop.Browser)
	assert.Equal(t, pid, s.browserPid(t3.OrbID), "冻结保留浏览器")
	assert.Equal(t, t3.OrbID, s.browserProfile(p.ID).Holder.OrbID)

	// 6. 桌面 WS：设备令牌取票据 → 与假 Xvnc 回显往返；last_user_interaction_at 前进
	before := s.orb(t3.OrbID).LastUserInteractionAt
	var tk struct {
		Ticket string `json:"ticket"`
	}
	s.rest("POST", "/api/v1/ws-ticket", nil, &tk, 200)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(wctx, strings.Replace(s.http.URL, "http", "ws", 1)+"/api/v1/orbs/"+t3.OrbID+"/desktop/ws?ticket="+tk.Ticket, nil)
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("RFB;"), 1<<14)
	require.NoError(t, c.Write(wctx, websocket.MessageBinary, payload))
	var echo []byte
	for len(echo) < len(payload) {
		_, b, err := c.Read(wctx)
		require.NoError(t, err)
		echo = append(echo, b...)
	}
	assert.Equal(t, payload, echo)
	_ = c.Close(websocket.StatusNormalClosure, "")
	after := s.orb(t3.OrbID).LastUserInteractionAt
	require.NotNil(t, after)
	if before != nil {
		assert.Greater(t, *after, *before)
	}

	// 7. 不带 Profile 的线程：浏览器在冻结 / 唤醒之间保留（DELETE 是空操作）
	t4 := s.newThread(map[string]any{"prompt": "four", "browserProfileId": ""})
	s.waitOrb(t4.OrbID, func(o apigen.Orb) bool { return o.Desktop != nil && o.Desktop.DisplayReady }, "显示就绪")
	pid4 := s.startBrowser(t4.OrbID)
	s.freeze(t4)
	s.wake(t4)
	o4 := s.waitOrb(t4.OrbID, func(o apigen.Orb) bool { return o.Desktop != nil }, "桌面")
	assert.Nil(t, o4.Desktop.Profile)
	assert.Equal(t, pid4, s.browserPid(t4.OrbID))
}

// 计划步骤 3「验证」e2e 8：桌面未启用的栈——Orb.desktop 缺省；桌面接口回 409 orbd_outdated。
func TestFullStackDesktopDisabled(t *testing.T) {
	t.Setenv("ORYN_DESKTOP", "")
	s := newStack(t, desktopIdle())
	var p apigen.BrowserProfile
	s.rest("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: "work"}, &p, 201)
	th := s.newThread(map[string]any{"prompt": "x", "browserProfileId": ""})
	assert.Nil(t, s.orb(th.OrbID).Desktop)
	var e struct{ Code string }
	s.rest("PUT", "/api/v1/orbs/"+th.OrbID+"/desktop/profile", apigen.OrbDesktopProfilePut{ProfileID: p.ID}, &e, 409)
	assert.Equal(t, "orbd_outdated", e.Code)
	s.rest("POST", "/api/v1/orbs/"+th.OrbID+"/desktop/browser/start", nil, &e, 409)
	assert.Equal(t, "orbd_outdated", e.Code)
}
