package artifacts

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// 导出格式。
const (
	ExportHTML     = "html"
	ExportZip      = "zip"
	ExportMarkdown = "md"
	ExportPDF      = "pdf"
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
// `zip` 打包该版本的全部文件；`md` 只对文档可用（没有图片时是 `document.md` 本身，有图片时是含 `images/` 的 zip）；
// `pdf` 只对幻灯片可用，由控制面渲染器打印查看器的 `?view=print`（渲染不可用回 render_unavailable）。
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
		// 没有自己的 index.html 的画布补一个生成的概览页，解开后直接打开 index.html 就能看；再导入时会被去掉
		overview, _, err := s.overviewFor(ctx, a, v.Version, "")
		if err != nil {
			return Export{}, err
		}
		return Export{Name: base + ".zip", ContentType: "application/zip", write: func(w io.Writer) error {
			zw := zip.NewWriter(w)
			for _, f := range files {
				if err := s.zipFile(zw, f, v); err != nil {
					return err
				}
			}
			if overview != nil {
				iw, err := zw.CreateHeader(&zip.FileHeader{Name: EntryHTML, Method: zip.Deflate, Modified: v.CreatedAt})
				if err != nil {
					return err
				}
				if _, err := iw.Write(overview); err != nil {
					return err
				}
			}
			return zw.Close()
		}}, nil
	case ExportMarkdown:
		if a.Type != TypeDocument {
			return Export{}, opErr(CodeInvalid, "artifact %s 不是文档，不能导出为 md。", id)
		}
		if len(files) == 1 {
			f := files[0]
			return Export{Name: base + ".md", ContentType: "text/markdown; charset=utf-8", write: func(w io.Writer) error {
				rc, err := s.c.open(f.Sha256)
				if err != nil {
					return err
				}
				defer rc.Close()
				_, err = io.Copy(w, rc)
				return err
			}}, nil
		}
		// 文档版本里只有 document.md 与 images/ 下的图片：整版打包，解开后相对路径照常能引用
		return Export{Name: base + ".zip", ContentType: "application/zip", write: func(w io.Writer) error {
			zw := zip.NewWriter(w)
			for _, f := range files {
				if err := s.zipFile(zw, f, v); err != nil {
					return err
				}
			}
			return zw.Close()
		}}, nil
	case ExportPDF:
		if a.Type != TypeSlides {
			return Export{}, opErr(CodeInvalid, "artifact %s 不是幻灯片，不能导出为 pdf。", id)
		}
		out, err := s.slidesPDF(ctx, a.ID, v.Version)
		if err != nil {
			return Export{}, err
		}
		return Export{Name: base + ".pdf", ContentType: "application/pdf", write: func(w io.Writer) error {
			_, err := w.Write(out)
			return err
		}}, nil
	}
	return Export{}, opErr(CodeInvalid, "artifact %s 不能导出为 %q（页面与画布可导出为 html 或 zip，文档另可导出为 md，幻灯片另可导出为 pdf）。", id, format)
}

// slidesPDF 用控制面渲染器把幻灯片某个版本的查看器打印视图（`?view=print`，每张一页，`@page` 等于幻灯片尺寸）
// 打印成 PDF：窗口等于幻灯片尺寸、倍率 1。渲染要在写响应头之前完成，失败才能映射成状态码。
func (s *Service) slidesPDF(ctx context.Context, id string, version int) ([]byte, error) {
	entry, _, err := s.ReadFile(ctx, id, version, EntrySlides)
	if err != nil {
		return nil, err
	}
	var deck struct {
		Aspect string `json:"aspect"`
	}
	_ = json.Unmarshal(entry, &deck) // 入口已通过校验；万一解析不了按缺省的 16:9
	width, height := slideSize(deck.Aspect)
	r := s.renderer()
	if r == nil {
		return nil, opErr(CodeRenderUnavailable, "控制面没有配置渲染器，不能导出 PDF。")
	}
	out, err := r.Run(ctx, render.Job{ArtifactID: id, Version: version, Query: "view=print",
		Width: width, Height: height, Scale: 1, PDF: true})
	if errors.Is(err, render.ErrUnavailable) {
		return nil, opErr(CodeRenderUnavailable, "控制面现在不能渲染（%s），所以不能导出 PDF。", err)
	}
	if err != nil {
		return nil, fmt.Errorf("把 artifact %s 版本 %d 打印成 PDF 失败：%w", id, version, err)
	}
	return out, nil
}

// slideSize 返回幻灯片画板的尺寸（CSS 像素），与查看器的 slideSize 同值：16:9 为 1280×720，4:3 为 960×720。
func slideSize(aspect string) (width, height int) {
	if aspect == "4:3" {
		return 960, 720
	}
	return 1280, 720
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
