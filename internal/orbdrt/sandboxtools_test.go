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

// M30：真跑 oryn-orb.ts 的 sandbox_* 工具（testdata/sandbox_tools.ts）：8 个工具、流式输出、错误映射与沙箱指引的开关。
func TestSandboxTools(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("没有 bun，跳过扩展实跑检查")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bun, "testdata/sandbox_tools.ts", "assets/oryn-orb.ts").CombinedOutput()
	require.NoError(t, err, "sandbox_tools.ts 失败：\n%s", string(out))
	assert.Equal(t, "OK", strings.TrimSpace(string(out)))
}
