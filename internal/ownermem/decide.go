package ownermem

// decide.go：用判断模型（Gemini Decisions API，默认 gemini-decision-1）给召回的候选打分重排（R12）。
// 提示词是设计文档 §7.2 的省 token 版：规则、问题与档位定义放在共享 input（一次请求只计费一次），
// 每条候选一个 score 题，只带短标签。分数是 0–3 的期望分。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	// decideCandidates 是送去打分的候选数（Hindsight 重排关闭，按 RRF 顺序取前 50）。
	decideCandidates = 50
	// recallKeep 是最多给出的条数。
	recallKeep = 20
	// directScore、partialScore 是分档阈值：≥2.0 正常给出，1.5–2.0 标注部分相关，<1.5 不给。
	directScore   = 2.0
	partialScore  = 1.5
	decideTimeout = 30 * time.Second
	maxDecideBody = 1 << 20
)

// decideInput 是共享 input（{today}、{query} 处替换）。
const decideInput = `你在为 Oryn 的全局助手挑选记忆。记忆库里的事实是从主人的历史对话中抽取的；主人是 Oryn 的使用者，也是现在向助手提问的人。助手会把你排在前面的事实当作背景来回答问题，所以排在前面的必须是能直接、正确地回答这个问题的事实。

今天：{today}
主人的问题：{query}

判断每条候选事实时，同时考虑：
1. 对象是否一致。先弄清问题问的是谁、是哪个对象。问题里的"我"指主人本人。问主人本人的属性（所在地、时区、身份、设备、经历、习惯）时，只有明确在说主人本人的事实才算数；属于某个项目、任务、服务或 agent 的设置与参数（例如"某个定时任务的时间基准是新加坡时间"）不是主人的个人属性，不能拿来回答。事实里出现"该任务""这个项目"等指代不明的说法时，不能假定它说的就是问题里的对象。
2. 是否直接回答。事实本身要包含回答所需的信息；只是同一话题、同一项目下的另一件事，不算有用。
3. 是否是当前状态。问"现在""最近""进展""完成了吗"时，同一件事较新的状态比旧计划、旧进展更有用；"打算""准备""计划"不等于已经完成，一个阶段完成也不代表后续阶段完成。事实后面的 When 是它发生或被提到的时间。
4. 是否是主人的意思。主人明确说过的话、做出的决定，比 agent 提出而主人没有确认的方案更有用。
5. 场景是否一致。主人在某个具体场景里的决定只在那个场景成立，不能拿来回答关于一般习惯或其他场景的问题。

评分档位：
0. 无关：和问题说的不是同一件事。
1. 不能用：话题相近，但说的是另一件事；或者对象不一致（例如问的是主人本人，事实说的是某个任务、项目或 agent 的设置）；或者场景不一致。
2. 部分有用：说的是同一个对象、同一件事，但只是背景、旧状态、未确认的方案，或者只能回答问题的一小部分。
3. 直接可用：对象一致、场景一致、是当前状态，能直接回答问题或问题的关键部分。`

const decideQuestion = "候选事实：{fact}\n\n这条事实对正确回答主人的问题有多大用处？"

var decideCriteria = []string{"无关", "不能用", "部分有用", "直接可用"}

// errDecideUnset 是判断模型接口没有配置。
var errDecideUnset = fmt.Errorf("判断模型接口未配置")

// decideScores 一次请求给全部候选打分，返回与 facts 同序的期望分。
func (s *Service) decideScores(ctx context.Context, query string, facts []string, now time.Time) ([]float64, error) {
	ep, ok := s.decideEndpoint(ctx)
	if !ok {
		return nil, errDecideUnset
	}
	questions := make(map[string]any, len(facts))
	for i, f := range facts {
		questions[fmt.Sprintf("f%d", i)] = map[string]any{
			"type": "score", "instructions": strings.Replace(decideQuestion, "{fact}", f, 1), "criteria": decideCriteria,
		}
	}
	input := strings.NewReplacer("{today}", now.Format("2006-01-02"), "{query}", query).Replace(decideInput)
	model := ep.model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	raw, _ := json.Marshal(map[string]any{"model": model, "input": input, "questions": questions})
	ctx, cancel := context.WithTimeout(ctx, decideTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ep.base, "/")+"/v1beta/decisions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", ep.key)
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDecideBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("判断模型返回 %d：%s", resp.StatusCode, truncate(string(body), 300))
	}
	var out struct {
		Answers map[string]struct {
			Score *float64 `json:"score"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析判断模型应答失败：%w", err)
	}
	scores := make([]float64, len(facts))
	for i := range facts {
		a, ok := out.Answers[fmt.Sprintf("f%d", i)]
		if !ok || a.Score == nil {
			return nil, fmt.Errorf("判断模型应答缺少 f%d 的分数", i)
		}
		scores[i] = *a.Score
	}
	return scores, nil
}

// rankedFact 是重排后给出的一条事实；Partial 表示 1.5–2.0 档（部分相关，可能是旧状态或属于别的对象）。
type rankedFact struct {
	Fact    hsFact
	Score   float64
	Partial bool
}

// rerankResult 是重排结果。Reranked 为假时按 RRF 顺序给出（判断模型未配置或调用失败，Err 说明原因）；
// NoDirect 表示没有一条 ≥2.0（记忆里没有能直接回答的条目）。
type rerankResult struct {
	Items    []rankedFact
	Reranked bool
	NoDirect bool
	Err      error
}

// rerank 对按 RRF 排好的候选打分分档：≥2.0 正常、1.5–2.0 标注、<1.5 丢弃，按分数降序（同分保持原序），最多 20 条。
// 判断模型不可用时按 RRF 顺序给出前 20 条。
func (s *Service) rerank(ctx context.Context, query string, cands []hsFact) rerankResult {
	if len(cands) > decideCandidates {
		cands = cands[:decideCandidates]
	}
	fallback := func(err error) rerankResult {
		out := rerankResult{Err: err}
		for _, f := range cands[:min(len(cands), recallKeep)] {
			out.Items = append(out.Items, rankedFact{Fact: f})
		}
		return out
	}
	if len(cands) == 0 {
		return rerankResult{Reranked: true, NoDirect: true}
	}
	texts := make([]string, len(cands))
	for i, f := range cands {
		texts[i] = f.Text
	}
	scores, err := s.decideScores(ctx, query, texts, s.cfg.Now())
	if err != nil {
		return fallback(err)
	}
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	out := rerankResult{Reranked: true, NoDirect: true}
	for _, i := range order {
		if scores[i] < partialScore || len(out.Items) == recallKeep {
			break
		}
		direct := scores[i] >= directScore
		if direct {
			out.NoDirect = false
		}
		out.Items = append(out.Items, rankedFact{Fact: cands[i], Score: scores[i], Partial: !direct})
	}
	return out
}
