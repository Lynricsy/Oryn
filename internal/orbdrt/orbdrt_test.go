package orbdrt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

const thread1 = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

func TestHello(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.Equal(t, "test-orbd", h.OrbdVersion)
	assert.Equal(t, "omp/0.0.0-fakeomp", h.AgentVersion)
	assert.Equal(t, runtime.GOOS, h.Os)
	assert.Equal(t, runtime.GOARCH, h.Arch)
	assert.NotNil(t, h.Threads)
	// hello 是 ctl 上的第一条消息。
	cp.mu.Lock()
	assert.Equal(t, "hello", cp.notifs[0].method)
	cp.mu.Unlock()
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestSetupRun(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	home := rt.home

	// 本地仓库：带可执行的 .oryn/setup 与 .agents/resume（.oryn/resume 缺失时回退）。
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".oryn"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".oryn", "setup"), []byte("#!/bin/sh\necho repo-setup >> \"$HOME/repo-setup.txt\"\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".agents", "resume"), []byte("#!/bin/sh\necho repo-resume >> \"$HOME/resume.txt\"\n"), 0o755))
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "init")

	setupScript := "echo setup-ran\necho \"$FOO\" >> \"$HOME/setup.txt\"\necho to-stderr >&2\n"
	resumeScript := "echo project-resume >> \"$HOME/resume.txt\"\n"
	params := apigen.LinkCtlSetupRunParams{
		Workdir:  "~/workspace/repo",
		Repo:     &apigen.LinkCtlSetupRepo{URL: repo, Shallow: true},
		GitHosts: []string{"github.com"},
		Scripts:  apigen.LinkCtlSetupScripts{Setup: &setupScript, Resume: &resumeScript},
		OmpHome: []apigen.LinkCtlOmpHomeFile{
			{Path: "models.yml", Mode: 0o600, ContentB64: base64.StdEncoding.EncodeToString([]byte("providers: {}\n"))},
			{Path: "skills/demo/SKILL.md", Mode: 0o644, ContentB64: base64.StdEncoding.EncodeToString([]byte("# demo\n"))},
		},
		Env: map[string]string{"FOO": "bar"},
	}
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	assert.True(t, res.Ok)
	assert.True(t, res.SetupRan)

	b, err := os.ReadFile(filepath.Join(home, ".omp", "agent", "models.yml"))
	require.NoError(t, err)
	assert.Equal(t, "providers: {}\n", string(b))
	fi, err := os.Stat(filepath.Join(home, ".omp", "agent", "models.yml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	_, err = os.Stat(filepath.Join(home, ".omp", "agent", "skills", "demo", "SKILL.md"))
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(home, "workspace", "repo", ".git", "HEAD"))
	assert.FileExists(t, filepath.Join(home, ".oryn", "setup.done"))
	readFile := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(home, name))
		return string(b)
	}
	assert.Equal(t, "bar\n", readFile("setup.txt"))
	assert.Equal(t, "repo-setup\n", readFile("repo-setup.txt"))
	assert.Equal(t, "project-resume\nrepo-resume\n", readFile("resume.txt"))
	assert.Contains(t, readFile(".cache/oryn/logs/setup.log"), "setup-ran")

	out, err := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "--get",
		"url.http://"+rt.EgressAddr()+"/git/github.com/.insteadOf").Output()
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/\n", string(out))

	// setup.log 通知流式上报（stdout 与 stderr 分开）。
	cp.waitNotif(t, 0, "setup.log", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlSetupLogParams
		_ = json.Unmarshal(raw, &p)
		return p.Phase == apigen.Setup && p.Stream == apigen.Stdout && strings.Contains(p.Data, "setup-ran")
	})
	cp.waitNotif(t, 0, "setup.log", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlSetupLogParams
		_ = json.Unmarshal(raw, &p)
		return p.Stream == apigen.Stderr && strings.Contains(p.Data, "to-stderr")
	})

	// 第二次：标记存在，跳过 setup 与 clone，但 resume 照跑。
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	assert.True(t, res.Ok)
	assert.False(t, res.SetupRan)
	assert.Equal(t, "bar\n", readFile("setup.txt"))
	assert.Equal(t, "repo-setup\n", readFile("repo-setup.txt"))
	assert.Equal(t, "project-resume\nrepo-resume\nproject-resume\nrepo-resume\n", readFile("resume.txt"))

	// resume.run：不 clone、不 setup。
	require.NoError(t, cp.call(t, "resume.run", params, &res))
	assert.False(t, res.SetupRan)
	assert.Equal(t, "bar\n", readFile("setup.txt"))
	assert.Equal(t, strings.Repeat("project-resume\nrepo-resume\n", 3), readFile("resume.txt"))
}

