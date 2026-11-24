package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/designengine"
)

// 导出格式。
const (
	ExportHTML     = "html"
	ExportZip      = "zip"
	ExportMarkdown = "md"
	ExportPDF      = "pdf"
	ExportProject  = "project"
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
// `pdf` 只对幻灯片可用，由控制面渲染器打印查看器的 `?view=print`（渲染不可用回 render_unavailable）；
// `project` 只对设计画布可用，打包成可独立构建、可部署到 box 的 Vite 工程（见 exportProject）。
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
	case ExportProject:
		return s.exportProject(ctx, a, v, files, base)
	}
	return Export{}, opErr(CodeInvalid, "artifact %s 不能导出为 %q（页面与画布可导出为 html 或 zip，文档另可导出为 md，幻灯片另可导出为 pdf，设计画布另可导出为 project）。", id, format)
}

// exportProject 把设计画布的一个版本导出为工程 zip（M29 D2）：版本里的 src/**、designengine 的工程外壳
// （index.html、main.tsx、vite.config.ts、server.mjs、.oryn/box.yaml、README.md 等）、合并后的 package.json
// 与严格对应的 package-lock.json（box 构建用 npm ci）。锁的来源依次是：版本里封存时生成的 dist/_project/package-lock.json
// （根依赖须与当前 src/package.json 合并的结果一致）；没有额外设计依赖时由引擎锁现场改写；都不行回 invalid，不降级成 npm install。
func (s *Service) exportProject(ctx context.Context, a Artifact, v Version, files []File, base string) (Export, error) {
	if a.Kind != KindCanvas {
		return Export{}, opErr(CodeInvalid, "artifact %s 不是设计画布，不能导出为工程。", a.ID)
	}
	raw, _, err := s.ReadFile(ctx, a.ID, v.Version, EntryCanvas)
	if err != nil {
		return Export{}, err
	}
	doc, err := canvas.Parse(raw)
	if err != nil || doc.Design == nil {
		return Export{}, opErr(CodeInvalid, "artifact %s 版本 %d 不是设计画布（canvas.json 没有 design），不能导出为工程。", a.ID, v.Version)
	}
	has := func(p string) bool { return slices.ContainsFunc(files, func(f File) bool { return f.Path == p }) }
	var designPkg []byte
	if has("src/package.json") {
		if designPkg, _, err = s.ReadFile(ctx, a.ID, v.Version, "src/package.json"); err != nil {
			return Export{}, err
		}
	}
	manifest, err := designengine.ProjectManifest(designPkg)
	if err != nil {
		return Export{}, opErr(CodeInvalid, "版本 %d 的 %s", v.Version, err)
	}
	conflicts, _ := designengine.ProjectConflicts(designPkg) // 与 ProjectManifest 同一解析，上面已成功
	var lock []byte
	if sealed := path.Join(designengine.ProjectDir, "package-lock.json"); has(sealed) {
		if lock, _, err = s.ReadFile(ctx, a.ID, v.Version, sealed); err != nil {
			return Export{}, err
		}
		if err := designengine.VerifyProjectLock(designPkg, lock); err != nil {
			return Export{}, opErr(CodeInvalid, "版本 %d 的 %s 与 src/package.json 对不上（%s），请让 agent 重新封存一次。", v.Version, sealed, err)
		}
	} else {
		var ok bool
		if lock, ok, err = designengine.ProjectLock(designPkg); err != nil {
			return Export{}, err
		}
		if !ok {
			return Export{}, opErr(CodeInvalid, "版本 %d 有额外依赖但缺少工程锁（%s），请让 agent 重新封存一次。", v.Version, sealed)
		}
	}

	// 帧按画布上的顺序，名字取画框名；起始帧取 design.start 指向的画框
	p := designengine.Project{Title: a.Title, Theme: has("src/theme.css"), Conflicts: conflicts}
	for _, pg := range doc.Pages {
		id := strings.TrimSuffix(strings.TrimPrefix(pg.Source, "src/frames/"), ".tsx")
		if pg.Source == "" || slices.ContainsFunc(p.Frames, func(f designengine.ProjectFrame) bool { return f.ID == id }) {
			continue
		}
		p.Frames = append(p.Frames, designengine.ProjectFrame{ID: id, Name: pg.Name})
		if doc.Design.Start != nil && *doc.Design.Start == pg.ID {
			p.Start = id
		}
	}
	var src []File // 版本里的 src/**（封存不同步 node_modules，这里再防一道）
	for _, f := range files {
		if strings.HasPrefix(f.Path, "src/") && !strings.Contains(f.Path, "/node_modules/") {
			src = append(src, f)
		}
	}
	shell := designengine.ProjectFiles(p)
	shell = append(shell, designengine.EjectFile{Path: "package.json", Data: manifest}, designengine.EjectFile{Path: "package-lock.json", Data: lock})
	name := fmt.Sprintf("%s-v%d-project.zip", base, v.Version)
	return Export{Name: name, ContentType: "application/zip", write: func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, f := range src {
			if err := s.zipFile(zw, f, v); err != nil {
				return err
			}
		}
		for _, f := range shell {
			fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: v.CreatedAt})
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, bytes.NewReader(f.Data)); err != nil {
				return err
			}
		}
		return zw.Close()
	}}, nil
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
