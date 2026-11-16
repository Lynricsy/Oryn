package egress

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 出站代理门禁（M15 步骤 5.4，R16 / R19 / R21，docs/plan-m15.md 的覆盖表）。
//
// 项目门禁（推送 / PR）与线程限制（只读评审、只发 COMMENT、实现需授权）来自 setup 时生成的 Policy.GitHub；
// 会在线程期间变化的状态（实现授权、是否发布任务、prepare 通过记录、head SHA、默认分支、Oryn 的 hook）
// 每次写请求经 GateLookup 向控制面现查，不进缓存的策略。门禁开启时一切无法确定的情况都拒绝。

// GateConfig 是项目的门禁配置（projects.github_gates）。Push、PR 都为假时等于没有门禁。
type GateConfig struct {
	Push bool // 推送门禁：每个 refs/heads/* 新 SHA 须有 push 级以上的 github_prepare 通过记录
	PR   bool // PR 门禁：head SHA 须有 pr 级通过记录，正文满足下面两条
	// PRHeadings 是 PR 正文必需的二级标题（不含 `## `）。
	PRHeadings []string
	// RequireClosingRef 要求 PR 正文含 `Fixes|Closes|Resolves #N`。
	RequireClosingRef bool
}

// 门禁结论（GitHubWrite.Gate 与审计的 gate 列）：经门禁检查后放行、跳过检查后放行；其余非空值是拒绝原因。
const (
	GatePass    = "pass"
	GateSkipped = "skipped"
)

// prepare 的门禁级别（同 orbdrt.PrepareLevelPush / PrepareLevelPR）。
const (
	levelPush = "push"
	levelPR   = "pr"
)

// defaultBranchTTL 是仓库默认分支在 Handler 里的缓存时长。
const defaultBranchTTL = 10 * time.Minute

// GateLookup 是门禁向控制面现查的状态，都以该 orb 自己的线程与身份槽位为准。查询失败时实现应返回
// 不放行的结果（false / error），门禁据此拒绝。
type GateLookup interface {
	// PrepareOK 报告该 orb 是否登记过 sha 在 level（push / pr）上的 github_prepare 通过记录；pr 级同时满足 push。
	PrepareOK(sha, level string) (ok, skipped bool)
	// ImplAuthorized 报告该 orb 线程的任务是否已获得实现授权（R16）。
	ImplAuthorized() bool
	// HeadSHA 用该 orb 自己槽位的令牌查询分支当前指向的提交。repo 是 `owner/repo`、数值仓库 id 或仓库 node id；
	// ref 是分支短名，或 `owner:branch`（owner 名下与 repo 同名的 fork）。
	HeadSHA(repo, ref string) (string, error)
	// DefaultBranch 用同一令牌查询仓库的默认分支短名；repo 的形式同 HeadSHA。
	DefaultBranch(repo string) (string, error)
	// IsOrynHook 报告 id 是否是 Oryn 管理的仓库 hook；repo（owner/repo）可空（/repositories/{id} 形态的路径）。
	IsOrynHook(repo string, id int64) bool
	// ReleaseThread 报告该 orb 的线程是否为进行中的发布任务（v4），取控制面的任务状态。
	ReleaseThread() bool
}

// NoGateLookup 是没有 GitHub 路由的 Resolver 用的 GateLookup：一律不放行（这类 Resolver 的策略里没有 GitHub 门禁，不会被调用）。
type NoGateLookup struct{}

func (NoGateLookup) PrepareOK(string, string) (bool, bool) { return false, false }
func (NoGateLookup) ImplAuthorized() bool                  { return false }
func (NoGateLookup) HeadSHA(string, string) (string, error) {
	return "", fmt.Errorf("egress: 没有 GitHub 门禁查询")
}
func (NoGateLookup) DefaultBranch(string) (string, error) {
	return "", fmt.Errorf("egress: 没有 GitHub 门禁查询")
}
func (NoGateLookup) IsOrynHook(string, int64) bool { return true }
func (NoGateLookup) ReleaseThread() bool           { return false }

// 拒绝原因（给 agent 看，代理回 `oryn: <原因>`）。
const (
	denyReviewOnly = "外部 PR 评审线程是只读工作区，不能推送、创建 PR、合并或修改 ref"
	denyUnparsed   = "门禁开启时不放行无法解析的 GitHub 写请求（请求体超过 1 MiB、带压缩编码，或参数无法静态求值）"
	denyRefUnknown = "推送门禁开启时不放行无法确定新 SHA 的改 ref 操作（contents、merges、update-branch、createCommitOnBranch 等）；请在本地提交，运行 github_prepare 后 git push"
	denyOrynHook   = "这是 Oryn 管理的仓库 hook，不能修改或删除"
	denyImplAuth   = "本任务尚未获得实现授权，不能创建 PR：需要分类为 bug 或 documentation，或维护者打上 accepted 标签、评论 go ahead"
)

// closingRefRe 匹配 PR 正文里的关闭引用（R19）。
var closingRefRe = regexp.MustCompile(`(?i)\b(?:fix(?:e[sd])?|close[sd]?|resolve[sd]?)\s+(?:[\w.-]+/[\w.-]+)?#\d+\b`)

