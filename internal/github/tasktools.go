package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 内 issue 任务工具的控制面处理器（M15 R17，步骤 5.2）：github_classify_issue、github_task_state、
// github_abort_task；github_autoclose 见 autoclose.go。调用方线程已由 orbs 校验。

// codeNoTask 是线程没有 GitHub 任务时工具回的错误码。
const codeNoTask = "no_github_task"

// 分类体系（移植自 robomp；标签名与取值相同）。
var (
	primaryClasses   = []string{"bug", "wontfix", "documentation", "enhancement", "proposal", "question", "invalid", "duplicate"}
	priorityLabels   = []string{"prio:p0", "prio:p1", "prio:p2", "prio:p3"}
	functionalLabels = []string{"agent", "tool", "tui", "cli", "prompting", "sdk", "auth", "setup", "ux", "providers"}
	platformLabels   = []string{"platform:linux", "platform:macos", "platform:windows", "platform:wsl"}
	providerLabel    = regexp.MustCompile(`^provider:[a-z0-9._-]+$`)
	branchSlug       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// 工具可设的非终态状态；needs_info 另加 needs-info 标签。
var toolTaskStates = []string{taskReproducing, taskFixing, taskReviewing, taskOpened, taskNeedsInfo}

const needsInfoLabel = "needs-info"

// 工具参数的上限（与 oryn-orb.ts 的 schema 一致）。
const (
	maxRationale  = 2000
	maxSlug       = 60
	maxNote       = 2000
	maxReproTitle = 200
	maxReproCmd   = 4000
	maxReproOut   = 20000
	maxAbortWhy   = 2000
)

// decodeParams 把工具参数解成 out；不认识的字段与类型不符都回 invalid_params。
func decodeParams(params map[string]any, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return errf(codeInvalidParams, err.Error())
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errf(codeInvalidParams, "参数无效："+err.Error())
	}
	return nil
}

// toolTask 取调用线程的 issue 任务与所在仓库（owner/repo）。
func (s *Service) toolTask(ctx context.Context, threadID string) (db.ThreadIssue, string, error) {
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, threadID)
	if err != nil {
		if isNoRows(err) {
			return ti, "", errf(codeNoTask, "this thread was not started for a GitHub issue")
		}
		return ti, "", err
	}
	full, err := s.repoFullName(ctx, ti.RepoID)
	if err != nil {
		return ti, "", err
	}
	if full == "" {
		return ti, "", errf(codeNoTask, "the repository of this thread's GitHub task is no longer bound")
	}
	return ti, full, nil
}

// addLabels 以线程的身份给 issue 加标签（控制面直连，先预登记防回流）。
func (s *Service) addLabels(ctx context.Context, threadID, repo string, number int64, labels []string) error {
	path := issuePath(repo, number, "/labels")
	_, err := s.selfWrite(ctx, s.threadSlot(ctx, threadID), http.MethodPost, path, egress.CatIssueUpdate, []string{path},
		map[string]any{"labels": labels}, "")
	return err
}

// toolClassify 实现 github_classify_issue：记分类、打标签；bug 须带 priority；带 branch_slug 时给出建议分支名。
func (s *Service) toolClassify(ctx context.Context, threadID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
	var p struct {
		Primary    string   `json:"primary"`
		Priority   string   `json:"priority"`
		Functional []string `json:"functional"`
		Provider   string   `json:"provider"`
		Platform   string   `json:"platform"`
		Rationale  string   `json:"rationale"`
		BranchSlug string   `json:"branch_slug"`
	}
	if err := decodeParams(params, &p); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	switch {
	case !slices.Contains(primaryClasses, p.Primary):
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "primary must be one of "+strings.Join(primaryClasses, ", "))
	case p.Primary == "bug" && p.Priority == "":
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "bug requires priority")
	case p.Priority != "" && !slices.Contains(priorityLabels, p.Priority):
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "priority must be one of "+strings.Join(priorityLabels, ", "))
	case p.Provider != "" && !providerLabel.MatchString(p.Provider):
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "provider must look like provider:<name>")
	case p.Platform != "" && !slices.Contains(platformLabels, p.Platform):
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "platform must be one of "+strings.Join(platformLabels, ", "))
	case strings.TrimSpace(p.Rationale) == "" || utf8.RuneCountInString(p.Rationale) > maxRationale:
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, fmt.Sprintf("rationale must be 1–%d characters", maxRationale))
	case p.BranchSlug != "" && (len(p.BranchSlug) > maxSlug || !branchSlug.MatchString(p.BranchSlug)):
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "branch_slug must be kebab-case, at most 60 characters")
	}
	for _, f := range p.Functional {
		if !slices.Contains(functionalLabels, f) {
			return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "functional must be from "+strings.Join(functionalLabels, ", "))
		}
	}
	ti, repo, err := s.toolTask(ctx, threadID)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if _, err := s.st.Q.SetThreadIssueClassification(ctx, db.SetThreadIssueClassificationParams{Classification: &p.Primary, ThreadID: threadID}); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	slog.Info("github: 任务分类", "thread", threadID, "number", ti.Number, "primary", p.Primary, "rationale", clip(p.Rationale, 200))
	labels := []string{p.Primary}
	for _, l := range append([]string{p.Priority, p.Provider, p.Platform}, p.Functional...) {
		if l != "" && !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	details := map[string]any{"classification": p.Primary, "labels": labels}
	text := fmt.Sprintf("Recorded classification %q for %s#%d and applied labels %s.", p.Primary, repo, ti.Number, strings.Join(labels, ", "))
	if err := s.addLabels(ctx, threadID, repo, ti.Number, labels); err != nil {
		slog.Warn("github: 分类打标签失败", "thread", threadID, "err", err)
		details["labelError"] = err.Error()
		text = fmt.Sprintf("Recorded classification %q for %s#%d, but applying labels failed: %v. Add them with gh if needed.", p.Primary, repo, ti.Number, err)
	}
	authorized := ti.ImplAuthorized || implFreeClasses[p.Primary]
	if trig, ok, err := s.threadRule(ctx, threadID); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	} else if !ok || !trig.ImplAuth {
		authorized = true // 规则没开「实现需授权」
	}
	details["implAuthorized"] = authorized
	text += fmt.Sprintf("\nimplAuthorized: %t", authorized)
	if p.BranchSlug != "" {
		th, err := s.st.Q.GetThread(ctx, threadID)
		if err != nil {
			return apigen.LinkCtlGithubCallResult{}, err
		}
		// 线程短 id：短码；老线程没有短码时取线程 id 末 8 位
		short := threadID[max(len(threadID)-8, 0):]
		if th.ShortCode != nil && *th.ShortCode != "" {
			short = *th.ShortCode
		}
		branch := "oryn/" + strings.ToLower(short) + "/" + p.BranchSlug
		details["branch"] = branch
		text += "\nbranch: " + branch
	}
	s.th.Publish(ctx, threadID)
	s.changed()
	return apigen.LinkCtlGithubCallResult{Text: text, Details: details}, nil
}

