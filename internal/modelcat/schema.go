package modelcat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/core"
)

const (
	// schemaTimeout 是一次 RPC schema 运行的时限。
	schemaTimeout = 30 * time.Second
	// schemaFile 是缓存目录下保存设置 schema 的文件。
	schemaFile = "settings-schema.json"
	// schemaRequestID 是 get_settings_schema 请求的 id。
	schemaRequestID = "1"
	// maxRPCLine 是单行 RPC 输出的上限（与 ready 帧里的 maxReassembledFrameBytes 一致）。
	maxRPCLine = 64 << 20
	// rpcExitGrace 是读到响应、关闭 stdin 后等 omp 自行退出的时间，超时则杀掉进程组。
	rpcExitGrace = 3 * time.Second
)

// schemaRequest 是写给 omp RPC stdin 的请求行。
var schemaRequest = []byte(`{"id":"` + schemaRequestID + `","type":"get_settings_schema"}` + "\n")

// SettingEntry 是 omp 设置 schema 中的一项（只取控制面需要的字段）。
type SettingEntry struct {
	// Path 是设置键的点分路径，如 "thinkingBudgets.low"。
	Path string `json:"path"`
	// Type 是 boolean、number、string、enum、array、record 之一。
	Type string `json:"type"`
	// RestartRequired 为真表示修改后要重启会话才生效。
	RestartRequired bool `json:"restartRequired,omitempty"`
	TuiOnly         bool `json:"tuiOnly,omitempty"`
	Secret          bool `json:"secret,omitempty"`
	Advanced        bool `json:"advanced,omitempty"`
}

// SettingsSchema 是 `get_settings_schema` 的结果（按钉住的 Core 版本固定）。
type SettingsSchema struct {
	Entries []SettingEntry `json:"entries"`
}

// RestartRequired 报告修改 config.yml 的 path 键是否要重启会话：
//   - 与某项精确匹配时取该项的值；
//   - 位于 record/array 型项之下（如 "ttsr.disabledRules.0"）时取该项的值；
//   - 是若干项的上级（如整体改了 "thinkingBudgets"）时，任一下级项需要重启即为真；
//   - schema 里没有的键为假。
func (s SettingsSchema) RestartRequired(path string) bool {
	for _, e := range s.Entries {
		switch {
		case e.Path == path:
			return e.RestartRequired
		case strings.HasPrefix(path, e.Path+".") && (e.Type == "record" || e.Type == "array"):
			return e.RestartRequired
		case e.RestartRequired && strings.HasPrefix(e.Path, path+"."):
			return true
		}
	}
	return false
}

// schemaEntry 是一份成功的 schema 及其缓存键（Core 钉）；也是 schemaFile 的格式。
type schemaEntry struct {
	Key    string         `json:"key"`
	Schema SettingsSchema `json:"schema"`
}

// schemaCall 是一次 schema 运行；done 关闭后 res/err 只读。
type schemaCall struct {
	done chan struct{}
	res  SettingsSchema
	err  error
}

// SettingsSchema 返回钉住的 Core 的设置 schema。缓存键只看本架构的 Core 钉（与配置无关），
// 命中内存或磁盘缓存时直接返回；否则一次性运行 `omp --mode rpc --no-session`（并发调用合并为一次）。
// 运行失败时返回上一份结果（来自旧的 Core 钉），没有时返回错误。
func (c *Catalog) SettingsSchema(ctx context.Context) (SettingsSchema, error) {
	key := core.Pin()[pinKey()]
	c.schemaMu.Lock()
	if s := c.schema; s != nil && s.Key == key {
		c.schemaMu.Unlock()
		return s.Schema, nil
	}
	cl := c.schemaFlight
	if cl == nil {
		cl = &schemaCall{done: make(chan struct{})}
		c.schemaFlight = cl
		go c.runSchema(cl, key)
	}
	c.schemaMu.Unlock()
	select {
	case <-cl.done:
	case <-ctx.Done():
		return SettingsSchema{}, ctx.Err()
	}
	if cl.err == nil {
		return cl.res, nil
	}
	c.schemaMu.Lock()
	old := c.schema
	c.schemaMu.Unlock()
	if old == nil {
		return SettingsSchema{}, cl.err
	}
	return old.Schema, nil
}

