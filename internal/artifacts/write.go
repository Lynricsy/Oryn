package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 能力名（页面运行时 `oryn.use`）。
var capabilityOrder = []string{"db", "files", "sample"}

// Author 是版本的作者；零值 = 用户。
type Author struct{ ThreadID, Label string }

// FileChange 是一次写入里的一个文件改动：Remove 为真时删除 Path，否则以 Data 的内容覆盖或新增。
type FileChange struct {
	Path   string
	Data   io.Reader
	Remove bool
}

// WriteParams 是 Write 的参数。
type WriteParams struct {
	ID          string
	BaseVersion int
	Author      Author
	Note        string
	Files       []FileChange
	// Capabilities 非 nil 时替换能力声明（参与冲突检查，伪路径 #capabilities）。
	Capabilities *[]string
	// Unchecked 里的路径不参与冲突检查（服务端自己合并好的文件，如画布的 canvas.json）。
	Unchecked []string
}

// CreateParams 是 Create 的参数。Origin 为空按 agent。
type CreateParams struct {
	Title, Type, ProjectID, ThreadID, Origin string
	Author                                   Author
	Note                                     string
	Files                                    []FileChange
	Capabilities                             []string
}

const capsPath = "#capabilities"

// entry 是清单里的一个文件。
type entry struct {
	sha  string
	size int64
}

// manifest 是一个版本的完整文件清单。
type manifest map[string]entry

func (m manifest) paths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// staged 是一次写入里已经落进内容存储的新文件；data 只对需要检查内容的文件保留。
type staged struct {
	writes  map[string]stored
	removes []string
}

// keepBytes 报告写入时是否要在内存里保留该文件的字节（HTML 的 UTF-8 校验、canvas.json 与类型化入口文件的解析）。
// 类型化 artifact 的图片只校验文件头，不整体留在内存里。
func keepBytes(path, typ string) bool {
	return IsHTML(path) || path == EntryCanvas || (typ != "" && path == TypeEntry(typ))
}

// stage 校验路径并把新文件落进内容存储（事务之外，流式）。
func (s *Service) stage(changes []FileChange, typ string) (staged, error) {
	out := staged{writes: map[string]stored{}}
	seen := map[string]bool{}
	for _, ch := range changes {
		if p := PathProblem(ch.Path); p != "" {
			return staged{}, opErr(CodeInvalid, "文件路径 %q 不合法：%s。", ch.Path, p)
		}
		if seen[ch.Path] {
			return staged{}, opErr(CodeInvalid, "同一次写入里路径 %s 出现了两次。", ch.Path)
		}
		seen[ch.Path] = true
		if ch.Remove {
			out.removes = append(out.removes, ch.Path)
			continue
		}
		if ch.Data == nil {
			return staged{}, opErr(CodeInvalid, "文件 %s 缺少内容。", ch.Path)
		}
		st, err := s.c.put(ch.Data, MaxFileBytes, keepBytes(ch.Path, typ))
		if errors.Is(err, errFileTooLarge) {
			return staged{}, opErr(CodeTooLarge, "文件 %s 超过单文件上限 %d MiB。请拆小或压缩。", ch.Path, MaxFileBytes>>20)
		}
		if err != nil {
			return staged{}, err
		}
		if IsHTML(ch.Path) && !utf8.Valid(st.data) {
			return staged{}, opErr(CodeInvalid, "文件 %s 不是合法的 UTF-8。HTML 文件必须用 UTF-8 编码。", ch.Path)
		}
		out.writes[ch.Path] = st
	}
	return out, nil
}

// apply 返回 base 应用 st 之后的清单。删除不存在的文件是错误。
func (st staged) apply(base manifest) (manifest, error) {
	m := make(manifest, len(base)+len(st.writes))
	for p, e := range base {
		m[p] = e
	}
	for _, p := range st.removes {
		if _, ok := m[p]; !ok {
			return nil, opErr(CodeInvalid, "要删除的文件 %s 在最新版本里不存在。", p)
		}
		delete(m, p)
	}
	for p, w := range st.writes {
		m[p] = entry{sha: w.sha, size: w.size}
	}
	return m, nil
}

