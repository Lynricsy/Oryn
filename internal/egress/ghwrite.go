package egress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// 写操作类别。门禁（步骤 5.4）与防回流预登记（步骤 2.3）都按类别处理。
const (
	CatCommentCreate = "comment.create" // issue/PR 会话评论、提交评论
	CatCommentUpdate = "comment.update"
	CatCommentDelete = "comment.delete"
	CatIssueCreate   = "issue.create"
	CatIssueUpdate   = "issue.update" // 关闭/重开/编辑/标签/指派/子 issue
	CatPRCreate      = "pr.create"
	CatPRUpdate      = "pr.update"      // 编辑/关闭/重开/ready/draft/请求评审/自动合并关闭
	CatPRMerge       = "pr.merge"       // 合并、开启自动合并、入合并队列
	CatReview        = "review.submit"  // 创建或提交 review（带 event）
	CatReviewComment = "review.comment" // 行内评审评论
	CatRefUpdate     = "ref.update"     // SHA 可知的改 ref
	CatRefUnknown    = "ref.unknown"    // 改 ref 但 SHA 不可知（contents、merges、createCommitOnBranch…）
	CatHookWrite     = "hook.write"     // 仓库 hook 的非 GET
	CatReaction      = "reaction"
	CatOther         = "other" // 其它写操作（审计，不参与门禁）
)

// zeroSHA 是删除 ref 时的新 SHA（与 git 的约定一致）。
const zeroSHA = "0000000000000000000000000000000000000000"

// ErrUnparsable 表示请求体或路径无法静态解析；门禁开启时调用方据此拒绝。
var ErrUnparsable = errors.New("egress: 无法解析的 GitHub 写请求")

// GitHubOp 是一次 GitHub 写操作的识别结果。
type GitHubOp struct {
	Category string
	Op       string // REST: "POST /repos/{owner}/{repo}/issues/{number}/comments" 形式的模板; GraphQL: mutation 字段名（非别名）
	Repo     string // "owner/repo"；REST /repos/{o}/{r} 形态可得，否则空
	RepoID   int64  // REST /repositories/{id} 形态
	Number   int64  // 路径里的 issue/PR 编号
	HookID   int64  // CatHookWrite
	Target   string // 防回流预登记目标：REST = 规范化路径；GraphQL = input 中的 subjectId/pullRequestId/issueId/labelableId/assignableId/repositoryId/id 第一个非空者
	Body     string // 正文原文（body 字段），没有则空
	// 门禁所需
	HeadRef, BaseRef string // pr.create：REST head/base；GraphQL headRefName/baseRefName
	HeadRepoID       string // GraphQL headRepositoryId
	RepositoryID     string // GraphQL repositoryId
	ReviewEvent      string // review.submit：大写 APPROVE/REQUEST_CHANGES/COMMENT；空 = 待提交
	SHA              string // ref.update：新 SHA（REST git/refs 的 sha；GraphQL updateRef/createRef 的 oid）
	RefName          string
	Merge            bool // 该操作是合并类（便于 ReviewOnly 判断）
}

// GitHubRequest 是一个发往 api.github.com 的请求的识别结果。
type GitHubRequest struct {
	Mutation bool       // false = GET 或 GraphQL query（不审计、不过门禁）
	Ops      []GitHubOp // GraphQL 一个 mutation 文档可含多个字段
}

// ParseGitHubRequest 识别发往 api.github.com 的请求。path 是上游路径（不含 /gh/<host> 前缀），rawPath 为原始转义形式（可空）。
// 解析不了的 JSON / GraphQL 返回 ErrUnparsable（调用方在门禁开启时据此拒绝）。
func ParseGitHubRequest(method, path, rawPath string, body []byte) (GitHubRequest, error) {
	method = strings.ToUpper(method)
	if method == http.MethodGet || method == http.MethodHead {
		return GitHubRequest{}, nil
	}
	segs, err := ghSplitPath(path, rawPath)
	if err != nil {
		return GitHubRequest{}, err
	}
	if method == http.MethodPost && len(segs) == 1 && strings.EqualFold(ghTrimFormat(segs[0]), "graphql") {
		return parseGraphQL(body)
	}
	op, err := parseREST(method, segs, body)
	if err != nil {
		return GitHubRequest{}, err
	}
	return GitHubRequest{Mutation: true, Ops: []GitHubOp{op}}, nil
}

