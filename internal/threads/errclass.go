package threads

import (
	"regexp"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 失败原因原文里能抽出参数的几种形态（docker / provider 的报错、orbd 的 ctl 错误 `code: message`）。
var (
	pullDeniedRe = regexp.MustCompile(`pull access denied for ([^\s,]+)`)
	noImageRe    = regexp.MustCompile(`(?i)no such image:\s*([^\s,]+)`)
	snapshotRe   = regexp.MustCompile(`快照镜像 (\S+) 不存在`)
	exitStatusRe = regexp.MustCompile(`exit status (\d+)`)
	quotedRe     = regexp.MustCompile(`"([^"]+)"`)
)

// classifyFailure 把线程失败原因原文归类，供客户端显示一句人话（原文仍在 reason）。在转 API 时计算：
// orb 失败原因落在 orb_events 里，规则改了历史行也跟着变。detail 是抽出的参数（镜像名、退出码、目录），没有时为空。
// 依据只有原文的前缀与子串：agent_failed 是 orbd 的 ctl 错误 `code: message`，orb_failed 是 provider 的报错原样透传。
func classifyFailure(kind apigen.ThreadErrorKind, reason string) (apigen.ThreadErrorCause, string) {
	switch {
	case strings.Contains(reason, "pull access denied"):
		return apigen.ImagePullDenied, submatch(pullDeniedRe, reason)
	case noImageRe.MatchString(reason):
		return apigen.ImageMissing, submatch(noImageRe, reason)
	case snapshotRe.MatchString(reason):
		return apigen.ImageMissing, submatch(snapshotRe, reason)
	case reason == "no hello within connect timeout":
		return apigen.OrbConnectTimeout, ""
	case strings.HasPrefix(reason, "fork copy:"):
		return apigen.ForkFailed, ""
	case strings.HasPrefix(reason, "dir_not_served:"):
		return apigen.DirNotServed, submatch(quotedRe, reason)
	case strings.HasPrefix(reason, "invalid_params:"):
		return apigen.InvalidParams, ""
	}
	if kind == apigen.AgentFailed {
		if strings.Contains(reason, "deadline exceeded") {
			return apigen.AgentStartTimeout, ""
		}
		return apigen.AgentStartFailed, ""
	}
	if exitStatusRe.MatchString(reason) {
		return apigen.ProviderCommand, submatch(exitStatusRe, reason)
	}
	return apigen.Unknown, ""
}

func submatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

// newThreadError 组装带归类的 ThreadError。
func newThreadError(kind apigen.ThreadErrorKind, reason string) *apigen.ThreadError {
	cause, detail := classifyFailure(kind, reason)
	e := &apigen.ThreadError{Kind: kind, Reason: reason, Cause: &cause}
	if detail != "" {
		e.Detail = &detail
	}
	return e
}
