package design

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceHash(t *testing.T) {
	a := []byte("export default function A() { return null }\n")
	b := []byte("@theme { --color-bg: #fff; }\n")

	// 算法本身：按路径排序后逐个拼接 `<path>\n<sha256 hex>\n`，再取 sha256。
	line := func(p string, data []byte) string {
		s := sha256.Sum256(data)
		return p + "\n" + hex.EncodeToString(s[:]) + "\n"
	}
	want := sha256.Sum256([]byte(line("src/frames/a.tsx", a) + line("src/theme.css", b)))
	base := map[string][]byte{"src/theme.css": b, "src/frames/a.tsx": a}
	require.Equal(t, hex.EncodeToString(want[:]), SourceHash(base))

	// node_modules（任意层级）与 src/ 之外的文件不计入。
	withNoise := map[string][]byte{
		"src/theme.css": b, "src/frames/a.tsx": a,
		"src/node_modules/x/index.js":     []byte("x"),
		"src/lib/node_modules/y/index.js": []byte("y"),
		"dist/a.html":                     []byte("<p>"),
		"canvas.json":                     []byte("{}"),
		"src/":                            nil,
	}
	require.Equal(t, SourceHash(base), SourceHash(withNoise))

	// 内容或路径变了，指纹就变。
	changed := map[string][]byte{"src/theme.css": b, "src/frames/a.tsx": append([]byte(nil), a[:len(a)-1]...)}
	require.NotEqual(t, SourceHash(base), SourceHash(changed))
	renamed := map[string][]byte{"src/theme.css": b, "src/frames/b.tsx": a}
	require.NotEqual(t, SourceHash(base), SourceHash(renamed))

	// 空集合也有确定的值（空串的 sha256）。
	empty := sha256.Sum256(nil)
	require.Equal(t, hex.EncodeToString(empty[:]), SourceHash(nil))
}
