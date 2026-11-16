package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// pull_request_opened 规则（M15 R12）：外部 PR 评审。由 pulls.go 的 onPullRequest 在 PR 入库之后调用。

// pullTriggerActions 是触发 pull_request_opened 规则的 pull_request 动作。
var pullTriggerActions = map[string]bool{"opened": true, "reopened": true, "ready_for_review": true}

// onPullTrigger 按 pull_request_opened 规则处理 PR 事件：只处理非草稿、open 状态、作者不是 bot 的 PR；建出的线程是只读的
// 评审工作区（review_only），身份只用 Agent 账号（见 fire）。动作不相关或仓库没有这类启用规则时返回空串，结果由调用方决定。
func (s *Service) onPullTrigger(ctx context.Context, action string, body []byte) (string, error) {
	if !pullTriggerActions[action] {
		return "", nil
	}
	var ev hookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", err
	}
	if ev.PullRequest == nil {
		return "", nil
	}
	cands, err := s.st.Q.ListEnabledGithubTriggersByRepo(ctx, db.ListEnabledGithubTriggersByRepoParams{RepoID: &ev.Repository.ID, Event: evPullOpened})
	if err != nil || len(cands) == 0 {
		return "", err
	}
	var st struct {
		PullRequest struct {
			State string `json:"state"`
			Draft bool   `json:"draft"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return "", err
	}
	pr := *ev.PullRequest
	switch {
	case st.PullRequest.State != "open":
		return "ignored:state", nil
	case st.PullRequest.Draft:
		return "ignored:draft", nil
	case pr.User.Type == "Bot":
		return "ignored:bot_author", nil
	}
	// pull_request 载荷没有 issue 视图的 pull_request 标记：补上，kind 与 PR 关联按 PR 处理
	pr.PullRequest = json.RawMessage(`{}`)
	tev := triggerEvent{
		event: evPullOpened, repoID: ev.Repository.ID, repo: ev.Repository.FullName, issue: pr, sender: senderOf(ev.Sender),
		self: pullProbe(ev, action),
	}
	if strings.EqualFold(ev.Sender.Login, pr.User.Login) {
		tev.assoc = pr.AuthorAssociation
	}
	return s.fire(ctx, tev, cands)
}

// pullProbe：pull_request 事件。opened 对应建 PR（createPullRequest 的 repositoryId，或 POST …/pulls；补充键为响应里
// 的 node:<PR node_id>），正文为 PR 正文；其余动作对应改 PR（按 PR 的 node_id 或 PATCH …/pulls/{n}），无正文，按
// selfWriteWindow 匹配。
func pullProbe(ev hookEvent, action string) selfProbe {
	p := selfProbe{sender: senderOf(ev.Sender)}
	pr := ev.PullRequest
	if pr == nil {
		return p
	}
	if action == "opened" {
		p.category, p.body, p.keys = egress.CatPRCreate, pr.body(), objectKeys("", 0, pr.NodeID)
		p.targets = append([]string{ev.Repository.NodeID}, restTargets(ev.Repository, "/pulls")...)
		return p
	}
	p.category = egress.CatPRUpdate
	p.targets = append([]string{pr.NodeID}, restTargets(ev.Repository, fmt.Sprintf("/pulls/%d", pr.Number))...)
	return p
}
