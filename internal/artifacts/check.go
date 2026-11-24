package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// OpCheck 是控制面的设计检查（plan-m29 C5、C8）：用 headless Chromium 打开最新版本的页面跑探针，汇总成
// check_json 写回该版本。助手与 Runner 用它；orb 线程用 orbd 的 design_check（工作副本）。
const OpCheck = "check_artifact"

// 检查的窗口：设计画框取画框尺寸，其余页面取这个缺省值（与 screenshot_artifact 的缺省一致）。
const checkWidth, checkHeight = 1280, 800

type checkArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// checkTarget 是一个要检查的页面：Frame 是画框（page）id，网页与不在画布上的文件为空。
type checkTarget struct {
	Frame         string
	Path          string
	Width, Height int
}

// frameCheck 是一个页面的检查结果：Err 非空表示没跑成（页面里没有结果节点、渲染失败等）。
type frameCheck struct {
	Issues []design.Issue
	Err    string
}

// agentCheckArtifact 是 check_artifact：参数 {id, path?}。设计画布检查每个带 source 的画框（dist/<id>.html），
// 其它画布检查每个画框，网页检查入口；给了 path 只检查它（画框的文件或版本里的一个 HTML 文件）。
// 渲染不可用时 status 为 unverified，不报错。只有不带 path 的完整检查才写进最新版本的 check_json（见 storeCheck）：
// 单个页面的结论代表不了整个版本，写进去会让一张干净的帧盖掉别的帧已知的问题。
func (s *Service) agentCheckArtifact(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p checkArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	if a.Type != "" {
		return Result{}, opErr(CodeInvalid, "%s 是 %s 类型的 artifact，check_artifact 只检查 HTML 页面与画布的画框。", a.ID, a.Type)
	}
	v, err := s.Version(ctx, a.ID, a.Version)
	if err != nil {
		return Result{}, err
	}
	targets, doc, err := s.checkTargets(ctx, a, v.Version, p.Path)
	if err != nil {
		return Result{}, err
	}
	results, down := s.runChecks(ctx, a.ID, v.Version, targets)
	chk := design.Check{Status: design.CheckClean, Issues: []design.Issue{}}
	unverified := 0
	for i, r := range results {
		if r.Err != "" {
			unverified++
			continue
		}
		for _, is := range r.Issues {
			is.Frame = targets[i].Frame
			chk.Issues = append(chk.Issues, is)
		}
	}
	switch {
	case len(chk.Issues) > 0:
		chk.Status = design.CheckIssues
	case unverified > 0 || len(targets) == 0:
		chk.Status = design.CheckUnverified
	}
	if p.Path == "" {
		if doc != nil && doc.Design != nil {
			if chk.Fingerprint, err = s.sourceHash(ctx, a.ID, v.Version); err != nil {
				return Result{}, err
			}
		}
		if err := s.storeCheck(ctx, a.ID, v, chk); err != nil {
			return Result{}, err
		}
	}
	return Result{
		Text: checkText(a, v.Version, chk, targets, results, down),
		Details: obj{"op": OpCheck, "id": a.ID, "title": a.Title, "kind": a.Kind, "version": v.Version,
			"status": chk.Status, "issues": len(chk.Issues), "frames": len(targets)},
	}, nil
}

// checkTargets 列出要检查的页面；画布另返回解析好的 canvas.json。
func (s *Service) checkTargets(ctx context.Context, a Artifact, version int, path string) ([]checkTarget, *canvas.Doc, error) {
	var doc *canvas.Doc
	if a.Kind == KindCanvas {
		b, _, err := s.ReadFile(ctx, a.ID, version, EntryCanvas)
		if err != nil {
			return nil, nil, err
		}
		d, err := canvas.Parse(b)
		if err != nil {
			return nil, nil, fmt.Errorf("artifact %s 版本 %d 的 canvas.json：%w", a.ID, version, err)
		}
		doc = &d
	}
	pageTarget := func(pg canvas.Page) checkTarget {
		return checkTarget{Frame: pg.ID, Path: pg.File, Width: int(math.Round(pg.Width)), Height: int(math.Round(pg.Height))}
	}
	if path != "" {
		if doc != nil {
			if i := slices.IndexFunc(doc.Pages, func(pg canvas.Page) bool { return pg.File == path }); i >= 0 {
				return []checkTarget{pageTarget(doc.Pages[i])}, doc, nil
			}
		}
		files, err := s.Files(ctx, a.ID, version)
		if err != nil {
			return nil, nil, err
		}
		if !IsHTML(path) || !slices.ContainsFunc(files, func(f File) bool { return f.Path == path }) {
			return nil, nil, opErr(CodeInvalid, "版本 %d 里没有 HTML 文件 %s。path 取画框的 file 或该版本文件清单里的 .html 路径；不给表示检查全部画框（网页是入口）。", version, path)
		}
		return []checkTarget{{Path: path, Width: checkWidth, Height: checkHeight}}, doc, nil
	}
	if doc == nil {
		return []checkTarget{{Path: EntryHTML, Width: checkWidth, Height: checkHeight}}, nil, nil
	}
	var out []checkTarget
	for _, pg := range doc.Pages {
		if doc.Design == nil || pg.Source != "" {
			out = append(out, pageTarget(pg))
		}
	}
	return out, doc, nil
}