// ghSplitPath 解码路径并按 / 切段：去掉空段（含结尾斜杠），`.`/`..` 段无法确定上游语义，视为无法解析。
func ghSplitPath(p, rawPath string) ([]string, error) {
	if rawPath != "" {
		d, err := url.PathUnescape(rawPath)
		if err != nil {
			return nil, ErrUnparsable
		}
		p = d
	}
	var segs []string
	for s := range strings.SplitSeq(p, "/") {
		switch s {
		case "":
			continue
		case ".", "..":
			return nil, ErrUnparsable
		}
		segs = append(segs, s)
	}
	return segs, nil
}

// ghTrimFormat 去掉 Rails 风格的 ".json" 格式后缀，避免 `merge.json` 之类的写法绕开识别。
func ghTrimFormat(s string) string {
	if len(s) > 5 && strings.EqualFold(s[len(s)-5:], ".json") {
		return s[:len(s)-5]
	}
	return s
}

// ---- REST ----

// restRoute 是一条 REST 写路由。pattern 相对 /repos/{owner}/{repo} 或 /repositories/{repository_id}；
// `{x}` 匹配任意一段，`{x*}` 匹配剩余的一段或多段（只能在末尾）。
type restRoute struct {
	methods []string
	raw     string
	pattern []string
	cat     string
	merge   bool
	fill    func(op *GitHubOp, obj ghObject, params map[string]string) error
}

func rr(methods, pattern, cat string, fill func(*GitHubOp, ghObject, map[string]string) error) restRoute {
	return restRoute{
		methods: strings.Split(methods, "|"),
		raw:     pattern,
		pattern: strings.Split(pattern, "/"),
		cat:     cat,
		merge:   cat == CatPRMerge,
		fill:    fill,
	}
}

