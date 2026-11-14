package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

const fakeDocker = `#!/bin/sh
echo "$*" >> "$FAKE_DOCKER_DIR/log"
case "$1 $2" in
"container inspect")
  st=$(cat "$FAKE_DOCKER_DIR/state")
  if [ "$st" = missing ]; then echo "Error: No such container: $5" >&2; exit 1; fi
  echo "$st"; exit 0;;
"image inspect")
  if [ -f "$FAKE_DOCKER_DIR/image" ]; then echo sha256:abc; exit 0; fi
  echo "Error: No such image: $5" >&2; exit 1;;
esac
if [ "$1" = create ]; then
  echo "ENV $ORYN_URL $ORYN_ORB_ID $ORYN_ORB_TOKEN" >> "$FAKE_DOCKER_DIR/log"
  for a in "$@"; do case "$a" in seccomp=*) cp "${a#seccomp=}" "$FAKE_DOCKER_DIR/seccomp.json"; echo "${a#seccomp=}" > "$FAKE_DOCKER_DIR/seccomp.path";; esac; done
  echo cid
fi
if [ "$1" = cp ] && [ "$2" != -a ]; then
  if [ -f "$FAKE_DOCKER_DIR/tokenfile.tar" ]; then cat "$FAKE_DOCKER_DIR/tokenfile.tar"; exit 0; fi
  echo "Error response from daemon: Could not find the file /home/user/.oryn/orb-token in container x" >&2; exit 1
fi
if [ "$1" = cp ] && [ "$2" = -a ]; then cat > "$FAKE_DOCKER_DIR/cpin.tar"; fi
if [ "$1" = exec ] && [ "$2" = -i ]; then cat > "$FAKE_DOCKER_DIR/stdin"; fi
if [ "$1" = rmi ] && [ ! -f "$FAKE_DOCKER_DIR/image" ]; then echo "Error: No such image: $2" >&2; exit 1; fi
exit 0
`

type fakeDockerEnv struct{ dir string }

func newFakeDocker(t *testing.T) *fakeDockerEnv {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_DIR", dir)
	return &fakeDockerEnv{dir: dir}
}

func (f *fakeDockerEnv) set(t *testing.T, state string, image bool) {
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "state"), []byte(state), 0o644))
	for _, n := range []string{"log", "image", "tokenfile.tar", "cpin.tar", "stdin", "seccomp.json", "seccomp.path"} {
		_ = os.Remove(filepath.Join(f.dir, n))
	}
	if image {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "image"), nil, 0o644))
	}
}

// seccompArg 把日志里 seccomp 临时文件的路径换成固定占位符。
var seccompArg = regexp.MustCompile(`seccomp=\S+`)

func (f *fakeDockerEnv) log(t *testing.T) []string {
	b, err := os.ReadFile(filepath.Join(f.dir, "log"))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(seccompArg.ReplaceAllString(string(b), "seccomp=PROFILE"), "\n"), "\n")
}

