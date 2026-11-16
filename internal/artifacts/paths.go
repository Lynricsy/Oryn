package artifacts

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// ReservedPrefix 是 Oryn 自己用的路径前缀（帧里的 /_oryn/…），artifact 的文件不能以它开头。
const ReservedPrefix = "_oryn"

// PathProblem 返回 artifact 文件路径的问题（中文，给 agent 与用户看）；合法时返回空串。
func PathProblem(p string) string {
	switch {
	case p == "":
		return "路径不能为空"
	case len(p) > MaxPathBytes:
		return fmt.Sprintf("路径超过 %d 字节", MaxPathBytes)
	case !utf8.ValidString(p):
		return "路径不是合法的 UTF-8"
	case strings.HasPrefix(p, "/"):
		return "路径必须是相对路径（不能以 / 开头）"
	case strings.HasPrefix(p, ReservedPrefix):
		return "路径不能以 " + ReservedPrefix + " 开头（保留给 Oryn）"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "路径里不能有控制字符"
		}
		if strings.ContainsRune(`\:*?"<>|`, r) {
			return fmt.Sprintf("路径里不能有字符 %q", r)
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		switch {
		case seg == "":
			return "路径里不能有空段（连续的 / 或以 / 结尾）"
		case seg == "." || seg == "..":
			return "路径里不能有 . 或 .. 段"
		case len(seg) > 255:
			return "路径的每一段不能超过 255 字节"
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return fmt.Sprintf("路径段 %q 不能以点或空格结尾", seg)
		}
	}
	return ""
}

// CaseClash 返回 paths 里与前面某个路径只差大小写的那个路径；没有时返回空串。
func CaseClash(paths []string) string {
	seen := make(map[string]string, len(paths))
	for _, p := range paths {
		k := strings.ToLower(p)
		if prev, ok := seen[k]; ok && prev != p {
			return p
		}
		seen[k] = p
	}
	return ""
}

var mimeByExt = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".mp3":   "audio/mpeg",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".wasm":  "application/wasm",
	".pdf":   "application/pdf",
	".txt":   "text/plain; charset=utf-8",
	".md":    "text/markdown; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".xml":   "application/xml",
	".glb":   "model/gltf-binary",
}

// MimeOf 按扩展名返回 MIME 类型；不在表里的回 application/octet-stream。
func MimeOf(p string) string {
	if m, ok := mimeByExt[strings.ToLower(path.Ext(p))]; ok {
		return m
	}
	return "application/octet-stream"
}

// IsHTML 报告路径是否是 HTML 文件（.html / .htm）。
func IsHTML(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".html" || ext == ".htm"
}
