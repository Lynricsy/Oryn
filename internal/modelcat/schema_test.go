package modelcat

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/core"
)

// testdata/rpc-settings-schema.jsonl 是真实 Core（空 HOME）`--mode rpc --no-session` 的输出节选：
// 启动时的 extension_ui_request 与 ready 两行，以及只保留 10 项的 get_settings_schema 响应。
const schemaTestdata = "testdata/rpc-settings-schema.jsonl"

func restartKeys(s SettingsSchema) []string {
	var out []string
	for _, e := range s.Entries {
		if e.RestartRequired {
			out = append(out, e.Path)
		}
	}
	return out
}

func recordedResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(schemaTestdata)
	require.NoError(t, err)
	for l := range bytes.SplitSeq(b, []byte("\n")) {
		if isSchemaResponse(l) {
			return l
		}
	}
	t.Fatal("testdata 中没有 response 行")
	return nil
}

// fakeRPCOmp 写一个假 omp：校验参数与环境，先输出启动行，读到请求后回放响应，等 stdin 关闭再退出。
// 每次运行往 dir/count 追加一行，并把 HOME 写到 dir/home。
func fakeRPCOmp(t *testing.T, dir, tail string) string {
	t.Helper()
	data, err := filepath.Abs(schemaTestdata)
	require.NoError(t, err)
	script := `#!/bin/sh
[ "$*" = "--mode rpc --no-session" ] || { echo "bad args: $*" >&2; exit 2; }
[ "$PI_CODING_AGENT_DIR" = "$HOME/.omp/agent" ] && [ -d "$PI_CODING_AGENT_DIR" ] || { echo "bad agent dir" >&2; exit 3; }
echo run >> '` + filepath.Join(dir, "count") + `'
printf '%s' "$HOME" > '` + filepath.Join(dir, "home") + `'
head -n 2 '` + data + `'
IFS= read -r req
[ "$req" = '{"id":"1","type":"get_settings_schema"}' ] || { echo "bad request: $req" >&2; exit 4; }
echo '{"type":"extension_ui_request","id":"1","method":"setWidget"}'
tail -n +3 '` + data + `'
` + tail + "\n"
	omp := filepath.Join(dir, "omp")
	require.NoError(t, os.WriteFile(omp, []byte(script), 0o755))
	return omp
}

func runs(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "count"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(b), "run\n")
}

