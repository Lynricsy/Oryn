package orbdrt

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// Profile 目录、标记、归档与还原（M8 计划 2.6）。Profile 的真相在控制面：orb 进入 ready 之前由控制面经
// PUT / DELETE /desktop/profile 定下本地目录；标记文件记下当前目录对应哪个 Profile 与使用方式。
// 归档只收 Chromium `Default` 子 profile 里的登录态（Cookie、Local Storage、IndexedDB、Preferences、扩展…），
// 不收缓存与会话恢复数据；同一目录两次归档字节相同（控制面据此去重）。

// 归档上限（docs/protocol.md「M8 限额」）；变量便于测试调小。
var (
	maxProfileArchiveBytes  int64 = 256 << 20 // gzip 后
	maxProfileUnpackedBytes int64 = 2 << 30
	maxProfileEntries             = 200000
)

// profileMarker 是 `.oryn/desktop/profile.json`。ProfileID 为空表示「已定为不用 Profile」。
type profileMarker struct {
	OrbID     string                      `json:"orbId"`
	ProfileID string                      `json:"profileId"`
	Name      string                      `json:"name,omitempty"`
	Mode      apigen.BrowserProfileAccess `json:"mode,omitempty"`
	// ReadonlyReason / ReadonlyBy 只在 Mode=readonly 时有意义（taken_over 时 By 是接管方线程标题）。
	ReadonlyReason apigen.BrowserProfileReadonlyReason `json:"readonlyReason,omitempty"`
	ReadonlyBy     string                              `json:"readonlyBy,omitempty"`
}

// access 返回使用方式（缺省 exclusive）。
func (m *profileMarker) access() apigen.BrowserProfileAccess {
	if m.Mode == apigen.BrowserProfileAccessReadonly {
		return apigen.BrowserProfileAccessReadonly
	}
	return apigen.BrowserProfileAccessExclusive
}

func (m *profileMarker) readonlyReason() apigen.BrowserProfileReadonlyReason {
	if m.ReadonlyReason.Valid() {
		return m.ReadonlyReason
	}
	return apigen.BrowserProfileReadonlyReasonRequested
}

