package providerrt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
if [ "$1" = run ]; then echo "ENV $ORYN_URL $ORYN_ORB_ID $ORYN_ORB_TOKEN" >> "$FAKE_DOCKER_DIR/log"; echo cid; fi
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
	_ = os.Remove(filepath.Join(f.dir, "log"))
	_ = os.Remove(filepath.Join(f.dir, "image"))
	if image {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "image"), nil, 0o644))
	}
}

func (f *fakeDockerEnv) log(t *testing.T) []string {
	b, err := os.ReadFile(filepath.Join(f.dir, "log"))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
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
	runArgs := func(image, cpus, mem string) string {
		return "run -d --init --name orb-x --hostname orb-x --label oryn-orb=1 --cpus " + cpus + " --memory " + mem +
			" --shm-size 1g --add-host host.docker.internal:host-gateway -e ORYN_URL -e ORYN_ORB_ID -e ORYN_ORB_TOKEN --runtime runc " +
			image + " orbd run"
	}
	cases := []struct {
		name  string
		state string
		image bool
		job   Job
		want  []string
	}{
		{"spawn new", "missing", false, spawn(""), []string{inspect, runArgs("oryn-orb:dev", "2", "4g"), "ENV http://172.17.0.1:7600 orb-x oot_tok"}},
		{"spawn new from snapshot, unknown type → default", "missing", false, func() Job {
			j := spawn("oryn-snap-1")
			j.Spawn.Type = "huge"
			return j
		}(), []string{inspect, runArgs("oryn-snap-1", "4.5", "8g"), "ENV http://172.17.0.1:7600 orb-x oot_tok"}},
		{"spawn paused", "paused", false, spawn(""), []string{inspect, "unpause orb-x", "exec orb-x pkill -HUP -x orbd"}},
		{"spawn exited", "exited", false, spawn(""), []string{inspect, "start orb-x"}},
		{"spawn running", "running", false, spawn(""), []string{inspect}},
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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nenv | grep -E '^(ORB_|ORYN_)' | sort > \""+dir+"/env\"\necho hello-from-dispatch\n[ \"$ORB_OPERATION\" != delete ]\n"), 0o755))
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
