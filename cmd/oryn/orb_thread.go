package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
