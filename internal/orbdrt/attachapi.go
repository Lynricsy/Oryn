package orbdrt

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 附件投递（M3 计划 5.1）：控制面把消息附件放进 $HOME/.oryn/attachments/<threadId>/<id>/<safeName>，
// 该目录在仓库之外，附件不会进仓库。只经 fs 流服务，不经 REST 暴露。

const maxAttachmentName = 200

var attachmentIDRe = regexp.MustCompile(`^att_[0-9A-Z]{26}$`)

// safeName 净化附件文件名：取基名，`/ \ NUL` 与控制字符换成 `_`，前导 `.` 前加 `_`，保留扩展名截到 200 字节。
func safeName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, c := range name {
		if c == '\\' || c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(c)
	}
	name = b.String()
	if name == "" {
		name = "_"
	}
	if strings.HasPrefix(name, ".") {
		name = "_" + name
	}
	if len(name) <= maxAttachmentName {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) >= maxAttachmentName/2 {
		ext = ""
	}
	return truncateUTF8(strings.TrimSuffix(name, ext), maxAttachmentName-len(ext)) + ext
}

// truncateUTF8 把 s 截到至多 n 字节且不切断码点。
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (r *Runtime) attachmentsDir() string { return filepath.Join(r.home, ".oryn", "attachments") }

// attachmentPath 校验 threadId / id 并返回附件目录与文件路径。
func (r *Runtime) attachmentPath(threadID, id, name string) (dir, path string, err error) {
	if !validThreadID(threadID) {
		return "", "", badRequest("invalid threadId %q", threadID)
	}
	if !attachmentIDRe.MatchString(id) {
		return "", "", badRequest("invalid attachment id %q", id)
	}
	if name == "" {
		return "", "", badRequest("name is required")
	}
	dir = filepath.Join(r.attachmentsDir(), threadID, id)
	return dir, filepath.Join(dir, safeName(name)), nil
}

func (r *Runtime) handleAttachEnsure(w http.ResponseWriter, req *http.Request) {
	var body apigen.OrbAttachmentEnsureRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	_, path, err := r.attachmentPath(body.ThreadID, body.ID, body.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	fi, err := os.Lstat(path)
	present := err == nil && fi.Mode().IsRegular() && fi.Size() == body.Size
	writeJSON(w, http.StatusOK, apigen.OrbAttachmentEnsureResult{Present: present, Path: path})
}

func (r *Runtime) handleAttachPut(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	dir, path, err := r.attachmentPath(q.Get("threadId"), q.Get("id"), q.Get("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	want := q.Get("sha256")
	if !sha256Re.MatchString(want) {
		writeErr(w, badRequest("invalid sha256 %q", want))
		return
	}
	if req.ContentLength > r.maxAttachmentBytes {
		writeErr(w, errf(http.StatusRequestEntityTooLarge, "too_large", "attachment exceeds %d bytes", r.maxAttachmentBytes))
		return
	}
	h := sha256.New()
	tmp, err := r.receiveFile(http.MaxBytesReader(w, req.Body, r.maxAttachmentBytes), "att-", h)
	if err != nil {
		writeErr(w, err)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(tmp)
		writeErr(w, errf(http.StatusUnprocessableEntity, "checksum_mismatch", "sha256 %s does not match %s", got, want))
		return
	}
	err = os.Chmod(tmp, 0o644)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = movePath(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.OrbAttachmentEnsureResult{Present: true, Path: path})
}

func (r *Runtime) handleAttachDelete(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	threadID, id := q.Get("threadId"), q.Get("id")
	if !validThreadID(threadID) {
		writeErr(w, badRequest("invalid threadId %q", threadID))
		return
	}
	dir := filepath.Join(r.attachmentsDir(), threadID)
	if id != "" {
		if !attachmentIDRe.MatchString(id) {
			writeErr(w, badRequest("invalid attachment id %q", id))
			return
		}
		dir = filepath.Join(dir, id)
	}
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
