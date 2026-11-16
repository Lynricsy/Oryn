package egress

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestParseGitHubRESTCategories(t *testing.T) {
	cases := []struct {
		name, method, path, raw, body string
		want                          GitHubOp
	}{
		{name: "issue 评论", method: "POST", path: "/repos/Owner/Repo/issues/12/comments", body: `{"body":"hi"}`,
			want: GitHubOp{Category: CatCommentCreate, Op: "POST /repos/{owner}/{repo}/issues/{number}/comments", Repo: "owner/repo", Number: 12, Target: "/repos/owner/repo/issues/12/comments", Body: "hi"}},
		{name: "repositories 形态", method: "post", path: "/repositories/42/issues/7/comments/", body: `{"body":"x"}`,
			want: GitHubOp{Category: CatCommentCreate, Op: "POST /repositories/{repository_id}/issues/{number}/comments", RepoID: 42, Number: 7, Target: "/repositories/42/issues/7/comments", Body: "x"}},
		{name: "百分号编码路径", method: "PUT", path: "/repos/o/r/pulls/3/merge", raw: "/repos/o/r/pulls/%33/m%65rge",
			want: GitHubOp{Category: CatPRMerge, Op: "PUT /repos/{owner}/{repo}/pulls/{number}/merge", Repo: "o/r", Number: 3, Target: "/repos/o/r/pulls/3/merge", Merge: true}},
		{name: "编码斜杠", method: "PUT", path: "/repos/o/r/pulls/3%2Fmerge", raw: "/repos/o/r/pulls%2F3%2Fmerge",
			want: GitHubOp{Category: CatPRMerge, Op: "PUT /repos/{owner}/{repo}/pulls/{number}/merge", Repo: "o/r", Number: 3, Target: "/repos/o/r/pulls/3/merge", Merge: true}},
		{name: "格式后缀", method: "PUT", path: "/repos/o/r/pulls/3/merge.json",
			want: GitHubOp{Category: CatPRMerge, Op: "PUT /repos/{owner}/{repo}/pulls/{number}/merge", Repo: "o/r", Number: 3, Target: "/repos/o/r/pulls/3/merge", Merge: true}},
		{name: "大小写与重复斜杠", method: "PATCH", path: "//REPOS/O/R//Issues/Comments/9", body: `{"body":"e"}`,
			want: GitHubOp{Category: CatCommentUpdate, Op: "PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}", Repo: "o/r", Target: "/repos/o/r/issues/comments/9", Body: "e"}},
		{name: "删除评论", method: "DELETE", path: "/repos/o/r/issues/comments/9",
			want: GitHubOp{Category: CatCommentDelete, Op: "DELETE /repos/{owner}/{repo}/issues/comments/{comment_id}", Repo: "o/r", Target: "/repos/o/r/issues/comments/9"}},
		{name: "提交评论", method: "POST", path: "/repos/o/r/commits/abc/comments", body: `{"body":"c"}`,
			want: GitHubOp{Category: CatCommentCreate, Op: "POST /repos/{owner}/{repo}/commits/{commit_sha}/comments", Repo: "o/r", Target: "/repos/o/r/commits/abc/comments", Body: "c"}},
		{name: "创建 issue", method: "POST", path: "/repos/o/r/issues", body: `{"title":"t","body":"b"}`,
			want: GitHubOp{Category: CatIssueCreate, Op: "POST /repos/{owner}/{repo}/issues", Repo: "o/r", Target: "/repos/o/r/issues", Body: "b"}},
		{name: "编辑 issue", method: "PATCH", path: "/repos/o/r/issues/5", body: `{"state":"closed"}`,
			want: GitHubOp{Category: CatIssueUpdate, Op: "PATCH /repos/{owner}/{repo}/issues/{number}", Repo: "o/r", Number: 5, Target: "/repos/o/r/issues/5"}},
		{name: "加标签（数组体）", method: "POST", path: "/repos/o/r/issues/5/labels", body: `["bug"]`,
			want: GitHubOp{Category: CatIssueUpdate, Op: "POST /repos/{owner}/{repo}/issues/{number}/labels", Repo: "o/r", Number: 5, Target: "/repos/o/r/issues/5/labels"}},
		{name: "删标签", method: "DELETE", path: "/repos/o/r/issues/5/labels/bug",
			want: GitHubOp{Category: CatIssueUpdate, Op: "DELETE /repos/{owner}/{repo}/issues/{number}/labels/{name}", Repo: "o/r", Number: 5, Target: "/repos/o/r/issues/5/labels/bug"}},
		{name: "指派", method: "DELETE", path: "/repos/o/r/issues/5/assignees", body: `{"assignees":["a"]}`,
			want: GitHubOp{Category: CatIssueUpdate, Op: "DELETE /repos/{owner}/{repo}/issues/{number}/assignees", Repo: "o/r", Number: 5, Target: "/repos/o/r/issues/5/assignees"}},
		{name: "创建 PR", method: "POST", path: "/repos/o/r/pulls", body: `{"head":"me:feat","base":"main","title":"t","body":"pb"}`,
			want: GitHubOp{Category: CatPRCreate, Op: "POST /repos/{owner}/{repo}/pulls", Repo: "o/r", Target: "/repos/o/r/pulls", Body: "pb", HeadRef: "me:feat", BaseRef: "main"}},
		{name: "编辑 PR", method: "PATCH", path: "/repos/o/r/pulls/8", body: `{"state":"closed"}`,
			want: GitHubOp{Category: CatPRUpdate, Op: "PATCH /repos/{owner}/{repo}/pulls/{number}", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8"}},
		{name: "请求评审", method: "POST", path: "/repos/o/r/pulls/8/requested_reviewers", body: `{"reviewers":["x"]}`,
			want: GitHubOp{Category: CatPRUpdate, Op: "POST /repos/{owner}/{repo}/pulls/{number}/requested_reviewers", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/requested_reviewers"}},
		{name: "update-branch", method: "PUT", path: "/repos/o/r/pulls/8/update-branch",
			want: GitHubOp{Category: CatRefUnknown, Op: "PUT /repos/{owner}/{repo}/pulls/{number}/update-branch", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/update-branch"}},
		{name: "review 带 event", method: "POST", path: "/repos/o/r/pulls/8/reviews", body: `{"event":"approve","body":"lgtm","comments":[{"path":"a","line":1,"body":"n"}]}`,
			want: GitHubOp{Category: CatReview, Op: "POST /repos/{owner}/{repo}/pulls/{number}/reviews", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/reviews", Body: "lgtm", ReviewEvent: "APPROVE"}},
		{name: "待提交 review", method: "POST", path: "/repos/o/r/pulls/8/reviews", body: `{"body":"draft"}`,
			want: GitHubOp{Category: CatReview, Op: "POST /repos/{owner}/{repo}/pulls/{number}/reviews", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/reviews", Body: "draft"}},
		{name: "提交 review", method: "POST", path: "/repos/o/r/pulls/8/reviews/77/events", body: `{"event":"REQUEST_CHANGES"}`,
			want: GitHubOp{Category: CatReview, Op: "POST /repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/events", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/reviews/77/events", ReviewEvent: "REQUEST_CHANGES"}},
		{name: "行内评审评论", method: "POST", path: "/repos/o/r/pulls/8/comments", body: `{"body":"nit","path":"a.go","line":3}`,
			want: GitHubOp{Category: CatReviewComment, Op: "POST /repos/{owner}/{repo}/pulls/{number}/comments", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/comments", Body: "nit"}},
		{name: "评审评论回复", method: "POST", path: "/repos/o/r/pulls/8/comments/5/replies", body: `{"body":"ok"}`,
			want: GitHubOp{Category: CatReviewComment, Op: "POST /repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/replies", Repo: "o/r", Number: 8, Target: "/repos/o/r/pulls/8/comments/5/replies", Body: "ok"}},
		{name: "编辑评审评论", method: "PATCH", path: "/repos/o/r/pulls/comments/5", body: `{"body":"e"}`,
			want: GitHubOp{Category: CatCommentUpdate, Op: "PATCH /repos/{owner}/{repo}/pulls/comments/{comment_id}", Repo: "o/r", Target: "/repos/o/r/pulls/comments/5", Body: "e"}},
		{name: "reaction", method: "POST", path: "/repos/o/r/issues/comments/5/reactions", body: `{"content":"+1"}`,
			want: GitHubOp{Category: CatReaction, Op: "POST /repos/{owner}/{repo}/issues/comments/{comment_id}/reactions", Repo: "o/r", Target: "/repos/o/r/issues/comments/5/reactions"}},
		{name: "建 ref", method: "POST", path: "/repos/o/r/git/refs", body: `{"ref":"refs/heads/x","sha":"abc"}`,
			want: GitHubOp{Category: CatRefUpdate, Op: "POST /repos/{owner}/{repo}/git/refs", Repo: "o/r", Target: "/repos/o/r/git/refs", SHA: "abc", RefName: "refs/heads/x"}},
		{name: "改 ref", method: "PATCH", path: "/repos/o/r/git/refs/heads/feat/a", raw: "/repos/o/r/git/refs/heads/feat%2Fa", body: `{"sha":"def","force":true}`,
			want: GitHubOp{Category: CatRefUpdate, Op: "PATCH /repos/{owner}/{repo}/git/refs/{ref*}", Repo: "o/r", Target: "/repos/o/r/git/refs/heads/feat/a", SHA: "def", RefName: "refs/heads/feat/a"}},
		{name: "删 ref", method: "DELETE", path: "/repos/o/r/git/refs/heads/x",
			want: GitHubOp{Category: CatRefUpdate, Op: "DELETE /repos/{owner}/{repo}/git/refs/{ref*}", Repo: "o/r", Target: "/repos/o/r/git/refs/heads/x", SHA: zeroSHA, RefName: "refs/heads/x"}},
		{name: "contents PUT", method: "PUT", path: "/repos/o/r/contents/docs/a.md", body: `{"message":"m","content":"eA==","branch":"main"}`,
			want: GitHubOp{Category: CatRefUnknown, Op: "PUT /repos/{owner}/{repo}/contents/{path*}", Repo: "o/r", Target: "/repos/o/r/contents/docs/a.md", RefName: "main"}},
		{name: "contents DELETE", method: "DELETE", path: "/repos/o/r/contents/a.json", body: `{"message":"m","sha":"s"}`,
			want: GitHubOp{Category: CatRefUnknown, Op: "DELETE /repos/{owner}/{repo}/contents/{path*}", Repo: "o/r", Target: "/repos/o/r/contents/a.json"}},
		{name: "merges", method: "POST", path: "/repos/o/r/merges", body: `{"base":"main","head":"x"}`,
			want: GitHubOp{Category: CatRefUnknown, Op: "POST /repos/{owner}/{repo}/merges", Repo: "o/r", Target: "/repos/o/r/merges", RefName: "main", Merge: true}},
		{name: "merge-upstream", method: "POST", path: "/repos/o/r/merge-upstream", body: `{"branch":"main"}`,
			want: GitHubOp{Category: CatRefUnknown, Op: "POST /repos/{owner}/{repo}/merge-upstream", Repo: "o/r", Target: "/repos/o/r/merge-upstream", RefName: "main", Merge: true}},
		{name: "hook PATCH", method: "PATCH", path: "/repos/o/r/hooks/123", body: `{"active":false}`,
			want: GitHubOp{Category: CatHookWrite, Op: "PATCH /repos/{owner}/{repo}/hooks/{hook_id}", Repo: "o/r", HookID: 123, Target: "/repos/o/r/hooks/123"}},
		{name: "hook DELETE", method: "DELETE", path: "/repositories/42/hooks/123",
			want: GitHubOp{Category: CatHookWrite, Op: "DELETE /repositories/{repository_id}/hooks/{hook_id}", RepoID: 42, HookID: 123, Target: "/repositories/42/hooks/123"}},
		{name: "hook config", method: "PATCH", path: "/repos/o/r/hooks/123/config", body: `{"url":"x"}`,
			want: GitHubOp{Category: CatHookWrite, Op: "PATCH /repos/{owner}/{repo}/hooks/{hook_id}/config", Repo: "o/r", HookID: 123, Target: "/repos/o/r/hooks/123/config"}},
		{name: "hook 创建", method: "POST", path: "/repos/o/r/hooks", body: `{"name":"web"}`,
			want: GitHubOp{Category: CatHookWrite, Op: "POST /repos/{owner}/{repo}/hooks", Repo: "o/r", Target: "/repos/o/r/hooks"}},
		{name: "hook 未知子路径", method: "PUT", path: "/repos/o/r/hooks/123/whatever",
			want: GitHubOp{Category: CatHookWrite, Op: "PUT /repos/{owner}/{repo}/hooks/123/whatever", Repo: "o/r", HookID: 123, Target: "/repos/o/r/hooks/123/whatever"}},
		{name: "fork", method: "POST", path: "/repos/o/r/forks", body: `{}`,
			want: GitHubOp{Category: CatOther, Op: "POST /repos/{owner}/{repo}/forks", Repo: "o/r", Target: "/repos/o/r/forks"}},
		{name: "release", method: "POST", path: "/repos/o/r/releases", body: `{"tag_name":"v1","body":"notes"}`,
			want: GitHubOp{Category: CatOther, Op: "POST /repos/{owner}/{repo}/releases", Repo: "o/r", Target: "/repos/o/r/releases", Body: "notes"}},
		{name: "未知仓库写操作", method: "POST", path: "/repos/o/r/dispatches", body: `not json`,
			want: GitHubOp{Category: CatOther, Op: "POST /repos/{owner}/{repo}/dispatches", Repo: "o/r", Target: "/repos/o/r/dispatches"}},
		{name: "非仓库路径", method: "POST", path: "/user/repos", body: `{"name":"x"}`,
			want: GitHubOp{Category: CatOther, Op: "POST /user/repos", Target: "/user/repos"}},
		{name: "GraphQL 的非 POST", method: "DELETE", path: "/graphql",
			want: GitHubOp{Category: CatOther, Op: "DELETE /graphql", Target: "/graphql"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := ParseGitHubRequest(c.method, c.path, c.raw, []byte(c.body))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !req.Mutation || len(req.Ops) != 1 {
				t.Fatalf("req = %+v", req)
			}
			if req.Ops[0] != c.want {
				t.Fatalf("op =\n %+v\nwant\n %+v", req.Ops[0], c.want)
			}
		})
	}
}

func TestParseGitHubReadsAreNotMutations(t *testing.T) {
	for _, m := range []string{"GET", "head"} {
		req, err := ParseGitHubRequest(m, "/repos/o/r/issues/1/comments", "", nil)
		if err != nil || req.Mutation || len(req.Ops) != 0 {
			t.Fatalf("%s: req=%+v err=%v", m, req, err)
		}
	}
}

func TestParseGitHubRESTUnparsable(t *testing.T) {
	cases := []struct{ name, method, path, raw, body string }{
		{"评论体不是 JSON", "POST", "/repos/o/r/issues/1/comments", "", `{"body":`},
		{"重复顶层键", "POST", "/repos/o/r/pulls/1/reviews", "", `{"event":"COMMENT","event":"APPROVE"}`},
		{"event 类型不符", "POST", "/repos/o/r/pulls/1/reviews", "", `{"event":1}`},
		{"提交 review 缺 event", "POST", "/repos/o/r/pulls/1/reviews/2/events", "", `{}`},
		{"PR 缺 head", "POST", "/repos/o/r/pulls", "", `{"base":"main"}`},
		{"PR 无请求体", "POST", "/repos/o/r/pulls", "", ``},
		{"建 ref 缺 sha", "POST", "/repos/o/r/git/refs", "", `{"ref":"refs/heads/x"}`},
		{"改 ref 缺 sha", "PATCH", "/repos/o/r/git/refs/heads/x", "", `{"force":true}`},
		{"hook id 非数字", "DELETE", "/repos/o/r/hooks/12a", "", ``},
		{"点段", "PUT", "/repos/o/r/pulls/1/x/../merge", "", ``},
		{"编码点段", "PUT", "/repos/o/r/pulls/1/x/../merge", "/repos/o/r/pulls/1/x/%2e%2e/merge", ``},
		{"坏转义", "POST", "/repos/o/r/issues", "/repos/o/r/%zz", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseGitHubRequest(c.method, c.path, c.raw, []byte(c.body)); !errors.Is(err, ErrUnparsable) {
				t.Fatalf("err = %v, want ErrUnparsable", err)
			}
		})
	}
}

// gqlBody 构造 GraphQL 请求体。
func gqlBody(t *testing.T, query string, vars map[string]any, opName string) []byte {
	t.Helper()
	m := map[string]any{"query": query}
	if vars != nil {
		m["variables"] = vars
	}
	if opName != "" {
		m["operationName"] = opName
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseGitHubGraphQL(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		vars   map[string]any
		opName string
		want   []GitHubOp
	}{
		{name: "addComment 变量 input",
			query: `mutation CommentCreate($input: AddCommentInput!) { addComment(input: $input) { commentEdge { node { url } } } }`,
			vars:  map[string]any{"input": map[string]any{"subjectId": "I_1", "body": "hello"}},
			want:  []GitHubOp{{Category: CatCommentCreate, Op: "addComment", Target: "I_1", Body: "hello"}}},
		{name: "addComment 内联 input 与嵌套变量",
			query: `mutation($b: String!) { addComment(input: {subjectId: "PR_1", body: $b}) { clientMutationId } }`,
			vars:  map[string]any{"b": "inline"},
			want:  []GitHubOp{{Category: CatCommentCreate, Op: "addComment", Target: "PR_1", Body: "inline"}}},
		{name: "别名用字段名",
			query: `mutation { harmless: addComment(input: {subjectId: "I_2", body: "a"}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatCommentCreate, Op: "addComment", Target: "I_2", Body: "a"}}},
		{name: "fragment 与 inline fragment",
			query: `mutation { ...F ... on Mutation { closeIssue(input: {issueId: "I_3"}) { clientMutationId } } }
fragment F on Mutation { ...G addComment(input: {subjectId: "I_3", body: "f"}) { clientMutationId } }
fragment G on Mutation { mergePullRequest(input: {pullRequestId: "PR_9"}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatPRMerge, Op: "mergePullRequest", Target: "PR_9", Merge: true},
				{Category: CatCommentCreate, Op: "addComment", Target: "I_3", Body: "f"},
				{Category: CatIssueUpdate, Op: "closeIssue", Target: "I_3"},
			}},
		{name: "按 operationName 选择",
			query:  `query Q { viewer { login } } mutation M { addReaction(input: {subjectId: "C_1", content: HOORAY}) { clientMutationId } }`,
			opName: "M",
			want:   []GitHubOp{{Category: CatReaction, Op: "addReaction", Target: "C_1"}}},
		{name: "createPullRequest",
			query: `mutation PullRequestCreate($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { id url } } }`,
			vars: map[string]any{"input": map[string]any{"repositoryId": "R_1", "baseRefName": "main", "headRefName": "me:feat",
				"headRepositoryId": "R_2", "title": "t", "body": "pb"}},
			want: []GitHubOp{{Category: CatPRCreate, Op: "createPullRequest", Target: "R_1", Body: "pb", HeadRef: "me:feat", BaseRef: "main", HeadRepoID: "R_2", RepositoryID: "R_1"}}},
		{name: "addPullRequestReview 枚举 event",
			query: `mutation { addPullRequestReview(input: {pullRequestId: "PR_1", event: APPROVE, body: "ok"}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatReview, Op: "addPullRequestReview", Target: "PR_1", Body: "ok", ReviewEvent: "APPROVE"}}},
		{name: "addPullRequestReview 待提交",
			query: `mutation { addPullRequestReview(input: {pullRequestId: "PR_1"}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatReview, Op: "addPullRequestReview", Target: "PR_1"}}},
		{name: "submitPullRequestReview 变量默认值",
			query: `mutation($e: PullRequestReviewEvent = COMMENT) { submitPullRequestReview(input: {pullRequestReviewId: "PRR_1", event: $e}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatReview, Op: "submitPullRequestReview", ReviewEvent: "COMMENT"}}},
		{name: "PR 状态类",
			query: `mutation { a: closePullRequest(input: {pullRequestId: "P"}) { clientMutationId } b: disablePullRequestAutoMerge(input: {pullRequestId: "P"}) { clientMutationId } c: requestReviewsByLogin(input: {pullRequestId: "P", userLogins: ["x"]}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatPRUpdate, Op: "closePullRequest", Target: "P"},
				{Category: CatPRUpdate, Op: "disablePullRequestAutoMerge", Target: "P"},
				{Category: CatPRUpdate, Op: "requestReviewsByLogin", Target: "P"},
			}},
		{name: "合并类",
			query: `mutation { enablePullRequestAutoMerge(input: {pullRequestId: "P"}) { clientMutationId } enqueuePullRequest(input: {pullRequestId: "P"}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatPRMerge, Op: "enablePullRequestAutoMerge", Target: "P", Merge: true},
				{Category: CatPRMerge, Op: "enqueuePullRequest", Target: "P", Merge: true},
			}},
		{name: "评审评论",
			query: `mutation { addPullRequestReviewThread(input: {pullRequestId: "P", body: "nit", path: "a", line: 1}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatReviewComment, Op: "addPullRequestReviewThread", Target: "P", Body: "nit"}}},
		{name: "评论改删",
			query: `mutation { updateIssueComment(input: {id: "IC_1", body: "new"}) { clientMutationId } deleteIssueComment(input: {id: "IC_1"}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatCommentUpdate, Op: "updateIssueComment", Target: "IC_1", Body: "new"},
				{Category: CatCommentDelete, Op: "deleteIssueComment", Target: "IC_1"},
			}},
		{name: "issue 类",
			query: `mutation { createIssue(input: {repositoryId: "R", title: "t", body: "b"}) { clientMutationId } addLabelsToLabelable(input: {labelableId: "L", labelIds: ["x"]}) { clientMutationId } replaceActorsForAssignable(input: {assignableId: "A", actorIds: []}) { clientMutationId } addSubIssue(input: {issueId: "I", subIssueId: "S"}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatIssueCreate, Op: "createIssue", Target: "R", Body: "b", RepositoryID: "R"},
				{Category: CatIssueUpdate, Op: "addLabelsToLabelable", Target: "L"},
				{Category: CatIssueUpdate, Op: "replaceActorsForAssignable", Target: "A"},
				{Category: CatIssueUpdate, Op: "addSubIssue", Target: "I"},
			}},
		{name: "ref 类",
			query: `mutation($oid: GitObjectID!) { createRef(input: {repositoryId: "R", name: "refs/heads/x", oid: $oid}) { clientMutationId } updateRef(input: {refId: "REF", oid: "bbb"}) { clientMutationId } deleteRef(input: {refId: "REF"}) { clientMutationId } }`,
			vars:  map[string]any{"oid": "aaa"},
			want: []GitHubOp{
				{Category: CatRefUpdate, Op: "createRef", Target: "R", RepositoryID: "R", SHA: "aaa", RefName: "refs/heads/x"},
				{Category: CatRefUpdate, Op: "updateRef", SHA: "bbb"},
				{Category: CatRefUpdate, Op: "deleteRef", SHA: zeroSHA},
			}},
		{name: "updateRefs 逐个 ref",
			query: `mutation($u: [RefUpdate!]!) { updateRefs(input: {repositoryId: "R", refUpdates: $u}) { clientMutationId } }`,
			vars: map[string]any{"u": []any{
				map[string]any{"name": "refs/heads/a", "afterOid": "1"},
				map[string]any{"name": "refs/heads/b", "afterOid": "2"},
			}},
			want: []GitHubOp{
				{Category: CatRefUpdate, Op: "updateRefs", Target: "R", RepositoryID: "R", SHA: "1", RefName: "refs/heads/a"},
				{Category: CatRefUpdate, Op: "updateRefs", Target: "R", RepositoryID: "R", SHA: "2", RefName: "refs/heads/b"},
			}},
		{name: "SHA 不可知",
			query: `mutation { createCommitOnBranch(input: {branch: {repositoryNameWithOwner: "o/r", branchName: "main"}, expectedHeadOid: "x", message: {headline: "m"}}) { clientMutationId } mergeBranch(input: {repositoryId: "R", base: "main", head: "x"}) { clientMutationId } updatePullRequestBranch(input: {pullRequestId: "P"}) { clientMutationId } }`,
			want: []GitHubOp{
				{Category: CatRefUnknown, Op: "createCommitOnBranch", RefName: "main"},
				{Category: CatRefUnknown, Op: "mergeBranch", Target: "R", RepositoryID: "R", RefName: "main", Merge: true},
				{Category: CatRefUnknown, Op: "updatePullRequestBranch", Target: "P"},
			}},
		{name: "未知 mutation 与 __typename",
			query: `mutation { __typename starRepository(input: {starrableId: "R"}) { clientMutationId } }`,
			want:  []GitHubOp{{Category: CatOther, Op: "starRepository"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := ParseGitHubRequest("POST", "/graphql", "", gqlBody(t, c.query, c.vars, c.opName))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !req.Mutation || !slices.Equal(req.Ops, c.want) {
				t.Fatalf("req =\n %+v\nwant\n %+v", req.Ops, c.want)
			}
		})
	}
}

func TestParseGitHubGraphQLQueryIsNotMutation(t *testing.T) {
	body := gqlBody(t, `query Q { viewer { login } } mutation M { addComment(input: {subjectId: "I", body: "x"}) { clientMutationId } }`, nil, "Q")
	req, err := ParseGitHubRequest("POST", "/graphql/", "", body)
	if err != nil || req.Mutation || len(req.Ops) != 0 {
		t.Fatalf("req=%+v err=%v", req, err)
	}
}

func TestParseGitHubGraphQLUnparsable(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		query  string
		vars   map[string]any
		opName string
	}{
		{name: "非 JSON", body: `{"query":`},
		{name: "缺 query", body: `{"variables":{}}`},
		{name: "重复 query 键", body: `{"query":"query { viewer { login } }","query":"mutation { addComment(input:{}) { clientMutationId } }"}`},
		{name: "语法错误", query: `mutation { addComment(`},
		{name: "多 operation 无名", query: `query A { viewer { login } } mutation B { addComment(input: {subjectId: "I"}) { clientMutationId } }`},
		{name: "operationName 不存在", query: `mutation A { addComment(input: {subjectId: "I"}) { clientMutationId } }`, opName: "B"},
		{name: "operation 重名", query: `mutation A { addComment(input: {subjectId: "I"}) { clientMutationId } } mutation A { closeIssue(input: {issueId: "I"}) { clientMutationId } }`, opName: "A"},
		{name: "fragment 缺失", query: `mutation { ...Missing }`},
		{name: "fragment 循环", query: `mutation { ...A } fragment A on Mutation { ...B } fragment B on Mutation { ...A }`},
		{name: "event 变量缺失", query: `mutation($e: PullRequestReviewEvent) { addPullRequestReview(input: {pullRequestId: "P", event: $e}) { clientMutationId } }`},
		{name: "整个 input 变量缺失", query: `mutation($i: AddPullRequestReviewInput!) { addPullRequestReview(input: $i) { clientMutationId } }`},
		{name: "submit 缺 event", query: `mutation { submitPullRequestReview(input: {pullRequestReviewId: "R"}) { clientMutationId } }`},
		{name: "createPullRequest 缺 head", query: `mutation { createPullRequest(input: {repositoryId: "R", baseRefName: "main"}) { clientMutationId } }`},
		{name: "createRef 的 oid 不是字符串", query: `mutation($o: GitObjectID!) { createRef(input: {repositoryId: "R", name: "refs/heads/x", oid: $o}) { clientMutationId } }`, vars: map[string]any{"o": 1}},
		{name: "updateRefs 缺 afterOid", query: `mutation { updateRefs(input: {repositoryId: "R", refUpdates: [{name: "refs/heads/a"}]}) { clientMutationId } }`},
		{name: "input 字段重复", query: `mutation { addPullRequestReview(input: {pullRequestId: "P", event: COMMENT, event: APPROVE}) { clientMutationId } }`},
		{name: "variables 不是对象", body: `{"query":"mutation { closeIssue(input: {issueId: \"I\"}) { clientMutationId } }","variables":[1]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(c.body)
			if c.body == "" {
				body = gqlBody(t, c.query, c.vars, c.opName)
			}
			if _, err := ParseGitHubRequest("POST", "/graphql", "", body); !errors.Is(err, ErrUnparsable) {
				t.Fatalf("err = %v, want ErrUnparsable", err)
			}
		})
	}
}

func TestParseGitHubGraphQLStringVariables(t *testing.T) {
	body := `{"query":"mutation($i: AddCommentInput!) { addComment(input: $i) { clientMutationId } }","variables":"{\"i\":{\"subjectId\":\"I_9\",\"body\":\"s\"}}"}`
	req, err := ParseGitHubRequest("POST", "/graphql", "", []byte(body))
	want := []GitHubOp{{Category: CatCommentCreate, Op: "addComment", Target: "I_9", Body: "s"}}
	if err != nil || !slices.Equal(req.Ops, want) {
		t.Fatalf("req=%+v err=%v", req, err)
	}
}

func TestNormalizeBody(t *testing.T) {
	// 附录 B-5 实测正文
	raw := "  第一行 line1\r\nline2 with trailing   \r\n\r\n```\ncode\n```\n  "
	want := "第一行 line1\nline2 with trailing   \n\n```\ncode\n```"
	if got := NormalizeBody(raw); got != want {
		t.Fatalf("NormalizeBody = %q", got)
	}
	if NormalizeBody("a\rb") != "a\nb" {
		t.Fatal("单独的 \\r 未统一")
	}
	if BodyHash(raw) != BodyHash(want) || BodyHash(raw) == "" || len(BodyHash(raw)) != 64 {
		t.Fatalf("BodyHash = %q", BodyHash(raw))
	}
	if BodyHash("") != "" || BodyHash(" \r\n ") != "" {
		t.Fatal("空正文应返回空哈希")
	}
	if BodyHash("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("BodyHash(abc) = %s", BodyHash("abc"))
	}
}

func TestRespKeys(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"issue 评论", `{"id":1,"node_id":"IC_1","html_url":"https://github.com/o/r/issues/1#issuecomment-1","user":{"node_id":"U_1","html_url":"https://github.com/u"}}`,
			[]string{"node:IC_1", "comment:1"}},
		{"review", `{"id":5,"node_id":"PRR_5","html_url":"https://github.com/o/r/pull/2#pullrequestreview-5"}`,
			[]string{"node:PRR_5", "review:5"}},
		{"评审评论", `{"node_id":"PRRC_7","html_url":"https://github.com/o/r/pull/2#discussion_r7","pull_request_url":"https://api.github.com/repos/o/r/pulls/2"}`,
			[]string{"node:PRRC_7", "review_comment:7"}},
		{"GraphQL 风格的嵌套 url", `{"data":{"addComment":{"commentEdge":{"node":{"url":"https://github.com/o/r/pull/3#issuecomment-99"}}}}}`,
			[]string{"comment:99"}},
		{"无锚点", `{"node_id":"R_1","html_url":"https://github.com/o/r"}`, []string{"node:R_1"}},
		{"非 JSON", `<html>`, nil},
		{"空锚点", `{"url":"x#issuecomment-"}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RespKeys([]byte(c.body)); !slices.Equal(got, c.want) {
				t.Fatalf("RespKeys = %q, want %q", got, c.want)
			}
		})
	}
}