// restRoutes 按顺序匹配，先到先得；字面段更具体的路由排在前面。
var restRoutes = []restRoute{
	// issue 与会话评论
	rr("POST", "issues", CatIssueCreate, nil),
	rr("PATCH", "issues/comments/{comment_id}", CatCommentUpdate, nil),
	rr("DELETE", "issues/comments/{comment_id}", CatCommentDelete, nil),
	rr("POST", "issues/comments/{comment_id}/reactions", CatReaction, nil),
	rr("DELETE", "issues/comments/{comment_id}/reactions/{reaction_id}", CatReaction, nil),
	rr("PATCH", "issues/{number}", CatIssueUpdate, nil),
	rr("POST", "issues/{number}/comments", CatCommentCreate, nil),
	rr("POST|PUT|DELETE", "issues/{number}/labels", CatIssueUpdate, nil),
	rr("DELETE", "issues/{number}/labels/{name}", CatIssueUpdate, nil),
	rr("POST|DELETE", "issues/{number}/assignees", CatIssueUpdate, nil),
	rr("PUT|DELETE", "issues/{number}/lock", CatIssueUpdate, nil),
	rr("POST", "issues/{number}/sub_issues", CatIssueUpdate, nil),
	rr("DELETE", "issues/{number}/sub_issue", CatIssueUpdate, nil),
	rr("PATCH", "issues/{number}/sub_issues/priority", CatIssueUpdate, nil),
	rr("POST", "issues/{number}/dependencies/blocked_by", CatIssueUpdate, nil),
	rr("DELETE", "issues/{number}/dependencies/blocked_by/{issue_id}", CatIssueUpdate, nil),
	rr("POST", "issues/{number}/reactions", CatReaction, nil),
	rr("DELETE", "issues/{number}/reactions/{reaction_id}", CatReaction, nil),

	// PR 与评审
	rr("PATCH", "pulls/comments/{comment_id}", CatCommentUpdate, nil),
	rr("DELETE", "pulls/comments/{comment_id}", CatCommentDelete, nil),
	rr("POST", "pulls/comments/{comment_id}/reactions", CatReaction, nil),
	rr("DELETE", "pulls/comments/{comment_id}/reactions/{reaction_id}", CatReaction, nil),
	rr("POST", "pulls", CatPRCreate, fillPRCreate),
	rr("PATCH", "pulls/{number}", CatPRUpdate, nil),
	rr("PUT", "pulls/{number}/merge", CatPRMerge, nil),
	rr("PUT", "pulls/{number}/update-branch", CatRefUnknown, nil),
	rr("POST|DELETE", "pulls/{number}/requested_reviewers", CatPRUpdate, nil),
	rr("POST", "pulls/{number}/reviews", CatReview, fillReview(false)),
	rr("PUT", "pulls/{number}/reviews/{review_id}", CatCommentUpdate, nil),
	rr("DELETE", "pulls/{number}/reviews/{review_id}", CatCommentDelete, nil),
	rr("POST", "pulls/{number}/reviews/{review_id}/events", CatReview, fillReview(true)),
	rr("PUT", "pulls/{number}/reviews/{review_id}/dismissals", CatPRUpdate, nil),
	rr("POST", "pulls/{number}/comments", CatReviewComment, nil),
	rr("POST", "pulls/{number}/comments/{comment_id}/replies", CatReviewComment, nil),

	// 提交评论
	rr("POST", "commits/{commit_sha}/comments", CatCommentCreate, nil),
	rr("PATCH", "comments/{comment_id}", CatCommentUpdate, nil),
	rr("DELETE", "comments/{comment_id}", CatCommentDelete, nil),
	rr("POST", "comments/{comment_id}/reactions", CatReaction, nil),
	rr("DELETE", "comments/{comment_id}/reactions/{reaction_id}", CatReaction, nil),

	// ref
	rr("POST", "git/refs", CatRefUpdate, fillRefCreate),
	rr("PATCH", "git/refs/{ref*}", CatRefUpdate, fillRefPatch),
	rr("DELETE", "git/refs/{ref*}", CatRefUpdate, fillRefDelete),
	rr("PUT|DELETE", "contents/{path*}", CatRefUnknown, fillRefField("branch")),
	rr("POST", "merges", CatRefUnknown, fillMergeRef("base")),
	rr("POST", "merge-upstream", CatRefUnknown, fillMergeRef("branch")),
	rr("POST", "branches/{branch}/rename", CatRefUnknown, fillBranchRename),

	// hook（其余 hooks/* 写操作见 parseREST 的兜底）
	rr("POST", "hooks", CatHookWrite, nil),
	rr("PATCH|DELETE", "hooks/{hook_id}", CatHookWrite, nil),
	rr("PATCH", "hooks/{hook_id}/config", CatHookWrite, nil),
	rr("POST", "hooks/{hook_id}/pings", CatHookWrite, nil),
	rr("POST", "hooks/{hook_id}/tests", CatHookWrite, nil),
	rr("POST", "hooks/{hook_id}/deliveries/{delivery_id}/attempts", CatHookWrite, nil),

	// 仅审计
	rr("POST", "forks", CatOther, nil),
	rr("POST", "releases", CatOther, nil),
	rr("PATCH|DELETE", "releases/{release_id}", CatOther, nil),
	rr("POST", "releases/{release_id}/reactions", CatReaction, nil),
	rr("DELETE", "releases/{release_id}/reactions/{reaction_id}", CatReaction, nil),
}

// match 返回规范化后的段（字面段取模板写法）与占位符取值。
func (rt *restRoute) match(method string, rest []string) ([]string, map[string]string, bool) {
	if !slices.Contains(rt.methods, method) {
		return nil, nil, false
	}
	var canon []string
	params := map[string]string{}
	for i, ps := range rt.pattern {
		if strings.HasPrefix(ps, "{") && strings.HasSuffix(ps, "*}") {
			if i >= len(rest) {
				return nil, nil, false
			}
			params[ps[1:len(ps)-2]] = strings.Join(rest[i:], "/")
			return append(canon, rest[i:]...), params, true
		}
		if i >= len(rest) {
			return nil, nil, false
		}
		seg := rest[i]
		if i == len(rest)-1 {
			seg = ghTrimFormat(seg)
		}
		if strings.HasPrefix(ps, "{") {
			params[ps[1:len(ps)-1]] = seg
			canon = append(canon, seg)
			continue
		}
		if !strings.EqualFold(seg, ps) {
			return nil, nil, false
		}
		canon = append(canon, ps)
	}
	if len(rest) != len(rt.pattern) {
		return nil, nil, false
	}
	return canon, params, true
}

