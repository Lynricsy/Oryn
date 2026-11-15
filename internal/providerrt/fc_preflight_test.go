package providerrt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// V3：槽位 uid/gid 区间与 subuid / subgid 各段、已有用户和组的 id 不得重叠；重叠时列出冲突并建议一个可用的 uid_base。
func TestFCSlotIDConflicts(t *testing.T) {
	n := config.FirecrackerMaxSlots
	passwd := "root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534::/:/usr/bin/nologin\nlyn:x:1000:1000::/home/lyn:/bin/zsh\n"
	group := "root:x:0:\nwheel:x:998:lyn\n"
	for _, tc := range []struct {
		name  string
		base  int
		files map[string]string
		want  []string // 错误里应含的片段；nil = 通过
	}{
		{"本机现状：sysbox 与验收用户的 subuid", 200000, map[string]string{
			"subuid": "sysbox:100000:65536\noryn-m11b:165536:65536\n", "subgid": "sysbox:100000:65536\noryn-m11b:165536:65536\n",
			"passwd": passwd, "group": group,
		}, []string{"/etc/subuid 的 oryn-m11b:165536:65536", "/etc/subgid 的 oryn-m11b:165536:65536", "[200000, 216320)", "例如 300000"}},
		{"不重叠", 300000, map[string]string{
			"subuid": "sysbox:100000:65536\noryn-m11b:165536:65536\n", "subgid": "oryn-m11b:165536:65536\n",
			"passwd": passwd, "group": group,
		}, nil},
		{"紧挨着不算重叠", 231072, map[string]string{"subuid": "a:165536:65536\n", "subgid": "b:" + "247392" + ":10\n"}, nil},
		{"只有 subgid 重叠", 300000, map[string]string{"subgid": "c:310000:5\n"}, []string{"/etc/subgid 的 c:310000:5", "例如 400000"}},
		{"已有用户的 uid", 200000, map[string]string{"passwd": passwd + "svc:x:200007:100::/:/bin/false\n"}, []string{"/etc/passwd 的用户 svc"}},
		{"已有组的 gid", 200000, map[string]string{"group": group + "jail:x:216319:\n"}, []string{"/etc/group 的组 jail"}},
		{"空文件与坏行", 200000, map[string]string{"subuid": "\n# comment\nbroken\nx:y:z\n", "passwd": "short\n"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := slotIDConflicts(tc.base, n, tc.files)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

// 预检从注入的 etc 目录读文件（不碰真实 /etc）；文件缺失按空处理。
func TestFCPreflightSlotIDs(t *testing.T) {
	c := newFCTest(t)
	etc := t.TempDir()
	c.d.etcDir = etc
	c.d.Cfg.UIDBase = 200000 // 落在下面 oryn-m11b 的 subuid 段 [165536, 231072) 里
	require.NoError(t, c.d.checkSlotIDs())
	require.NoError(t, os.WriteFile(filepath.Join(etc, "subuid"), []byte("oryn-m11b:165536:65536\n"), 0o644))
	err := c.d.checkSlotIDs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oryn-m11b:165536:65536")
	c.d.Cfg.UIDBase = 300000
	require.NoError(t, c.d.checkSlotIDs())
}
