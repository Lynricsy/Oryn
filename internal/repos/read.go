package repos

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/gitdiff"
	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	emptyTree       = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	refsMax         = 10000    // 分支 / 标签至多列出的个数
	refsOutMax      = 16 << 20 // for-each-ref 的输出上限
	refsLimitDef    = 50
	refsLimitMax    = 200
	commitsLimitDef = 30
	commitsLimitMax = 100
	logOutMax       = 4 << 20  // 一页提交的输出上限
	commitBodyMax   = 64 << 10 // 提交正文上限
	commitFilesMax  = 2000
	diffListMax     = 16 << 20 // name-status / numstat 的输出上限
	patchMax        = 1 << 20  // 单文件 patch 上限（同 orbd）
	treeEntriesMax  = 5000
	treeOutMax      = 16 << 20 // ls-tree 的输出上限
	treeWalkMax     = 2000     // 找子项最后提交时至多读的提交数
	treeWalkTime    = 3 * time.Second
	treeCacheMax    = 256
	blobMax         = 64 << 20 // 同 web 的 MAX_PREVIEW_BLOB_BYTES
	blobTimeout     = 30 * time.Minute
	sniffBytes      = 8 << 10
	symlinkMax      = 4 << 10
	pathMax         = 4096
	noThreadTTL     = 24 * time.Hour // GitHub 反查没有线程的结果在这段时间内不再反查
	pullLookupWait  = 15 * time.Second
)

var (
	errNotReady       = &Error{Status: http.StatusConflict, Code: "repo_not_ready", Message: "仓库镜像还没有就绪（未克隆、克隆中或克隆失败）"}
	errRefNotFound    = &Error{Status: http.StatusNotFound, Code: "ref_not_found", Message: "找不到这个分支、标签或提交"}
	errCommitNotFound = &Error{Status: http.StatusNotFound, Code: "commit_not_found", Message: "找不到这个提交"}
	errPathNotFound   = &Error{Status: http.StatusNotFound, Code: "path_not_found", Message: "仓库里没有这个路径"}
	errNotAFile       = &Error{Status: http.StatusConflict, Code: "not_a_file", Message: "这个路径不是文件"}

	shaArgRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	cursorRe = regexp.MustCompile(`^([0-9a-f]{40}(?:[0-9a-f]{24})?):([0-9]{1,7})$`)
	offRe    = regexp.MustCompile(`^[0-9]{1,7}$`)
)

func invalid(format string, a ...any) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: fmt.Sprintf(format, a...)}
}

// literal 把仓库内路径写成不带通配语义的 pathspec（同 orbdrt/gitapi.go 的 literal）。
func literal(p string) string { return ":(literal)" + p }

// validRepoPath 校验仓库内相对路径；root 为真时允许空串（根）。
func validRepoPath(p string, root bool) error {
	if p == "" {
		if root {
			return nil
		}
		return invalid("path 不能为空")
	}
	if len(p) > pathMax || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return invalid("path 不合法：%q", p)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return invalid("path 不合法：%q", p)
		}
	}
	return nil
}

func validRefArg(ref string) error {
	if ref != "" && !ValidRef(ref) {
		return invalid("ref 不合法：%q", ref)
	}
	return nil
}

func validShaArg(sha string) error {
	if !shaArgRe.MatchString(sha) {
		return invalid("sha 不合法：%q", sha)
	}
	return nil
}

// pageLimit 取分页大小：nil = def；超出 [1, max] → 400。
func pageLimit(p *int, def, max int) (int, error) {
	if p == nil {
		return def, nil
	}
	if *p < 1 || *p > max {
		return 0, invalid("limit 必须在 1 到 %d 之间", max)
	}
	return *p, nil
}

// repoRead 是一次读请求要用的项目、镜像行与镜像目录。
type repoRead struct {
	prj db.Project
	row *db.ProjectRepo
	dir string
}

// openRead 是每个读接口的入口：项目没有仓库（地址或托管）→ no_repo；git 不可用 → repo_unavailable；仓库目录不在 → repo_not_ready。
// 与 Status 一样经 ensure 核对地址，未归档项目按需在后台克隆或拉取（不阻塞）。
func (s *Service) openRead(ctx context.Context, projectID string) (repoRead, error) {
	prj, err := s.project(ctx, projectID)
	if err != nil {
		return repoRead{}, err
	}
	if !hasRepo(prj) && !prj.Hosted {
		return repoRead{}, errNoRepo
	}
	if s.reason != "" {
		return repoRead{}, errUnavailable
	}
	row, err := s.ensure(ctx, prj, prj.ArchivedAt == nil)
	if err != nil {
		return repoRead{}, err
	}
	dir := s.dir(prj.ID)
	if !exists(dir) {
		return repoRead{}, errNotReady
	}
	return repoRead{prj: prj, row: row, dir: dir}, nil
}

// gitRead 执行镜像上的读命令；非零退出码按 notFound（nil 时为通用错误）处理。
func (s *Service) gitRead(ctx context.Context, dir string, limit int, notFound error, args ...string) (gitResult, error) {
	res, err := s.git(ctx, gitRun{dir: dir, read: true, limit: limit}, args...)
	if err != nil {
		return res, err
	}
	if res.code != 0 && !res.truncated {
		if notFound != nil {
			return res, notFound
		}
		return res, fmt.Errorf("git %s: exit %d: %s", args[0], res.code, strings.TrimSpace(res.stderr))
	}
	return res, nil
}

