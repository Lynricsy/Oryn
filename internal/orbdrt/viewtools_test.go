package orbdrt

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M33：真跑 oryn-orb.ts 的视图工具（testdata/view_tools.ts）：注册与契约文件一致、C3 的六种结果与 details.view.spec、
// 子 agent 的隐藏与拒绝、指引的追加条件（orb 与 Runner）。
func TestViewTools(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("没有 bun，跳过扩展实跑检查")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bun, "testdata/view_tools.ts", "assets/oryn-orb.ts", "../../schema/view-tools.json").CombinedOutput()
	require.NoError(t, err, "view_tools.ts 失败：\n%s", string(out))
	assert.Equal(t, "OK", strings.TrimSpace(string(out)))
}
