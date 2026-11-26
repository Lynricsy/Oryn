package providerrt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lynricsy/Oryn/internal/config"
)

// docker 驱动的 Agent 沙箱（M30）：中继、网络切换与 provider 重启后的中继恢复。

// sandboxLinkMount 是沙箱容器里挂 <socket_dir>/<orbId>/ 的位置（只读）；orbd 拨其中的 link.sock。
// 挂目录而不是挂 socket 文件：provider 重启后重建 socket，文件挂载会失效。
const sandboxLinkMount = "/run/oryn-link"

// sandboxInspectFormat 读沙箱容器的网络模式标签与当前接入的网络。
const sandboxInspectFormat = `{{index .Config.Labels "oryn-sandbox"}}|{{json .NetworkSettings.Networks}}`

func (d *Docker) sandboxSocketDir() string {
	if d.Cfg.Sandbox.SocketDir != "" {
		return d.Cfg.Sandbox.SocketDir
	}
	return config.DefaultSandboxSocketDir
}

// ensureSandboxRelay 建好 orbID 的中继（<socket_dir>/<orbID>/link.sock，0666；已有转发到同一目标的则沿用），返回目标 host:port。
func (d *Docker) ensureSandboxRelay(orbID, orynURL string) (string, error) {
	target, err := relayTarget(orynURL)
	if err != nil {
		return "", err
	}
	if d.relays.has(orbID, target) {
		return target, nil
	}
	return target, d.listenSandbox(orbID, target)
}

// listenSandbox 建 <socket_dir>/<orbID>/（0755）与其中的 link.sock（0666，容器内 orbd 用户要能连），登记中继。
func (d *Docker) listenSandbox(orbID, target string) error {
	base := d.sandboxSocketDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("docker: 建沙箱中继目录: %w", err)
	}
	dir := filepath.Join(base, orbID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("docker: 建沙箱中继目录: %w", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return fmt.Errorf("docker: 建沙箱中继目录: %w", err)
	}
	return d.relays.listen(orbID, filepath.Join(dir, "link.sock"), 0o666, -1, target)
}

// setNetwork 实现 set_network：按容器的 oryn-sandbox 标签（创建时的模式）与当前接入的网络，
// 把沙箱接入或断开 docker.sandbox.network。已处于目标状态时不做事。
func (d *Docker) setNetwork(ctx context.Context, j Job, out io.Writer) error {
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, out, nil, "container", "inspect", "--format", sandboxInspectFormat, j.OrbID); err != nil {
		return err
	}
	label, nets, _ := strings.Cut(strings.TrimSpace(stdout.String()), "|")
	switch label {
	case "":
		return fmt.Errorf("docker: 容器 %s 不是沙箱", j.OrbID)
	case "none":
		if j.Network == "egress" {
			return fmt.Errorf("network_unavailable: sandbox %s was created without a network", j.OrbID)
		}
		return nil
	}
	var networks map[string]json.RawMessage
	if err := json.Unmarshal([]byte(nets), &networks); err != nil {
		return fmt.Errorf("docker: 解析容器 %s 的网络 %q: %w", j.OrbID, nets, err)
	}
	net := d.Cfg.Sandbox.Network
	_, connected := networks[net]
	switch {
	case j.Network == "egress" && !connected:
		return d.run(ctx, out, nil, "network", "connect", net, j.OrbID)
	case j.Network == "none" && connected:
		return d.run(ctx, out, nil, "network", "disconnect", "-f", net, j.OrbID)
	}
	return nil
}

// Housekeep 实现 Housekeeper：先为在运行的沙箱容器重建中继（provider 重启后恢复 link），之后等 ctx 结束再关闭全部中继。
func (d *Docker) Housekeep(ctx context.Context) {
	d.restoreRelays(ctx)
	<-ctx.Done()
	d.relays.closeAll()
}

// restoreRelays 按容器标签 oryn-sandbox-link 为每台在运行的沙箱重建监听与中继；失败只记日志。
func (d *Docker) restoreRelays(ctx context.Context) {
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, io.Discard, nil, "ps", "--filter", "label=oryn-sandbox",
		"--format", `{{.Names}}|{{.Label "oryn-sandbox-link"}}`); err != nil {
		slog.Warn("恢复沙箱中继：列出沙箱容器失败", "err", err)
		return
	}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		name, target, _ := strings.Cut(strings.TrimSpace(line), "|")
		if name == "" || target == "" || d.relays.has(name, target) {
			continue
		}
		if err := d.listenSandbox(name, target); err != nil {
			slog.Warn("恢复沙箱中继失败", "orb", name, "err", err)
		}
	}
}
