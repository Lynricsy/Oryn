package providerrt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/fcguest"
)

// fcHost 是驱动对宿主的副作用：进程、磁盘、网络（真实现见 fc_host.go，单元测试用内存假实现）。
type fcHost interface {
	// Launch 重建 jail、用 jailer 启动 firecracker 并等 api.sock 就绪。
	Launch(ctx context.Context, vm vmMeta) error
	// Alive 报告该 orb 的 firecracker 是否还有进程（暂停也算）。
	Alive(orbID string) bool
	// Kill 用 cgroup.kill 结束进程（没有则跳过），再清掉 cgroup 与 jail 目录；幂等。
	Kill(orbID string) error
	// TapUp 建好 tap（已存在则先删再建）；TapDown 删掉 tap（不存在不算错）。
	TapUp(vm vmMeta) error
	TapDown(vm vmMeta) error
	// MakeDisk 在 path 建 gb GiB 的稀疏 ext4 空盘，属主为 uid。
	MakeDisk(path string, gb int, uid int) error
	// CopySparse 稀疏复制 src → dst（属主为 uid），返回 dst 实际占用的字节数。
	CopySparse(src, dst string, uid int) (bytes int64, err error)
}

// fcAPI 是 jail 里 api.sock 上的 Firecracker HTTP API。
type fcAPI interface {
	// Boot 配置机器、磁盘、网卡、vsock 并 InstanceStart。
	Boot(ctx context.Context, vm vmMeta) error
	// State 返回 Running | Paused | Not started；不可达返回错误。
	State(ctx context.Context, orbID string) (string, error)
	// SetState 发 PATCH /vm（Paused | Resumed）。
	SetState(ctx context.Context, orbID, state string) error
	// CtrlAltDel 发 SendCtrlAltDel（guest-init 把它当作 shutdown{30}）。
	CtrlAltDel(ctx context.Context, orbID string) error
}

// fcGuest 是 jail 里 v.sock 上的 fcguest 协议。
type fcGuest interface {
	Do(ctx context.Context, orbID string, req fcguest.Request) error
}

// Firecracker 的 VM 状态（GET / 的 state）。
const (
	fcStateRunning = "Running"
	fcStatePaused  = "Paused"
)

// fcBootArgs 返回内核命令行：只读根 /dev/vda，init 为 orbd（PID 1 时进入 guest-init），
// 内核按 ip= 静态配置 eth0（guest/30，网关为宿主侧地址），reboot=k 使 guest 复位时 Firecracker 退出。
func fcBootArgs(vm vmMeta) string {
	return "console=ttyS0 reboot=k panic=1 pci=off nomodule i8042.noaux i8042.nomux i8042.dumbkbd quiet " +
		"root=/dev/vda ro init=/usr/local/bin/orbd ip=" + vm.GuestIP + "::" + vm.HostIP + ":255.255.255.252::eth0:off"
}

// fcVsockCID 是 guest 的 vsock CID。
const fcVsockCID = 3

// unixAPI 是 fcAPI 的真实现：标准库 http.Client 经 unix socket 拨号（不用 SDK）。
type unixAPI struct {
	sock func(orbID string) string
}

// apiError 是 Firecracker API 的非 2xx 应答。
type apiError struct {
	method, path string
	status       int
	body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("firecracker API %s %s: %d %s", e.method, e.path, e.status, strings.TrimSpace(e.body))
}

func (a unixAPI) do(ctx context.Context, orbID, method, path string, body any, out any) error {
	sock := a.sock(orbID)
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnix(ctx, sock)
		},
		DisableKeepAlives: true,
	}}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return &apiError{method: method, path: path, status: resp.StatusCode, body: string(rb)}
	}
	if out != nil {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("firecracker API %s %s: 解析应答: %w", method, path, err)
		}
	}
	return nil
}

// fcConsoleDir 是 jail 里放串口日志的目录：Launch 在上面挂一个容量固定的 tmpfs（见 mountConsole）；
// fcConsoleLog 是其中 guest 串口输出的文件（Launch 预先建好并交给槽位 uid）。启动失败时把末尾带进任务输出。
const (
	fcConsoleDir = "console"
	fcConsoleLog = "console.log"
)