// gateRun 是一个请求的门禁求值：同一请求内只查一次是否发布任务。
type gateRun struct {
	h       *Handler
	gh      *GitHubAccess
	release *bool
}

// active 报告门禁或线程限制是否开启：开启时无法解析的写请求一律拒绝（R19「失败即拒绝」）。
func (g *gateRun) active() bool {
	return g.pushGate() || g.prGate() || g.gh.ReviewOnly || g.gh.CommentOnlyReviews || g.gh.ImplAuth
}

func (g *gateRun) pushGate() bool { return g.gh.Gates != nil && g.gh.Gates.Push }
func (g *gateRun) prGate() bool   { return g.gh.Gates != nil && g.gh.Gates.PR }

func (g *gateRun) isRelease() bool {
	if g.release == nil {
		v := g.h.R.ReleaseThread()
		g.release = &v
	}
	return *g.release
}

// check 对一个写操作求门禁结论：denied 为真时 verdict 是拒绝原因，否则为 ""（不经门禁）、GatePass 或 GateSkipped。
// op 是 REST / GraphQL 写操作的识别结果（推送与无法识别的 GraphQL 为 nil）。
func (g *gateRun) check(w GitHubWrite, op *GitHubOp) (verdict string, denied bool) {
	if g.gh == nil {
		return "", false
	}
	if w.Method == MethodPush {
		return g.push(w)
	}
	// Oryn 的 hook 不论门禁开关始终受保护；路径级信息（请求体解析不了时）也带 hook id
	if op != nil && op.Category == CatHookWrite && op.HookID != 0 &&
		(w.Method == http.MethodPatch || w.Method == http.MethodDelete) && g.h.R.IsOrynHook(op.Repo, op.HookID) {
		return denyOrynHook, true
	}
	if w.Unparsed || op == nil {
		if g.active() {
			return denyUnparsed, true
		}
		return "", false
	}
	switch op.Category {
	case CatPRCreate:
		return g.prCreate(op)
	case CatRefUpdate:
		if g.gh.ReviewOnly {
			return denyReviewOnly, true
		}
		if !g.pushGate() {
			return "", false
		}
		ref := op.RefName
		if ref == "" {
			return "推送门禁开启时不放行无法确定 ref 名的改 ref 操作（如只带 refId 的 updateRef / deleteRef）", true
		}
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		if !strings.HasPrefix(ref, "refs/heads/") {
			return "", false
		}
		return g.branchUpdate(opRepo(op), ref, op.SHA)
	case CatRefUnknown:
		if g.gh.ReviewOnly {
			return denyReviewOnly, true
		}
		if g.pushGate() {
			return denyRefUnknown, true
		}
	case CatPRMerge:
		if g.gh.ReviewOnly {
			return denyReviewOnly, true
		}
	case CatReview:
		if g.gh.CommentOnlyReviews {
			if op.ReviewEvent != "" && op.ReviewEvent != "COMMENT" {
				return "本线程只能提交 COMMENT 类型的 review，不能 " + op.ReviewEvent, true
			}
			return GatePass, false
		}
	}
	return "", false
}

// prCreate：ReviewOnly 拒绝；ImplAuth 时须已获授权；PR 门禁开启时 head SHA 须有 pr 级通过记录、正文满足项目规则。
func (g *gateRun) prCreate(op *GitHubOp) (string, bool) {
	if g.gh.ReviewOnly {
		return denyReviewOnly, true
	}
	verdict := ""
	if g.gh.ImplAuth {
		if !g.h.R.ImplAuthorized() {
			return denyImplAuth, true
		}
		verdict = GatePass
	}
	if !g.prGate() {
		return verdict, false
	}
	repo, ref, ok := prHead(op)
	if !ok {
		return "PR 门禁开启时须能确定 PR 的 head 分支与仓库", true
	}
	sha, err := g.h.R.HeadSHA(repo, ref)
	if err != nil || !isOID(sha) {
		return fmt.Sprintf("PR 门禁：查询 head %s 的提交失败（%v）", ref, err), true
	}
	ok, skipped := g.h.R.PrepareOK(sha, levelPR)
	if !ok {
		return fmt.Sprintf("PR 门禁：head %s 的 %s 没有 pr 级 github_prepare 通过记录；先运行 github_prepare level=pr", ref, short(sha)), true
	}
	if msg := prBodyProblem(op.Body, g.gh.Gates); msg != "" {
		return "PR 门禁：" + msg, true
	}
	if skipped {
		return GateSkipped, false
	}
	return GatePass, false
}

// push 检查一次 git-receive-pack（推送门禁 + ReviewOnly）。只检查 refs/heads/*；空命令段放行。
func (g *gateRun) push(w GitHubWrite) (string, bool) {
	if w.Unparsed {
		if g.active() {
			return denyUnparsed, true
		}
		return "", false
	}
	if len(w.Refs) == 0 {
		return "", false
	}
	if g.gh.ReviewOnly {
		return denyReviewOnly, true
	}
	if !g.pushGate() {
		return "", false
	}
	verdict := ""
	for _, u := range w.Refs {
		if !strings.HasPrefix(u.Ref, "refs/heads/") {
			continue
		}
		v, denied := g.branchUpdate(w.Repo, u.Ref, u.New)
		if denied {
			return v, true
		}
		verdict = mergeVerdict(verdict, v)
	}
	return verdict, false
}