func parseREST(method string, segs []string, body []byte) (GitHubOp, error) {
	op := GitHubOp{Category: CatOther}
	var prefix, tmpl string
	var rest []string
	switch {
	case len(segs) >= 3 && strings.EqualFold(segs[0], "repos"):
		op.Repo = strings.ToLower(segs[1]) + "/" + strings.ToLower(ghTrimFormat(segs[2]))
		prefix, tmpl, rest = "/repos/"+op.Repo, "/repos/{owner}/{repo}", segs[3:]
	case len(segs) >= 2 && strings.EqualFold(segs[0], "repositories"):
		id := ghTrimFormat(segs[1])
		op.RepoID, _ = ghDigits(id)
		prefix, tmpl, rest = "/repositories/"+id, "/repositories/{repository_id}", segs[2:]
	default:
		p := "/" + strings.Join(segs, "/")
		op.Op, op.Target = method+" "+p, p
		obj, _ := decodeGHObject(body)
		op.Body, _, _ = obj.str("body")
		return op, nil
	}

	var params map[string]string
	var rt *restRoute
	for i := range restRoutes {
		canon, ps, ok := restRoutes[i].match(method, rest)
		if !ok {
			continue
		}
		rt, params = &restRoutes[i], ps
		op.Category, op.Merge = rt.cat, rt.merge
		op.Op = method + " " + tmpl + "/" + rt.raw
		op.Target = prefix + "/" + strings.Join(canon, "/")
		break
	}
	if rt == nil {
		op.Op = method + " " + tmpl
		op.Target = prefix
		if len(rest) > 0 {
			op.Op += "/" + strings.Join(rest, "/")
			op.Target += "/" + strings.Join(rest, "/")
		}
		if len(rest) > 0 && strings.EqualFold(ghTrimFormat(rest[0]), "hooks") {
			op.Category = CatHookWrite
			if len(rest) > 1 {
				params = map[string]string{"hook_id": ghTrimFormat(rest[1])}
			}
		}
	}
	if n, ok := params["number"]; ok {
		op.Number, _ = ghDigits(n)
	}
	if h, ok := params["hook_id"]; ok {
		// hook id 决定是否命中 Oryn 自己的 hook，取不到确定数值时不能放行
		id, ok := ghDigits(h)
		if !ok {
			return GitHubOp{}, ErrUnparsable
		}
		op.HookID = id
	}

	obj, err := decodeGHObject(body)
	if err != nil {
		if op.Category != CatOther {
			return GitHubOp{}, ErrUnparsable
		}
		obj = nil
	}
	op.Body, _, _ = obj.str("body")
	if rt != nil && rt.fill != nil {
		if err := rt.fill(&op, obj, params); err != nil {
			return GitHubOp{}, ErrUnparsable
		}
	}
	return op, nil
}

// ghDigits 只接受纯十进制数字。
func ghDigits(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func fillPRCreate(op *GitHubOp, obj ghObject, _ map[string]string) (err error) {
	if op.HeadRef, err = obj.require("head"); err != nil {
		return err
	}
	op.BaseRef, err = obj.require("base")
	return err
}

func fillReview(required bool) func(*GitHubOp, ghObject, map[string]string) error {
	return func(op *GitHubOp, obj ghObject, _ map[string]string) error {
		ev, ok, err := obj.str("event")
		if err != nil || (required && (!ok || ev == "")) {
			return ErrUnparsable
		}
		op.ReviewEvent = strings.ToUpper(ev)
		return nil
	}
}

func fillRefCreate(op *GitHubOp, obj ghObject, _ map[string]string) (err error) {
	if op.RefName, err = obj.require("ref"); err != nil {
		return err
	}
	op.SHA, err = obj.require("sha")
	return err
}

func fillRefPatch(op *GitHubOp, obj ghObject, params map[string]string) (err error) {
	op.RefName = qualifyRef(params["ref"])
	op.SHA, err = obj.require("sha")
	return err
}

func fillRefDelete(op *GitHubOp, _ ghObject, params map[string]string) error {
	op.RefName, op.SHA = qualifyRef(params["ref"]), zeroSHA
	return nil
}

func fillRefField(key string) func(*GitHubOp, ghObject, map[string]string) error {
	return func(op *GitHubOp, obj ghObject, _ map[string]string) error {
		op.RefName, _, _ = obj.str(key)
		return nil
	}
}

// fillMergeRef 用于 merges / merge-upstream：SHA 不可知，标记为合并类。
func fillMergeRef(key string) func(*GitHubOp, ghObject, map[string]string) error {
	return func(op *GitHubOp, obj ghObject, _ map[string]string) error {
		op.RefName, _, _ = obj.str(key)
		op.Merge = true
		return nil
	}
}

func fillBranchRename(op *GitHubOp, _ ghObject, params map[string]string) error {
	op.RefName = "refs/heads/" + params["branch"]
	return nil
}

// qualifyRef 把 git/refs/{ref} 路径里的 "heads/x" 补成 "refs/heads/x"。
func qualifyRef(r string) string {
	if strings.HasPrefix(r, "refs/") {
		return r
	}
	return "refs/" + r
}

// ghObject 是 JSON 对象的顶层字段（未展开）。nil 表示没有请求体或请求体不是对象。
type ghObject map[string]json.RawMessage

// decodeGHObject 解析请求体顶层对象；重复的顶层键视为无法解析（避免与上游解析器取值不一致）。
// 空体与合法的非对象 JSON 返回 nil。
func decodeGHObject(b []byte) (ghObject, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, nil
	}
	if b[0] != '{' {
		if !json.Valid(b) {
			return nil, ErrUnparsable
		}
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return nil, ErrUnparsable
	}
	obj := ghObject{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, ErrUnparsable
		}
		k, ok := t.(string)
		if !ok {
			return nil, ErrUnparsable
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, ErrUnparsable
		}
		if _, dup := obj[k]; dup {
			return nil, ErrUnparsable
		}
		obj[k] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, ErrUnparsable
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrUnparsable
	}
	return obj, nil
}