// TestSetupGitIdentity：setup.run 与 resume.run 都把身份写进 $HOME/.gitconfig；缺省时不动现有值；
// 含换行的身份被拒绝且不写入任何东西。
func TestSetupGitIdentity(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	gitconfig := filepath.Join(rt.home, ".gitconfig")
	get := func(key string) string {
		t.Helper()
		out, err := exec.Command("git", "config", "--file", gitconfig, "--get", key).Output()
		require.NoError(t, err, key)
		return strings.TrimSuffix(string(out), "\n")
	}
	require.NoError(t, os.WriteFile(gitconfig, []byte("[user]\n\tname = Existing\n\temail = existing@example.com\n"), 0o644))

	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{}, &res))
	assert.Equal(t, "Existing", get("user.name"), "缺省身份不改动现有配置")
	assert.Equal(t, "existing@example.com", get("user.email"))

	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{
		GitIdentity: &apigen.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"},
	}, &res))
	assert.Equal(t, "Ada Lovelace", get("user.name"))
	assert.Equal(t, "ada@example.com", get("user.email"))

	// resume.run（唤醒）同样写入：已有 orb 也能拿到新设置。名字以 - 开头也不被当成选项。
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
		GitIdentity: &apigen.GitIdentity{Name: "--Grace", Email: "grace@example.com"},
	}, &res))
	assert.Equal(t, "--Grace", get("user.name"))
	assert.Equal(t, "grace@example.com", get("user.email"))

	for _, id := range []apigen.GitIdentity{
		{Name: "Evil\n[core]\n\tpager = sh", Email: "e@example.com"},
		{Name: "Evil", Email: "e@example.com\r"},
	} {
		err := cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{GitIdentity: &id}, &res)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, link.CodeInvalidParams, ce.Code)
	}
	assert.Equal(t, "--Grace", get("user.name"))
	b, err := os.ReadFile(gitconfig)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "pager")
}

func TestSetupFailureAndResumeBackground(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.ResumeWait = 500 * time.Millisecond })
	bad := "echo boom; exit 3"
	var failed apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: &bad}}, &failed),
		"脚本失败不是 RPC 错误：orb 仍要就绪，agent 才能修复脚本")
	assert.False(t, failed.Ok)
	assert.True(t, failed.SetupRan)
	require.NotNil(t, failed.SetupError)
	assert.Contains(t, *failed.SetupError, "exit status 3")
	assert.NoFileExists(t, filepath.Join(rt.home, ".oryn", "setup.done"), "失败不写标记，下次重跑")

	// resume 超过等待时间的部分在后台继续：请求在等待时间后即返回。
	slow := "sleep 2; echo late >> \"$HOME/late.txt\""
	start := time.Now()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Resume: &slow}}, &res))
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.NoFileExists(t, filepath.Join(rt.home, "late.txt"))
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(rt.home, "late.txt"))
		return err == nil
	}, 10*time.Second, 100*time.Millisecond)
}

