package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// orb 内的线程协作与线程内定时（M9）：`oryn thread new|send|show|wait` 与 `oryn schedule set|show|pause|resume|rm`
// 经 orbd 本地控制口转成 ctl 请求，调用方线程取 ORYN_THREAD_ID。

// thread wait 的退出码。
const (
	exitWaitIdle    = 0
	exitWaitFailed  = 2 // 调用错误（不在 orb 内、控制口出错、不允许的目标…）
	exitWaitInput   = 3
	exitWaitError   = 4
	exitWaitTimeout = 124
	// defaultWaitTimeout 与 orbd 的缺省一致；maxWaitTimeout 是 orbd 接受的上限。
	defaultWaitTimeout = 30 * time.Minute
	maxWaitTimeout     = 24 * time.Hour
)

// exitCodeError 让 main 以指定退出码结束（错误文本照常打印到 stderr）。
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// usageErr 是退出码 2 的调用错误。
func usageErr(format string, args ...any) error {
	return &exitCodeError{code: 2, err: fmt.Errorf(format, args...)}
}

// inOrb 判定是否在 orb 内且没有显式的 API 凭据：此时线程与定时命令走 orbd 控制口。
func inOrb() bool { return os.Getenv("ORYN_ORB") == "1" && os.Getenv("ORYN_API_TOKEN") == "" }

// errUnavailableInOrb 是 orb 内不提供的命令。
func errUnavailableInOrb(name string) error {
	return usageErr("`oryn %s` 在 orb 内不可用", name)
}

// callerThread 返回调用方线程（ORYN_THREAD_ID）。
func callerThread() (string, error) {
	id := os.Getenv("ORYN_THREAD_ID")
	if id == "" {
		return "", usageErr("ORYN_THREAD_ID 未设置：只能在 Oryn 线程的 agent 里调用")
	}
	return id, nil
}

// sockErr 把控制口错误包装成退出码 2。
func sockErr(err error) error {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return err
	}
	return &exitCodeError{code: 2, err: err}
}

// orbThreadNew 在本项目开一个新线程（自带新 orb）。
func orbThreadNew(ctx context.Context, prompt, title, orbType string) (apigen.LinkCtlThreadInfo, error) {
	caller, err := callerThread()
	if err != nil {
		return apigen.LinkCtlThreadInfo{}, err
	}
	p := apigen.LinkCtlThreadCreateParams{ThreadID: caller, Prompt: prompt}
	if title != "" {
		p.Title = &title
	}
	if orbType != "" {
		p.Type = &orbType
	}
	var info apigen.LinkCtlThreadInfo
	if err := sockCall(ctx, orbsock.MethodThreadNew, p, &info, nil); err != nil {
		return apigen.LinkCtlThreadInfo{}, sockErr(err)
	}
	return info, nil
}

// orbThreadSend 向亲属线程追加一条消息。
func orbThreadSend(ctx context.Context, target, message string, steer bool) error {
	caller, err := callerThread()
	if err != nil {
		return err
	}
	p := apigen.LinkCtlThreadSendParams{ThreadID: caller, Target: target, Message: message}
	if steer {
		p.Steer = &steer
	}
	if err := sockCall(ctx, orbsock.MethodThreadSend, p, nil, nil); err != nil {
		return sockErr(err)
	}
	return nil
}

// orbThreadGet 读取亲属线程的状态与最后一条回复。
func orbThreadGet(ctx context.Context, target string) (apigen.LinkCtlThreadInfo, error) {
	caller, err := callerThread()
	if err != nil {
		return apigen.LinkCtlThreadInfo{}, err
	}
	var info apigen.LinkCtlThreadInfo
	if err := sockCall(ctx, orbsock.MethodThreadGet, apigen.LinkCtlThreadGetParams{ThreadID: caller, Target: target}, &info, nil); err != nil {
		return apigen.LinkCtlThreadInfo{}, sockErr(err)
	}
	return info, nil
}

