package coordhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 降权账号（C10.1′ / C10.2′）的注入测试 ----

// fakeAccounts 是注入的 passwd：lookup 查表，add 模拟 useradd（把账号加进表并记录调用）。
type fakeAccounts struct {
	mu      sync.Mutex
	users   map[string]*user.User
	added   []string
	addErr  error
	nextUID int
}

func newFakeAccounts(users ...*user.User) *fakeAccounts {
	f := &fakeAccounts{users: map[string]*user.User{}, nextUID: 990}
	for _, u := range users {
		f.users[u.Username] = u
	}
	return f
}

func (f *fakeAccounts) lookup(name string) (*user.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[name]; ok {
		return u, nil
	}
	return nil, user.UnknownUserError(name)
}

func (f *fakeAccounts) add(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, name)
	if f.addErr != nil {
		return f.addErr
	}
	id := strconv.Itoa(f.nextUID)
	f.nextUID++
	f.users[name] = &user.User{Username: name, Uid: id, Gid: id}
	return nil
}

func fakeUser(name string, uid int) *user.User {
	return &user.User{Username: name, Uid: strconv.Itoa(uid), Gid: strconv.Itoa(uid)}
}

// injectedHost 建一个注入了 passwd、uidBusy 与 killUID 的 Host（不真建账号、不扫 /proc、不杀进程）。
func injectedHost(t *testing.T, userSetting string, f *fakeAccounts, busy func(int) int, kills *[]int) *Host {
	t.Helper()
	h := New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: userSetting, EgressPort: freePort(t),
		Command: []string{testBin, "coordinator-host"}}, nil, nil, nil)
	h.lookupUser, h.addUser = f.lookup, f.add
	if busy == nil {
		busy = func(int) int { return 0 }
	}
	h.uidBusy = busy
	h.killUID = func(uid int) { *kills = append(*kills, uid) }
	return h
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("降权账号只在 orynd 以 root 运行时使用")
	}
}

// 空串、nobody、nfsnobody 与解析出 65534 的账号都走「自动」：账号不存在时调用一次 useradd（参数正确），之后复用。
func TestAccountAutoCreatesDedicatedUser(t *testing.T) {
	requireRoot(t)
	for _, setting := range []string{"", "nobody", "nfsnobody", "shared-65534"} {
		t.Run(fmt.Sprintf("user=%q", setting), func(t *testing.T) {
			var kills []int
			f := newFakeAccounts(fakeUser("nobody", 65534), fakeUser("shared-65534", 65534))
			h := injectedHost(t, setting, f, nil, &kills)
			name := AutoUserName(h.cfg.Dir)
			assert.Regexp(t, `^oryn-coord-[0-9a-f]{8}$`, name)
			a, err := h.target()
			require.NoError(t, err)
			assert.Equal(t, name, a.name)
			assert.True(t, a.auto)
			assert.NotEqual(t, 65534, a.uid)
			assert.Equal(t, []string{name}, f.added, "useradd exactly once")
			b, err := h.target()
			require.NoError(t, err)
			assert.Equal(t, a, b)
			assert.Equal(t, []string{name}, f.added, "the existing account is reused")
			assert.Empty(t, kills)
		})
	}
	assert.Equal(t, []string{"--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", nologinShell(),
		"--user-group", "oryn-coord-0000abcd"}, useraddArgs("oryn-coord-0000abcd"))
	assert.Contains(t, nologinShells, nologinShell())
	// 后缀只取决于协调目录的绝对路径。
	assert.Equal(t, AutoUserName("/var/lib/oryn-coordinator"), AutoUserName("/var/lib/oryn-coordinator/"))
	assert.NotEqual(t, AutoUserName("/var/lib/a"), AutoUserName("/var/lib/b"))
}