func TestAgentPromptAndEntries(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	startAgent(t, cp, thread1, nil)
	st := waitAgentStream(t, cp)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "starting"))
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))

	fr := readFrames(st)
	sendCmd(t, st, `{"id":"c1","type":"prompt","message":"hello"}`)
	frames := fr.until(t, "session_settled")
	ts := types(frames)
	require.Equal(t, "response", ts[0])
	var resp struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	require.NoError(t, json.Unmarshal(frames[0], &resp))
	assert.Equal(t, "c1", resp.ID)
	assert.True(t, resp.Success)
	assert.Equal(t, turnTypes, compress(ts[1:]))
	for _, f := range frames {
		assert.NotEqual(t, "ready", frameType(f))
		assert.NotContains(t, string(f), `"id":"orbd-`)
	}

	lines := waitEntries(t, cp, thread1, 6)
	assert.Equal(t, []string{"user", "assistant", "toolResult", "assistant"}, messageRoles(lines))
	assert.Contains(t, lines[0], `"type":"title"`)
	assert.Contains(t, lines[1], `"type":"session"`)

	// 会话目录在 $HOME 之下，hello 重连后列出该线程。
	matches, _ := filepath.Glob(filepath.Join(rt.home, ".oryn", "threads", thread1, "*.jsonl"))
	require.Len(t, matches, 1)
	n := cp.notifCount()
	cp.killLink()
	_, raw := cp.waitNotif(t, n, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	require.Len(t, h.Threads, 1)
	assert.Equal(t, thread1, h.Threads[0].ThreadID)
	assert.True(t, h.Threads[0].AgentRunning)
	// 重连后新流打开，并再报一次 ready。
	waitAgentStream(t, cp)
	cp.waitNotif(t, n, "agent.status", statusIs(thread1, "ready"))

	// agent.start 幂等：返回同一 pid，不重启。
	var r1, r2 apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{ThreadID: thread1, InitFrames: initFrames(thread1)}, &r1))
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{ThreadID: thread1, InitFrames: initFrames(thread1)}, &r2))
	assert.Equal(t, r1.Pid, r2.Pid)

	// 优雅停止：关闭 stdin 后 fakeomp 以 0 退出。
	var stop apigen.LinkCtlAgentStopResult
	require.NoError(t, cp.call(t, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: thread1, GraceSeconds: 5}, &stop))
	assert.Equal(t, 0, stop.ExitCode)
	_, raw = cp.waitNotif(t, n, "agent.status", statusIs(thread1, "exited"))
	var s apigen.LinkCtlAgentStatusParams
	require.NoError(t, json.Unmarshal(raw, &s))
	assert.Nil(t, s.RestartsLeft, "requested stop carries no restartsLeft")
}

func TestReconnectResume(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	startAgent(t, cp, thread1, map[string]string{"FAKEOMP_DELAY_MS": "60"})
	st := waitAgentStream(t, cp)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	fr := readFrames(st)
	sendCmd(t, st, `{"id":"c1","type":"prompt","message":"hello"}`)
	first := fr.until(t, "message_update")

	// 断开 link 并在约 1.5s 内拒绝重连：期间 fakeomp 继续输出帧并写会话文件。
	n := cp.notifCount()
	cp.blocked.Store(true)
	cp.killLink()
	time.Sleep(1500 * time.Millisecond)
	cp.blocked.Store(false)

	cp.waitNotif(t, n, "hello", nil)
	st2 := waitAgentStream(t, cp)
	second := readFrames(st2).until(t, "session_settled")
	require.NotEmpty(t, second)
	assert.NotEqual(t, "response", frameType(second[0]), "new stream resumes mid-turn")

	all := append(append([]json.RawMessage{}, first...), second...)
	seen := map[string]bool{}
	for _, f := range all {
		require.False(t, seen[string(f)], "duplicate frame %s", f)
		seen[string(f)] = true
	}
	ts := types(all)
	require.Equal(t, "response", ts[0])
	assert.Equal(t, turnTypes, compress(ts[1:]), "buffered frames replayed in order")

	lines := waitEntries(t, cp, thread1, 6)
	assert.Equal(t, []string{"user", "assistant", "toolResult", "assistant"}, messageRoles(lines))
}

func TestAgentCrashRestart(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	pid := startAgent(t, cp, thread1, nil)
	waitAgentStream(t, cp)
	i, _ := cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))

	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	j, raw := cp.waitNotif(t, i, "agent.status", statusIs(thread1, "exited"))
	var s apigen.LinkCtlAgentStatusParams
	require.NoError(t, json.Unmarshal(raw, &s))
	require.NotNil(t, s.ExitCode)
	assert.Equal(t, 128+int(syscall.SIGKILL), *s.ExitCode)
	require.NotNil(t, s.RestartsLeft)
	assert.Equal(t, 3, *s.RestartsLeft)
	require.NotNil(t, s.StderrTail)
	k, _ := cp.waitNotif(t, j, "agent.status", statusIs(thread1, "starting"))
	cp.waitNotif(t, k, "agent.status", statusIs(thread1, "ready"))

	// 新进程使用新的 agent 流，可正常处理命令。
	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	sendCmd(t, st, `{"id":"c2","type":"prompt","message":"again"}`)
	frames := fr.until(t, "prompt_result")
	assert.Equal(t, "response", frameType(frames[0]))
}