// str 取字符串字段：缺省或 null 返回 ok=false；类型不符返回 ErrUnparsable。
func (o ghObject) str(key string) (string, bool, error) {
	raw, ok := o[key]
	if !ok || string(raw) == "null" {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, ErrUnparsable
	}
	return s, true, nil
}

// require 取必填的非空字符串字段。
func (o ghObject) require(key string) (string, error) {
	s, ok, err := o.str(key)
	if err != nil || !ok || s == "" {
		return "", ErrUnparsable
	}
	return s, nil
}

// ---- GraphQL ----

// gqlCategories 把 mutation 字段名映射到类别；未列出的归入 CatOther。
var gqlCategories = map[string]string{
	"createPullRequest":               CatPRCreate,
	"revertPullRequest":               CatPRCreate, // 新建回滚分支与 PR，按创建 PR 处理
	"updatePullRequest":               CatPRUpdate,
	"closePullRequest":                CatPRUpdate,
	"reopenPullRequest":               CatPRUpdate,
	"markPullRequestReadyForReview":   CatPRUpdate,
	"convertPullRequestToDraft":       CatPRUpdate,
	"requestReviews":                  CatPRUpdate,
	"requestReviewsByLogin":           CatPRUpdate,
	"disablePullRequestAutoMerge":     CatPRUpdate,
	"dismissPullRequestReview":        CatPRUpdate,
	"mergePullRequest":                CatPRMerge,
	"enablePullRequestAutoMerge":      CatPRMerge,
	"enqueuePullRequest":              CatPRMerge,
	"updatePullRequestBranch":         CatRefUnknown,
	"addPullRequestReview":            CatReview,
	"submitPullRequestReview":         CatReview,
	"updatePullRequestReview":         CatCommentUpdate,
	"deletePullRequestReview":         CatCommentDelete,
	"addPullRequestReviewComment":     CatReviewComment,
	"addPullRequestReviewThread":      CatReviewComment,
	"addPullRequestReviewThreadReply": CatReviewComment,
	"updatePullRequestReviewComment":  CatCommentUpdate,
	"deletePullRequestReviewComment":  CatCommentDelete,
	"addComment":                      CatCommentCreate,
	"updateIssueComment":              CatCommentUpdate,
	"deleteIssueComment":              CatCommentDelete,
	"createIssue":                     CatIssueCreate,
	"updateIssue":                     CatIssueUpdate,
	"closeIssue":                      CatIssueUpdate,
	"reopenIssue":                     CatIssueUpdate,
	"addLabelsToLabelable":            CatIssueUpdate,
	"removeLabelsFromLabelable":       CatIssueUpdate,
	"clearLabelsFromLabelable":        CatIssueUpdate,
	"replaceActorsForAssignable":      CatIssueUpdate,
	"addAssigneesToAssignable":        CatIssueUpdate,
	"removeAssigneesFromAssignable":   CatIssueUpdate,
	"addSubIssue":                     CatIssueUpdate,
	"removeSubIssue":                  CatIssueUpdate,
	"reprioritizeSubIssue":            CatIssueUpdate,
	"addBlockedBy":                    CatIssueUpdate,
	"removeBlockedBy":                 CatIssueUpdate,
	"updateIssueIssueType":            CatIssueUpdate,
	"lockLockable":                    CatIssueUpdate,
	"unlockLockable":                  CatIssueUpdate,
	"createRef":                       CatRefUpdate,
	"updateRef":                       CatRefUpdate,
	"updateRefs":                      CatRefUpdate,
	"deleteRef":                       CatRefUpdate,
	"createCommitOnBranch":            CatRefUnknown,
	"mergeBranch":                     CatRefUnknown,
	"addReaction":                     CatReaction,
	"removeReaction":                  CatReaction,
}