// useradd 失败：协调功能不可用，原因带 useradd 的输出、手工命令与「或指定专用账号」；不回退到 nobody。
func TestAccountUseraddFailureIsUnavailable(t *testing.T) {
	requireRoot(t)
	requireSandbox(t)
	var kills []int
	f := newFakeAccounts(fakeUser("nobody", 65534))
	f.addErr = errors.New("exit status 10: useradd: cannot lock /etc/passwd; try again later.")
	h := injectedHost(t, "", f, nil, &kills)
	name := AutoUserName(h.cfg.Dir)
	err := h.Check(context.Background())
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, CodeUnavailable, he.Code)
	assert.Contains(t, he.Message, "cannot lock /etc/passwd")
	assert.Contains(t, he.Message, manualUseradd(name))
	assert.Contains(t, he.Message, "or set coordinator.user to a dedicated account")
	_, err = h.prepare(context.Background())
	require.Error(t, err)
	_, statErr := os.Stat(h.homesDir())
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing is prepared without an account")
}

// 显式账号：存在则直接用（不建账号、不算本实例专用）；是 root 则不可用。
func TestAccountExplicit(t *testing.T) {
	requireRoot(t)
	var kills []int
	f := newFakeAccounts(fakeUser("svc-coord", 1234), fakeUser("toor", 0))
	h := injectedHost(t, "svc-coord", f, nil, &kills)
	a, err := h.target()
	require.NoError(t, err)
	assert.Equal(t, account{name: "svc-coord", uid: 1234, gid: 1234}, a)
	assert.Empty(t, f.added)

	h = injectedHost(t, "toor", f, nil, &kills)
	_, err = h.target()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `coordinator.user "toor": account "toor" must not be root`)

	// 显式写了本实例的专用账号名：与自动同等对待（允许清扫）。
	h = injectedHost(t, "", f, nil, &kills)
	name := AutoUserName(h.cfg.Dir)
	f.users[name] = fakeUser(name, 1500)
	h.cfg.User = name
	a, err = h.target()
	require.NoError(t, err)
	assert.True(t, a.auto)
	assert.Empty(t, f.added)
}

// 独占校验：有本实例以外的进程用着该账号 → 不可用，且没有任何 kill；没有 → 可用。自动与显式账号都做。
func TestAccountBusyIsUnavailableWithoutKilling(t *testing.T) {
	requireRoot(t)
	requireSandbox(t)
	for _, setting := range []string{"", "svc-coord"} {
		t.Run(fmt.Sprintf("user=%q", setting), func(t *testing.T) {
			var kills []int
			busyPID := 4242
			var asked []int
			f := newFakeAccounts(fakeUser("svc-coord", 1234))
			h := injectedHost(t, setting, f, func(uid int) int { asked = append(asked, uid); return busyPID }, &kills)
			err := h.Check(context.Background())
			var he *Error
			require.ErrorAs(t, err, &he)
			a, terr := h.target()
			require.NoError(t, terr)
			assert.Equal(t, fmt.Sprintf("coordinator account %q (uid %d) is used by other processes (pid 4242); use a dedicated account", a.name, a.uid), he.Message)
			assert.Equal(t, []int{a.uid}, asked)
			_, err = h.prepare(context.Background())
			require.ErrorAs(t, err, &he)
			assert.Empty(t, kills, "the busy check never kills anything")

			busyPID = 0
			require.NoError(t, h.Check(context.Background()))
			assert.Empty(t, kills)
		})
	}
}

// ---- 真机：独占校验认得出本实例的进程，认不出的不杀 ----

// startAs 以 uid/gid 起一个独立会话（setsid）的 sleep，模拟一个不在协调宿主进程树里的进程；返回 pid 与 Wait 的结果。
func startAs(t *testing.T, uid, gid int) (int, <-chan *os.ProcessState) {
	t.Helper()
	cmd := exec.Command("/usr/bin/sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	require.NoError(t, cmd.Start())
	done := make(chan *os.ProcessState, 1)
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		done <- cmd.ProcessState
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd.Process.Pid, done
}

func TestAccountExclusiveReal(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, nil)
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	require.NoError(t, e.h.Check(context.Background()), "the running child is ours")
	// 同一协调目录的另一个 orynd 进程（例如运行中执行的 `coordinator check`）也认得出它。
	other := New(e.cfg, nil, nil, nil)
	require.NoError(t, other.Check(context.Background()))

	a, err := e.h.target()
	require.NoError(t, err)
	pid, done := startAs(t, a.uid, a.gid)
	err = e.h.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("coordinator account %q (uid %d) is used by other processes (pid %d)", a.name, a.uid, pid))
	assert.NoError(t, syscall.Kill(pid, 0), "the foreign process is left alone")
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	<-done
	require.NoError(t, e.h.Check(context.Background()))
}