func printThreadInfo(w io.Writer, info apigen.LinkCtlThreadInfo) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\nTitle\t%s\nState\t%s\nURL\t%s\n", info.ID, info.Title, info.State, info.URL)
	if err := tw.Flush(); err != nil {
		return err
	}
	if info.LastAssistantText != nil && *info.LastAssistantText != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(*info.LastAssistantText, "\n"))
	}
	return nil
}

// threadWait 等目标线程静止：stdout 为最后一条 assistant 文本（--json 为线程信息）。
// 退出码 0 完成 / 3 在等主人回答 / 4 出错 / 124 超时 / 2 调用错误。
func threadWait(ctx context.Context, target string, timeout time.Duration, asJSON bool, stdout, stderr io.Writer) int {
	if !inOrb() {
		fmt.Fprintln(stderr, "oryn: `oryn thread wait` 只能在 orb 内使用；orb 外请用 `oryn thread attach --exit-on-idle`")
		return exitWaitFailed
	}
	caller, err := callerThread()
	if err != nil {
		fmt.Fprintln(stderr, "oryn:", err)
		return exitWaitFailed
	}
	if timeout <= 0 || timeout > maxWaitTimeout {
		fmt.Fprintf(stderr, "oryn: --timeout 须在 (0, %s]\n", maxWaitTimeout)
		return exitWaitFailed
	}
	secs := int((timeout + time.Second - 1) / time.Second)
	var info apigen.LinkCtlThreadInfo
	err = sockCall(ctx, orbsock.MethodThreadWait, orbsock.ThreadWaitParams{ThreadID: caller, Target: target, TimeoutSeconds: secs}, &info, nil)
	var se *orbsock.Error
	switch {
	case errors.As(err, &se) && se.Code == orbsock.CodeTimeout:
		fmt.Fprintf(stderr, "oryn: 等待超时（%s），线程仍未静止\n", timeout)
		return exitWaitTimeout
	case err != nil:
		fmt.Fprintln(stderr, "oryn:", err)
		return exitWaitFailed
	}
	if asJSON {
		_ = printJSONTo(stdout, info)
	} else if info.LastAssistantText != nil {
		fmt.Fprintln(stdout, strings.TrimRight(*info.LastAssistantText, "\n"))
	}
	switch info.State {
	case apigen.ThreadStateAwaitingInput:
		fmt.Fprintln(stderr, "oryn: 线程在等主人回答")
		return exitWaitInput
	case apigen.ThreadStateError:
		fmt.Fprintln(stderr, "oryn: 线程出错")
		return exitWaitError
	}
	return exitWaitIdle
}

// ---- orb 内取亲属线程的改动 ----

const (
	// pullRefPrefix 是 `oryn thread pull` 写入的 ref 前缀：refs/oryn/threads/<id>/head 与 .../snapshot。
	pullRefPrefix = "refs/oryn/threads/"
	// pullTmpPrefix 下是每次 pull 独有的临时 ref（<临时目录名>/{head,snapshot}），发布后即删。
	pullTmpPrefix = "refs/oryn/pull-tmp/"
	// maxPullHave 与 orbd /sync/prepare 接受的 have 上限一致。
	maxPullHave = 256
)

var pullShaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// threadPull 把亲属线程（父线程或自己开的子线程）仓库的改动取进 dir 所在仓库：控制面在对方 orb 上把工作区快照成提交、
// 按本仓库已有的提交打包缺少的对象并写到本仓库 git 目录下的临时文件，这里 fetch 后写
// refs/oryn/threads/<target>/head（对方已提交的工作）与 .../snapshot（另含未提交改动；没有时同 head）。
// 不动工作区、index 与当前分支；同一线程再取一次覆盖这两个 ref。
func threadPull(ctx context.Context, dir, target string) (apigen.LinkCtlThreadPullResult, error) {
	var res apigen.LinkCtlThreadPullResult
	if !inOrb() {
		return res, usageErr("`oryn thread pull` 只能在 orb 内使用；本机取线程的改动用 `oryn sync`")
	}
	caller, err := callerThread()
	if err != nil {
		return res, err
	}
	top, err := gitIn(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return res, usageErr("%s 不在 git 仓库里", dir)
	}
	ns := pullRefPrefix + target + "/"
	if _, err := gitIn(ctx, top, "check-ref-format", ns+"head"); err != nil {
		return res, usageErr("线程 id %q 无效", target)
	}
	gitDir, err := gitIn(ctx, top, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return res, err
	}
	have, err := pullHave(ctx, top, ns)
	if err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp(gitDir, "oryn-pull-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "changes.bundle")
	// 本次调用独有的临时 ref（同一目标的并发 pull 互不覆盖），结束时无论成败都删掉
	tmpRefs := pullTmpPrefix + filepath.Base(tmp) + "/"
	defer func() {
		for _, ref := range []string{tmpRefs + "head", tmpRefs + "snapshot"} {
			_, _ = gitIn(context.WithoutCancel(ctx), top, "update-ref", "-d", ref)
		}
	}()
	p := apigen.LinkCtlThreadPullParams{ThreadID: caller, Target: target, Have: have, BundlePath: bundle}
	if err := sockCall(ctx, orbsock.MethodThreadPull, p, &res, nil); err != nil {
		var se *orbsock.Error
		if errors.As(err, &se) && se.Code == "sync_no_common" {
			return res, sockErr(fmt.Errorf("%w\n提示：两边的仓库没有共同的提交；先 `git fetch origin`，或确认当前目录是同一个项目的仓库", err))
		}
		return res, sockErr(err)
	}
	if !pullShaRe.MatchString(res.Head) || !pullShaRe.MatchString(res.Snapshot) {
		return res, fmt.Errorf("控制面返回的提交无效（head %q，snapshot %q）", res.Head, res.Snapshot)
	}
	if res.Bundled {
		// 带 fsck 取进本次的临时 ref，校验后再按返回的 sha 发布：bundle 里没有的 ref（对象本地已有）也能对上
		cmd := exec.CommandContext(ctx, "git", "-C", top, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
			"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--quiet", bundle, "+refs/oryn/sync/*:"+tmpRefs+"*")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return res, fmt.Errorf("git fetch bundle 失败: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	for _, c := range []string{res.Head, res.Snapshot} {
		if _, err := gitIn(ctx, top, "cat-file", "-e", c+"^{commit}"); err != nil {
			return res, fmt.Errorf("本仓库缺少对方的提交 %s", c[:7])
		}
	}
	if res.Snapshot != res.Head {
		if parents, err := gitIn(ctx, top, "rev-parse", res.Snapshot+"^@"); err != nil || parents != res.Head {
			return res, fmt.Errorf("快照 %s 不是 head %s 的直接子提交", res.Snapshot[:7], res.Head[:7])
		}
	}
	return res, publishPull(ctx, top, ns, res.Head, res.Snapshot)
}

// publishPull 在一个 ref 事务里写 ns 下的 head 与 snapshot：要么都更新，要么都不变（并发 pull 不会留下一新一旧）。
func publishPull(ctx context.Context, top, ns, head, snapshot string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", top, "update-ref", "--stdin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader("update " + ns + "head " + head + "\nupdate " + ns + "snapshot " + snapshot + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写 %shead 与 %ssnapshot 失败（两者都未改动）: %w\n%s", ns, ns, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pullHave 列出本仓库已有、对方可能也有的提交（去重，至多 maxPullHave）：HEAD、上次取回的 ref、远端分支、本地分支。
func pullHave(ctx context.Context, top, ns string) ([]string, error) {
	have := []string{}
	seen := map[string]bool{}
	add := func(sha string) {
		if pullShaRe.MatchString(sha) && !seen[sha] && len(have) < maxPullHave {
			seen[sha] = true
			have = append(have, sha)
		}
	}
	if head, err := gitIn(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		add(head)
	}
	refs, err := gitIn(ctx, top, "for-each-ref", "--sort=-committerdate", "--format=%(objectname)",
		strings.TrimSuffix(ns, "/"), "refs/remotes", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("列出本仓库的分支失败: %w", err)
	}
	for sha := range strings.SplitSeq(refs, "\n") {
		add(sha)
	}
	return have, nil
}

// printThreadPull 打印取回的 ref 与下一步可用的 git 命令。
func printThreadPull(w io.Writer, target string, res apigen.LinkCtlThreadPullResult) error {
	ns := pullRefPrefix + target + "/"
	from := "分离的 HEAD"
	if res.Branch != nil {
		from = "分支 " + *res.Branch
	}
	fmt.Fprintf(w, "已取回线程 %s 的改动（%s）：\n", target, from)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  %shead\t%s\t已提交的工作\n", ns, res.Head[:7])
	if res.Dirty {
		fmt.Fprintf(tw, "  %ssnapshot\t%s\t另含未提交的改动（一个快照提交）\n", ns, res.Snapshot[:7])
	} else {
		fmt.Fprintf(tw, "  %ssnapshot\t%s\t没有未提交的改动，同 head\n", ns, res.Snapshot[:7])
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "看改了什么：git log --oneline HEAD..%[1]shead；git diff HEAD...%[1]ssnapshot\n并入：git merge %[1]shead（或 cherry-pick）；未提交的部分在 snapshot 上\n", ns)
	return err
}

// ---- orb 内的线程内定时 ----

// orbScheduleSet 给调用方线程设置定时（只接受线程内定时的字段）。
func orbScheduleSet(ctx context.Context, in apigen.ScheduleInput) (apigen.Schedule, error) {
	caller, err := callerThread()
	if err != nil {
		return apigen.Schedule{}, err
	}
	var res apigen.LinkCtlScheduleResult
	if err := sockCall(ctx, orbsock.MethodScheduleSet, apigen.LinkCtlSchedulePutParams{ThreadID: caller, Input: in}, &res, nil); err != nil {
		return apigen.Schedule{}, sockErr(err)
	}
	if res.Schedule == nil {
		return apigen.Schedule{}, errors.New("orbd 没有返回定时")
	}
	return *res.Schedule, nil
}

// orbScheduleGet 返回调用方线程的定时；没有时为 nil。
func orbScheduleGet(ctx context.Context) (*apigen.Schedule, error) {
	caller, err := callerThread()
	if err != nil {
		return nil, err
	}
	var res apigen.LinkCtlScheduleResult
	if err := sockCall(ctx, orbsock.MethodScheduleGet, apigen.LinkCtlScheduleParams{ThreadID: caller}, &res, nil); err != nil {
		return nil, sockErr(err)
	}
	return res.Schedule, nil
}

// orbScheduleEnable 暂停 / 恢复调用方线程的定时。
func orbScheduleEnable(ctx context.Context, enabled bool) (apigen.Schedule, error) {
	caller, err := callerThread()
	if err != nil {
		return apigen.Schedule{}, err
	}
	var res apigen.LinkCtlScheduleResult
	if err := sockCall(ctx, orbsock.MethodScheduleEnable, apigen.LinkCtlScheduleEnableParams{ThreadID: caller, Enabled: enabled}, &res, nil); err != nil {
		return apigen.Schedule{}, sockErr(err)
	}
	if res.Schedule == nil {
		return apigen.Schedule{}, errors.New("orbd 没有返回定时")
	}
	return *res.Schedule, nil
}

// orbScheduleDelete 删除调用方线程的定时。
func orbScheduleDelete(ctx context.Context) error {
	caller, err := callerThread()
	if err != nil {
		return err
	}
	if err := sockCall(ctx, orbsock.MethodScheduleDelete, apigen.LinkCtlScheduleParams{ThreadID: caller}, nil, nil); err != nil {
		return sockErr(err)
	}
	return nil
}