func (c *Catalog) runSchema(cl *schemaCall, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	res, err := c.fetchSchema(ctx)
	if err != nil {
		slog.Warn("获取设置 schema 失败", "err", err)
	} else if err := c.writeCache(schemaFile, schemaEntry{Key: key, Schema: res}); err != nil {
		slog.Warn("设置 schema 缓存写入失败", "dir", c.cfg.CacheDir, "err", err)
	}
	c.schemaMu.Lock()
	if err == nil {
		c.schema = &schemaEntry{Key: key, Schema: res}
	}
	cl.res, cl.err = res, err
	c.schemaFlight = nil
	c.schemaMu.Unlock()
	close(cl.done)
}

// fetchSchema 在空的临时 HOME（0700，结束即删）里运行 omp RPC 并解析响应。schema 只取决于 Core 版本，
// 所以不渲染 omp-home，也不起 egress。
func (c *Catalog) fetchSchema(ctx context.Context) (SettingsSchema, error) {
	tmp, err := newTempHome()
	if err != nil {
		return SettingsSchema{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	line, err := c.cfg.SchemaRun(ctx, tmp)
	if err != nil {
		return SettingsSchema{}, err
	}
	return parseSchemaResponse(line)
}

// parseSchemaResponse 解析 get_settings_schema 的 response 行。
func parseSchemaResponse(line []byte) (SettingsSchema, error) {
	var resp struct {
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Data    *SettingsSchema `json:"data"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return SettingsSchema{}, fmt.Errorf("解析 get_settings_schema 响应: %w", err)
	}
	if !resp.Success {
		return SettingsSchema{}, fmt.Errorf("get_settings_schema 失败: %s", resp.Error)
	}
	if resp.Data == nil || resp.Data.Entries == nil {
		return SettingsSchema{}, errors.New("get_settings_schema 响应缺少 entries")
	}
	return *resp.Data, nil
}

// isSchemaResponse 报告 RPC 输出行是否为本次请求的 response（启动时还会先输出 ready、
// extension_ui_request 等行，后者也带 id，所以要同时比对 type）。
func isSchemaResponse(line []byte) bool {
	var h struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	return json.Unmarshal(line, &h) == nil && h.Type == "response" && h.ID == schemaRequestID
}

// loadSchemaDisk 读取落盘的 schema；不存在或损坏时返回 nil。键与当前 Core 钉不同的也保留，作失败时的回退。
func (c *Catalog) loadSchemaDisk() *schemaEntry {
	var e schemaEntry
	if !c.readCache(schemaFile, &e) || e.Key == "" || e.Schema.Entries == nil {
		return nil
	}
	return &e
}

// rpcSettingsSchema 是默认的 Config.SchemaRun：以 RPC 模式启动 omp（环境变量从零构造、独立进程组），
// 写入请求行，读 stdout 直到拿到响应；之后关闭 stdin 等它退出，超过 rpcExitGrace 则杀掉进程组。
func (c *Catalog) rpcSettingsSchema(ctx context.Context, home string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.cfg.OmpPath, "--mode", "rpc", "--no-session")
	cmd.Dir = home
	cmd.Env = ompEnv(home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), maxRPCLine)
	var line []byte
	_, err = stdin.Write(schemaRequest)
	if err == nil {
		for sc.Scan() {
			if isSchemaResponse(sc.Bytes()) {
				line = bytes.Clone(sc.Bytes())
				break
			}
		}
		err = sc.Err()
	}
	stopRPC(cmd, stdin, stdout)
	switch {
	case line != nil:
		return line, nil
	case ctx.Err() != nil:
		err = ctx.Err()
	case err == nil:
		err = errors.New("omp 在响应前关闭了输出")
	}
	// stopRPC 返回后进程已退出，stderr 不再被写入
	return nil, fmt.Errorf("omp RPC get_settings_schema: %w%s", err, errDetail(stderr.Bytes()))
}

// stopRPC 关闭 stdin 让 omp 自行退出，同时排空剩余 stdout（免得它因管道写满卡住）；
// rpcExitGrace 内没退出则杀掉进程组。返回时进程已被回收。
func stopRPC(cmd *exec.Cmd, stdin io.Closer, stdout io.Reader) {
	_ = stdin.Close()
	exited := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(rpcExitGrace):
		_ = cmd.Cancel()
		<-exited
	}
}