// fcSerialRateLimit 限制串口写文件的速率：开机日志可一次写 1 MiB，之后每秒 16 KiB。限速只能拖慢写入，
// 总量由 tmpfs 的容量限定（fc_console.go）。
var fcSerialRateLimit = map[string]any{"size": 16 << 10, "one_time_burst": 1 << 20, "refill_time": 1000}

// Boot 实现 fcAPI：依次 machine-config、boot-source、两块盘、eth0、vsock、串口、entropy（失败忽略）、InstanceStart。
// 路径都是 jail（chroot）里的路径。
func (a unixAPI) Boot(ctx context.Context, vm vmMeta) error {
	steps := []struct {
		path string
		body any
	}{
		{"/machine-config", map[string]any{"vcpu_count": vm.CPUs, "mem_size_mib": vm.MemMB, "smt": false}},
		{"/boot-source", map[string]any{"kernel_image_path": "/vmlinux", "boot_args": fcBootArgs(vm)}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": "/base.ext4", "is_root_device": false, "is_read_only": true}},
		{"/drives/data", map[string]any{"drive_id": "data", "path_on_host": "/disk.ext4", "is_root_device": false, "is_read_only": false}},
		{"/network-interfaces/eth0", map[string]any{"iface_id": "eth0", "host_dev_name": vm.Tap, "guest_mac": vm.MAC}},
		{"/vsock", map[string]any{"guest_cid": fcVsockCID, "uds_path": "/v.sock"}},
		{"/serial", map[string]any{"serial_out_path": "/" + fcConsoleDir + "/" + fcConsoleLog, "rate_limiter": fcSerialRateLimit}},
	}
	for _, s := range steps {
		if err := a.do(ctx, vm.OrbID, http.MethodPut, s.path, s.body, nil); err != nil {
			return err
		}
	}
	// entropy 设备只是锦上添花（guest 有 RDRAND），旧版本或配置不支持时忽略。
	_ = a.do(ctx, vm.OrbID, http.MethodPut, "/entropy", map[string]any{}, nil)
	return a.do(ctx, vm.OrbID, http.MethodPut, "/actions", map[string]any{"action_type": "InstanceStart"}, nil)
}

// State 实现 fcAPI。
func (a unixAPI) State(ctx context.Context, orbID string) (string, error) {
	var info struct {
		State string `json:"state"`
	}
	if err := a.do(ctx, orbID, http.MethodGet, "/", nil, &info); err != nil {
		return "", err
	}
	return info.State, nil
}

// SetState 实现 fcAPI。
func (a unixAPI) SetState(ctx context.Context, orbID, state string) error {
	return a.do(ctx, orbID, http.MethodPatch, "/vm", map[string]any{"state": state}, nil)
}

// CtrlAltDel 实现 fcAPI。
func (a unixAPI) CtrlAltDel(ctx context.Context, orbID string) error {
	return a.do(ctx, orbID, http.MethodPut, "/actions", map[string]any{"action_type": "SendCtrlAltDel"}, nil)
}

// vsockGuest 是 fcGuest 的真实现：经 jail 里的 v.sock 走 fcguest 协议（握手、一问一答）。
// 不用 fcguest.Call：它按路径直接拨号，路径超过 sun_path 时会失败；这里用 dialUnix 拨好再握手。
type vsockGuest struct {
	sock func(orbID string) string
}

// Do 实现 fcGuest：语义同 fcguest.Call（整个过程受 ctx 约束；guest 应答 ok=false 时返回 *fcguest.RemoteError）。
func (g vsockGuest) Do(ctx context.Context, orbID string, req fcguest.Request) error {
	conn, err := dialUnix(ctx, g.sock(orbID))
	if err != nil {
		return fmt.Errorf("fcguest %s: %w", req.Op, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	r, err := fcguest.Handshake(conn, fcguest.Port)
	if err == nil {
		err = fcguest.WriteRequest(conn, req)
	}
	var resp fcguest.Response
	if err == nil {
		resp, err = fcguest.ReadResponse(r)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("fcguest %s: %w", req.Op, ctxErr)
		}
		return fmt.Errorf("fcguest %s: %w", req.Op, err)
	}
	if !resp.OK {
		return &fcguest.RemoteError{Op: req.Op, Msg: resp.Error}
	}
	return nil
}
