package design

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// SourceHash 是设计画布源码的指纹（plan-m29 C3 的 sealedHash）：取 `src/` 下、不在任意层级 `node_modules/`
// 里的文件，按路径字节序排序，逐个拼接 `<path>\n<sha256(内容) 小写 hex>\n`，对整串取 sha256 小写 hex。
// orbd 封存判脏与控制面 check_json.fingerprint、stale 判定都用它，两边必须算出同一个值。
// path 是以 `src/` 开头、`/` 分隔的相对路径；其余条目忽略。
func SourceHash(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		if isSourcePath(p) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	h := sha256.New()
	var sum [sha256.Size]byte
	var hexSum [sha256.Size * 2]byte
	for _, p := range paths {
		sum = sha256.Sum256(files[p])
		hex.Encode(hexSum[:], sum[:])
		h.Write([]byte(p))
		h.Write([]byte{'\n'})
		h.Write(hexSum[:])
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// isSourcePath 报告 p 是否计入指纹：在 src/ 下，且没有哪一段是 node_modules。
func isSourcePath(p string) bool {
	rest, ok := strings.CutPrefix(p, "src/")
	if !ok || rest == "" {
		return false
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if seg == "node_modules" {
			return false
		}
	}
	return true
}