func TestAgentCrashGivesUp(t *testing.T) {
	cp := newFakeCP(t)
	// 不存在的 --script 让 fakeomp 启动即退出（退出码 2）：重启耗尽后 restartsLeft=0。
	startRuntime(t, cp, func(c *Config) { c.RestartDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond} })
	startAgent(t, cp, thread1, map[string]string{"FAKEOMP_SCRIPT": "/nonexistent/script"})
	_, raw := cp.waitNotif(t, 0, "agent.status", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlAgentStatusParams
		_ = json.Unmarshal(raw, &p)
		return p.Status == "exited" && p.RestartsLeft != nil && *p.RestartsLeft == 0
	})
	var s apigen.LinkCtlAgentStatusParams
	require.NoError(t, json.Unmarshal(raw, &s))
	assert.Equal(t, 2, *s.ExitCode)
	assert.Contains(t, *s.StderrTail, "read script")
}

func TestEgress(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	req, err := http.NewRequest(http.MethodPost, "http://"+rt.EgressAddr()+"/llm/anthropic/v1/messages", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("X-Test", "oryn-ph-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "egress POST /llm/anthropic/v1/messages oryn-ph-1", string(body))
}

func TestTruncateResets(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	startAgent(t, cp, thread1, nil)
	st := waitAgentStream(t, cp)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	fr := readFrames(st)
	sendCmd(t, st, `{"id":"c1","type":"prompt","message":"hello"}`)
	fr.until(t, "session_settled")
	lines := waitEntries(t, cp, thread1, 6)

	matches, _ := filepath.Glob(filepath.Join(rt.home, ".oryn", "threads", thread1, "*.jsonl"))
	require.Len(t, matches, 1)
	keep := int64(len(lines[0]) + len(lines[1]) + 2)
	n := cp.notifCount()
	require.NoError(t, os.Truncate(matches[0], keep))

	i, _ := cp.waitNotif(t, n, "entries.reset", nil)
	cp.waitNotif(t, i, "entries.reset_done", nil)
	require.Eventually(t, func() bool { return len(cp.mainLines(thread1)) == 2 }, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, lines[:2], cp.mainLines(thread1))

	// 整体替换（inode 变化）同样触发重置。
	n = cp.notifCount()
	tmp := matches[0] + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(lines[0]+"\n"+lines[1]+"\n"+lines[2]+"\n"), 0o644))
	require.NoError(t, os.Rename(tmp, matches[0]))
	i, _ = cp.waitNotif(t, n, "entries.reset", nil)
	cp.waitNotif(t, i, "entries.reset_done", nil)
	require.Eventually(t, func() bool { return len(cp.mainLines(thread1)) == 3 }, 10*time.Second, 20*time.Millisecond)
}

func TestArtifactTranscriptsAndPartialLines(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dir := filepath.Join(rt.home, ".oryn", "threads", thread1)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "S1", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S1.jsonl"), []byte(`{"type":"session","id":"s"}`+"\n"+`{"type":"message","id":"a"}`+"\n"+`{"type":"mess`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S1", "task-1.jsonl"), []byte(`{"type":"message","id":"x"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S1", "sub", "task-2.jsonl"), []byte(`{"type":"message","id":"y"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S1", "notes.txt"), []byte("ignored\n"), 0o644))
	require.NoError(t, rt.reg.add(thread1, dir))
	rt.thread(thread1, dir)

	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		f := cp.files[thread1]
		return f["S1.jsonl"] != nil && len(f["S1.jsonl"].lines) == 2 &&
			f["S1/task-1.jsonl"] != nil && f["S1/sub/task-2.jsonl"] != nil
	}, 10*time.Second, 20*time.Millisecond)
	cp.mu.Lock()
	assert.Equal(t, []string{"S1.jsonl", "S1/sub/task-2.jsonl", "S1/task-1.jsonl"}, sortedKeys(cp.files[thread1]))
	assert.EqualValues(t, len(`{"type":"session","id":"s"}`+"\n"+`{"type":"message","id":"a"}`+"\n"), cp.files[thread1]["S1.jsonl"].offset)
	cp.mu.Unlock()

	// 补全残行后才发送。
	f, err := os.OpenFile(filepath.Join(dir, "S1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`age","id":"b"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		l := cp.files[thread1]["S1.jsonl"].lines
		return len(l) == 3 && l[2] == `{"type":"message","id":"b"}`
	}, 10*time.Second, 20*time.Millisecond)
}