// gqlTargetKeys 是 GraphQL 预登记目标的取值顺序。
var gqlTargetKeys = []string{"subjectId", "pullRequestId", "issueId", "labelableId", "assignableId", "repositoryId", "id"}

// gqlUnresolved 标记引用了未提供且无默认值的变量的位置。
type gqlUnresolved struct{}

func parseGraphQL(body []byte) (GitHubRequest, error) {
	obj, err := decodeGHObject(body)
	if err != nil || obj == nil {
		return GitHubRequest{}, ErrUnparsable
	}
	query, err := obj.require("query")
	if err != nil {
		return GitHubRequest{}, err
	}
	opName, _, err := obj.str("operationName")
	if err != nil {
		return GitHubRequest{}, err
	}
	vars, err := gqlVariables(obj["variables"])
	if err != nil {
		return GitHubRequest{}, err
	}
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return GitHubRequest{}, ErrUnparsable
	}
	op, err := gqlSelectOperation(doc, opName)
	if err != nil {
		return GitHubRequest{}, err
	}
	if op.Operation != ast.Mutation {
		return GitHubRequest{}, nil
	}
	seen := map[string]bool{}
	for _, f := range doc.Fragments {
		if seen[f.Name] {
			return GitHubRequest{}, ErrUnparsable
		}
		seen[f.Name] = true
	}
	var fields []*ast.Field
	st := gqlFragState{active: map[string]bool{}, done: map[string]bool{}}
	if err := gqlCollect(doc, op.SelectionSet, &st, &fields); err != nil {
		return GitHubRequest{}, err
	}
	defaults := map[string]*ast.Value{}
	for _, d := range op.VariableDefinitions {
		if d.DefaultValue != nil {
			defaults[d.Variable] = d.DefaultValue
		}
	}
	req := GitHubRequest{Mutation: true}
	for _, f := range fields {
		if f.Name == "__typename" {
			continue
		}
		ops, err := gqlMutationOps(f, vars, defaults)
		if err != nil {
			return GitHubRequest{}, err
		}
		req.Ops = append(req.Ops, ops...)
	}
	return req, nil
}

// gqlVariables 解析 variables：缺省或 null 为空；也接受 JSON 编码后的字符串形式。
func gqlVariables(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, ErrUnparsable
		}
		return gqlVariables(json.RawMessage(s))
	}
	if raw[0] != '{' {
		return nil, ErrUnparsable
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var vars map[string]any
	if err := dec.Decode(&vars); err != nil {
		return nil, ErrUnparsable
	}
	return vars, nil
}

// gqlSelectOperation 按 operationName 选取 operation；无名时文档必须只有一个 operation。
func gqlSelectOperation(doc *ast.QueryDocument, name string) (*ast.OperationDefinition, error) {
	var found *ast.OperationDefinition
	if name == "" {
		if len(doc.Operations) != 1 {
			return nil, ErrUnparsable
		}
		return doc.Operations[0], nil
	}
	for _, op := range doc.Operations {
		if op.Name != name {
			continue
		}
		if found != nil {
			return nil, ErrUnparsable
		}
		found = op
	}
	if found == nil {
		return nil, ErrUnparsable
	}
	return found, nil
}

type gqlFragState struct {
	active map[string]bool // 当前展开链上的 fragment，用于发现循环
	done   map[string]bool // 已展开过的 fragment：同一 fragment 在根上重复展开得到相同字段，会被 GraphQL 合并，跳过即可
}

