package artifacts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/artifacts/sample"
)

// sample 能力（M14 步骤 7.5）：把页面的问题交给用户自己的模型，在一个没有任何工具的一次性会话里回答
// （执行端见 internal/artifacts/sample：控制面自己运行钉住的 Core，不经过 orb，所以任何 artifact 都能用）。
// 需要用户同意（consent.go），最新版本必须声明 sample，声明了它的页面用严格 CSP（policy.go）。
//
// 只有一个流式操作 ask(prompt, {format: "text" | "json", images}?)：
//   - prompt 是非空字符串，不超过 MaxSamplePromptChars 个字符；
//   - images 最多 MaxSampleImages 张，每张是 `data:image/(png|jpeg|gif|webp);base64,…`，解码后不超过 MaxSampleImageBytes；
//   - format "json" 时在提示前加一句「只回复一个 JSON 值」，答案先找 ```json 围栏再解析，解析失败回 failed。
//
// 流事件：`{type: "text", text}`（回答的增量）与最后一条 `{type: "answer", text, json?}`。
// 限额：每个 artifact 同时 MaxSampleRunning 个、每分钟 MaxSamplePerMinute 个（被拒的不算）；全局进程数由执行端限制。
// 可用条件（不满足时 use 回 null、stream 回 unsupported）：接了执行端且 coordinator.omp_path 指向的文件存在。

// sample 能力的上限。
const (
	MaxSamplePromptChars = 100_000
	MaxSampleImages      = 4
	MaxSampleImageBytes  = 5 << 20
	MaxSampleRunning     = 2
	MaxSamplePerMinute   = 10
)

// sampleJSONPrefix 是 format: "json" 时加在提示前面的要求。
const sampleJSONPrefix = "只回复一个 JSON 值，不要任何其它文字。\n\n"

var (
	sampleImageRe = regexp.MustCompile(`^data:(image/(?:png|jpeg|gif|webp));base64,`)
	// sampleFenceRe 取第一个 ```json（或不标语言的 ```）围栏里的内容。
	sampleFenceRe = regexp.MustCompile("(?s)```(?:json)?[ \\t]*\\r?\\n(.*?)```")
)

// sampleSet 是 sample 能力的进程内状态（零值可用）。
type sampleSet struct {
	mu      sync.Mutex
	sampler *sample.Sampler
	// busy：artifact id → 进行中的问题数；starts：artifact id → 最近一分钟内开始的问题的时间。
	busy   map[string]int
	starts map[string][]time.Time
}

// AttachSampler 接上 sample 的执行端；不接时 sample 能力一律不可用。须在开始服务之前调用。
func (s *Service) AttachSampler(sp *sample.Sampler) {
	s.samples.mu.Lock()
	s.samples.sampler = sp
	s.samples.mu.Unlock()
}

// SampleAvailable 报告 sample 能力当前是否可用（接了执行端且钉住的 Core 在）；s 为 nil 时为假。
func (s *Service) SampleAvailable() bool {
	return s != nil && s.sampler().Available()
}

func (s *Service) sampler() *sample.Sampler {
	s.samples.mu.Lock()
	defer s.samples.mu.Unlock()
	return s.samples.sampler
}

// sampleSlot 占一个 artifact 的提问名额：同时进行的满了或这一分钟的次数用完回 rate_limited。成功时返回释放函数。
func (s *Service) sampleSlot(id string) (func(), error) {
	now := s.now()
	s.samples.mu.Lock()
	defer s.samples.mu.Unlock()
	if s.samples.busy[id] >= MaxSampleRunning {
		return nil, opErr(CodeRateLimited, "同时进行的提问太多了（每个 artifact 最多 %d 个），等前面的回答完再问。", MaxSampleRunning)
	}
	recent := s.samples.starts[id][:0]
	for _, at := range s.samples.starts[id] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= MaxSamplePerMinute {
		s.samples.starts[id] = recent
		return nil, opErr(CodeRateLimited, "提问太频繁了（每个 artifact 每分钟最多 %d 个），稍后再问。", MaxSamplePerMinute)
	}
	if s.samples.busy == nil {
		s.samples.busy = map[string]int{}
		s.samples.starts = map[string][]time.Time{}
	}
	s.samples.starts[id] = append(recent, now)
	s.samples.busy[id]++
	return func() {
		s.samples.mu.Lock()
		defer s.samples.mu.Unlock()
		if s.samples.busy[id]--; s.samples.busy[id] <= 0 {
			delete(s.samples.busy, id)
		}
		// 一分钟之前的记录不再影响限额
		if starts := s.samples.starts[id]; len(starts) > 0 && s.now().Sub(starts[len(starts)-1]) >= time.Minute {
			delete(s.samples.starts, id)
		}
	}, nil
}

// sampleCap 是 sample 能力。
func (h *Host) sampleCap() runtimeCap {
	return runtimeCap{
		declared:  true,
		available: func(context.Context, *runtimeCall) bool { return h.s.SampleAvailable() },
		streams:   map[string]streamOp{"ask": h.sampleAsk},
	}
}