func TestCursorMismatchResend(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dir := filepath.Join(rt.home, ".oryn", "threads", thread1)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	var content strings.Builder
	for i := range 1200 { // 超过单批行数上限，分多批发送
		content.WriteString(fmt.Sprintf(`{"type":"message","id":"%08x"}`, i) + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S.jsonl"), []byte(content.String()), 0o644))
	require.NoError(t, rt.reg.add(thread1, dir))
	rt.thread(thread1, dir)
	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		f := cp.files[thread1]["S.jsonl"]
		return f != nil && len(f.lines) == 1200
	}, 10*time.Second, 20*time.Millisecond)

	// 服务端丢失后半部分：重连后按服务端游标续传，不重不漏。
	cp.mu.Lock()
	f := cp.files[thread1]["S.jsonl"]
	f.lines = f.lines[:700]
	var off int64
	for _, l := range f.lines {
		off += int64(len(l) + 1)
	}
	f.offset = off
	cp.mu.Unlock()
	cp.killLink()
	require.Eventually(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return len(cp.files[thread1]["S.jsonl"].lines) == 1200
	}, 10*time.Second, 20*time.Millisecond)
	cp.mu.Lock()
	lines := cp.files[thread1]["S.jsonl"].lines
	cp.mu.Unlock()
	assert.Equal(t, strings.Split(strings.TrimSuffix(content.String(), "\n"), "\n"), lines)
}

func TestBlobFetch(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	sha := strings.Repeat("ab", 32)
	require.NoError(t, os.MkdirAll(filepath.Join(rt.home, ".omp", "agent", "blobs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rt.home, ".omp", "agent", "blobs", sha), []byte("PNGDATA"), 0o644))

	var res apigen.LinkCtlBlobFetchResult
	require.NoError(t, cp.call(t, "blob.fetch", apigen.LinkCtlBlobFetchParams{Sha256: strings.Repeat("cd", 32)}, &res))
	assert.False(t, res.Found)
	require.NoError(t, cp.call(t, "blob.fetch", apigen.LinkCtlBlobFetchParams{Sha256: sha}, &res))
	assert.True(t, res.Found)
	select {
	case st := <-cp.blobs:
		assert.Equal(t, sha, st.Header.SHA256)
		assert.EqualValues(t, 7, st.Header.Size)
		b, err := io.ReadAll(st)
		require.NoError(t, err)
		assert.Equal(t, "PNGDATA", string(b))
	case <-time.After(10 * time.Second):
		t.Fatal("no blob stream")
	}
	err := cp.call(t, "blob.fetch", apigen.LinkCtlBlobFetchParams{Sha256: "../etc/passwd"}, nil)
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, link.CodeInvalidParams, ce.Code)
}

func TestFsOps(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	src := filepath.Join(rt.home, ".oryn", "threads", "T-a")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "S"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "S.jsonl"), []byte("x\n"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(src, "S", "t.jsonl"), []byte("y\n"), 0o644))
	require.NoError(t, os.Symlink("S.jsonl", filepath.Join(src, "link")))

	var ok apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: "~/.oryn/threads/T-a", To: "~/.oryn/threads/T-b"}, &ok))
	assert.True(t, ok.Ok)
	dst := filepath.Join(rt.home, ".oryn", "threads", "T-b")
	fi, err := os.Stat(filepath.Join(dst, "S.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm())
	assert.FileExists(t, filepath.Join(dst, "S", "t.jsonl"))
	target, err := os.Readlink(filepath.Join(dst, "link"))
	require.NoError(t, err)
	assert.Equal(t, "S.jsonl", target)

	// 移动会话文件进已存在目录：同名产物目录随之移动。
	require.NoError(t, os.MkdirAll(filepath.Join(rt.home, ".oryn", "threads", "T-c"), 0o755))
	require.NoError(t, cp.call(t, "fs.move", apigen.LinkCtlFsMoveParams{From: src + "/S.jsonl", To: "~/.oryn/threads/T-c"}, &ok))
	assert.FileExists(t, filepath.Join(rt.home, ".oryn", "threads", "T-c", "S.jsonl"))
	assert.FileExists(t, filepath.Join(rt.home, ".oryn", "threads", "T-c", "S", "t.jsonl"))
	assert.NoFileExists(t, filepath.Join(src, "S.jsonl"))

	var ce *link.CtlError
	err = cp.call(t, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: "/etc", To: "~/etc"}, nil)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, link.CodeInvalidParams, ce.Code)
	err = cp.call(t, "fs.move", apigen.LinkCtlFsMoveParams{From: "~/.oryn", To: "~/../escape"}, nil)
	require.ErrorAs(t, err, &ce)
	require.NoError(t, os.Symlink("/etc", filepath.Join(rt.home, "etc-link")))
	err = cp.call(t, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: "~/etc-link/hostname", To: "~/h"}, nil)
	require.ErrorAs(t, err, &ce)
}