func TestDockerDriverCommands(t *testing.T) {
	fd := newFakeDocker(t)
	d := NewDocker(config.Docker{
		Image: "oryn-orb:dev", DefaultType: "medium", ExtraRunArgs: []string{"--runtime", "runc"},
		Types: map[string]config.DockerType{"small": {CPUs: 2, Memory: "4g"}, "medium": {CPUs: 4.5, Memory: "8g"}},
	})
	spawn := func(snap string) Job {
		p := &apigen.JobSpawnParams{Type: "small", Env: apigen.JobSpawnEnv{OrynURL: "http://172.17.0.1:7600", OrynOrbID: "orb-x", OrynOrbToken: "oot_tok"}}
		if snap != "" {
			p.SnapshotID = &snap
		}
		return Job{ID: "job_1", Op: "spawn", OrbID: "orb-x", Spawn: p}
	}
	inspect := "container inspect --format {{.State.Status}} orb-x"
	createArgs := func(image, cpus, mem string) string {
		return "create --init --name orb-x --hostname orb-x --label oryn-orb=1 --cpus " + cpus + " --memory " + mem +
			" --shm-size 1g --add-host host.docker.internal:host-gateway --cap-drop NET_RAW --cap-drop NET_ADMIN" +
			" -e ORYN_URL -e ORYN_ORB_ID -e ORYN_ORB_TOKEN --security-opt seccomp=PROFILE --runtime runc " + image + " orbd run"
	}
	cpFrom := "cp orb-x:/home/user/.oryn/orb-token -"
	writeToken := "exec -i orb-x sh -c " + writeTokenScript
	hup := "exec orb-x pkill -HUP -x orbd"
	cases := []struct {
		name  string
		state string
		image bool
		job   Job
		want  []string
	}{
		{"spawn new", "missing", false, spawn(""), []string{inspect, createArgs("oryn-orb:dev", "2", "4g"), "ENV http://172.17.0.1:7600 orb-x oot_tok", cpFrom, "start orb-x"}},
		{"spawn new from snapshot, unknown type → default", "missing", false, func() Job {
			j := spawn("oryn-snap-1")
			j.Spawn.Type = "huge"
			return j
		}(), []string{inspect, createArgs("oryn-snap-1", "4.5", "8g"), "ENV http://172.17.0.1:7600 orb-x oot_tok", cpFrom, "start orb-x"}},
		{"spawn paused", "paused", false, spawn(""), []string{inspect, "unpause orb-x", writeToken, hup}},
		{"spawn exited", "exited", false, spawn(""), []string{inspect, "start orb-x", writeToken, hup}},
		{"spawn running", "running", false, spawn(""), []string{inspect, writeToken, hup}},
		{"pause freeze", "running", false, Job{Op: "pause", OrbID: "orb-x", PauseMode: "freeze"}, []string{inspect, "pause orb-x"}},
		{"pause freeze already paused", "paused", false, Job{Op: "pause", OrbID: "orb-x", PauseMode: "freeze"}, []string{inspect}},
		{"pause stop paused", "paused", false, Job{Op: "pause", OrbID: "orb-x", PauseMode: "stop"}, []string{inspect, "unpause orb-x", "stop -t 30 orb-x"}},
		{"pause stop running", "running", false, Job{Op: "pause", OrbID: "orb-x", PauseMode: "stop"}, []string{inspect, "stop -t 30 orb-x"}},
		{"snapshot", "running", false, Job{Op: "snapshot", OrbID: "orb-x", SnapshotID: "oryn-snap-1"}, []string{"image inspect --format {{.Id}} oryn-snap-1", "commit orb-x oryn-snap-1"}},
		{"snapshot exists", "running", true, Job{Op: "snapshot", OrbID: "orb-x", SnapshotID: "oryn-snap-1"}, []string{"image inspect --format {{.Id}} oryn-snap-1"}},
		{"delete", "running", false, Job{Op: "delete", OrbID: "orb-x"}, []string{"rm -f orb-x"}},
		{"delete_snapshot", "running", true, Job{Op: "delete_snapshot", SnapshotID: "oryn-snap-1"}, []string{"rmi oryn-snap-1"}},
		{"delete_snapshot missing is ok", "running", false, Job{Op: "delete_snapshot", SnapshotID: "oryn-snap-1"}, []string{"rmi oryn-snap-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd.set(t, c.state, c.image)
			out := newTailBuffer(1 << 16)
			require.NoError(t, d.Exec(context.Background(), c.job, out), out.String())
			assert.Equal(t, c.want, fd.log(t))
			assert.NotContains(t, out.String(), "oot_tok", "token must not appear in output/argv")
		})
	}

	t.Run("existing container receives the rotated token on stdin", func(t *testing.T) {
		fd.set(t, "paused", false)
		require.NoError(t, d.Exec(context.Background(), spawn(""), newTailBuffer(1024)))
		b, err := os.ReadFile(filepath.Join(fd.dir, "stdin"))
		require.NoError(t, err)
		assert.Equal(t, "oot_tok", string(b))
	})

	t.Run("snapshot image carrying the source orb token file gets it replaced before start", func(t *testing.T) {
		fd.set(t, "missing", false)
		var src bytes.Buffer
		tw := tar.NewWriter(&src)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "orb-token", Mode: 0o600, Size: 8, Uid: 1000, Gid: 1000, Uname: "user", Gname: "user"}))
		_, _ = tw.Write([]byte("oot_srcx"))
		require.NoError(t, tw.Close())
		require.NoError(t, os.WriteFile(filepath.Join(fd.dir, "tokenfile.tar"), src.Bytes(), 0o644))
		out := newTailBuffer(1 << 16)
		require.NoError(t, d.Exec(context.Background(), spawn("oryn-snap-1"), out), out.String())
		log := fd.log(t)
		assert.Equal(t, []string{cpFrom, "cp -a - orb-x:/home/user/.oryn", "start orb-x"}, log[len(log)-3:], "替换发生在 start 之前")
		f, err := os.Open(filepath.Join(fd.dir, "cpin.tar"))
		require.NoError(t, err)
		defer f.Close()
		tr := tar.NewReader(f)
		hdr, err := tr.Next()
		require.NoError(t, err)
		assert.Equal(t, "orb-token", hdr.Name)
		assert.EqualValues(t, 0o600, hdr.Mode)
		assert.Equal(t, 1000, hdr.Uid, "沿用镜像内文件的属主")
		body, _ := io.ReadAll(tr)
		assert.Equal(t, "oot_tok", string(body))
		assert.NotContains(t, out.String(), "oot_tok")
	})

	t.Run("new containers get the userns-enabled seccomp profile; the temp file is removed", func(t *testing.T) {
		fd.set(t, "missing", false)
		require.NoError(t, d.Exec(context.Background(), spawn(""), newTailBuffer(1<<16)))
		b, err := os.ReadFile(filepath.Join(fd.dir, "seccomp.json"))
		require.NoError(t, err)
		assert.Equal(t, orbSeccompProfile, b)
		var prof struct {
			DefaultAction string `json:"defaultAction"`
			Syscalls      []struct {
				Names    []string        `json:"names"`
				Action   string          `json:"action"`
				Args     json.RawMessage `json:"args"`
				Includes json.RawMessage `json:"includes"`
				Excludes json.RawMessage `json:"excludes"`
			} `json:"syscalls"`
		}
		require.NoError(t, json.Unmarshal(b, &prof))
		assert.Equal(t, "SCMP_ACT_ERRNO", prof.DefaultAction)
		unconditional := map[string]bool{}
		for _, s := range prof.Syscalls {
			if s.Action == "SCMP_ACT_ALLOW" && s.Args == nil && s.Includes == nil && s.Excludes == nil {
				for _, n := range s.Names {
					unconditional[n] = true
				}
			}
		}
		assert.True(t, unconditional["clone"] && unconditional["unshare"], "clone / unshare allowed without CAP_SYS_ADMIN")
		assert.False(t, unconditional["mount"] || unconditional["setns"], "the rest stays as in Docker's default profile")
		p, err := os.ReadFile(filepath.Join(fd.dir, "seccomp.path"))
		require.NoError(t, err)
		_, err = os.Stat(strings.TrimSpace(string(p)))
		assert.ErrorIs(t, err, os.ErrNotExist, "temp profile removed after spawn")
	})

	t.Run("extra_run_args with their own seccomp option win", func(t *testing.T) {
		fd.set(t, "missing", false)
		own := NewDocker(config.Docker{Image: "oryn-orb:dev", DefaultType: "small", ExtraRunArgs: []string{"--security-opt", "seccomp=unconfined"},
			Types: map[string]config.DockerType{"small": {CPUs: 2, Memory: "4g"}}})
		require.NoError(t, own.Exec(context.Background(), spawn(""), newTailBuffer(1<<16)))
		raw, err := os.ReadFile(filepath.Join(fd.dir, "log"))
		require.NoError(t, err)
		assert.Contains(t, string(raw), "-e ORYN_ORB_TOKEN --security-opt seccomp=unconfined oryn-orb:dev orbd run")
		assert.Equal(t, 1, strings.Count(string(raw), "seccomp="))
	})

	t.Run("pause missing container fails", func(t *testing.T) {
		fd.set(t, "missing", false)
		assert.Error(t, d.Exec(context.Background(), Job{Op: "pause", OrbID: "orb-x", PauseMode: "freeze"}, newTailBuffer(1024)))
	})
}