// loadMarker 读标记。标记的 orbId 不是本 orb（快照分叉出的 orb 带着源 orb 的目录）或无法解析时，
// 立刻删除 profile/ 与标记：Profile 未定，等控制面重新决定。
func (d *desktop) loadMarker() {
	b, err := os.ReadFile(d.markerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var m profileMarker
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err == nil && m.OrbID == d.r.cfg.OrbID {
		d.marker = &m
		return
	}
	d.log.Info("discarding browser profile of another orb", "markerOrb", m.OrbID, "err", err)
	_ = os.Remove(d.markerPath())
	if err := os.RemoveAll(d.profileDir()); err != nil {
		d.log.Warn("remove foreign browser profile", "err", err)
	}
}

// writeMarker 原子写标记（0600）。
func (d *desktop) writeMarker(m profileMarker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(d.markerPath(), append(b, '\n'), 0o600)
}

// removeMarker 删除标记文件并把内存里的标记置空（Profile 未定）。调用方持有 opMu。
func (d *desktop) removeMarker() error {
	if err := os.Remove(d.markerPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	d.mu.Lock()
	d.marker = nil
	d.bumpLocked()
	d.unlockAndEmit()
	return nil
}

// commitMarker 写标记并更新内存状态；dirty 为 nil 时不动 dirty。调用方持有 opMu。
func (d *desktop) commitMarker(m profileMarker, dirty *bool) error {
	if err := d.writeMarker(m); err != nil {
		return err
	}
	d.mu.Lock()
	d.marker = &m
	if dirty != nil {
		d.dirty = *dirty
	}
	d.bumpLocked()
	d.unlockAndEmit()
	return nil
}

// ---- 收录规则 ----

// profileIncluded 报告 user-data-dir 下的相对路径（`/` 分隔）是否进归档：顶层只收文件 `Local State` 与目录
// `Default`；`Default` 之下，目录名含 `Cache` 或等于 `blob_storage` / `Session Storage` / `Sessions` 的整棵
// 子树不收，文件名以 `.tmp` 结尾的不收。
func profileIncluded(rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	switch parts[0] {
	case "Local State":
		return len(parts) == 1 && !isDir
	case "Default":
		if len(parts) == 1 {
			return isDir
		}
	default:
		return false
	}
	dirs := parts[1:]
	if !isDir {
		name := dirs[len(dirs)-1]
		if name == "" || strings.HasSuffix(name, ".tmp") {
			return false
		}
		dirs = dirs[:len(dirs)-1]
	}
	for _, d := range dirs {
		if d == "" || strings.Contains(d, "Cache") || d == "blob_storage" || d == "Session Storage" || d == "Sessions" {
			return false
		}
	}
	return true
}

// ---- 归档 ----

// writeProfileArchive 把 dir 里收录的普通文件按相对路径排序写成 tar.gz：tar 头只带名字、大小、0600 与
// 秒级 mtime（uid / gid / 用户名为空），gzip 用 BestSpeed 且头里不写名字与时间，内容不变时字节相同。
// dir 不存在时写出空归档。
func writeProfileArchive(w io.Writer, dir string) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 根目录不存在（空 Profile）或遍历中途消失的条目
			}
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if de.IsDir() {
			if !profileIncluded(rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		if !de.Type().IsRegular() || !profileIncluded(rel, false) {
			return nil
		}
		return addArchiveFile(tw, p, rel)
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addArchiveFile(tw *tar.Writer, p, rel string) error {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     rel,
		Size:     fi.Size(),
		Mode:     0o600,
		ModTime:  fi.ModTime().Truncate(time.Second),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, f, fi.Size()); err != nil {
		return fmt.Errorf("archive %s: %w", rel, err)
	}
	return nil
}

// ---- 还原 ----

func invalidArchive(format string, args ...any) *apiError {
	return errf(http.StatusBadRequest, "invalid_archive", format, args...)
}

func archiveTooLarge(format string, args ...any) *apiError {
	return errf(http.StatusRequestEntityTooLarge, "too_large", format, args...)
}

// extractProfileArchive 把 tar.gz 解到 dir（dir 须不存在或为空）。只接受普通文件与目录；拒绝绝对路径、
// 含 `..` 的路径、符号链接、硬链接、设备，以及收录规则之外的路径；超过条目数或解包字节上限即失败。
// 失败时 dir 里可能留有部分内容，由调用方删除。
func extractProfileArchive(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return wrapArchiveReadErr(err)
	}
	tr := tar.NewReader(gz)
	var entries int
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return wrapArchiveReadErr(err)
		}
		entries++
		if entries > maxProfileEntries {
			return archiveTooLarge("archive has more than %d entries", maxProfileEntries)
		}
		var isDir bool
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			isDir = true
		default:
			return invalidArchive("entry %q: type %q not allowed", hdr.Name, hdr.Typeflag)
		}
		name := hdr.Name
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." ||
			strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || strings.HasSuffix(name, "/..") {
			return invalidArchive("entry %q: invalid path", hdr.Name)
		}
		if !profileIncluded(name, isDir) {
			return invalidArchive("entry %q: outside the browser profile", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if isDir {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if hdr.Size < 0 {
			return invalidArchive("entry %q: negative size", hdr.Name)
		}
		total += hdr.Size
		if total > maxProfileUnpackedBytes {
			return archiveTooLarge("archive unpacks to more than %d bytes", maxProfileUnpackedBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return wrapArchiveReadErr(err)
		}
		mt := hdr.ModTime
		_ = os.Chtimes(target, mt, mt)
	}
	// 读到 gzip 末尾才校验 CRC 与长度：截断或尾部损坏的归档在这里暴露。
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return wrapArchiveReadErr(err)
	}
	return nil
}

// wrapArchiveReadErr 把读归档时的错误归类：请求体超限 → too_large；写盘错误原样（no_space 等）；
// 其余（gzip / tar 格式、截断）→ invalid_archive。
func wrapArchiveReadErr(err error) error {
	var mbe *http.MaxBytesError
	var pe *fs.PathError
	switch {
	case errors.As(err, &mbe):
		return archiveTooLarge("archive exceeds %d bytes", mbe.Limit)
	case errors.As(err, &pe):
		return err
	default:
		return invalidArchive("%v", err)
	}
}

// ---- profile.mode（ctl） ----

// setProfileMode 处理 orynd 的 profile.mode 请求：只在标记的 Profile 相同时生效，把标记改为只读、dirty=false，
// 浏览器与标签页不动；之后导出一律 409 profile_readonly。
func (d *desktop) setProfileMode(p apigen.LinkCtlProfileModeParams) (apigen.LinkCtlOkResult, error) {
	if p.Mode != apigen.BrowserProfileAccessReadonly {
		return apigen.LinkCtlOkResult{}, ctlErr(link.CodeInvalidParams, "mode %q: exclusive access is only granted by reloading the profile", p.Mode)
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.mu.Lock()
	m := d.marker
	d.mu.Unlock()
	if m == nil || m.ProfileID == "" || m.ProfileID != p.ProfileID {
		have := ""
		if m != nil {
			have = m.ProfileID
		}
		return apigen.LinkCtlOkResult{}, ctlErr("profile_mismatch", "local profile is %q, not %q", have, p.ProfileID)
	}
	nm := *m
	nm.Mode = apigen.BrowserProfileAccessReadonly
	nm.ReadonlyReason = apigen.BrowserProfileReadonlyReasonTakenOver
	if p.Reason.Valid() {
		nm.ReadonlyReason = p.Reason
	}
	nm.ReadonlyBy = ""
	if p.By != nil {
		nm.ReadonlyBy = *p.By
	}
	if err := d.commitMarker(nm, new(false)); err != nil {
		return apigen.LinkCtlOkResult{}, ctlErr("failed", "write profile marker: %v", err)
	}
	d.log.Info("browser profile switched to read-only", "profile", p.ProfileID, "reason", nm.ReadonlyReason)
	return apigen.LinkCtlOkResult{Ok: true}, nil
}
