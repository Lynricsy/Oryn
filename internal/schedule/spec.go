package schedule

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // 宿主没有 zoneinfo 时也能解析 IANA 时区
	"unicode"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 定时种类、模式与重叠策略（与 apigen 的枚举同值）。
const (
	KindCron  = "cron"
	KindEvery = "every"
	KindOnce  = "once"

	ModeThread    = "thread"
	ModeNewThread = "new_thread"

	OverlapSkip  = "skip"
	OverlapAllow = "allow"
)

const (
	minEvery        = time.Minute
	maxEvery        = 8784 * time.Hour
	onceGrace       = 60 * time.Second
	maxPromptRunes  = 20000
	maxNameRunes    = 80
	defaultKeepLast = 5
	maxKeepLast     = 50
	// maxRoleLen 是 role 名的长度上限（字节），与新建线程的校验一致。
	maxRoleLen = 128
	// maxMissedScan 是统计被合并的错过次数时的迭代上限。
	maxMissedScan = 1000
	// previewCount 是预览给出的触发次数。
	previewCount = 5
	// hostProvider 是协调宿主的内置 provider，不能用来跑定时新建的工作线程。
	hostProvider = "@host"
	// 运行前闸门：命令长度（字符）、执行上限（秒）与主机名长度（同 SSH 主机名的上限）。
	maxGateCommandRunes = 8000
	defaultGateTimeout  = 60
	maxGateTimeout      = 600
	maxGateHostLen      = 64
)

// cronMacros 是允许的 cron 宏（不含 @every / @reboot）。
var cronMacros = map[string]bool{
	"@hourly": true, "@daily": true, "@midnight": true, "@weekly": true, "@monthly": true, "@yearly": true, "@annually": true,
}

// NextAfter 返回 kind/spec 在 loc 时区里严格晚于 after 的下一次触发；没有下一次（once、cron 永不触发）时第二个返回值为 false。
// spec 须已通过 Validate。
func NextAfter(kind, spec string, loc *time.Location, after time.Time) (time.Time, bool) {
	switch kind {
	case KindCron:
		sched, err := cron.ParseStandard(spec)
		if err != nil {
			return time.Time{}, false
		}
		next := sched.Next(after.In(loc))
		return next, !next.IsZero()
	case KindEvery:
		d, err := time.ParseDuration(spec)
		if err != nil || d <= 0 {
			return time.Time{}, false
		}
		return after.Add(d), true
	default:
		return time.Time{}, false
	}
}

// countMissed 返回 (scheduledFor, now] 内 scheduledFor 之后的触发数（迭代上限 maxMissedScan）。
func countMissed(kind, spec string, loc *time.Location, scheduledFor, now time.Time) int {
	if kind == KindEvery {
		d, err := time.ParseDuration(spec)
		if err != nil || d <= 0 || !now.After(scheduledFor) {
			return 0
		}
		return int(min(int64(now.Sub(scheduledFor)/d), maxMissedScan))
	}
	n := 0
	t := scheduledFor
	for n < maxMissedScan {
		next, ok := NextAfter(kind, spec, loc, t)
		if !ok || next.After(now) {
			break
		}
		n++
		t = next
	}
	return n
}

// spec 返回 Input 在库里的 spec 列：cron 表达式、规范化的时长串，once 为空。
func (in Input) spec() string {
	switch in.Kind {
	case KindCron:
		return in.Cron
	case KindEvery:
		return in.Every
	}
	return ""
}

// Validate 校验并规范化定义：时间字段（validateTime）与模式字段。不查库：线程 / 项目 / Space 是否存在、
// 同名冲突与目标不可改由 Put 判断。
func Validate(in Input, now time.Time) (Input, *time.Location, error) {
	norm, loc, err := validateTime(in, now)
	if err != nil {
		return Input{}, nil, err
	}
	if strings.TrimSpace(norm.Prompt) == "" {
		return Input{}, nil, invalid("prompt is required")
	}
	if utf8.RuneCountInString(norm.Prompt) > maxPromptRunes {
		return Input{}, nil, invalid("prompt must be at most %d characters", maxPromptRunes)
	}
	norm.Name = strings.TrimSpace(norm.Name)
	if utf8.RuneCountInString(norm.Name) > maxNameRunes {
		return Input{}, nil, invalid("name must be at most %d characters", maxNameRunes)
	}
	switch norm.Mode {
	case "", ModeThread:
		if norm.ProjectID != "" || norm.SpaceID != "" || norm.Role != "" || norm.ThinkingLevel != "" || norm.Provider != "" ||
			norm.OrbType != "" || norm.Overlap != "" || norm.Model != nil || norm.KeepLast != nil {
			return Input{}, nil, invalid("projectId, spaceId, role, model, thinkingLevel, provider, orbType, overlap and keepLast are only for mode new_thread")
		}
		if norm.HandToAssistant != nil && *norm.HandToAssistant {
			return Input{}, nil, invalid("handToAssistant is only for mode new_thread")
		}
	case ModeNewThread:
		if norm.ThreadID != "" {
			return Input{}, nil, invalid("threadId is only for mode thread")
		}
		if norm.Name == "" {
			return Input{}, nil, invalid("name is required for mode new_thread")
		}
		switch norm.Overlap {
		case "":
			norm.Overlap = OverlapSkip
		case OverlapSkip, OverlapAllow:
		default:
			return Input{}, nil, invalid("overlap must be skip or allow")
		}
		keep := defaultKeepLast
		if norm.KeepLast != nil {
			keep = *norm.KeepLast
		}
		if keep < 1 || keep > maxKeepLast {
			return Input{}, nil, invalid("keepLast must be between 1 and %d", maxKeepLast)
		}
		norm.KeepLast = &keep
		if norm.Provider == hostProvider {
			return Input{}, nil, invalid("provider %s cannot run scheduled threads", hostProvider)
		}
		if norm.Model != nil && (strings.TrimSpace(norm.Model.Provider) == "" || strings.TrimSpace(norm.Model.ID) == "") {
			return Input{}, nil, invalid("model must have provider and id")
		}
		norm.Role = strings.TrimSpace(norm.Role)
		if len(norm.Role) > maxRoleLen || strings.ContainsFunc(norm.Role, unicode.IsControl) {
			return Input{}, nil, invalid("role must be at most %d bytes without control characters", maxRoleLen)
		}
		if norm.Role != "" && norm.Model != nil {
			return Input{}, nil, invalid("role and model are mutually exclusive")
		}
	default:
		return Input{}, nil, invalid("mode must be thread or new_thread")
	}
	if norm.Gate != nil {
		g, err := validGate(*norm.Gate)
		if err != nil {
			return Input{}, nil, err
		}
		norm.Gate = &g
	}
	return norm, loc, nil
}