func TestDockerInfo(t *testing.T) {
	d := NewDocker(config.Docker{DefaultType: "medium"})
	info := d.Info()
	assert.Equal(t, apigen.Docker, info.Driver)
	assert.True(t, info.Capabilities.Freeze)
	assert.Equal(t, []apigen.OrbType{
		{Name: "large", Cpus: 8, MemoryMB: 16384},
		{Name: "medium", Cpus: 4, MemoryMB: 8192},
		{Name: "small", Cpus: 2, MemoryMB: 4096},
	}, info.Types)
	assert.Equal(t, 512, memoryMB("512m"))
	assert.Equal(t, 1, memoryMB("1048576"))
}

func TestDispatchEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "dispatch.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nenv | grep -E '^(ORB_[A-Z_]*|ORYN_ORB_TOKEN|ORYN_URL)=' | sort > \""+dir+"/env\"\necho hello-from-dispatch\n[ \"$ORB_OPERATION\" != delete ]\n"), 0o755))
	d := NewDispatch(script, config.Docker{Image: "oryn-orb:dev", DefaultType: "small"})
	snap := "oryn-snap-2"
	out := newTailBuffer(1024)
	require.NoError(t, d.Exec(context.Background(), Job{Op: "spawn", OrbID: "orb-y", Spawn: &apigen.JobSpawnParams{
		Type: "small", SnapshotID: &snap, Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_z"},
	}}, out))
	assert.Contains(t, out.String(), "hello-from-dispatch")
	b, err := os.ReadFile(filepath.Join(dir, "env"))
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		"ORB_ID=orb-y", "ORB_IMAGE=oryn-orb:dev", "ORB_OPERATION=spawn", "ORB_PAUSE_MODE=", "ORB_SNAPSHOT_ID=oryn-snap-2",
		"ORB_TYPE=small", "ORYN_ORB_TOKEN=oot_z", "ORYN_URL=http://u",
	}, "\n")+"\n", string(b))

	require.NoError(t, d.Exec(context.Background(), Job{Op: "pause", OrbID: "orb-y", PauseMode: "freeze"}, out))
	b, _ = os.ReadFile(filepath.Join(dir, "env"))
	assert.Contains(t, string(b), "ORB_PAUSE_MODE=freeze")
	assert.Error(t, d.Exec(context.Background(), Job{Op: "delete", OrbID: "orb-y"}, out), "non-zero exit = failure")
}