// ---- 残留进程的清理策略（C10.2′）----

// 自动账号且没有 PID 命名空间 → 停止后按 uid 清扫；有 PID 命名空间 → 不清扫；显式账号 → 不清扫。
func TestCleanupPolicy(t *testing.T) {
	requireRoot(t)
	const explicit = "oryn-test-explicit"
	for _, c := range []struct {
		name     string
		portMode bool
		explicit bool
		want     bool
	}{
		{"auto-port-mode", true, false, true},
		{"auto-pid-namespace", false, false, false},
		{"explicit-port-mode", true, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var kills []int
			e := newEnv(t, func(cfg *Config) {
				if c.explicit {
					// 显式账号：真实存在、名字不是本实例的专用账号名（借用测试目录的专用账号的 uid）。
					require.NoError(t, runUseradd(AutoUserName(cfg.Dir)))
					cfg.User = explicit
				}
			}, func(h *Host) {
				h.noNS = c.portMode
				h.killUID = func(uid int) { mu.Lock(); kills = append(kills, uid); mu.Unlock() }
				if c.explicit {
					auto := AutoUserName(h.cfg.Dir)
					h.lookupUser = func(name string) (*user.User, error) {
						if name == explicit {
							name = auto
						}
						return user.Lookup(name)
					}
				}
			})
			if !c.portMode {
				if e.h.probe(); !e.h.useNS {
					t.Skip("本机建不了可用的网络命名空间")
				}
			}
			succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
			e.hello()
			child := e.child()
			require.NotNil(t, child)
			succeeded(t, e.runJob("pause", map[string]string{"mode": "stop"}), e.h)
			<-child.done
			mu.Lock()
			defer mu.Unlock()
			if c.want {
				assert.Equal(t, []int{accountUID(t, e.cfg.Dir)}, kills)
			} else {
				assert.Empty(t, kills)
			}
		})
	}
}

// L1：自动账号、没有 PID 命名空间时，以该 uid setsid 出去的进程在 stop 之后已被清扫（等 Wait 返回）。
func TestSetsidLeftoverKilledOnStop(t *testing.T) {
	requireRoot(t)
	var mu sync.Mutex
	var naps []time.Duration
	e := newEnv(t, nil, func(h *Host) {
		h.noNS = true
		// 不真等：被杀的进程变成僵尸后即不再计入。
		h.sleep = func(d time.Duration) { mu.Lock(); naps = append(naps, d); mu.Unlock() }
	})
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	a, err := e.h.target()
	require.NoError(t, err)
	require.True(t, a.auto)
	_, done := startAs(t, a.uid, a.gid)
	succeeded(t, e.runJob("pause", map[string]string{"mode": "stop"}), e.h)
	select {
	case st := <-done:
		ws, _ := st.Sys().(syscall.WaitStatus)
		assert.True(t, ws.Signaled() && ws.Signal() == syscall.SIGKILL, "leftover was SIGKILLed: %v", st)
	case <-time.After(30 * time.Second):
		t.Fatal("the setsid leftover survived stop")
	}
	mu.Lock()
	assert.LessOrEqual(t, len(naps), killRounds)
	mu.Unlock()
}

// ---- 每 orb 独立 HOME（C5 / F2）----

func (e *testEnv) newOrb() string {
	e.t.Helper()
	id := ids.Orb()
	now := time.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateOrb(context.Background(), db.CreateOrbParams{
		ID: id, ProviderID: e.pid, Type: "host", State: "spawning", StateChangedAt: now, TokenHash: "x-" + id,
		CreatedAt: now, Coordinator: true,
	}))
	return id
}

func (e *testEnv) runJobOn(orbID, op string, params any) db.Job {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	job, err := e.q.Enqueue(ctx, e.pid, orbID, op, params)
	require.NoError(e.t, err)
	return e.wait(job)
}

