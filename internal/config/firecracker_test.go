package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M11b：driver: firecracker 的默认值（键与默认值逐字照计划）、YAML 覆盖与每条非法值。
func TestFirecrackerConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	load := func(body string) (Provider, error) {
		require.NoError(t, os.WriteFile(path, []byte("url: http://172.31.255.1:7600\ntoken_file: /tmp/t\ndriver: firecracker\n"+body), 0o600))
		return LoadProvider(path)
	}

	c, err := load("")
	require.NoError(t, err)
	assert.Equal(t, Firecracker{
		Bin:      "/usr/local/bin/firecracker",
		Jailer:   "/usr/local/bin/jailer",
		Kernel:   "/var/lib/oryn-fc/artifacts/vmlinux",
		Rootfs:   "/var/lib/oryn-fc/artifacts/rootfs.ext4",
		StateDir: "/var/lib/oryn-fc",
		DiskGB:   40,
		UIDBase:  2_000_000_000,
		Network:  FirecrackerNetwork{CIDR: "172.31.0.0/16", HostAddr: "172.31.255.1", DNS: []string{"1.1.1.1", "1.0.0.1"}},
		Limits:   FirecrackerLimits{MaxVMs: 8, MaxMemoryMB: 32768},
		Types: map[string]FirecrackerType{
			"small": {CPUs: 2, Memory: "4g"}, "medium": {CPUs: 4, Memory: "8g"}, "large": {CPUs: 8, Memory: "16g"},
		},
		DefaultType: "medium",
	}, c.Firecracker)
	assert.Equal(t, "firecracker", c.Driver)
	assert.Equal(t, 8192, c.Firecracker.Types["medium"].MemoryMB())
	assert.Equal(t, "172.31.0.0/16", c.Firecracker.Network.Prefix().String())

	c, err = load("firecracker:\n  disk_gb: 8\n  network: {cidr: 10.88.0.0/16, host_addr: 10.88.255.254}\n" +
		"  limits: {max_vms: 3, max_memory_mb: 12288}\n  types: {tiny: {cpus: 1, memory: 512m}}\n  default_type: tiny\n")
	require.NoError(t, err)
	assert.Equal(t, 8, c.Firecracker.DiskGB)
	assert.Equal(t, FirecrackerNetwork{CIDR: "10.88.0.0/16", HostAddr: "10.88.255.254", DNS: []string{"1.1.1.1", "1.0.0.1"}}, c.Firecracker.Network,
		"未写的 dns 保持默认")
	assert.Equal(t, FirecrackerLimits{MaxVMs: 3, MaxMemoryMB: 12288}, c.Firecracker.Limits)
	assert.Equal(t, map[string]FirecrackerType{"tiny": {CPUs: 1, Memory: "512m"}}, c.Firecracker.Types, "写了 types 就整表替换，不与默认合并")
	assert.Equal(t, 512, c.Firecracker.Types["tiny"].MemoryMB())
	assert.Equal(t, "/usr/local/bin/firecracker", c.Firecracker.Bin)

	for body, want := range map[string]string{
		"firecracker: {bin: \"\"}\n":                                                    "firecracker.bin",
		"firecracker: {jailer: \"\"}\n":                                                 "firecracker.jailer",
		"firecracker: {kernel: \"\"}\n":                                                 "firecracker.kernel",
		"firecracker: {rootfs: relative/rootfs.ext4}\n":                                 "firecracker.rootfs",
		"firecracker: {state_dir: \"\"}\n":                                              "firecracker.state_dir",
		"firecracker: {disk_gb: 7}\n":                                                   "firecracker.disk_gb 7 至少为 8",
		"firecracker: {uid_base: 0}\n":                                                  "firecracker.uid_base",
		"firecracker: {network: {cidr: 172.31.0.0/24}}\n":                               "firecracker.network.cidr",
		"firecracker: {network: {cidr: 172.31.1.0/16}}\n":                               "firecracker.network.cidr", // 主机位非零
		"firecracker: {network: {cidr: \"fd00::/16\"}}\n":                               "firecracker.network.cidr",
		"firecracker: {network: {cidr: nope}}\n":                                        "firecracker.network.cidr",
		"firecracker: {network: {host_addr: 172.31.254.1}}\n":                           "firecracker.network.host_addr", // 不在最后一个 /24
		"firecracker: {network: {host_addr: 10.0.0.1}}\n":                               "firecracker.network.host_addr",
		"firecracker: {network: {host_addr: x}}\n":                                      "firecracker.network.host_addr",
		"firecracker: {network: {dns: []}}\n":                                           "firecracker.network.dns",
		"firecracker: {network: {dns: [dns.google]}}\n":                                 "firecracker.network.dns",
		"firecracker: {limits: {max_vms: 0}}\n":                                         "firecracker.limits.max_vms",
		"firecracker: {limits: {max_memory_mb: 0}}\n":                                   "firecracker.limits.max_memory_mb",
		"firecracker: {default_type: huge}\n":                                           `firecracker.default_type "huge" 不在 types 中`,
		"firecracker: {types: {small: {cpus: 0, memory: 4g}}, default_type: small}\n":   "firecracker.types.small.cpus",
		"firecracker: {types: {small: {cpus: 2, memory: 4}}, default_type: small}\n":    "firecracker.types.small.memory",
		"firecracker: {types: {small: {cpus: 2, memory: 0g}}, default_type: small}\n":   "firecracker.types.small.memory",
		"firecracker: {types: {small: {cpus: 2, memory: \"\"}}, default_type: small}\n": "firecracker.types.small.memory",
		"firecracker: {types: {small: {cpus: 1.5, memory: 4g}}, default_type: small}\n": "cpus 1.5 必须是整数", // yaml.v3 本会截断成 1
		"firecracker: {types: {small: {cpus: 2, memory: 4g, nested_docker: true}}}\n":   "nested_docker",  // 类型表里的未知键同样报错
		"firecracker: {vcpus: 2}\n":                                                     "vcpus",          // 严格解码：未知键报错
	} {
		_, err := load(body)
		assert.ErrorContains(t, err, want, body)
	}

	// 非 firecracker 驱动不校验 firecracker 段；driver: runner 不是 provider 配置的合法值。
	require.NoError(t, os.WriteFile(path, []byte("url: http://127.0.0.1:7600\ntoken_file: /tmp/t\nfirecracker: {disk_gb: 1}\n"), 0o600))
	_, err = LoadProvider(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("url: http://127.0.0.1:7600\ntoken_file: /tmp/t\ndriver: runner\n"), 0o600))
	_, err = LoadProvider(path)
	assert.ErrorContains(t, err, `driver "runner" 无效`)
}