func TestShutdownStopsAgents(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	startAgent(t, cp, thread1, nil)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	var ok apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, "shutdown", apigen.LinkCtlShutdownParams{GraceSeconds: 5}, &ok))
	assert.True(t, ok.Ok)
	assert.False(t, rt.lookupThread(thread1).agentRunning())
}

func TestForeignThreadDropped(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	const foreign = "T-foreign"
	cp.mu.Lock()
	cp.foreign[foreign] = true
	cp.mu.Unlock()
	dir := filepath.Join(rt.home, ".oryn", "threads", foreign)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "S.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, rt.reg.add(foreign, dir))
	rt.thread(foreign, dir)
	require.Eventually(t, func() bool { return rt.lookupThread(foreign) == nil }, 10*time.Second, 20*time.Millisecond)
	assert.NotContains(t, rt.reg.list(), foreign)
	assert.NotContains(t, loadRegistry(rt.reg.path, rt.cfg.OrbID).list(), foreign)
}

func TestRegistryIgnoresForeignOrb(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "threads.json")
	r := loadRegistry(path, "orb-a")
	require.NoError(t, r.add("T-1", "/x"))
	assert.Equal(t, map[string]string{"T-1": "/x"}, loadRegistry(path, "orb-a").list())
	assert.Empty(t, loadRegistry(path, "orb-b").list(), "snapshot-derived orb must not claim source threads")
}

func TestOutboxOverflowGap(t *testing.T) {
	var o outbox
	big := make([]byte, 10<<20)
	for range 3 {
		o.push(outItem{data: big})
	}
	assert.False(t, o.takeGap())
	o.push(outItem{data: big})
	assert.True(t, o.takeGap())
	assert.Len(t, o.items, 1)
	assert.False(t, o.takeGap())
	h, ok := o.head()
	require.True(t, ok)
	o.pop(h)
	_, ok = o.head()
	assert.False(t, ok)
}

func TestMetricsSample(t *testing.T) {
	dir := t.TempDir()
	cg := filepath.Join(dir, "cg")
	nd := filepath.Join(dir, "eth0", "statistics")
	require.NoError(t, os.MkdirAll(cg, 0o755))
	require.NoError(t, os.MkdirAll(nd, 0o755))
	write := func(p, s string) { require.NoError(t, os.WriteFile(p, []byte(s), 0o644)) }
	write(filepath.Join(cg, "cpu.stat"), "usage_usec 1000000\nuser_usec 1\n")
	write(filepath.Join(cg, "memory.current"), "104857600\n")
	write(filepath.Join(cg, "memory.max"), "max\n")
	write(filepath.Join(dir, "meminfo"), "MemTotal:       2097152 kB\n")
	write(filepath.Join(nd, "rx_bytes"), "123\n")
	write(filepath.Join(nd, "tx_bytes"), "456\n")
	m := newMetricsReader(Config{CgroupDir: cg, NetDir: filepath.Join(dir, "eth0"), MemInfo: filepath.Join(dir, "meminfo"), DiskPath: dir})
	m.prevAt = time.Now().Add(-time.Second)
	write(filepath.Join(cg, "cpu.stat"), "usage_usec 1500000\n")
	s := m.sample(m.prevAt.Add(time.Second))
	assert.InDelta(t, 50, s.CPUPct, 0.1)
	assert.InDelta(t, 100, s.MemUsedMB, 0.01)
	assert.InDelta(t, 2048, s.MemLimitMB, 0.01)
	assert.EqualValues(t, 123, s.NetRxBytes)
	assert.EqualValues(t, 456, s.NetTxBytes)
	assert.Positive(t, s.DiskUsedMB)

	// 数据源缺失：全部为 0，不报错。
	empty := newMetricsReader(Config{CgroupDir: "/nonexistent", NetDir: "/nonexistent", MemInfo: "/nonexistent", DiskPath: "/nonexistent"})
	z := empty.sample(time.Now())
	assert.Zero(t, z.CPUPct)
	assert.Zero(t, z.MemUsedMB)
	assert.Zero(t, z.DiskUsedMB)
}