// resolveRef 解析 ref（空 = 基线分支）：依次按分支、标签、提交 sha 解析成提交；返回实际的 ref 名与提交 sha。
func (s *Service) resolveRef(ctx context.Context, r repoRead, ref string) (name, sha string, err error) {
	if ref == "" {
		if ref = s.baseBranch(ctx, r.prj, r.row, r.dir); ref == "" {
			return "", "", errRefNotFound
		}
	} else if err := validRefArg(ref); err != nil {
		return "", "", err
	}
	cands := []string{"refs/heads/" + ref, "refs/tags/" + ref}
	if shaArgRe.MatchString(ref) {
		cands = append(cands, ref)
	}
	for _, c := range cands {
		res, err := s.git(ctx, gitRun{dir: r.dir, read: true, limit: 256}, "rev-parse", "--verify", "-q", "--end-of-options", c+"^{commit}")
		if err != nil {
			return "", "", err
		}
		if res.code == 0 && !res.truncated {
			return ref, strings.TrimSpace(string(res.out)), nil
		}
	}
	return "", "", errRefNotFound
}

// records 把以 \x1e 结尾的记录切开（去掉记录间的换行与空记录）。最后一个 \x1e 之后只剩换行，或是被截断的半条，一律丢掉。
func records(out []byte) []string {
	recs := strings.Split(string(out), "\x1e")
	recs = recs[:len(recs)-1]
	kept := recs[:0]
	for _, r := range recs {
		if r = strings.Trim(r, "\n"); r != "" {
			kept = append(kept, r)
		}
	}
	return kept
}

func unixMs(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n * 1000
}

// trimEmail 去掉 for-each-ref 的 %(authoremail) 两侧的尖括号（:trim 修饰符要 git 2.33）。
func trimEmail(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">") }

// ---------------------------------------------------------------- refs

const refFormat = "%(refname)%1f%(objectname)%1f%(objectname:short)%1f%(*objectname)%1f%(*objectname:short)%1f" +
	"%(subject)%1f%(*subject)%1f%(authorname)%1f%(*authorname)%1f%(authoremail)%1f%(*authoremail)%1f" +
	"%(authordate:unix)%1f%(*authordate:unix)%1f%(objecttype)%1e"

