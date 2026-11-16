package artifacts

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"regexp"

	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
)

// runtimeAsset 是页面运行时在嵌入的 Web 构建产物里的路径。
const runtimeAsset = "sandbox/runtime.js"

// runtimeConfig 是注入页面的 `__orynStart(<配置>)` 的参数。
type runtimeConfig struct {
	V       int    `json:"v"`
	Mark    string `json:"mark"`
	Path    string `json:"path"`
	Runtime bool   `json:"runtime"`
}

const markAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newMark 返回每次响应新生成的 8 位 [a-z0-9] 源码标记：页面脚本后来生成的元素带不上它。
func newMark() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = markAlphabet[int(b[i])%len(markAlphabet)]
	}
	return string(b[:])
}

var scriptCloseRe = regexp.MustCompile(`(?i)</(script)`)

// inlineScript 把脚本源码转义成能放进内联 <script> 的形式（`</script` → `<\/script`，JS 里两者等价）。
func inlineScript(js []byte) []byte {
	return scriptCloseRe.ReplaceAll(js, []byte(`<\/$1`))
}

// servedHTML 返回帧里提供的 HTML：给每个起始标签加源码标记，再在 InsertionPoint 处插入内联的页面运行时
// 与 `__orynStart(<配置>)`。runtimeJS 为空（没跑过 make web）时只标记、不注入。
func servedHTML(src []byte, path string, runtime bool, runtimeJS []byte) []byte {
	mark := newMark()
	out := htmlsrc.MarkSources(src, mark)
	if len(runtimeJS) == 0 {
		return out
	}
	cfg, _ := json.Marshal(runtimeConfig{V: 1, Mark: mark, Path: path, Runtime: runtime}) // json 把 < > & 转义成 \u00XX
	at := htmlsrc.InsertionPoint(out)
	var b bytes.Buffer
	b.Grow(len(out) + len(runtimeJS) + len(cfg) + 48)
	b.Write(out[:at])
	b.WriteString("<script>")
	b.Write(runtimeJS)
	b.WriteString("\n__orynStart(")
	b.Write(cfg)
	b.WriteString(");</script>")
	b.Write(out[at:])
	return b.Bytes()
}