func TestParseJob(t *testing.T) {
	j, err := ParseJob(apigen.ProviderJob{ID: "job_1", Op: apigen.Pause, OrbID: "orb-1", Params: map[string]any{"mode": "stop"}})
	require.NoError(t, err)
	assert.Equal(t, "stop", j.PauseMode)
	_, err = ParseJob(apigen.ProviderJob{ID: "job_1", Op: apigen.Pause, OrbID: "orb-1", Params: map[string]any{"mode": "x"}})
	assert.Error(t, err)
	_, err = ParseJob(apigen.ProviderJob{ID: "job_1", Op: apigen.Snapshot, OrbID: "orb-1", Params: map[string]any{}})
	assert.Error(t, err)
	j, err = ParseJob(apigen.ProviderJob{ID: "job_1", Op: apigen.DeleteSnapshot, Params: map[string]any{"snapshotId": "oryn-snap-1"}})
	require.NoError(t, err)
	assert.Equal(t, "oryn-snap-1", j.SnapshotID)
}

// CP16：任务超时时整个进程组被终止，且不会因后代进程占着输出管道而挂住 worker。
func TestDispatchTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "dispatch.sh")
	// 子进程继承 stdout 并长时间运行；脚本自己也不退出。
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 300 &\necho $! > "+pidFile+"\nwait\n"), 0o755))
	d := NewDispatch(script, config.Docker{})
	d.WaitDelay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := d.Exec(ctx, Job{Op: "delete", OrbID: "orb-y"}, newTailBuffer(1024))
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "超时后应在 WaitDelay 内返回")
	b, rerr := os.ReadFile(pidFile)
	require.NoError(t, rerr)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond, "子进程随进程组被终止")
}

// CP16：脚本以 0 退出但留下仍持有输出管道的后台进程（如未重定向输出的 orbd）时，至多 WaitDelay 后按成功返回。
func TestDispatchSuccessWithLingeringChild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "dispatch.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 300 &\necho $! > "+pidFile+"\necho spawned\nexit 0\n"), 0o755))
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	d := NewDispatch(script, config.Docker{})
	d.WaitDelay = 200 * time.Millisecond
	start := time.Now()
	out := newTailBuffer(1024)
	require.NoError(t, d.Exec(context.Background(), Job{Op: "delete", OrbID: "orb-y"}, out))
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Contains(t, out.String(), "spawned")
}
