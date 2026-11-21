// Package lfs 是 Git LFS 指针与 HTTP API 的数据结构（M24）：控制面镜像客户端、托管仓库 LFS 服务端与出站代理共用。
//
// 协议依据：
//   - 指针：https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md
//   - Batch API：https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
package lfs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MediaType 是 LFS HTTP API 的请求与响应类型。
	MediaType = "application/vnd.git-lfs+json"
	// PointerMax：指针文件必须小于 1024 字节（spec.md）。
	PointerMax = 1024
	// BatchBodyMax 是批量请求体的读取上限。
	BatchBodyMax = 1 << 20
	// BatchMaxObjects 是单个批量请求的对象数上限。
	BatchMaxObjects = 1000
)

var (
	oidRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sizeRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
)

// 指针第一行可接受的两种版本（hawser 是 git-lfs 的旧名）。
var pointerVersions = []string{
	"version https://git-lfs.github.com/spec/v1",
	"version https://hawser.github.com/spec/v1",
}

// ValidOID 报告 s 是不是小写十六进制的 sha256（64 位）。
func ValidOID(s string) bool { return oidRe.MatchString(s) }

// Pointer 是一个规范 LFS 指针指向的对象。
type Pointer struct {
	OID  string
	Size int64
}

// ParsePointer 只认规范的三行指针（version、oid sha256:<64 hex>、size），其余一律返回 false。
// 带扩展键（ext-0-…）或其它键的指针不识别：扩展指针要经扩展程序才能还原内容，原样提供对象会给错内容。
func ParsePointer(b []byte) (Pointer, bool) {
	if len(b) == 0 || len(b) >= PointerMax || !utf8.Valid(b) || b[len(b)-1] != '\n' {
		return Pointer{}, false
	}
	lines := strings.Split(string(b[:len(b)-1]), "\n")
	if len(lines) != 3 {
		return Pointer{}, false
	}
	if lines[0] != pointerVersions[0] && lines[0] != pointerVersions[1] {
		return Pointer{}, false
	}
	oid, ok := strings.CutPrefix(lines[1], "oid sha256:")
	if !ok || !ValidOID(oid) {
		return Pointer{}, false
	}
	sz, ok := strings.CutPrefix(lines[2], "size ")
	if !ok || !sizeRe.MatchString(sz) {
		return Pointer{}, false
	}
	n, err := strconv.ParseInt(sz, 10, 64)
	if err != nil {
		return Pointer{}, false
	}
	return Pointer{OID: oid, Size: n}, true
}

// Encode 返回规范三行形式的指针（测试与 mock 用）。
func (p Pointer) Encode() []byte {
	var b bytes.Buffer
	b.WriteString(pointerVersions[0])
	b.WriteString("\noid sha256:")
	b.WriteString(p.OID)
	b.WriteString("\nsize ")
	b.WriteString(strconv.FormatInt(p.Size, 10))
	b.WriteByte('\n')
	return b.Bytes()
}

// ObjectPath 是对象在 root 下的路径 root/oid[0:2]/oid[2:4]/oid（与 git-lfs 的 .git/lfs/objects 布局相同）。
// oid 必须满足 ValidOID。
func ObjectPath(root, oid string) string {
	return filepath.Join(root, oid[0:2], oid[2:4], oid)
}

// Ref 是批量请求里的 ref（可选）。
type Ref struct {
	Name string `json:"name"`
}

// BatchRequest 是 POST …/info/lfs/objects/batch 的请求体。出站代理按这个结构重新序列化请求体，
// 输出的字段顺序就是声明顺序。
type BatchRequest struct {
	Operation string       `json:"operation"`
	Transfers []string     `json:"transfers"`
	Ref       *Ref         `json:"ref,omitempty"`
	Objects   []ObjectSpec `json:"objects"`
	HashAlgo  string       `json:"hash_algo,omitempty"`
}

// ObjectSpec 是批量请求里的一个对象。
type ObjectSpec struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// BatchResponse 是批量接口的 200 响应体。
type BatchResponse struct {
	Transfer         string         `json:"transfer,omitempty"`
	Objects          []ObjectResult `json:"objects"`
	HashAlgo         string         `json:"hash_algo,omitempty"`
	Message          string         `json:"message,omitempty"`
	RequestID        string         `json:"request_id,omitempty"`
	DocumentationURL string         `json:"documentation_url,omitempty"`
}

// ObjectResult 是批量响应里的一个对象：actions 与 error 二选一（都没有 = 无需传输）。
type ObjectResult struct {
	OID           string             `json:"oid"`
	Size          int64              `json:"size"`
	Authenticated bool               `json:"authenticated,omitempty"`
	Actions       map[string]*Action `json:"actions,omitempty"`
	Error         *ObjectError       `json:"error,omitempty"`
}

// Action 是一个传输动作（download / upload / verify）。
type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int64             `json:"expires_in,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
}

// ObjectError 是对象级错误。
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Expiry 返回动作的过期时刻：expires_in>0 优先；否则解析 expires_at（RFC 3339）；都没有（或解析失败）ok=false。
func (a *Action) Expiry(now time.Time) (time.Time, bool) {
	if a.ExpiresIn > 0 {
		return now.Add(time.Duration(a.ExpiresIn) * time.Second), true
	}
	if a.ExpiresAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, a.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ErrorBody 是 WriteError 的正文字节 {"message": msg}（出站代理在 ModifyResponse 里替换响应体时用）。
func ErrorBody(msg string) []byte {
	b, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{msg}) // 只含一个字符串字段，Marshal 不会失败
	return b
}

// WriteError 写 LFS 形式的错误：Content-Type MediaType，正文 {"message": msg}。git-lfs 会显示 message。
func WriteError(w http.ResponseWriter, status int, msg string) {
	body := ErrorBody(msg)
	w.Header().Set("Content-Type", MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