// F2：A 已运行并在 HOME 写了哨兵；按 spawn(B) → delete(A) 执行：B 的 HOME 与会话还在，A 的 HOME 已删，
// 存活的子进程恰为 B（A 在 spawn(B) 时就被停掉，且不算崩溃）。
func TestNewGenerationKeepsItsHome(t *testing.T) {
	e := newEnv(t, nil)
	a := e.orbID
	succeeded(t, e.runJobOn(a, "spawn", map[string]any{}), e.h)
	e.hello()
	e.ctl() // A 的 ctl 流
	childA := e.child()
	require.NotNil(t, childA)
	require.NoError(t, os.WriteFile(filepath.Join(e.h.homeDir(a), "sentinel-a"), []byte("a"), 0o644))

	b := e.newOrb()
	succeeded(t, e.runJobOn(b, "spawn", map[string]any{}), e.h)
	e.hello()
	select {
	case <-childA.done:
	default:
		t.Fatal("spawning B must stop A's coordinator host first")
	}
	// B 上走一个回合，会话写进 B 的 HOME。
	e.orbID = b
	ag, _ := e.startCoordinatorAgent(nil, "You are a test.\nReply briefly.", []apigen.OmpFrame{
		{"type": "open_session", "sessionDir": "~/.oryn/threads/" + coordThread},
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
	})
	ag.send(map[string]any{"id": "p1", "type": "prompt", "message": "hi"})
	ag.until("agent_end", typeIs("agent_end"))
	sess := filepath.Join(e.h.homeDir(b), ".oryn", "threads", coordThread)
	_, err := os.Stat(sess)
	require.NoError(t, err)

	succeeded(t, e.runJobOn(a, "delete", map[string]any{}), e.h)
	_, err = os.Stat(e.h.homeDir(a))
	assert.ErrorIs(t, err, os.ErrNotExist, "A's HOME is deleted")
	_, err = os.Stat(sess)
	assert.NoError(t, err, "B's session survives deleting A")
	assert.ErrorIs(t, syscall.Kill(childA.pid, 0), syscall.ESRCH)
	e.h.mu.Lock()
	var alive []string
	for id, c := range e.h.children {
		if c.alive() {
			alive = append(alive, id)
		}
	}
	e.h.mu.Unlock()
	assert.Equal(t, []string{b}, alive, "exactly B's coordinator host is alive")
	assert.Empty(t, e.f.pauses, "stopping the previous generation is not a crash")
}

// 旧布局迁移：<dir>/home 在 Run 时迁到 <dir>/homes/<协调 orb>；没有协调 orb 时删除。
func TestMigrateLegacyHome(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		require.NoError(t, os.MkdirAll(filepath.Join(c.Dir, "home", ".oryn"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(c.Dir, "home", ".oryn", "legacy"), []byte("x"), 0o600))
	})
	// 任务在迁移之后才会被认领：这条 pause（没有子进程，直接成功）完成即说明迁移已做完。
	succeeded(t, e.runJob("pause", map[string]string{"mode": "stop"}), e.h)
	b, err := os.ReadFile(filepath.Join(e.h.homeDir(e.orbID), ".oryn", "legacy"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(b))
	_, err = os.Stat(filepath.Join(e.cfg.Dir, "home"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	e.setState("deleted")
	require.NoError(t, os.MkdirAll(filepath.Join(e.cfg.Dir, "home"), 0o700))
	e.h.migrateLegacyHome(context.Background())
	_, err = os.Stat(filepath.Join(e.cfg.Dir, "home"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no coordinator orb: the legacy home is removed")
	_, err = os.Stat(filepath.Join(e.h.homeDir(e.orbID), ".oryn", "legacy"))
	assert.NoError(t, err, "the migrated home is untouched")
}

// delete 拒绝空的或带路径分隔符的 orb id（否则会删到 <dir>/homes 本身或之外）。
func TestDeleteRejectsUnsafeOrbID(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b"} {
		assert.False(t, validOrbID(id), id)
	}
	assert.True(t, validOrbID(ids.Orb()))
}
