package githubtest

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// 本文件是真实 gh 发出的只读 GraphQL 查询（M15 步骤 9.2 的 m15-accept：orb 里 `gh pr create`）的通用执行：按查询文档的
// 选择集（含别名、具名与内联 fragment）从对象图里取字段。根字段只认 repository(owner, name) 与 viewer；对象图里没有的字段
// 一律为 null（gh 对可选字段容忍 null）。只读，不改任何状态。

// gqlObj 是对象图里的一个对象：值为标量、gqlObj、[]any，或按参数求值的 gqlField。
type gqlObj map[string]any

// gqlField 是带参数的字段（如 pullRequests(headRefName:, states:)）。
type gqlField func(args map[string]any) any

// gqlQueryRoots 是通用执行认得的根字段。
var gqlQueryRoots = map[string]bool{"repository": true, "viewer": true}

// gqlGeneric 报告查询 op 的根字段是否全都认得（含经 fragment 展开的）。
func gqlGeneric(op *ast.OperationDefinition, frags ast.FragmentDefinitionList) bool {
	if op.Operation != ast.Query {
		return false
	}
	var ok func(set ast.SelectionSet) bool
	ok = func(set ast.SelectionSet) bool {
		for _, sel := range set {
			switch x := sel.(type) {
			case *ast.Field:
				if !gqlQueryRoots[x.Name] && x.Name != "__typename" {
					return false
				}
			case *ast.InlineFragment:
				if !ok(x.SelectionSet) {
					return false
				}
			case *ast.FragmentSpread:
				f := frags.ForName(x.Name)
				if f == nil || !ok(f.SelectionSet) {
					return false
				}
			}
		}
		return true
	}
	return len(op.SelectionSet) > 0 && ok(op.SelectionSet)
}

// gqlArgs 求字段的参数值（不依赖 schema：变量按名取，字面量原样）。
func gqlArgs(f *ast.Field, vars map[string]any) map[string]any {
	out := map[string]any{}
	for _, a := range f.Arguments {
		if v, err := a.Value.Value(vars); err == nil {
			out[a.Name] = v
		}
	}
	return out
}

// gqlExec 按选择集从 v 里取值。
func gqlExec(v any, set ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = gqlExec(e, set, frags, vars)
		}
		return out
	case gqlObj:
		out := map[string]any{}
		gqlFill(out, x, set, frags, vars)
		return out
	}
	return v
}

func gqlFill(out map[string]any, obj gqlObj, set ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any) {
	typeOK := func(cond string) bool {
		t, _ := obj["__typename"].(string)
		return cond == "" || t == "" || cond == t
	}
	for _, sel := range set {
		switch x := sel.(type) {
		case *ast.Field:
			alias := x.Alias
			if alias == "" {
				alias = x.Name
			}
			val := obj[x.Name]
			if f, ok := val.(gqlField); ok {
				val = f(gqlArgs(x, vars))
			}
			if len(x.SelectionSet) > 0 {
				val = gqlExec(val, x.SelectionSet, frags, vars)
			}
			out[alias] = val
		case *ast.InlineFragment:
			if typeOK(x.TypeCondition) {
				gqlFill(out, obj, x.SelectionSet, frags, vars)
			}
		case *ast.FragmentSpread:
			if f := frags.ForName(x.Name); f != nil && typeOK(f.TypeCondition) {
				gqlFill(out, obj, f.SelectionSet, frags, vars)
			}
		}
	}
}

// gqlRoot 是查询的根对象（令牌对应的用户 u 视角）。调用方持锁。
func (s *Server) gqlRoot(r *http.Request, u *User) gqlObj {
	return gqlObj{
		"__typename": "Query",
		"viewer":     gqlObj{"__typename": "User", "id": fmt.Sprintf("U_%d", u.ID), "databaseId": u.ID, "login": u.Login, "name": u.Name},
		"repository": gqlField(func(args map[string]any) any {
			owner, _ := args["owner"].(string)
			name, _ := args["name"].(string)
			rp, to := s.lookupRepo(owner, name)
			if rp == nil && to != "" {
				o, n, _ := strings.Cut(to, "/")
				rp, _ = s.lookupRepo(o, n)
			}
			if rp == nil {
				return nil
			}
			return s.repoGQL(r, rp, u)
		}),
	}
}

