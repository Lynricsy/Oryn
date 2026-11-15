package agentcfg_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

// EntrySize 是下发清单里一项（JSON + 分隔逗号）字节数的上界，路径含需转义或非 ASCII 字节时也成立。
func TestEntrySizeBoundsJSON(t *testing.T) {
	for _, path := range []string{"a", "rules/a.md", `we"ird\<&>.md`, "规则/中文.md", "x\x01y", strings.Repeat("长", 300)} {
		for _, size := range []int{0, 1, 2, 3, 4, 1000, 1 << 20} {
			e := apigen.LinkCtlOmpHomeFile{Path: path, Mode: 0o777, ContentB64: base64.StdEncoding.EncodeToString(make([]byte, size))}
			b, err := json.Marshal(e)
			require.NoError(t, err)
			assert.LessOrEqual(t, int64(len(b)+1), agentcfg.EntrySize(path, int64(size)), "%q %d", path, size)
		}
	}
}