// validGate 校验并规范化运行前闸门：给了主机名时去首尾空白后须非空且不含空白与控制字符（省略 = 本机沙箱），
// 命令非空白且不超过 8000 字符，执行上限缺省 60 秒、须在 1–600 秒。执行位置是否可用由保存时的 checkGate 判断。
func validGate(g apigen.ScheduleGate) (apigen.ScheduleGate, error) {
	if g.Host != nil {
		h := strings.TrimSpace(*g.Host)
		if h == "" || len(h) > maxGateHostLen || strings.ContainsFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return g, invalid("gate.host must be an SSH host name (at most %d bytes, no spaces) or omitted", maxGateHostLen)
		}
		g.Host = &h
	}
	if strings.TrimSpace(g.Command) == "" || utf8.RuneCountInString(g.Command) > maxGateCommandRunes {
		return g, invalid("gate.command must be 1–%d characters", maxGateCommandRunes)
	}
	sec := defaultGateTimeout
	if g.TimeoutSeconds != nil {
		sec = *g.TimeoutSeconds
	}
	if sec < 1 || sec > maxGateTimeout {
		return g, invalid("gate.timeoutSeconds must be between 1 and %d", maxGateTimeout)
	}
	g.TimeoutSeconds = &sec
	return g, nil
}

// validateTime 只校验时间字段（预览用）：种类、表达式 / 间隔 / 时刻、时区、截止时间。
func validateTime(in Input, now time.Time) (Input, *time.Location, error) {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil || in.Timezone == "Local" {
		return Input{}, nil, invalid("unknown timezone %q", in.Timezone)
	}
	switch in.Kind {
	case KindCron:
		expr := strings.TrimSpace(in.Cron)
		if expr == "" {
			return Input{}, nil, invalid("cron is required for kind cron")
		}
		if strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ=") {
			return Input{}, nil, invalid("cron must not carry a TZ= prefix; use timezone")
		}
		if strings.HasPrefix(expr, "@") {
			if !cronMacros[expr] {
				return Input{}, nil, invalid("unsupported cron macro %q", expr)
			}
		} else {
			fields := strings.Fields(expr)
			if len(fields) != 5 {
				return Input{}, nil, invalid("cron must have exactly 5 fields")
			}
			expr = strings.Join(fields, " ")
		}
		if _, err := cron.ParseStandard(expr); err != nil {
			return Input{}, nil, invalid("invalid cron expression: %v", err)
		}
		if _, ok := NextAfter(KindCron, expr, loc, now); !ok {
			return Input{}, nil, invalid("cron expression never fires")
		}
		in.Cron, in.Every, in.At = expr, "", nil
	case KindEvery:
		d, err := time.ParseDuration(strings.TrimSpace(in.Every))
		if err != nil {
			return Input{}, nil, invalid("invalid every %q", in.Every)
		}
		if d < minEvery || d > maxEvery {
			return Input{}, nil, invalid("every must be between 1m and 8784h")
		}
		if d%time.Second != 0 {
			return Input{}, nil, invalid("every must be a whole number of seconds")
		}
		in.Every, in.Cron, in.At = d.String(), "", nil
	case KindOnce:
		if in.At == nil {
			return Input{}, nil, invalid("at is required for kind once")
		}
		if *in.At < now.Add(-onceGrace).UnixMilli() {
			return Input{}, nil, invalid("at is in the past")
		}
		in.Cron, in.Every = "", ""
	default:
		return Input{}, nil, invalid("kind must be cron, every or once")
	}
	if in.EndsAt != nil && *in.EndsAt <= now.UnixMilli() {
		return Input{}, nil, invalid("endsAt must be in the future")
	}
	return in, loc, nil
}

// firstRun 返回新定义的首个 next_run_at（毫秒）；没有（截止时间之前不会触发）时为 nil。
func firstRun(in Input, loc *time.Location, now time.Time) *int64 {
	var next int64
	if in.Kind == KindOnce {
		next = *in.At
	} else {
		t, ok := NextAfter(in.Kind, in.spec(), loc, now)
		if !ok {
			return nil
		}
		next = t.UnixMilli()
	}
	if in.EndsAt != nil && next > *in.EndsAt {
		return nil
	}
	return &next
}

func invalid(format string, args ...any) *Error {
	return &Error{Code: CodeInvalidSchedule, Message: fmt.Sprintf(format, args...)}
}