// repoGQL 是仓库的 GraphQL 对象（gh 的 repo fragment、RepositoryNetwork、PullRequestForBranch 用到的字段）。调用方持锁。
func (s *Server) repoGQL(r *http.Request, rp *Repo, u *User) gqlObj {
	owner, name, _ := strings.Cut(rp.FullName, "/")
	branch := rp.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	perm := map[string]string{"admin": "ADMIN", "maintain": "MAINTAIN", "write": "WRITE"}[s.permOf(rp, u.Login)]
	if perm == "" {
		perm = "READ"
	}
	ownerType := "Organization"
	if ou, ok := s.Users[strings.ToLower(owner)]; ok && ou.Type != "Organization" {
		ownerType = "User"
	}
	var parent any
	for _, p := range s.Repos {
		if s.forkParent[rp.ID] == p.ID {
			parent = s.repoGQL(r, p, u)
		}
	}
	visibility := "PUBLIC"
	if rp.Private {
		visibility = "PRIVATE"
	}
	return gqlObj{
		"__typename": "Repository", "id": RepoNodeID(rp.ID), "databaseId": rp.ID, "name": name, "nameWithOwner": rp.FullName,
		"owner":       gqlObj{"__typename": ownerType, "id": fmt.Sprintf("O_%d", rp.OwnerID), "login": owner},
		"url":         s.baseURL(r) + "/" + rp.FullName,
		"sshUrl":      "git@github.com:" + rp.FullName + ".git",
		"description": rp.Description, "hasIssuesEnabled": true, "hasWikiEnabled": false, "hasDiscussionsEnabled": false,
		"hasProjectsEnabled": false, "isPrivate": rp.Private, "isArchived": false, "isFork": parent != nil, "visibility": visibility,
		"viewerPermission": perm, "viewerCanAdminister": perm == "ADMIN", "viewerCanPush": perm != "READ",
		"defaultBranchRef":   gqlObj{"__typename": "Ref", "name": branch},
		"parent":             parent,
		"mergeCommitAllowed": true, "rebaseMergeAllowed": true, "squashMergeAllowed": true,
		"pullRequestTemplates": []any{}, "issueTemplates": []any{},
		"pullRequests": gqlField(func(args map[string]any) any {
			head, _ := args["headRefName"].(string)
			var nodes []any
			for _, pr := range s.Pulls {
				open := pr.State == "open" && pr.MergedAt.IsZero()
				if pr.RepoID == rp.ID && (head == "" || pr.HeadRef == head) && (!gqlOnlyOpen(args) || open) {
					nodes = append(nodes, s.pullQueryGQL(r, rp, pr))
				}
			}
			if nodes == nil {
				nodes = []any{}
			}
			return gqlObj{"totalCount": len(nodes), "nodes": nodes, "pageInfo": gqlObj{"hasNextPage": false, "endCursor": nil}}
		}),
		"pullRequest": gqlField(func(args map[string]any) any {
			n, _ := args["number"].(int64)
			if pr := s.findPull(rp.ID, n); pr != nil {
				return s.pullQueryGQL(r, rp, pr)
			}
			return nil
		}),
	}
}

// gqlOnlyOpen 报告 pullRequests 的 states 参数是否只要 OPEN。
func gqlOnlyOpen(args map[string]any) bool {
	states, _ := args["states"].([]any)
	if len(states) == 0 {
		if s, ok := args["states"].(string); ok {
			return s == "OPEN"
		}
		return false
	}
	for _, s := range states {
		if s != "OPEN" {
			return false
		}
	}
	return true
}

// pullQueryGQL 是 PR 在查询里的对象。调用方持锁。
func (s *Server) pullQueryGQL(r *http.Request, rp *Repo, pr *Pull) gqlObj {
	m := gqlObj{"__typename": "PullRequest"}
	for k, v := range s.pullGQL(r, rp, pr) {
		m[k] = v
	}
	head := rp
	if pr.HeadRepoID != 0 {
		if hr := s.repoByID(pr.HeadRepoID); hr != nil {
			head = hr
		}
	}
	hOwner, hName, _ := strings.Cut(head.FullName, "/")
	m["title"], m["body"], m["isDraft"] = pr.Title, pr.Body, pr.Draft
	m["headRefName"], m["baseRefName"], m["headRefOid"] = pr.HeadRef, pr.BaseRef, pr.HeadSHA
	m["author"] = gqlObj{"__typename": "User", "login": pr.Author}
	m["headRepositoryOwner"] = gqlObj{"login": hOwner}
	m["headRepository"] = gqlObj{"name": hName, "nameWithOwner": head.FullName}
	m["isCrossRepository"] = head.ID != rp.ID
	return m
}
