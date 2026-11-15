package providerrt

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// V1 / V2：oryn-fc-net.sh 的 nft 规则自带反伪造（严格反向路径 + 源地址须在 guest 网段）与 IPv6 封堵，不依赖 sysctl；
// 有 root 与 nft 时用 `nft -c` 让内核校验整份规则（只检查，不装载）。
func TestFCNetRules(t *testing.T) {
	cmd := exec.Command("bash", filepath.Join("..", "..", "deploy", "firecracker", "oryn-fc-net.sh"), "rules")
	cmd.Env = append(os.Environ(), "ORYN_FC_CIDR=172.31.0.0/16", "ORYN_FC_HOST_ADDR=172.31.255.1", "ORYN_FC_LINK_PORTS=7600 7643")
	out, err := cmd.Output()
	require.NoError(t, err)
	rules := string(out)
	chain := func(name string) string {
		_, rest, ok := strings.Cut(rules, "chain "+name+" {")
		require.True(t, ok, name)
		body, _, _ := strings.Cut(rest, "\n\t}\n")
		return body
	}
	input, forward := chain("input"), chain("forward")
	spoof := `iifname "fc-*" fib saddr . iif oif missing drop`
	// 反伪造在任何放行规则之前。
	require.Contains(t, input, spoof)
	assert.Less(t, strings.Index(input, spoof), strings.Index(input, "ct state established,related accept"))
	assert.Contains(t, input, "tcp dport { 7600, 7643 } accept")
	for _, r := range []string{
		spoof,
		`iifname "fc-*" ip saddr != 172.31.0.0/16 drop`,
		`iifname "fc-*" meta nfproto ipv6 drop`,
		`oifname "fc-*" meta nfproto ipv6 drop`,
	} {
		assert.Contains(t, forward, r)
	}
	// 仍是「先建空表再删」的一次性原子替换。
	assert.True(t, strings.HasPrefix(rules, "table inet oryn_fc\ndelete table inet oryn_fc\n"))

	if os.Geteuid() != 0 {
		t.Skip("nft -c 需要 root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("没有 nft")
	}
	check := exec.Command("nft", "-c", "-f", "-")
	check.Stdin = bytes.NewReader(out)
	msg, err := check.CombinedOutput()
	require.NoError(t, err, string(msg))
}

// S5：构建产物锁文件里的镜像引用都锁 digest（name@sha256:<64 位十六进制>），其余 *Digest 字段也是 sha256。
func TestFCArtifactsLockPinned(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "images", "fc", "artifacts.lock.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(b, &doc))
	imageRe := regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	digestRe := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	images := 0
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, c := range v {
				walk(path+"."+k, c)
			}
		case string:
			switch {
			case strings.HasSuffix(path, "Image"):
				images++
				assert.Regexp(t, imageRe, v, "%s 须锁 digest", path)
			case strings.HasSuffix(path, "Digest"):
				assert.Regexp(t, digestRe, v, path)
			}
		}
	}
	walk("", doc)
	assert.Positive(t, images)
}
