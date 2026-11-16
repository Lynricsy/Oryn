package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 问题自动关闭（M15 R18，步骤 5.5）：agent 回答 question 后调用 github_autoclose，记下本线程身份在该 issue 上的最近一条
// 评论；到期时只看 issue 作者在这条评论上的 👎，有就取消，没有就以 completed 关闭 issue。期间有人发新评论、issue 被外部
// 关闭都同步取消。每条排期只执行一次（原子认领）。

// 自动关闭的结果（github_autoclose.outcome）。
const (
	autocloseClosed        = "closed"
	autocloseThumbsDown    = "thumbs_down"
	autocloseAlreadyClosed = "already_closed"
	autocloseCommented     = "commented"
	autocloseCommentGone   = "comment_gone"
	autocloseExternalClose = "closed_externally"
)

// maxCommentPages 是查找线程身份最近一条评论时读取的评论页数上限（每页 100 条）。
const maxCommentPages = 30

// toolAutoclose 实现 github_autoclose：number 缺省取线程任务的 issue；hours 缺省取规则的 autoclose_hours（再缺省取
// defaultAutocloseHours）。
func (s *Service) toolAutoclose(ctx context.Context, threadID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
	var p struct {
		Number *int64 `json:"number"`
		Hours  *int64 `json:"hours"`
	}
	if err := decodeParams(params, &p); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if p.Number != nil && *p.Number < 1 || p.Hours != nil && (*p.Hours < 1 || *p.Hours > maxAutocloseHours) {
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams, fmt.Sprintf("number must be ≥ 1 and hours 1–%d", maxAutocloseHours))
	}
	ti, repo, err := s.toolTask(ctx, threadID)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	number := ti.Number
	if p.Number != nil {
		number = *p.Number
	}
	hours := int64(defaultAutocloseHours)
	if trig, ok, err := s.threadRule(ctx, threadID); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	} else if ok {
		hours = autocloseHours(trig)
	}
	if p.Hours != nil {
		hours = *p.Hours
	}
	slot := s.threadSlot(ctx, threadID)
	ident, err := s.st.Q.GetGithubIdentity(ctx, slot)
	if err != nil {
		if isNoRows(err) {
			return apigen.LinkCtlGithubCallResult{}, errf("github_identity_required", "the thread's GitHub account is not connected")
		}
		return apigen.LinkCtlGithubCallResult{}, err
	}
	tok, err := s.tokenFor(ctx, slot)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	auth := "Bearer " + tok
	var issue struct {
		State string        `json:"state"`
		User  senderPayload `json:"user"`
	}
	if err := s.call(ctx, http.MethodGet, issuePath(repo, number, ""), auth, nil, &issue); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if issue.State == "closed" {
		return apigen.LinkCtlGithubCallResult{}, errf("issue_closed", fmt.Sprintf("%s#%d is already closed", repo, number))
	}
	var commentID int64
	if err := s.paged(ctx, issuePath(repo, number, "/comments")+"?per_page=100", auth, maxCommentPages, func(raw json.RawMessage) error {
		var page []struct {
			ID   int64         `json:"id"`
			User senderPayload `json:"user"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, c := range page {
			if strings.EqualFold(c.User.Login, ident.Login) {
				commentID = c.ID
			}
		}
		return nil
	}); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	if commentID == 0 {
		return apigen.LinkCtlGithubCallResult{}, errf("no_comment", fmt.Sprintf("post your answer comment on %s#%d first", repo, number))
	}
	due := s.now().Add(time.Duration(hours) * time.Hour)
	if err := s.st.Q.UpsertGithubAutoclose(ctx, db.UpsertGithubAutocloseParams{
		RepoID: ti.RepoID, Number: number, ThreadID: threadID, CommentID: commentID, Author: issue.User.Login, DueAt: due.UnixMilli(),
	}); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	slog.Info("github: 排期自动关闭", "thread", threadID, "number", number, "comment", commentID, "hours", hours)
	return apigen.LinkCtlGithubCallResult{
		Text: fmt.Sprintf("%s#%d will be closed as completed in %d hours unless %s reacts 👎 to your comment, someone comments, or it is closed otherwise.",
			repo, number, hours, issue.User.Login),
		Details: map[string]any{"number": number, "commentId": commentID, "dueAt": due.UnixMilli(), "author": issue.User.Login},
	}, nil
}

// cancelAutoclose 取消 (repoID, number) 待执行的自动关闭；失败只记日志。
func (s *Service) cancelAutoclose(ctx context.Context, repoID, number int64, outcome string) {
	n, err := s.st.Q.CancelGithubAutoclose(ctx, db.CancelGithubAutocloseParams{Outcome: &outcome, RepoID: repoID, Number: number})
	switch {
	case err != nil:
		slog.Warn("github: 取消自动关闭失败", "repo", repoID, "number", number, "err", err)
	case n > 0:
		slog.Info("github: 取消自动关闭", "repo", repoID, "number", number, "why", outcome)
	}
}

// scanAutoclose 原子认领到期的自动关闭（UPDATE … WHERE state='pending'，并发的扫描各自只拿到不同的行）并逐条执行。
// 认领之后、关闭之前这一项仍可被取消（新评论、外部关闭、任务终态）：runAutoclose 在发关闭请求前经
// CommitGithubAutoclose 原子地越过「不可取消」点，取消先到则不关闭；FinishGithubAutoclose 只改仍为 claimed 的行，
// 不覆盖已取消的结果。
func (s *Service) scanAutoclose(ctx context.Context) {
	due, err := s.st.Q.ClaimDueGithubAutocloses(ctx, s.nowMs())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("github: 认领到期的自动关闭失败", "err", err)
		}
		return
	}
	for _, ac := range due {
		state, outcome := s.runAutoclose(ctx, ac)
		if err := s.st.Q.FinishGithubAutoclose(ctx, db.FinishGithubAutocloseParams{
			State: state, Outcome: &outcome, RepoID: ac.RepoID, Number: ac.Number,
		}); err != nil {
			slog.Warn("github: 写回自动关闭结果失败", "repo", ac.RepoID, "number", ac.Number, "err", err)
		}
	}
}

// runAutoclose 执行一条已认领的自动关闭，返回终态（done / cancelled）与结果。令牌用线程身份；出错也不重试（每条只执行一次）。
// 查询 reactions 期间可能被取消：关闭请求之前必须先赢得 CommitGithubAutoclose。余下的窗口只有关闭请求本身的往返：
// 此时到达的评论与「关闭之后紧接着的评论」无法区分，不再取消。
func (s *Service) runAutoclose(ctx context.Context, ac db.GithubAutoclose) (state, outcome string) {
	repo, err := s.repoFullName(ctx, ac.RepoID)
	if err != nil || repo == "" {
		return "done", "error: repository not bound"
	}
	slot := s.threadSlot(ctx, ac.ThreadID)
	tok, err := s.tokenFor(ctx, slot)
	if err != nil {
		return "done", "error: " + clip(err.Error(), 200)
	}
	auth := "Bearer " + tok
	thumbs := false
	path := fmt.Sprintf("/repos/%s/issues/comments/%d/reactions?content=-1&per_page=100", repo, ac.CommentID)
	err = s.paged(ctx, path, auth, maxCommentPages, func(raw json.RawMessage) error {
		var page []struct {
			User senderPayload `json:"user"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, r := range page {
			if strings.EqualFold(r.User.Login, ac.Author) {
				thumbs = true
			}
		}
		return nil
	})
	switch {
	case isStatus(err, http.StatusNotFound):
		return "cancelled", autocloseCommentGone
	case err != nil:
		return "done", "error: " + clip(err.Error(), 200)
	case thumbs:
		slog.Info("github: issue 作者 👎，取消自动关闭", "repo", repo, "number", ac.Number)
		return "cancelled", autocloseThumbsDown
	}
	if n, err := s.st.Q.CommitGithubAutoclose(ctx, db.CommitGithubAutocloseParams{RepoID: ac.RepoID, Number: ac.Number}); err != nil {
		return "done", "error: " + clip(err.Error(), 200)
	} else if n == 0 {
		// 检查期间已被取消（新评论、外部关闭等）：行已是 cancelled，FinishGithubAutoclose 不会覆盖
		slog.Info("github: 自动关闭在检查期间被取消", "repo", repo, "number", ac.Number)
		return "cancelled", ""
	}
	target := issuePath(repo, ac.Number, "")
	_, err = s.selfWrite(ctx, slot, http.MethodPatch, target, egress.CatIssueUpdate, []string{target},
		map[string]any{"state": "closed", "state_reason": "completed"}, "")
	switch {
	case isStatus(err, http.StatusNotFound):
		return "done", autocloseAlreadyClosed
	case err != nil:
		return "done", "error: " + clip(err.Error(), 200)
	}
	slog.Info("github: 自动关闭 issue", "repo", repo, "number", ac.Number, "thread", ac.ThreadID)
	return "done", autocloseClosed
}