// branchUpdate 检查推送门禁下对一个分支的更新：非发布线程不能改默认分支（v4：orbd 的默认分支检查在 orb 内，不可信）；
// 删除分支（新 SHA 全零）不需要 prepare，其余新 SHA 须有 push 级以上通过记录。
func (g *gateRun) branchUpdate(repo, ref, sha string) (string, bool) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if !g.isRelease() {
		def, err := g.h.defaultBranch(repo)
		if err != nil {
			return fmt.Sprintf("推送门禁：查询 %s 的默认分支失败（%v）", repo, err), true
		}
		if branch == def {
			return "推送门禁：非发布任务不能推送默认分支 " + def + "；请推到新分支并开 PR", true
		}
	}
	if strings.Trim(sha, "0") == "" {
		return GatePass, false
	}
	ok, skipped := g.h.R.PrepareOK(strings.ToLower(sha), levelPush)
	if !ok {
		return fmt.Sprintf("推送门禁：%s 的 %s 没有 github_prepare 通过记录；先运行 github_prepare", branch, short(sha)), true
	}
	if skipped {
		return GateSkipped, false
	}
	return GatePass, false
}

// mergeVerdict 合并多个 ref 的结论：跳过检查优先于放行。
func mergeVerdict(a, b string) string {
	if a == GateSkipped || b == GateSkipped {
		return GateSkipped
	}
	if a == GatePass || b == GatePass {
		return GatePass
	}
	return ""
}

// opRepo 是写操作的目标仓库（HeadSHA / DefaultBranch 的 repo 形式）：owner/repo、数值 id 或 GraphQL 的 repositoryId。
func opRepo(op *GitHubOp) string {
	switch {
	case op.Repo != "":
		return op.Repo
	case op.RepoID > 0:
		return strconv.FormatInt(op.RepoID, 10)
	}
	return op.RepositoryID
}

// prHead 返回 HeadSHA 的参数：GraphQL 有 headRepositoryId 时取它与分支名；REST 的 head_repo 是 head 仓库名
// （owner 取 head 的 `owner:` 前缀，否则取 base 仓库的 owner）；其余按 base 仓库加原样的 head（可带 `owner:`）。
func prHead(op *GitHubOp) (repo, ref string, ok bool) {
	head := op.HeadRef
	if head == "" {
		return "", "", false
	}
	owner, branch, fork := strings.Cut(head, ":")
	if !fork {
		owner, branch = "", head
	}
	if branch == "" || (fork && owner == "") {
		return "", "", false
	}
	switch {
	case op.HeadRepoID != "":
		return op.HeadRepoID, branch, true
	case op.HeadRepo != "":
		if strings.Contains(op.HeadRepo, "/") {
			return op.HeadRepo, branch, true
		}
		if owner == "" {
			o, _, found := strings.Cut(op.Repo, "/")
			if !found {
				return "", "", false
			}
			owner = o
		}
		return owner + "/" + op.HeadRepo, branch, true
	}
	base := opRepo(op)
	if base == "" {
		return "", "", false
	}
	return base, head, true
}

// prBodyProblem 按项目规则检查 PR 正文；满足时返回空串。
func prBodyProblem(body string, g *GateConfig) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for _, want := range g.PRHeadings {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		found := false
		for _, l := range lines {
			if h, ok := strings.CutPrefix(strings.TrimSpace(l), "## "); ok && strings.EqualFold(strings.TrimSpace(h), want) {
				found = true
				break
			}
		}
		if !found {
			return "PR 正文缺少二级标题「## " + want + "」"
		}
	}
	if g.RequireClosingRef && !closingRefRe.MatchString(body) {
		return "PR 正文须含 `Fixes #N`、`Closes #N` 或 `Resolves #N`"
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// branchCache 缓存 orb 访问过的仓库默认分支（键为 GateLookup 的 repo 参数，小写）。
type branchCache struct {
	mu sync.Mutex
	m  map[string]branchEntry
}

type branchEntry struct {
	name string
	at   time.Time
}

// defaultBranch 返回仓库的默认分支，带 defaultBranchTTL 的缓存；查询失败不缓存。
func (h *Handler) defaultBranch(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("仓库未知")
	}
	key := strings.ToLower(repo)
	h.branches.mu.Lock()
	e, ok := h.branches.m[key]
	h.branches.mu.Unlock()
	if ok && time.Since(e.at) < defaultBranchTTL {
		return e.name, nil
	}
	name, err := h.R.DefaultBranch(repo)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("默认分支为空")
	}
	h.branches.mu.Lock()
	if h.branches.m == nil {
		h.branches.m = map[string]branchEntry{}
	}
	h.branches.m[key] = branchEntry{name: name, at: time.Now()}
	h.branches.mu.Unlock()
	return name, nil
}