// changedPaths 返回本次要写或删的路径。
func (st staged) changedPaths() []string {
	out := append([]string(nil), st.removes...)
	for p := range st.writes {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// NormalizeCapabilities 校验能力声明，去重并按固定顺序排列。
func NormalizeCapabilities(caps []string) ([]string, error) {
	set := map[string]bool{}
	for _, c := range caps {
		if !slices.Contains(capabilityOrder, c) {
			return nil, opErr(CodeInvalid, "未知的能力 %q；只能是 db、files、sample。", c)
		}
		set[c] = true
	}
	out := []string{}
	for _, c := range capabilityOrder {
		if set[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

func capsJSON(caps []string) string {
	if caps == nil {
		caps = []string{}
	}
	b, _ := json.Marshal(caps)
	return string(b)
}

func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(note); n > MaxNoteRunes {
		return "", opErr(CodeInvalid, "备注有 %d 个字符，上限是 %d。", n, MaxNoteRunes)
	}
	return note, nil
}

// filesKey 是清单的摘要：按路径排序拼接 path\x00sha256\n 后取 sha256；类型化时前面加 type\x00format\n。
func filesKey(typ string, format int, m manifest) string {
	h := sha256.New()
	if typ != "" {
		fmt.Fprintf(h, "%s\x00%d\n", typ, format)
	}
	for _, p := range m.paths() {
		fmt.Fprintf(h, "%s\x00%s\n", p, m[p].sha)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// shape 是根据清单算出的 kind、页数与格式。
type shape struct {
	kind   string
	pages  int
	format int
}

// fileBytes 返回清单里某个文件的字节：本次写入的取内存里的，其余从内容存储读。
func (s *Service) fileBytes(st staged, m manifest, path string) ([]byte, error) {
	if w, ok := st.writes[path]; ok && w.data != nil {
		return w.data, nil
	}
	e, ok := m[path]
	if !ok {
		return nil, nil
	}
	return s.c.read(e.sha)
}

// headBytes 返回清单里某个文件的前 n 个字节（文件更短时是全部）：本次写入且留在内存里的直接截取，其余从内容存储读。
func (s *Service) headBytes(st staged, m manifest, path string, n int) ([]byte, error) {
	if w, ok := st.writes[path]; ok && w.data != nil {
		return w.data[:min(n, len(w.data))], nil
	}
	f, err := s.c.open(m[path].sha)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:k], nil
}

// validateTyped 按类型规格校验类型化 artifact 的清单：入口文件给完整内容，其余文件给文件头。
func (s *Service) validateTyped(typ string, m manifest, st staged) (shape, error) {
	spec, ok := LookupType(typ)
	if !ok {
		return shape{}, opErr(CodeInvalid, "未知的类型 %q；只能是 document 或 slides。", typ)
	}
	files := make(map[string][]byte, len(m))
	for p := range m {
		var b []byte
		var err error
		if p == spec.Entry {
			b, err = s.fileBytes(st, m, p)
		} else {
			b, err = s.headBytes(st, m, p, sniffBytes)
		}
		if err != nil {
			return shape{}, err
		}
		files[p] = b
	}
	if ps := spec.Validate(files); len(ps) > 0 {
		return shape{}, typedInvalid(typ, ps)
	}
	return shape{kind: typ, pages: spec.Pages(files[spec.Entry]), format: spec.Format}, nil
}

// validate 校验一个版本的清单并算出它的形态。
func (s *Service) validate(typ string, m manifest, st staged) (shape, error) {
	if len(m) == 0 {
		return shape{}, opErr(CodeInvalid, "artifact 至少要有一个文件。")
	}
	if len(m) > MaxVersionFiles {
		return shape{}, opErr(CodeTooLarge, "一个版本最多 %d 个文件，这次有 %d 个。", MaxVersionFiles, len(m))
	}
	var total int64
	for _, e := range m {
		total += e.size
	}
	if total > MaxVersionBytes {
		return shape{}, opErr(CodeTooLarge, "一个版本的文件总共不能超过 %d MiB，这次是 %.1f MiB。", MaxVersionBytes>>20, float64(total)/(1<<20))
	}
	if p := CaseClash(m.paths()); p != "" {
		return shape{}, opErr(CodeInvalid, "路径 %s 与另一个文件只差大小写；同一版本里不能有这样的两个文件。", p)
	}
	if typ != "" {
		return s.validateTyped(typ, m, st)
	}
	_, hasHTML := m[EntryHTML]
	_, hasCanvas := m[EntryCanvas]
	if !hasHTML && !hasCanvas {
		return shape{}, opErr(CodeInvalid, "页面 artifact 必须有入口文件 index.html（画布则是 canvas.json）。")
	}
	if !hasCanvas {
		return shape{kind: KindWeb, pages: 1}, nil
	}
	b, err := s.fileBytes(st, m, EntryCanvas)
	if err != nil {
		return shape{}, err
	}
	pages, problem := canvasProblem(b, func(p string) bool { _, ok := m[p]; return ok })
	if problem != "" {
		return shape{}, opErr(CodeInvalid, "%s修正后再写；canvas.json 的格式见 read_artifact_guide 的 canvas 主题。", problem)
	}
	return shape{kind: KindCanvas, pages: pages}, nil
}

// canvasProblem 严格解析 canvas.json 并检查每个画框的文件都在版本里（has 报告文件是否存在）；
// 合法时返回画框数，否则返回问题说明（中文，带位置）。导入把它映射成 bad_canvas。
func canvasProblem(b []byte, has func(string) bool) (int, string) {
	doc, err := canvas.Parse(b)
	if err != nil {
		return 0, fmt.Sprintf("canvas.json 不合法：%v。", err)
	}
	if missing := canvas.MissingPageFiles(doc, has); len(missing) > 0 {
		return 0, fmt.Sprintf("canvas.json 的画框引用了这个版本里没有的文件：%s。把这些 HTML 文件一起写进来，或改画框的 file。", strings.Join(missing, "、"))
	}
	return len(doc.Pages), ""
}

// loadManifest 读一个版本的清单。
func loadManifest(ctx context.Context, q *db.Queries, id string, n int64) (manifest, error) {
	rows, err := q.ListArtifactFiles(ctx, db.ListArtifactFilesParams{ArtifactID: id, N: n})
	if err != nil {
		return nil, err
	}
	m := make(manifest, len(rows))
	for _, r := range rows {
		m[r.Path] = entry{sha: r.Sha256, size: r.Size}
	}
	return m, nil
}

// checkBase 是按文件粒度的乐观并发检查：base == head 直接通过；否则 paths（可含伪路径 #capabilities）里
// 任何一个在 base 与 head 两版里内容不同就冲突。
func checkBase(ctx context.Context, q *db.Queries, id string, base int, head int64, paths []string) error {
	if int64(base) == head {
		return nil
	}
	if base < 1 || int64(base) > head {
		return &OpError{Code: CodeInvalid, Message: fmt.Sprintf("baseVersion %d 不存在；artifact %s 现在是版本 %d。先 read_artifact 拿到最新版本号。", base, id, head), State: &State{Version: int(head)}}
	}
	bm, err := loadManifest(ctx, q, id, int64(base))
	if err != nil {
		return err
	}
	hm, err := loadManifest(ctx, q, id, head)
	if err != nil {
		return err
	}
	var changed []string
	for _, p := range paths {
		if p == capsPath {
			bv, err := q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: id, N: int64(base)})
			if err != nil {
				return err
			}
			hv, err := q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: id, N: head})
			if err != nil {
				return err
			}
			if bv.Capabilities != hv.Capabilities {
				changed = append(changed, p)
			}
			continue
		}
		if bm[p].sha != hm[p].sha {
			changed = append(changed, p)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	slices.Sort(changed)
	return &OpError{
		Code: CodeConflict,
		Message: fmt.Sprintf("%s 现在是版本 %d，%s 在你的 baseVersion %d 之后被改过。重新 read_artifact 后在版本 %d 上再改。",
			id, head, strings.Join(changed, "、"), base, head),
		State: &State{Version: int(head), Changed: changed},
	}
}

// insertVersion 在事务里插入版本行与文件行，并更新 artifact 的 head。
func (s *Service) insertVersion(ctx context.Context, q *db.Queries, id string, n int64, typ string, m manifest, sh shape, caps []string, author Author, note string) error {
	var size int64
	for _, e := range m {
		size += e.size
	}
	var format *int64
	if typ != "" {
		f := int64(sh.format)
		format = &f
	}
	now := s.now().UnixMilli()
	if err := q.InsertArtifactVersion(ctx, db.InsertArtifactVersionParams{
		ArtifactID: id, N: n, Size: size, Pages: int64(sh.pages), AuthorThreadID: nullable(author.ThreadID),
		AuthorLabel: author.Label, Note: nullable(note), Capabilities: capsJSON(caps), Format: format,
		FilesKey: filesKey(typ, sh.format, m), CreatedAt: now,
	}); err != nil {
		return err
	}
	for _, p := range m.paths() {
		e := m[p]
		if err := q.InsertArtifactFile(ctx, db.InsertArtifactFileParams{ArtifactID: id, N: n, Path: p, Sha256: e.sha, Size: e.size}); err != nil {
			return err
		}
	}
	return q.UpdateArtifactHead(ctx, db.UpdateArtifactHeadParams{ID: id, Head: n, Size: size, Pages: int64(sh.pages), Kind: sh.kind, UpdatedAt: now})
}

func (s *Service) rateCheck(a Author) error {
	if a.ThreadID != "" && !s.lim.allow(a.ThreadID, s.now(), WritesPerMinute) {
		return opErr(CodeRateLimited, "这个线程一分钟内写入 artifact 超过 %d 次。等一会儿再写，或把多处改动合并成一次 update_artifact。", WritesPerMinute)
	}
	return nil
}

func (s *Service) rateRecord(a Author) {
	if a.ThreadID != "" {
		s.lim.record(a.ThreadID, s.now())
	}
}

// Create 新建 artifact（版本 1）。
func (s *Service) Create(ctx context.Context, p CreateParams) (Artifact, error) {
	return s.create(ctx, p, nil)
}

// create 新建 artifact；pre 非 nil 时文件已经落进内容存储（导入流式写入），不再读 p.Files。
func (s *Service) create(ctx context.Context, p CreateParams, pre *staged) (Artifact, error) {
	title, err := CleanTitle(p.Title)
	if err != nil {
		return Artifact{}, err
	}
	note, err := cleanNote(p.Note)
	if err != nil {
		return Artifact{}, err
	}
	if p.Origin == "" {
		p.Origin = OriginAgent
	}
	if p.Origin != OriginAgent && p.Origin != OriginImport {
		return Artifact{}, opErr(CodeInvalid, "未知的来源 %q。", p.Origin)
	}
	if p.Type != "" && TypeEntry(p.Type) == "" {
		return Artifact{}, opErr(CodeInvalid, "未知的类型 %q；只能是 document 或 slides，普通页面与画布不写 type。", p.Type)
	}
	caps, err := NormalizeCapabilities(p.Capabilities)
	if err != nil {
		return Artifact{}, err
	}
	if err := s.rateCheck(p.Author); err != nil {
		return Artifact{}, err
	}
	var st staged
	if pre != nil {
		// 导入路径：调用方在流式落内容之前已经 holdContent，并持有到这里返回
		st = *pre
	} else {
		release := s.holdContent()
		defer release()
		if st, err = s.stage(p.Files, p.Type); err != nil {
			return Artifact{}, err
		}
	}
	if s.afterStage != nil {
		s.afterStage()
	}
	if len(st.removes) > 0 {
		return Artifact{}, opErr(CodeInvalid, "新建 artifact 时不能删除文件。")
	}
	m, _ := st.apply(nil)
	sh, err := s.validate(p.Type, m, st)
	if err != nil {
		return Artifact{}, err
	}
	id := ids.Artifact()
	now := s.now().UnixMilli()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertArtifact(ctx, db.InsertArtifactParams{
			ID: id, ProjectID: nullable(p.ProjectID), ThreadID: nullable(p.ThreadID), Origin: p.Origin, Type: nullable(p.Type),
			Kind: sh.kind, Title: title, Head: 1, Pages: int64(sh.pages), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return s.insertVersion(ctx, q, id, 1, p.Type, m, sh, caps, p.Author, note)
	})
	if err != nil {
		return Artifact{}, err
	}
	s.rateRecord(p.Author)
	s.versioned(id, 1)
	return s.Get(ctx, id)
}

// Write 在 BaseVersion 上写入一个新版本：新清单 = 最新版本的清单应用 Files（同路径覆盖，Remove 删除）。
// 冲突按文件粒度检查（见 checkBase），整个写入在一个事务里，失败时不落任何版本。
func (s *Service) Write(ctx context.Context, p WriteParams) (Artifact, error) {
	note, err := cleanNote(p.Note)
	if err != nil {
		return Artifact{}, err
	}
	if len(p.Files) == 0 && p.Capabilities == nil {
		return Artifact{}, opErr(CodeInvalid, "这次写入没有任何改动。")
	}
	var caps []string
	if p.Capabilities != nil {
		if caps, err = NormalizeCapabilities(*p.Capabilities); err != nil {
			return Artifact{}, err
		}
	}
	if !ValidID(p.ID) {
		return Artifact{}, notFound(p.ID)
	}
	unlock := s.locks.lock(p.ID)
	defer unlock()
	cur, err := getRow(ctx, s.st.Q, p.ID)
	if err != nil {
		return Artifact{}, err
	}
	if cur.DeletedAt != nil {
		return Artifact{}, deletedErr(p.ID)
	}
	if err := s.rateCheck(p.Author); err != nil {
		return Artifact{}, err
	}
	typ := deref(cur.Type)
	release := s.holdContent()
	defer release()
	st, err := s.stage(p.Files, typ)
	if err != nil {
		return Artifact{}, err
	}
	if s.afterStage != nil {
		s.afterStage()
	}
	var n int64
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		a, err := getRow(ctx, q, p.ID)
		if err != nil {
			return err
		}
		if a.DeletedAt != nil {
			return deletedErr(p.ID)
		}
		check := slices.DeleteFunc(st.changedPaths(), func(path string) bool { return slices.Contains(p.Unchecked, path) })
		if p.Capabilities != nil {
			check = append(check, capsPath)
		}
		if err := checkBase(ctx, q, p.ID, p.BaseVersion, a.Head, check); err != nil {
			return err
		}
		headM, err := loadManifest(ctx, q, p.ID, a.Head)
		if err != nil {
			return err
		}
		m, err := st.apply(headM)
		if err != nil {
			return err
		}
		sh, err := s.validate(typ, m, st)
		if err != nil {
			return err
		}
		if p.Capabilities == nil {
			hv, err := q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: p.ID, N: a.Head})
			if err != nil {
				return err
			}
			caps = versionFromRow(hv).Capabilities
		}
		n = a.Head + 1
		return s.insertVersion(ctx, q, p.ID, n, typ, m, sh, caps, p.Author, note)
	})
	if err != nil {
		return Artifact{}, err
	}
	s.rateRecord(p.Author)
	s.versioned(p.ID, int(n))
	return s.Get(ctx, p.ID)
}