// sampleOpts 是 ask 的第二个位置参数。
type sampleOpts struct {
	Format string   `json:"format"`
	Images []string `json:"images"`
}

// sampleQuestion 是校验过的一次提问。
type sampleQuestion struct {
	prompt string
	json   bool
	images []sample.Image
}

// parseSampleArgs 校验 ask 的参数。
func parseSampleArgs(args json.RawMessage) (sampleQuestion, error) {
	list, err := posArgs(args, 1, 2)
	if err != nil {
		return sampleQuestion{}, err
	}
	prompt, err := stringArg(list, 0, "prompt")
	if err != nil {
		return sampleQuestion{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return sampleQuestion{}, opErr(CodeInvalid, "prompt 不能是空的。")
	}
	if n := utf8.RuneCountInString(prompt); n > MaxSamplePromptChars {
		return sampleQuestion{}, opErr(CodeTooLarge, "prompt 有 %d 个字符，超过了 %d。", n, MaxSamplePromptChars)
	}
	var o sampleOpts
	if err := optsArg(list, 1, &o); err != nil {
		return sampleQuestion{}, err
	}
	q := sampleQuestion{prompt: prompt}
	switch o.Format {
	case "", "text":
	case "json":
		q.json = true
		q.prompt = sampleJSONPrefix + prompt
	default:
		return sampleQuestion{}, opErr(CodeInvalid, "format 只能是 text 或 json。")
	}
	if len(o.Images) > MaxSampleImages {
		return sampleQuestion{}, opErr(CodeInvalid, "一次最多 %d 张图片，给了 %d 张。", MaxSampleImages, len(o.Images))
	}
	for i, url := range o.Images {
		m := sampleImageRe.FindStringSubmatch(url)
		if m == nil {
			return sampleQuestion{}, opErr(CodeInvalid, "images[%d] 要是 data:image/png、jpeg、gif 或 webp 的 base64 地址。", i)
		}
		data := url[len(m[0]):]
		if base64.StdEncoding.DecodedLen(len(data)) > MaxSampleImageBytes+2 {
			return sampleQuestion{}, opErr(CodeTooLarge, "images[%d] 超过了 %d MiB。", i, MaxSampleImageBytes>>20)
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return sampleQuestion{}, opErr(CodeInvalid, "images[%d] 的 base64 不合法。", i)
		}
		if len(raw) > MaxSampleImageBytes {
			return sampleQuestion{}, opErr(CodeTooLarge, "images[%d] 超过了 %d MiB。", i, MaxSampleImageBytes>>20)
		}
		q.images = append(q.images, sample.Image{MimeType: m[1], Data: data})
	}
	return q, nil
}

// parseSampleJSON 从回答里取 JSON 值：有 ```json 围栏取围栏里的，否则取整个回答。
func parseSampleJSON(answer string) (json.RawMessage, bool) {
	text := answer
	if m := sampleFenceRe.FindStringSubmatch(answer); m != nil {
		text = m[1]
	}
	text = strings.TrimSpace(text)
	if text == "" || !json.Valid([]byte(text)) {
		return nil, false
	}
	return json.RawMessage(text), true
}

// sampleAsk 执行 ask：回答的增量作为 text 事件送出，最后送一条 answer。
func (h *Host) sampleAsk(ctx context.Context, rc *runtimeCall, emit func(event any) error) error {
	q, err := parseSampleArgs(rc.args)
	if err != nil {
		return err
	}
	sp := h.s.sampler()
	if !sp.Available() {
		return opErr(codeUnsupported, "现在不能向模型提问（控制面没有可用的 Core）。")
	}
	release, err := h.s.sampleSlot(rc.art.ID)
	if err != nil {
		return err
	}
	defer release()
	answer, err := sp.Ask(ctx, q.prompt, q.images, func(text string) error {
		return emit(map[string]any{"type": "text", "text": text})
	})
	if err != nil {
		return sampleErr(ctx, rc.art.ID, err)
	}
	ev := map[string]any{"type": "answer", "text": answer}
	if q.json {
		v, ok := parseSampleJSON(answer)
		if !ok {
			return opErr(codeFailed, "模型的回答不是一个合法的 JSON 值。可以换个问法再试。")
		}
		ev["json"] = v
	}
	return emit(ev)
}

// sampleErr 把执行端的错误换成页面看的码。
func sampleErr(ctx context.Context, artifactID string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var me *sample.ModelError
	switch {
	case errors.As(err, &me):
		return opErr(codeFailed, "模型没有给出回答：%s", me.Message)
	case errors.Is(err, sample.ErrTimeout):
		return opErr(codeFailed, "模型没有在时限内回答完，已经放弃。可以把问题拆小一些再问。")
	}
	if _, ok := AsOpError(err); ok {
		return err
	}
	slog.Warn("artifacts: sample 提问失败", "artifact", artifactID, "err", err)
	return opErr(codeFailed, "提问失败了，稍后再试。")
}