// gqlCollect 展开 fragment spread 与 inline fragment，收集根层字段。指令（@skip/@include）一律忽略，从严处理。
func gqlCollect(doc *ast.QueryDocument, set ast.SelectionSet, st *gqlFragState, out *[]*ast.Field) error {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			*out = append(*out, s)
		case *ast.InlineFragment:
			if err := gqlCollect(doc, s.SelectionSet, st, out); err != nil {
				return err
			}
		case *ast.FragmentSpread:
			if st.active[s.Name] {
				return ErrUnparsable
			}
			if st.done[s.Name] {
				continue
			}
			def := doc.Fragments.ForName(s.Name)
			if def == nil {
				return ErrUnparsable
			}
			st.active[s.Name] = true
			if err := gqlCollect(doc, def.SelectionSet, st, out); err != nil {
				return err
			}
			st.active[s.Name] = false
			st.done[s.Name] = true
		default:
			return ErrUnparsable
		}
	}
	return nil
}

// gqlResolve 把 AST 值代入变量求值；引用缺失变量的位置得到 gqlUnresolved。
func gqlResolve(v *ast.Value, vars map[string]any, defaults map[string]*ast.Value) (any, error) {
	switch v.Kind {
	case ast.Variable:
		if val, ok := vars[v.Raw]; ok {
			return val, nil
		}
		if d, ok := defaults[v.Raw]; ok {
			return gqlResolve(d, nil, nil)
		}
		return gqlUnresolved{}, nil
	case ast.StringValue, ast.BlockValue, ast.EnumValue:
		return v.Raw, nil
	case ast.IntValue, ast.FloatValue:
		return json.Number(v.Raw), nil
	case ast.BooleanValue:
		return v.Raw == "true", nil
	case ast.NullValue:
		return nil, nil
	case ast.ListValue:
		list := make([]any, 0, len(v.Children))
		for _, c := range v.Children {
			x, err := gqlResolve(c.Value, vars, defaults)
			if err != nil {
				return nil, err
			}
			list = append(list, x)
		}
		return list, nil
	case ast.ObjectValue:
		m := make(map[string]any, len(v.Children))
		for _, c := range v.Children {
			if _, dup := m[c.Name]; dup {
				return nil, ErrUnparsable
			}
			x, err := gqlResolve(c.Value, vars, defaults)
			if err != nil {
				return nil, err
			}
			m[c.Name] = x
		}
		return m, nil
	}
	return nil, ErrUnparsable
}

// gqlStr 取 input 对象里的字符串字段：缺省或 null 返回 ok=false；变量缺失或类型不符返回 ErrUnparsable。
func gqlStr(in any, key string) (string, bool, error) {
	if in == nil {
		return "", false, nil
	}
	m, ok := in.(map[string]any)
	if !ok {
		return "", false, ErrUnparsable
	}
	switch x := m[key].(type) {
	case nil:
		return "", false, nil
	case string:
		return x, true, nil
	default:
		return "", false, ErrUnparsable
	}
}

// gqlRequire 取必填的非空字符串字段。
func gqlRequire(in any, key string) (string, error) {
	s, ok, err := gqlStr(in, key)
	if err != nil || !ok || s == "" {
		return "", ErrUnparsable
	}
	return s, nil
}

// gqlLenient 取审计/预登记用的字段，取不到时留空。
func gqlLenient(in any, key string) string {
	s, _, _ := gqlStr(in, key)
	return s
}

