package artifacts

import (
	"archive/zip"
	"context"
	"io"
	"strings"
)

// 导出格式。
const (
	ExportHTML = "html"
	ExportZip  = "zip"
)

// Export 是一次导出：Name 是建议的下载文件名（`<标题>.<扩展名>`，调用方再做文件名清洗），
// Write 把内容流式写给 w。
type Export struct {
	Name, ContentType string
	write             func(w io.Writer) error
}

// Write 把导出的内容写给 w。
func (e Export) Write(w io.Writer) error { return e.write(w) }

// Export 准备导出某个版本（0 = 最新）：`html` 只对恰好一个 HTML 文件的版本可用，直接给那个文件；
// `zip` 打包该版本的全部文件。
func (s *Service) Export(ctx context.Context, id string, version int, format string) (Export, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Export{}, err
	}
	v, err := s.Version(ctx, id, version)
	if err != nil {
		return Export{}, err
	}
	files, err := s.Files(ctx, id, v.Version)
	if err != nil {
		return Export{}, err
	}
	// 标题里的 / 会被当成目录分隔，换掉
	base := strings.ReplaceAll(a.Title, "/", "_")
	switch format {
	case ExportHTML:
		if len(files) != 1 || !IsHTML(files[0].Path) {
			return Export{}, opErr(CodeInvalid, "版本 %d 有 %d 个文件，只能导出为 zip。", v.Version, len(files))
		}
		f := files[0]
		return Export{Name: base + ".html", ContentType: f.Mime, write: func(w io.Writer) error {
			rc, err := s.c.open(f.Sha256)
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(w, rc)
			return err
		}}, nil
	case ExportZip:
		return Export{Name: base + ".zip", ContentType: "application/zip", write: func(w io.Writer) error {
			zw := zip.NewWriter(w)
			for _, f := range files {
				if err := s.zipFile(zw, f, v); err != nil {
					return err
				}
			}
			return zw.Close()
		}}, nil
	}
	return Export{}, opErr(CodeInvalid, "artifact %s 不能导出为 %q（页面与画布只能导出为 html 或 zip）。", id, format)
}

// zipFile 把一个文件流式写进 zip。
func (s *Service) zipFile(zw *zip.Writer, f File, v Version) error {
	rc, err := s.c.open(f.Sha256)
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: v.CreatedAt})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}