// Refs 列出镜像的分支或标签：分支按最近提交、标签按创建时间倒序，默认分支排第一；cursor 是十进制 offset。
func (s *Service) Refs(ctx context.Context, projectID string, kind apigen.ProjectRepoRefKind, cursor string, limit *int) (apigen.ProjectRepoRefList, error) {
	var prefix, sortKey string
	switch kind {
	case apigen.RepoRefBranch:
		prefix, sortKey = "refs/heads/", "-committerdate"
	case apigen.RepoRefTag:
		prefix, sortKey = "refs/tags/", "-creatordate"
	default:
		return apigen.ProjectRepoRefList{}, invalid("kind 必须是 branch 或 tag")
	}
	n, err := pageLimit(limit, refsLimitDef, refsLimitMax)
	if err != nil {
		return apigen.ProjectRepoRefList{}, err
	}
	off := 0
	if cursor != "" {
		if !offRe.MatchString(cursor) {
			return apigen.ProjectRepoRefList{}, invalid("cursor 不合法")
		}
		off, _ = strconv.Atoi(cursor)
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return apigen.ProjectRepoRefList{}, err
	}
	res, err := s.gitRead(ctx, r.dir, refsOutMax, nil, "for-each-ref", "--sort="+sortKey,
		fmt.Sprintf("--count=%d", refsMax), "--format="+refFormat, prefix)
	if err != nil {
		return apigen.ProjectRepoRefList{}, err
	}
	def := ""
	if kind == apigen.RepoRefBranch {
		def = deref(deref(r.row).DefaultBranch)
	}
	var refs []apigen.ProjectRepoRef
	for _, rec := range records(res.out) {
		f := strings.Split(rec, "\x1f")
		if len(f) != 14 {
			continue
		}
		name := strings.TrimPrefix(f[0], prefix)
		ref := apigen.ProjectRepoRef{Name: name, Kind: kind, Default: def != "" && name == def}
		i := 0 // 字段偏移：附注标签取被指向对象（*）的字段
		if f[13] == "tag" {
			i = 1
			ref.Annotated = new(true)
		} else if kind == apigen.RepoRefTag {
			ref.Annotated = new(false)
		}
		pick := func(at int) string { return f[at+i] }
		ref.Commit = apigen.ProjectRepoCommitSummary{
			Sha: f[1+2*i], ShortSha: f[2+2*i], Subject: pick(5), AuthorName: pick(7),
			AuthorEmail: trimEmail(pick(9)), TS: unixMs(pick(11)),
		}
		refs = append(refs, ref)
	}
	if k := slices.IndexFunc(refs, func(x apigen.ProjectRepoRef) bool { return x.Default }); k > 0 {
		d := refs[k]
		copy(refs[1:k+1], refs[:k])
		refs[0] = d
	}
	out := apigen.ProjectRepoRefList{Refs: []apigen.ProjectRepoRef{}}
	if off < len(refs) {
		end := min(off+n, len(refs))
		out.Refs = refs[off:end]
		if end < len(refs) {
			out.NextCursor = new(strconv.Itoa(end))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- commits

const logFormat = "%H%x1f%h%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%s%x1e"

func parseCommit(rec string) (apigen.ProjectRepoCommit, bool) {
	f := strings.SplitN(rec, "\x1f", 7)
	if len(f) != 7 {
		return apigen.ProjectRepoCommit{}, false
	}
	return apigen.ProjectRepoCommit{
		Sha: f[0], ShortSha: f[1], Parents: append([]string{}, strings.Fields(f[2])...),
		AuthorName: f[3], AuthorEmail: f[4], TS: unixMs(f[5]), Subject: f[6],
	}, true
}

// Commits 列出 ref（空 = 基线分支）上的提交，新到旧，可按路径过滤。cursor = <头提交>:<offset>：翻页沿用第一页的头提交，
// 分支移动后后续页仍然稳定。
func (s *Service) Commits(ctx context.Context, projectID, ref, pathArg, cursor string, limit *int) (apigen.ProjectRepoCommitList, error) {
	var out apigen.ProjectRepoCommitList
	n, err := pageLimit(limit, commitsLimitDef, commitsLimitMax)
	if err != nil {
		return out, err
	}
	if err := validRepoPath(pathArg, true); err != nil {
		return out, err
	}
	if err := validRefArg(ref); err != nil {
		return out, err
	}
	var tip string
	off := 0
	if cursor != "" {
		m := cursorRe.FindStringSubmatch(cursor)
		if m == nil {
			return out, invalid("cursor 不合法")
		}
		tip = m[1]
		off, _ = strconv.Atoi(m[2])
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return out, err
	}
	name := ref
	if tip == "" {
		if name, tip, err = s.resolveRef(ctx, r, ref); err != nil {
			return out, err
		}
	} else if name == "" {
		name = s.baseBranch(ctx, r.prj, r.row, r.dir)
	}
	args := []string{"log", "--format=" + logFormat, fmt.Sprintf("--skip=%d", off), fmt.Sprintf("--max-count=%d", n+1),
		"--end-of-options", tip, "--"}
	if pathArg != "" {
		args = append(args, literal(pathArg))
	}
	res, err := s.gitRead(ctx, r.dir, logOutMax, errCommitNotFound, args...)
	if err != nil {
		return out, err
	}
	out.Ref, out.Commit, out.Commits = name, tip, []apigen.ProjectRepoCommit{}
	for _, rec := range records(res.out) {
		if c, ok := parseCommit(rec); ok {
			out.Commits = append(out.Commits, c)
		}
	}
	if len(out.Commits) > n {
		out.Commits = out.Commits[:n]
		out.NextCursor = new(fmt.Sprintf("%s:%d", tip, off+n))
	}
	shas := make([]string, len(out.Commits))
	for i, c := range out.Commits {
		shas[i] = c.Sha
	}
	links, _ := s.threadsFor(ctx, r.prj, shas)
	for i := range out.Commits {
		if l, ok := links[out.Commits[i].Sha]; ok {
			out.Commits[i].Thread = &l
		}
	}
	return out, nil
}

// Commit 返回一个提交的详情：正文、改动文件（合并提交相对第一个父提交，根提交相对空树）与关联线程。
func (s *Service) Commit(ctx context.Context, projectID, sha string) (apigen.ProjectRepoCommitDetail, error) {
	var out apigen.ProjectRepoCommitDetail
	if err := validShaArg(sha); err != nil {
		return out, err
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return out, err
	}
	res, err := s.gitRead(ctx, r.dir, commitBodyMax+(8<<10), errCommitNotFound, "show", "-s",
		"--format=%H%x1f%h%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ce%x1f%ct%x1f%s%x1f%b", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return out, err
	}
	f := strings.SplitN(string(res.out), "\x1f", 11)
	if len(f) != 11 {
		return out, errCommitNotFound
	}
	body := f[10]
	if res.truncated || len(body) > commitBodyMax {
		body = strings.ToValidUTF8(body[:min(len(body), commitBodyMax)], "")
	}
	out = apigen.ProjectRepoCommitDetail{
		Sha: f[0], ShortSha: f[1], Parents: append([]string{}, strings.Fields(f[2])...),
		AuthorName: f[3], AuthorEmail: f[4], TS: unixMs(f[5]),
		CommitterName: f[6], CommittedAt: unixMs(f[8]), Subject: f[9], Body: strings.TrimRight(body, "\n"),
		Files: []apigen.GitFileChange{},
	}
	if f[7] != "" {
		out.CommitterEmail = new(f[7])
	}
	base := emptyTree
	if len(out.Parents) > 0 {
		base = out.Parents[0]
	}
	ns, err := s.gitRead(ctx, r.dir, diffListMax, nil, "diff", "--no-ext-diff", "-M", "--name-status", "-z", base, out.Sha, "--")
	if err != nil {
		return out, err
	}
	nm, err := s.gitRead(ctx, r.dir, diffListMax, nil, "diff", "--no-ext-diff", "-M", "--numstat", "-z", base, out.Sha, "--")
	if err != nil {
		return out, err
	}
	byPath, order := gitdiff.ParseChanges(ns.out, nm.out)
	out.Truncated = ns.truncated
	for _, p := range order {
		c := byPath[p]
		out.Files = append(out.Files, *c)
		if !c.Binary {
			out.Additions += deref(c.Additions)
			out.Deletions += deref(c.Deletions)
		}
	}
	slices.SortFunc(out.Files, func(a, b apigen.GitFileChange) int { return strings.Compare(a.Path, b.Path) })
	if len(out.Files) > commitFilesMax {
		out.Files, out.Truncated = out.Files[:commitFilesMax], true
	}
	if l, ok := s.commitThread(ctx, r.prj, out.Sha); ok {
		out.Thread = &l
	}
	return out, nil
}

// CommitDiff 返回提交里一个文件的 unified diff（-U3，≤1MiB，规则同 orbd 的 handleGitDiff）。
func (s *Service) CommitDiff(ctx context.Context, projectID, sha, filePath, oldPath string) (apigen.GitFileDiff, error) {
	out := apigen.GitFileDiff{Path: filePath, Kind: apigen.Text}
	if err := validShaArg(sha); err != nil {
		return out, err
	}
	if err := validRepoPath(filePath, false); err != nil {
		return out, err
	}
	if oldPath != "" {
		if err := validRepoPath(oldPath, false); err != nil {
			return out, err
		}
		out.OldPath = &oldPath
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return out, err
	}
	res, err := s.gitRead(ctx, r.dir, 512, errCommitNotFound, "show", "-s", "--format=%H%x1f%P", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return out, err
	}
	full, parents, _ := strings.Cut(strings.TrimSpace(string(res.out)), "\x1f")
	base := emptyTree
	if ps := strings.Fields(parents); len(ps) > 0 {
		base = ps[0]
	}
	args := []string{"diff", "--no-ext-diff", "-M", "-U3", base, full, "--", literal(filePath)}
	if oldPath != "" && oldPath != filePath {
		args = append(args, literal(oldPath))
	}
	d, err := s.gitRead(ctx, r.dir, patchMax, nil, args...)
	if err != nil {
		return out, err
	}
	patch := d.out
	if d.truncated {
		patch = gitdiff.CutTruncated(patch)
		out.Truncated = true
	}
	if gitdiff.IsBinaryPatch(patch) {
		out.Kind, out.Truncated, patch = apigen.Binary, false, nil
	}
	out.Patch = string(patch)
	return out, nil
}

// ---------------------------------------------------------------- tree

// treeEntry 由 ls-tree --long 的一条记录（<mode> <type> <oid> <size>\t<path>）构造条目；name 与 path 由调用方给出。
func treeEntry(rec, name, p string) (apigen.ProjectRepoTreeEntry, bool) {
	meta, _, ok := strings.Cut(rec, "\t")
	f := strings.Fields(meta)
	if !ok || len(f) != 4 {
		return apigen.ProjectRepoTreeEntry{}, false
	}
	e := apigen.ProjectRepoTreeEntry{Name: name, Path: p, Oid: f[2]}
	switch f[0] {
	case "040000":
		e.Type, e.Mode = apigen.RepoEntryDir, 0o755
	case "120000":
		e.Type, e.Mode = apigen.RepoEntrySymlink, 0o777
	case "160000":
		e.Type, e.Mode = apigen.RepoEntrySubmodule, 0
	default:
		m, err := strconv.ParseUint(f[0], 8, 32)
		if err != nil {
			return apigen.ProjectRepoTreeEntry{}, false
		}
		e.Type, e.Mode = apigen.RepoEntryFile, int(m&0o777)
	}
	if e.Type == apigen.RepoEntryFile || e.Type == apigen.RepoEntrySymlink {
		e.Size, _ = strconv.ParseInt(f[3], 10, 64)
	}
	return e, true
}

// treeSelf 返回提交 commit 下 p 本身的条目（根为目录）；不存在 → path_not_found。
func (s *Service) treeSelf(ctx context.Context, dir, commit, p string) (apigen.ProjectRepoTreeEntry, error) {
	if p == "" {
		res, err := s.gitRead(ctx, dir, 256, errCommitNotFound, "rev-parse", "--verify", "-q", "--end-of-options", commit+"^{tree}")
		if err != nil {
			return apigen.ProjectRepoTreeEntry{}, err
		}
		return apigen.ProjectRepoTreeEntry{Type: apigen.RepoEntryDir, Mode: 0o755, Oid: strings.TrimSpace(string(res.out))}, nil
	}
	res, err := s.gitRead(ctx, dir, treeOutMax, nil, "ls-tree", "-z", "--long", commit, "--", literal(p))
	if err != nil {
		return apigen.ProjectRepoTreeEntry{}, err
	}
	for _, rec := range gitdiff.NULFields(res.out) {
		if _, rp, _ := strings.Cut(rec, "\t"); rp == p {
			if e, ok := treeEntry(rec, path.Base(p), p); ok {
				return e, nil
			}
		}
	}
	return apigen.ProjectRepoTreeEntry{}, errPathNotFound
}

// catBlobs 用一次 cat-file --batch 读出若干 blob 的内容（调用方保证都很小）；读不到的省略。
func (s *Service) catBlobs(ctx context.Context, dir string, oids []string, total int64) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(oids) == 0 {
		return out, nil
	}
	in := strings.Join(oids, "\n") + "\n"
	res, err := s.git(ctx, gitRun{dir: dir, read: true, stdin: strings.NewReader(in), limit: int(total) + len(oids)*128},
		"cat-file", "--batch")
	if err != nil {
		return out, err
	}
	b := res.out
	for len(b) > 0 {
		hdr, rest, ok := bytes.Cut(b, []byte{'\n'})
		if !ok {
			break
		}
		f := strings.Fields(string(hdr))
		if len(f) != 3 { // "<oid> missing" 等
			b = rest
			continue
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n+1 > len(rest) {
			break
		}
		out[f[0]] = rest[:n]
		b = rest[n+1:]
	}
	return out, nil
}

// Tree 返回 ref（空 = 基线分支）下 p 的条目；目录时列出直接子项（目录在前、按名不分大小写排序，至多 5000 个），
// lastCommits 时给子项找最后一次改动它的提交（至多读 treeWalkMax 个提交、treeWalkTime 时长）。
func (s *Service) Tree(ctx context.Context, projectID, ref, p string, lastCommits bool) (apigen.ProjectRepoTree, error) {
	var out apigen.ProjectRepoTree
	if err := validRepoPath(p, true); err != nil {
		return out, err
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return out, err
	}
	name, commit, err := s.resolveRef(ctx, r, ref)
	if err != nil {
		return out, err
	}
	self, err := s.treeSelf(ctx, r.dir, commit, p)
	if err != nil {
		return out, err
	}
	out = apigen.ProjectRepoTree{Ref: name, Commit: commit, Path: p, Self: self, Entries: []apigen.ProjectRepoTreeEntry{}, LastCommitsComplete: true}
	switch self.Type {
	case apigen.RepoEntryDir:
		if err := s.listDir(ctx, r.dir, &out); err != nil {
			return out, err
		}
		if lastCommits && len(out.Entries) > 0 {
			out.LastCommitsComplete = s.lastCommits(ctx, r.dir, commit, p, out.Entries)
		}
	case apigen.RepoEntryFile:
		head, err := s.gitRead(ctx, r.dir, sniffBytes, nil, "cat-file", "blob", self.Oid)
		if err != nil {
			return out, err
		}
		if ptr, ok := lfsPointer(self.Size, head.out); ok {
			if err := s.lfsSelf(ctx, r.prj, &out.Self, ptr); err != nil {
				return out, err
			}
			break
		}
		out.Self.Mime = new(contentType(self.Name, head.out))
		out.Self.Text = new(isText(head.out, head.truncated))
	case apigen.RepoEntrySymlink:
		if err := s.linkTargets(ctx, r.dir, []*apigen.ProjectRepoTreeEntry{&out.Self}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// listDir 读出目录 t.Self 的直接子项。
func (s *Service) listDir(ctx context.Context, dir string, t *apigen.ProjectRepoTree) error {
	res, err := s.gitRead(ctx, dir, treeOutMax, nil, "ls-tree", "-z", "--long", t.Self.Oid)
	if err != nil {
		return err
	}
	recs := gitdiff.NULFields(res.out)
	if res.truncated && len(recs) > 0 {
		recs, t.Truncated = recs[:len(recs)-1], true
	}
	for _, rec := range recs {
		_, name, _ := strings.Cut(rec, "\t")
		if e, ok := treeEntry(rec, name, path.Join(t.Path, name)); ok {
			t.Entries = append(t.Entries, e)
		}
	}
	slices.SortFunc(t.Entries, func(a, b apigen.ProjectRepoTreeEntry) int {
		ad, bd := a.Type == apigen.RepoEntryDir, b.Type == apigen.RepoEntryDir
		if ad != bd {
			if ad {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name))
	})
	if len(t.Entries) > treeEntriesMax {
		t.Entries, t.Truncated = t.Entries[:treeEntriesMax], true
	}
	var links []*apigen.ProjectRepoTreeEntry
	for i := range t.Entries {
		if t.Entries[i].Type == apigen.RepoEntrySymlink {
			links = append(links, &t.Entries[i])
		}
	}
	if err := s.lfsEntries(ctx, dir, t.Entries); err != nil {
		return err
	}
	return s.linkTargets(ctx, dir, links)
}

// lfsPointer 识别 blob 内容 b（大小 size）是不是合规的 Git LFS 指针；只看小于 lfs.PointerMax 的完整内容。
func lfsPointer(size int64, b []byte) (lfs.Pointer, bool) {
	if size <= 0 || size >= lfs.PointerMax || int64(len(b)) != size {
		return lfs.Pointer{}, false
	}
	return lfs.ParsePointer(b)
}

// lfsEntries 用一次 cat-file --batch 读出小文件的内容，是 LFS 指针的条目 size 改为对象大小并带上 lfs.oid。
// 目录条目从不取对象。
func (s *Service) lfsEntries(ctx context.Context, dir string, entries []apigen.ProjectRepoTreeEntry) error {
	var oids []string
	at := map[string][]int{}
	for i, e := range entries {
		if e.Type != apigen.RepoEntryFile || e.Size <= 0 || e.Size >= lfs.PointerMax {
			continue
		}
		if _, seen := at[e.Oid]; !seen {
			oids = append(oids, e.Oid)
		}
		at[e.Oid] = append(at[e.Oid], i)
	}
	blobs, err := s.catBlobs(ctx, dir, oids, int64(len(oids))*lfs.PointerMax)
	if err != nil {
		return err
	}
	for oid, b := range blobs {
		ptr, ok := lfsPointer(int64(len(b)), b)
		if !ok {
			continue
		}
		for _, i := range at[oid] {
			entries[i].Size = ptr.Size
			entries[i].Lfs = &apigen.ProjectRepoLfs{Oid: ptr.OID}
		}
	}
	return nil
}

// lfsSelf 给是 LFS 指针的 self 填上对象大小与 lfs.oid。对象不超过 inlineLFSMax（托管项目不限大小：对象就在本地）时先取对象，
// 按对象内容判断 mime 与 text；取不到时记 lfs.error，mime 只按扩展名、省略 text。更大的对象不取，mime 只按扩展名。
func (s *Service) lfsSelf(ctx context.Context, prj db.Project, e *apigen.ProjectRepoTreeEntry, ptr lfs.Pointer) error {
	e.Size = ptr.Size
	e.Lfs = &apigen.ProjectRepoLfs{Oid: ptr.OID}
	if ptr.Size > s.inlineLFSMax && !prj.Hosted {
		e.Mime = new(extType(e.Name))
		return nil
	}
	f, err := s.lfsObject(ctx, prj, ptr)
	if err != nil {
		if code, ok := lfsCode(err); ok {
			e.Lfs.Error = &code
			e.Mime = new(extType(e.Name))
			return nil
		}
		return err
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	head = head[:n]
	e.Mime = new(contentType(e.Name, head))
	e.Text = new(isText(head, ptr.Size > int64(n)))
	return nil
}

// lfsCode 取取对象失败的 lfs_* 错误码。
func lfsCode(err error) (apigen.ProjectRepoLfsError, bool) {
	var le *Error
	if !errors.As(err, &le) {
		return "", false
	}
	code := apigen.ProjectRepoLfsError(le.Code)
	return code, code.Valid()
}

// extType 只按扩展名判断媒体类型；认不出时为 application/octet-stream。
func extType(name string) string {
	return cmp.Or(mime.TypeByExtension(path.Ext(name)), "application/octet-stream")
}

// linkTargets 填上符号链接的目标（≤4KiB 的才读）。
func (s *Service) linkTargets(ctx context.Context, dir string, links []*apigen.ProjectRepoTreeEntry) error {
	var oids []string
	var total int64
	for _, e := range links {
		if e.Size <= symlinkMax {
			oids = append(oids, e.Oid)
			total += e.Size
		}
	}
	blobs, err := s.catBlobs(ctx, dir, oids, total)
	if err != nil {
		return err
	}
	for _, e := range links {
		if b, ok := blobs[e.Oid]; ok {
			e.Target = new(string(b))
		}
	}
	return nil
}

// lastCommits 给 entries 填上最后一次改动它们的提交；全部找到时返回 true。结果按提交与目录缓存（提交不变，结果可一直复用）。
func (s *Service) lastCommits(ctx context.Context, dir, commit, p string, entries []apigen.ProjectRepoTreeEntry) bool {
	key := commit + "\x00" + p
	s.treeMu.Lock()
	found, ok := s.treeCache[key]
	s.treeMu.Unlock()
	if !ok {
		want := make(map[string]bool, len(entries))
		for _, e := range entries {
			want[e.Name] = true
		}
		var cacheable bool
		found, cacheable = s.walkLastCommits(ctx, dir, commit, p, want)
		if cacheable {
			s.treeMu.Lock()
			if len(s.treeCache) >= treeCacheMax {
				clear(s.treeCache)
			}
			s.treeCache[key] = found
			s.treeMu.Unlock()
		}
	}
	complete := true
	for i := range entries {
		if c, ok := found[entries[i].Name]; ok {
			entries[i].LastCommit = &c
		} else {
			complete = false
		}
	}
	return complete
}

// walkLastCommits 流式读 `log --name-only`，把改动路径映射到 p 的直接子项名，首次见到就记下该提交。子项全部找到、
// 读满 treeWalkMax 个提交或耗时 treeWalkTime 时停止。cacheable = 结果是确定的（没有因超时或出错而提前结束）。
func (s *Service) walkLastCommits(ctx context.Context, dir, commit, p string, want map[string]bool) (map[string]apigen.ProjectRepoCommitSummary, bool) {
	found := map[string]apigen.ProjectRepoCommitSummary{}
	wctx, cancel := context.WithTimeout(ctx, treeWalkTime)
	defer cancel()
	args := []string{"log", "--no-renames", "--name-only", "--format=%x1e%H%x1f%h%x1f%an%x1f%ae%x1f%at%x1f%s", "--end-of-options", commit, "--"}
	prefix := ""
	if p != "" {
		args = append(args, literal(p))
		prefix = p + "/"
	}
	proc, err := s.startGit(wctx, gitRun{dir: dir, read: true}, args...)
	if err != nil {
		slog.Warn("读取子项的最后提交", "dir", dir, "err", err)
		return found, false
	}
	br := bufio.NewReaderSize(proc.stdout, 64<<10)
	var (
		cur     apigen.ProjectRepoCommitSummary
		have    bool
		seen    int
		stopped bool
		rerr    error
	)
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "\x1e") {
			if seen >= s.treeWalkMax {
				stopped = true
				break
			}
			f := strings.SplitN(line[1:], "\x1f", 6)
			have = len(f) == 6
			if have {
				seen++
				cur = apigen.ProjectRepoCommitSummary{Sha: f[0], ShortSha: f[1], AuthorName: f[2], AuthorEmail: f[3], TS: unixMs(f[4]), Subject: f[5]}
			}
		} else if have && line != "" {
			if strings.HasPrefix(line, `"`) {
				if u, uerr := strconv.Unquote(line); uerr == nil {
					line = u
				}
			}
			if rel, ok := strings.CutPrefix(line, prefix); ok {
				child, _, _ := strings.Cut(rel, "/")
				if _, dup := found[child]; want[child] && !dup {
					found[child] = cur
					if len(found) == len(want) {
						stopped = true
						break
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				rerr = err
			}
			break
		}
	}
	if stopped {
		proc.stop()
	}
	_, err = proc.finish(stopped, rerr)
	return found, err == nil && wctx.Err() == nil
}

// contentType 先按扩展名、再按前 512 字节判断媒体类型。
func contentType(name string, head []byte) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return http.DetectContentType(head[:min(len(head), 512)])
}

// isText 报告内容开头是否像文本：没有 NUL 且是合法 UTF-8（被截断时忽略末尾不完整的字符）。
func isText(b []byte, truncated bool) bool {
	if truncated {
		for k := 1; k <= utf8.UTFMax && k <= len(b); k++ {
			if utf8.RuneStart(b[len(b)-k]) {
				if !utf8.FullRune(b[len(b)-k:]) {
					b = b[:len(b)-k]
				}
				break
			}
		}
	}
	return bytes.IndexByte(b, 0) < 0 && utf8.Valid(b)
}

// ---------------------------------------------------------------- blob

// BlobContent 是 Blob 的结果。NotModified 时只有 OID 与 Immutable，File 与 Body 都为 nil；否则二者恰有一个非 nil，
// 调用方关闭。
type BlobContent struct {
	OID, Name, ContentType string
	Size                   int64 // LFS 时为对象大小
	Inline, Immutable      bool
	NotModified            bool
	File                   *os.File      // 本地文件（LFS 对象缓存）：调用方用 http.ServeContent，支持 Range
	Body                   io.ReadCloser // git cat-file 或上游 LFS 流：调用方 io.Copy，出错即中止连接
}

// setType 定下响应的媒体类型：text/html 改为 text/plain；不是 download 时安全的栅格图片内联。
func (b *BlobContent) setType(ct string, download bool) {
	if mt, _, _ := strings.Cut(ct, ";"); strings.EqualFold(strings.TrimSpace(mt), "text/html") {
		ct = "text/plain; charset=utf-8"
	}
	if mt, img := share.InlineImage(ct); img && !download {
		ct, b.Inline = mt, true
	}
	b.ContentType = ct
}

// Blob 打开 ref（空 = 基线分支）下文件 p 的内容：目录 → not_a_file；超过 64MiB → too_large。
// ifNoneMatch 与 ETag（"<blob oid>"）相同时不读内容，置 NotModified。Git LFS 指针返回对象内容：不超过 inlineLFSMax 的
// 对象经缓存（File），更大的只在 download 时从上游流式读取（Body），否则 too_large。download 时一律附件。
func (s *Service) Blob(ctx context.Context, projectID, ref, p, ifNoneMatch string, download bool) (*BlobContent, error) {
	if err := validRepoPath(p, false); err != nil {
		return nil, err
	}
	r, err := s.openRead(ctx, projectID)
	if err != nil {
		return nil, err
	}
	_, commit, err := s.resolveRef(ctx, r, ref)
	if err != nil {
		return nil, err
	}
	self, err := s.treeSelf(ctx, r.dir, commit, p)
	if err != nil {
		return nil, err
	}
	if self.Type != apigen.RepoEntryFile && self.Type != apigen.RepoEntrySymlink {
		return nil, errNotAFile
	}
	out := &BlobContent{OID: self.Oid, Name: self.Name, Size: self.Size, Immutable: ref != "" && ref == commit}
	if etagMatch(ifNoneMatch, `"`+self.Oid+`"`) {
		out.NotModified = true
		return out, nil
	}
	if self.Type == apigen.RepoEntryFile && self.Size > 0 && self.Size < lfs.PointerMax {
		res, err := s.gitRead(ctx, r.dir, lfs.PointerMax, nil, "cat-file", "blob", self.Oid)
		if err != nil {
			return nil, err
		}
		if ptr, ok := lfsPointer(self.Size, res.out); ok {
			if err := s.lfsBlob(ctx, r.prj, out, ptr, download); err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	if self.Size > s.maxBlobBytes {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: fmt.Sprintf("文件超过 %d MiB，不能在这里读取", s.maxBlobBytes>>20)}
	}
	ct := mime.TypeByExtension(path.Ext(self.Name))
	if ct == "" {
		head, err := s.gitRead(ctx, r.dir, 512, nil, "cat-file", "blob", self.Oid)
		if err != nil {
			return nil, err
		}
		ct = http.DetectContentType(head.out)
	}
	out.setType(ct, download)
	if out.Body, err = s.openGit(ctx, gitRun{dir: r.dir, read: true, timeout: blobTimeout}, "cat-file", "blob", self.Oid); err != nil {
		return nil, err
	}
	return out, nil
}

// lfsBlob 打开 LFS 指针 ptr 指向的对象：不超过 inlineLFSMax 的经缓存（File）；更大的只在 download 时给出——
// 镜像项目从上游流式读取，托管项目直接打开库里的对象（File）。
func (s *Service) lfsBlob(ctx context.Context, prj db.Project, out *BlobContent, ptr lfs.Pointer, download bool) error {
	out.Size = ptr.Size
	if ptr.Size > s.inlineLFSMax {
		if !download {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large",
				Message: fmt.Sprintf("Git LFS 对象超过 %d MiB，只能下载（download=1）", s.inlineLFSMax>>20)}
		}
		if !prj.Hosted {
			body, err := s.lfsOpenStream(ctx, prj, ptr)
			if err != nil {
				return err
			}
			out.Body = body
			out.setType(extType(out.Name), download)
			return nil
		}
	}
	f, err := s.lfsObject(ctx, prj, ptr)
	if err != nil {
		return err
	}
	ct := mime.TypeByExtension(path.Ext(out.Name))
	if ct == "" {
		head := make([]byte, 512)
		n, err := f.ReadAt(head, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			_ = f.Close()
			return err
		}
		ct = http.DetectContentType(head[:n])
	}
	out.File = f
	out.setType(ct, download)
	return nil
}

// etagMatch 按 If-None-Match 的规则（逗号分隔、弱比较、`*`）判断 etag 是否命中。
func etagMatch(header, etag string) bool {
	for v := range strings.SplitSeq(header, ",") {
		v = strings.TrimPrefix(strings.TrimSpace(v), "W/")
		if v == "*" || v == etag {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- commit ↔ thread

// threadsFor 查一页提交的关联线程：先看 repo_commit_threads（线程推送、经 PR 合并），未命中的再看线程里采样到的提交。
// fresh 是 24 小时内 GitHub 反查过、确定没有线程的提交。失败只记日志，返回已查到的部分。
func (s *Service) threadsFor(ctx context.Context, prj db.Project, shas []string) (links map[string]apigen.ProjectRepoThreadLink, noThread map[string]bool) {
	links, noThread = map[string]apigen.ProjectRepoThreadLink{}, map[string]bool{}
	if len(shas) == 0 {
		return links, noThread
	}
	rows, err := s.st.Q.ListRepoCommitThreads(ctx, db.ListRepoCommitThreadsParams{ProjectID: prj.ID, Shas: shas})
	if err != nil {
		slog.Warn("读取提交关联的线程", "project", prj.ID, "err", err)
		return links, noThread
	}
	since := s.cfg.Now().Add(-noThreadTTL).UnixMilli()
	for _, r := range rows {
		switch {
		case r.Source == "none":
			if r.LinkedAt > since {
				noThread[r.Sha] = true
			}
		case r.ThreadID != nil:
			links[r.Sha] = apigen.ProjectRepoThreadLink{ID: *r.ThreadID, Title: deref(r.Title), Source: apigen.ProjectRepoThreadLinkSource(r.Source)}
		}
	}
	var miss []string
	for _, sha := range shas {
		if _, ok := links[sha]; !ok {
			miss = append(miss, sha)
		}
	}
	if len(miss) == 0 {
		return links, noThread
	}
	sampled, err := s.st.Q.ListSampledCommitThreads(ctx, db.ListSampledCommitThreadsParams{ProjectID: prj.ID, Shas: miss})
	if err != nil {
		slog.Warn("读取线程采样到的提交", "project", prj.ID, "err", err)
		return links, noThread
	}
	for _, r := range sampled {
		if _, ok := links[r.Sha]; !ok {
			links[r.Sha] = apigen.ProjectRepoThreadLink{ID: r.ThreadID, Title: r.Title, Source: apigen.RepoThreadSampled}
		}
	}
	return links, noThread
}

// commitThread 查一个提交的关联线程；GitHub 项目在前两步都没有、且 24 小时内没有反查过时，再按「提交所属 PR → 线程」
// 反查一次（列表页从不反查，免得一页提交扇出成几十个 GitHub 请求）。
func (s *Service) commitThread(ctx context.Context, prj db.Project, sha string) (apigen.ProjectRepoThreadLink, bool) {
	links, noThread := s.threadsFor(ctx, prj, []string{sha})
	if l, ok := links[sha]; ok {
		return l, true
	}
	if prj.GithubRepoID == nil || noThread[sha] || s.gh == nil {
		return apigen.ProjectRepoThreadLink{}, false
	}
	gctx, cancel := context.WithTimeout(ctx, pullLookupWait)
	defer cancel()
	tid, ok, err := s.gh.CommitThread(gctx, prj.ID, sha)
	if err != nil {
		slog.Debug("按 PR 反查提交的线程", "project", prj.ID, "sha", sha, "err", err)
		return apigen.ProjectRepoThreadLink{}, false
	}
	now := s.cfg.Now().UnixMilli()
	if !ok {
		if err := s.st.Q.MarkRepoCommitNoThread(ctx, db.MarkRepoCommitNoThreadParams{ProjectID: prj.ID, Sha: sha, LinkedAt: now}); err != nil {
			slog.Warn("记录提交没有关联线程", "project", prj.ID, "err", err)
		}
		return apigen.ProjectRepoThreadLink{}, false
	}
	if err := s.st.Q.LinkRepoCommitThread(ctx, db.LinkRepoCommitThreadParams{
		ProjectID: prj.ID, Sha: sha, ThreadID: &tid, Source: "pull", LinkedAt: now,
	}); err != nil {
		slog.Warn("记录提交关联的线程", "project", prj.ID, "err", err)
	}
	t, err := s.st.Q.GetThread(ctx, tid)
	if err != nil {
		slog.Warn("读取线程", "thread", tid, "err", err)
		return apigen.ProjectRepoThreadLink{}, false
	}
	return apigen.ProjectRepoThreadLink{ID: tid, Title: t.Title, Source: apigen.RepoThreadPull}, true
}