// runChecks 逐个页面跑探针（并发由渲染器的上限约束）。渲染不可用时不再跑，down 是原因，每个页面都记为没跑成。
func (s *Service) runChecks(ctx context.Context, id string, version int, targets []checkTarget) (results []frameCheck, down string) {
	results = make([]frameCheck, len(targets))
	r := s.renderer()
	if r == nil {
		down = "控制面没有配置渲染器"
	} else if ok, reason := r.Status(); !ok {
		down = reason
	}
	if down != "" {
		for i := range results {
			results[i].Err = down
		}
		return results, down
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, t := range targets {
		wg.Go(func() {
			dom, err := r.Run(ctx, render.Job{ArtifactID: id, Version: version, Path: t.Path, Query: design.ProbeQuery + "=1",
				Width: t.Width, Height: t.Height, Scale: 1, DumpDOM: true})
			if errors.Is(err, render.ErrUnavailable) {
				mu.Lock()
				down = strings.TrimPrefix(err.Error(), render.ErrUnavailable.Error()+"：")
				mu.Unlock()
			}
			if err != nil {
				results[i].Err = "渲染失败：" + err.Error()
				return
			}
			issues, err := design.ParseProbe(dom)
			if err != nil {
				results[i].Err = err.Error()
				return
			}
			results[i].Issues = issues
		})
	}
	wg.Wait()
	return results, down
}

// sourceHash 是某个版本 src/ 下文件的 design.SourceHash（check_json.fingerprint）。
func (s *Service) sourceHash(ctx context.Context, id string, version int) (string, error) {
	files, err := s.Files(ctx, id, version)
	if err != nil {
		return "", err
	}
	src := map[string][]byte{}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "src/") {
			continue
		}
		b, _, err := s.ReadFile(ctx, id, version, f.Path)
		if err != nil {
			return "", err
		}
		src[f.Path] = b
	}
	return design.SourceHash(src), nil
}

// storeCheck 把检查结果写进版本的 check_json，并通知列表重取。没跑成（unverified）时不覆盖该版本已有的结果：
// 版本内容不变，之前（封存时或上一次）的结论仍然成立。
func (s *Service) storeCheck(ctx context.Context, id string, v Version, chk design.Check) error {
	if chk.Status == design.CheckUnverified && len(v.Check) > 0 {
		return nil
	}
	b, err := json.Marshal(chk)
	if err != nil {
		return err
	}
	if err := s.st.Q.SetArtifactVersionCheck(ctx, db.SetArtifactVersionCheckParams{CheckJson: new(string(b)), ArtifactID: id, N: int64(v.Version)}); err != nil {
		return err
	}
	s.changed()
	return nil
}

// checkText 是给 agent 看的摘要：首行总结，随后每处问题一行 `rule frame loc text`（缺的字段写 -），最后列出没跑成的页面。
func checkText(a Artifact, version int, chk design.Check, targets []checkTarget, results []frameCheck, down string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s v%d 设计检查：%s，%d 处问题（%d 个页面）", a.ID, version, chk.Status, len(chk.Issues), len(targets))
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for _, is := range chk.Issues {
		fmt.Fprintf(&b, "\n%s %s %s %s", is.Rule, dash(is.Frame), dash(is.Loc), dash(is.Text))
		if len(is.Detail) > 0 {
			fmt.Fprintf(&b, " (%s)", is.Detail)
		}
	}
	switch {
	case down != "":
		fmt.Fprintf(&b, "\n控制面现在不能渲染页面（%s），检查没有跑成。这和页面内容无关，不要重试 check_artifact。", down)
	case len(targets) == 0:
		b.WriteString("\n没有可检查的页面。")
	default:
		for i, r := range results {
			if r.Err != "" {
				name := targets[i].Frame
				if name == "" {
					name = targets[i].Path
				}
				fmt.Fprintf(&b, "\n未检查 %s：%s", name, r.Err)
			}
		}
	}
	return b.String()
}

// isDesignFrame 报告版本里的 path 是不是设计画框的产物（canvas.json 带 design，且有带 source 的画框指向它）。
// 这类页面由引擎入口自己加载 ./probe.js（site.go 的 withProbe）。
func (s *Service) isDesignFrame(ctx context.Context, id string, version int, path string) (bool, error) {
	b, _, err := s.ReadFile(ctx, id, version, EntryCanvas)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, err := canvas.Parse(b)
	if err != nil || doc.Design == nil {
		return false, nil
	}
	return slices.ContainsFunc(doc.Pages, func(pg canvas.Page) bool { return pg.Source != "" && pg.File == path }), nil
}