// RestoreVersion 把旧版本 version 的文件清单、能力声明与格式复制成新的最新版本。冲突检查的路径是两版之间
// 内容不同的全部路径。
func (s *Service) RestoreVersion(ctx context.Context, id string, version, base int, author Author) (Artifact, error) {
	if !ValidID(id) {
		return Artifact{}, notFound(id)
	}
	unlock := s.locks.lock(id)
	defer unlock()
	if err := s.rateCheck(author); err != nil {
		return Artifact{}, err
	}
	var n int64
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		a, err := getRow(ctx, q, id)
		if err != nil {
			return err
		}
		if a.DeletedAt != nil {
			return deletedErr(id)
		}
		if version < 1 || int64(version) > a.Head {
			return opErr(CodeNotFound, "artifact %s 没有版本 %d（最新是版本 %d）。", id, version, a.Head)
		}
		if int64(version) == a.Head {
			return opErr(CodeInvalid, "版本 %d 已经是最新版本，不需要恢复。", version)
		}
		old, err := loadManifest(ctx, q, id, int64(version))
		if err != nil {
			return err
		}
		headM, err := loadManifest(ctx, q, id, a.Head)
		if err != nil {
			return err
		}
		ov, err := q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: id, N: int64(version)})
		if err != nil {
			return err
		}
		hv, err := q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: id, N: a.Head})
		if err != nil {
			return err
		}
		var diff []string
		for p, e := range old {
			if headM[p].sha != e.sha {
				diff = append(diff, p)
			}
		}
		for p := range headM {
			if _, ok := old[p]; !ok {
				diff = append(diff, p)
			}
		}
		if ov.Capabilities != hv.Capabilities {
			diff = append(diff, capsPath)
		}
		if err := checkBase(ctx, q, id, base, a.Head, diff); err != nil {
			return err
		}
		v := versionFromRow(ov)
		n = a.Head + 1
		sh := shape{kind: a.Kind, pages: v.Pages, format: v.Format}
		if deref(a.Type) == "" {
			if _, ok := old[EntryCanvas]; ok {
				sh.kind = KindCanvas
			} else {
				sh.kind = KindWeb
			}
		}
		return s.insertVersion(ctx, q, id, n, deref(a.Type), old, sh, v.Capabilities, author, fmt.Sprintf("恢复自版本 %d", version))
	})
	if err != nil {
		return Artifact{}, err
	}
	s.rateRecord(author)
	s.versioned(id, int(n))
	return s.Get(ctx, id)
}
