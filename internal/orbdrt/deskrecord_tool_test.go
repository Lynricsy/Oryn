package orbdrt

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M25：oryn-orb.ts 注册录屏工具 desktop_record（写法同 internal/github/presets_test.go 的 registeredOrbTools），
// 经 orbd.sock 发 desktop.record.start / stop，桌面指引里写明何时录屏。
func TestDesktopRecordTool(t *testing.T) {
	src, err := os.ReadFile("assets/oryn-orb.ts")
	require.NoError(t, err)
	var names []string
	for _, m := range regexp.MustCompile(`name:\s*"([a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		names = append(names, m[1])
	}
	require.Contains(t, names, "desktop_ask_user", "oryn-orb.ts 里应能解析出桌面工具")
	assert.Contains(t, names, "desktop_record")
	for _, s := range []string{`"desktop.record.start"`, `"desktop.record.stop"`, "registerDesktopRecord(pi)", "desktop_record start"} {
		assert.Contains(t, string(src), s)
	}
}