// toolTaskState 实现 github_task_state：改任务状态；needs_info 加 needs-info 标签；repro 写进工具结果（成为线程条目）。
func (s *Service) toolTaskState(ctx context.Context, threadID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
	var p struct {
		State string `json:"state"`
		Note  string `json:"note"`
		Repro *struct {
			Title      string `json:"title"`
			Command    string `json:"command"`
			Output     string `json:"output"`
			ExitCode   *int   `json:"exit_code"`
			Reproduced *bool  `json:"reproduced"`
		} `json:"repro"`
	}
	if err := decodeParams(params, &p); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if !slices.Contains(toolTaskStates, p.State) {
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "state must be one of "+strings.Join(toolTaskStates, ", "))
	}
	if utf8.RuneCountInString(p.Note) > maxNote {
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, fmt.Sprintf("note must be at most %d characters", maxNote))
	}
	if r := p.Repro; r != nil {
		switch {
		case strings.TrimSpace(r.Title) == "" || utf8.RuneCountInString(r.Title) > maxReproTitle,
			strings.TrimSpace(r.Command) == "" || utf8.RuneCountInString(r.Command) > maxReproCmd,
			utf8.RuneCountInString(r.Output) > maxReproOut, r.ExitCode == nil, r.Reproduced == nil:
			return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, "repro needs title, command, output, exit_code and reproduced within limits")
		}
	}
	ti, repo, err := s.toolTask(ctx, threadID)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if issueTaskTerminal(ti.State) {
		return apigen.LinkCtlGithubCallResult{}, errf("task_finished", "the GitHub task is already "+ti.State)
	}
	if err := s.setTask(ctx, ti, p.State, p.Note); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	details := map[string]any{"state": p.State}
	text := fmt.Sprintf("Task %s#%d is now %s.", repo, ti.Number, p.State)
	if p.State == taskNeedsInfo {
		if err := s.addLabels(ctx, threadID, repo, ti.Number, []string{needsInfoLabel}); err != nil {
			slog.Warn("github: 加 needs-info 标签失败", "thread", threadID, "err", err)
			details["labelError"] = err.Error()
			text += fmt.Sprintf(" Adding the %s label failed: %v.", needsInfoLabel, err)
		} else {
			text += " Added the " + needsInfoLabel + " label."
		}
	}
	if r := p.Repro; r != nil {
		verdict := "not reproduced"
		if *r.Reproduced {
			verdict = "reproduced"
		}
		details["repro"] = map[string]any{
			"title": r.Title, "command": r.Command, "output": r.Output, "exit_code": *r.ExitCode, "reproduced": *r.Reproduced,
		}
		text += fmt.Sprintf("\n\n### Repro: %s (%s, exit %d)\n```\n$ %s\n%s\n```", r.Title, verdict, *r.ExitCode, r.Command, r.Output)
	}
	return apigen.LinkCtlGithubCallResult{Text: text, Details: details}, nil
}

// toolAbortTask 实现 github_abort_task：任务置为 abandoned（冻结 orb）、写一行审计，不在 GitHub 上发言。
func (s *Service) toolAbortTask(ctx context.Context, orbID, threadID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
	var p struct {
		Reason string `json:"reason"`
	}
	if err := decodeParams(params, &p); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if strings.TrimSpace(p.Reason) == "" || utf8.RuneCountInString(p.Reason) > maxAbortWhy {
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, fmt.Sprintf("reason must be 1–%d characters", maxAbortWhy))
	}
	ti, repo, err := s.toolTask(ctx, threadID)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if err := s.st.Q.InsertGithubAudit(ctx, db.InsertGithubAuditParams{
		At: s.nowMs(), ThreadID: &threadID, OrbID: orbID, Slot: s.orbSlot(ctx, orbID), Method: "TASK",
		Op: "github_abort_task: " + clip(p.Reason, 500), Repo: repo,
	}); err != nil {
		slog.Warn("github: 记录放弃任务的审计失败", "thread", threadID, "err", err)
	}
	// setTask 取消待执行的自动关闭，并排队冻结 orb（在本次工具应答之后）
	if err := s.setTask(ctx, ti, taskAbandoned, "github_abort_task: "+clip(p.Reason, 200)); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	return apigen.LinkCtlGithubCallResult{
		Text:    fmt.Sprintf("Task %s#%d is abandoned; the owner will see your reason. Stop working on it now.", repo, ti.Number),
		Details: map[string]any{"state": taskAbandoned},
	}, nil
}