func gqlMutationOps(f *ast.Field, vars map[string]any, defaults map[string]*ast.Value) ([]GitHubOp, error) {
	cat, ok := gqlCategories[f.Name]
	if !ok {
		cat = CatOther
	}
	op := GitHubOp{Category: cat, Op: f.Name, Merge: cat == CatPRMerge}
	var in any
	var nInput int
	for _, a := range f.Arguments {
		if a.Name != "input" {
			continue
		}
		nInput++
		v, err := gqlResolve(a.Value, vars, defaults)
		if err != nil {
			return nil, err
		}
		in = v
	}
	if nInput > 1 {
		return nil, ErrUnparsable
	}
	for _, k := range gqlTargetKeys {
		if s := gqlLenient(in, k); s != "" {
			op.Target = s
			break
		}
	}
	op.Body = gqlLenient(in, "body")
	op.RepositoryID = gqlLenient(in, "repositoryId")

	var err error
	switch f.Name {
	case "createPullRequest":
		if op.HeadRef, err = gqlRequire(in, "headRefName"); err != nil {
			return nil, err
		}
		if op.BaseRef, err = gqlRequire(in, "baseRefName"); err != nil {
			return nil, err
		}
		if op.RepositoryID, err = gqlRequire(in, "repositoryId"); err != nil {
			return nil, err
		}
		if op.HeadRepoID, _, err = gqlStr(in, "headRepositoryId"); err != nil {
			return nil, err
		}
	case "addPullRequestReview", "submitPullRequestReview":
		ev, ok, err := gqlStr(in, "event")
		if err != nil || (f.Name == "submitPullRequestReview" && (!ok || ev == "")) {
			return nil, ErrUnparsable
		}
		op.ReviewEvent = strings.ToUpper(ev)
	case "createRef":
		if op.RefName, err = gqlRequire(in, "name"); err != nil {
			return nil, err
		}
		if op.SHA, err = gqlRequire(in, "oid"); err != nil {
			return nil, err
		}
	case "updateRef":
		if op.SHA, err = gqlRequire(in, "oid"); err != nil {
			return nil, err
		}
	case "deleteRef":
		op.SHA = zeroSHA
	case "updateRefs":
		return gqlRefUpdates(op, in)
	case "createCommitOnBranch":
		if m, ok := in.(map[string]any); ok {
			op.RefName = gqlLenient(m["branch"], "branchName")
		}
	case "mergeBranch":
		op.RefName = gqlLenient(in, "base")
		op.Merge = true
	}
	return []GitHubOp{op}, nil
}

// gqlRefUpdates 把 updateRefs 的每个 refUpdate 展开为一个操作，便于逐个 ref 过门禁。
func gqlRefUpdates(base GitHubOp, in any) ([]GitHubOp, error) {
	m, ok := in.(map[string]any)
	if !ok {
		return nil, ErrUnparsable
	}
	list, ok := m["refUpdates"].([]any)
	if !ok {
		return nil, ErrUnparsable
	}
	if len(list) == 0 {
		return []GitHubOp{base}, nil
	}
	ops := make([]GitHubOp, 0, len(list))
	for _, u := range list {
		op := base
		var err error
		if op.RefName, err = gqlRequire(u, "name"); err != nil {
			return nil, err
		}
		if op.SHA, err = gqlRequire(u, "afterOid"); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// ---- 正文与响应 ----

// NormalizeBody 把正文统一为 \n 换行并去掉首尾空白。
func NormalizeBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// BodyHash 返回 NormalizeBody 后的 SHA-256 十六进制；空串返回 ""。
func BodyHash(s string) string {
	s = NormalizeBody(s)
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// respAnchors 是 html_url 片段到补充键前缀的映射。
var respAnchors = [...][2]string{
	{"#issuecomment-", "comment:"},
	{"#pullrequestreview-", "review:"},
	{"#discussion_r", "review_comment:"},
}

// RespKeys 从 REST 写操作的 JSON 响应里取补充键："node:<node_id>"、"comment:<id>"（html_url 含 #issuecomment-）、
// "review:<id>"（#pullrequestreview-）、"review_comment:<id>"（#discussion_r）；也从任意 JSON 里的 url 字符串取锚点。
func RespKeys(body []byte) []string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return nil
	}
	var keys []string
	add := func(k string) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["node_id"].(string); ok && s != "" {
			add("node:" + s)
		}
	}
	respWalk(v, add)
	return keys
}

// respWalk 按键名排序遍历 JSON，从键名以 url 结尾的字符串值里取锚点。
func respWalk(v any, add func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if s, ok := x[k].(string); ok {
				if strings.HasSuffix(strings.ToLower(k), "url") {
					respAnchorKeys(s, add)
				}
				continue
			}
			respWalk(x[k], add)
		}
	case []any:
		for _, e := range x {
			respWalk(e, add)
		}
	}
}

func respAnchorKeys(s string, add func(string)) {
	for _, a := range respAnchors {
		i := strings.Index(s, a[0])
		if i < 0 {
			continue
		}
		rest := s[i+len(a[0]):]
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n > 0 {
			add(a[1] + rest[:n])
		}
	}
}