func TestSettingsSchemaFakeOmp(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "model-catalog")
	omp := fakeRPCOmp(t, dir, "cat > /dev/null")
	c := New(Config{OmpPath: omp, CacheDir: cacheDir}, newHome(t), nil)

	s, err := c.SettingsSchema(t.Context())
	require.NoError(t, err)
	require.Len(t, s.Entries, 10)
	assert.Equal(t, []string{"thinkingBudgets.low", "thinkingBudgets.high", "ttsr.enabled", "ttsr.disabledRules", "lsp.lazy"}, restartKeys(s))
	assert.Equal(t, SettingEntry{Path: "auth.broker.token", Type: "string", Secret: true, Advanced: true}, s.Entries[0])
	assert.Equal(t, SettingEntry{Path: "git.enabled", Type: "boolean", TuiOnly: true}, s.Entries[2])

	for path, want := range map[string]bool{
		"lsp.lazy":                    true,
		"thinkingBudgets.low":         true,
		"ttsr.disabledRules":          true,
		"ttsr.disabledRules.0":        true, // array 型项之下
		"thinkingBudgets":             true, // 下级项需要重启
		"ttsr":                        true,
		"modelRoles":                  false,
		"modelRoles.default":          false, // record 型项之下
		"statusLine.segmentOptions.x": false,
		"git.enabled":                 false,
		"lsp.lazyX":                   false,
		"unknown.key":                 false,
	} {
		assert.Equal(t, want, s.RestartRequired(path), path)
	}

	home, err := os.ReadFile(filepath.Join(dir, "home"))
	require.NoError(t, err)
	assert.NoDirExists(t, string(home), "临时 HOME 用完即删")

	// 同一 Core 钉：内存命中，不再运行
	_, err = c.SettingsSchema(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, runs(t, dir))

	// 重启后磁盘命中：即使 omp 已不可用也不运行
	fi, err := os.Stat(filepath.Join(cacheDir, schemaFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	c2 := New(Config{OmpPath: filepath.Join(dir, "missing"), CacheDir: cacheDir}, newHome(t), nil)
	s2, err := c2.SettingsSchema(t.Context())
	require.NoError(t, err)
	assert.Equal(t, s, s2)
}

// omp 拿到响应后不理会 stdin 关闭：宽限期后整个进程组被杀，结果照常返回。
func TestSettingsSchemaKillsLingeringOmp(t *testing.T) {
	dir := t.TempDir()
	omp := fakeRPCOmp(t, dir, "sleep 60")
	c := New(Config{OmpPath: omp}, newHome(t), nil)
	start := time.Now()
	s, err := c.SettingsSchema(t.Context())
	require.NoError(t, err)
	assert.Len(t, restartKeys(s), 5)
	assert.Less(t, time.Since(start), rpcExitGrace+5*time.Second)
}

func TestSettingsSchemaConcurrentAndFailure(t *testing.T) {
	resp := recordedResponse(t)
	var n atomic.Int32
	var fail atomic.Bool
	gate := make(chan struct{})
	run := func(ctx context.Context, home string) ([]byte, error) {
		n.Add(1)
		<-gate
		if fail.Load() {
			return nil, errors.New("omp 退出码 1")
		}
		return resp, nil
	}
	cacheDir := t.TempDir()

	// 没有任何旧结果时失败：返回错误
	fail.Store(true)
	close(gate)
	_, err := New(Config{SchemaRun: run, CacheDir: cacheDir}, newHome(t), nil).SettingsSchema(t.Context())
	assert.ErrorContains(t, err, "退出码 1")

	// 并发 10 次只运行 1 次
	fail.Store(false)
	n.Store(0)
	gate = make(chan struct{})
	c := New(Config{SchemaRun: run, CacheDir: cacheDir}, newHome(t), nil)
	var wg sync.WaitGroup
	got := make([]SettingsSchema, 10)
	errs := make([]error, 10)
	for i := range 10 {
		wg.Go(func() { got[i], errs[i] = c.SettingsSchema(t.Context()) })
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	assert.EqualValues(t, 1, n.Load())
	for i := range 10 {
		require.NoError(t, errs[i])
		assert.Len(t, restartKeys(got[i]), 5)
	}

	// 磁盘上是旧 Core 钉的结果、这次运行失败：返回旧结果
	b, err := os.ReadFile(filepath.Join(cacheDir, schemaFile))
	require.NoError(t, err)
	key := core.Pin()[pinKey()]
	require.Contains(t, string(b), key)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, schemaFile), bytes.Replace(b, []byte(key), []byte("old-pin"), 1), 0o600))
	fail.Store(true)
	n.Store(0)
	old, err := New(Config{SchemaRun: run, CacheDir: cacheDir}, newHome(t), nil).SettingsSchema(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n.Load(), "键不符时重跑")
	assert.Len(t, restartKeys(old), 5)
}

func TestParseSchemaResponse(t *testing.T) {
	_, err := parseSchemaResponse([]byte(`{"id":"1","type":"response","success":false,"error":"unknown command"}`))
	assert.ErrorContains(t, err, "unknown command")
	_, err = parseSchemaResponse([]byte(`{"id":"1","type":"response","success":true,"data":{}}`))
	assert.ErrorContains(t, err, "entries")
	assert.False(t, isSchemaResponse([]byte(`{"type":"extension_ui_request","id":"1"}`)))
	assert.False(t, isSchemaResponse([]byte(`{"type":"response","id":"2"}`)))
}

// 用仓库里构建好的真实 Core 跑一次：ORYN_REAL_CORE=1 go test -run TestSettingsSchemaRealCore -v ./internal/modelcat/
func TestSettingsSchemaRealCore(t *testing.T) {
	if os.Getenv("ORYN_REAL_CORE") != "1" {
		t.Skip("设置 ORYN_REAL_CORE=1 才运行")
	}
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	omp, err := filepath.Abs(filepath.Join("..", "..", "build", "core", arch, "omp"))
	require.NoError(t, err)
	c := New(Config{OmpPath: omp}, newHome(t), nil)
	start := time.Now()
	s, err := c.SettingsSchema(t.Context())
	require.NoError(t, err)
	keys := restartKeys(s)
	t.Logf("耗时 %s，共 %d 项，restartRequired %d 项：%s", time.Since(start).Round(time.Millisecond), len(s.Entries), len(keys), strings.Join(keys, ", "))
	assert.NotEmpty(t, keys)
}
