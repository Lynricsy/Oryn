//go:build ownermem_eval

package ownermem

// eval_test.go：M17 R23 真实数据回归。对真实 Hindsight 与判断模型运行 testdata/eval/cases.json 的用例，
// 输出每题的排名、分数与通过与否。默认测试不运行（需要构建标签 ownermem_eval 与下面的环境变量）：
//
//	ORYN_OWNERMEM_EVAL_URL          Hindsight 根地址（必需）
//	ORYN_OWNERMEM_EVAL_KEY          Hindsight API key（必需）
//	ORYN_OWNERMEM_EVAL_BANK         默认 bank（缺省 omem-real）
//	ORYN_OWNERMEM_EVAL_DECIDE_KEY   Decisions API key（必需）
//	ORYN_OWNERMEM_EVAL_DECIDE_URL   缺省 https://generativelanguage.googleapis.com
//	ORYN_OWNERMEM_EVAL_DECIDE_MODEL 缺省 gemini-decision-1
//
//	go test -tags ownermem_eval ./internal/ownermem/ -run Eval -v

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

type evalCase struct {
	Name     string     `json:"name"`
	Bank     string     `json:"bank"`
	Query    string     `json:"query"`
	Relevant [][]string `json:"relevant"`
	Trap     [][]string `json:"trap"`
	MustNot  []string   `json:"mustNot"`
	// Expect：top1 = 第一条是相关事实且 ≥2.0；noDirect = 没有 ≥2.0 的条目；partial = 没有 ≥2.0、相关事实在 1.5–2.0 档；
	// clean = 召回的前 50 条候选里没有 mustNot 的说法（写入阶段的负例）。
	Expect string `json:"expect"`
}

type evalTopic struct {
	Name         string   `json:"name"`
	MustNotMatch []string `json:"mustNotMatch"`
	RequireWith  []struct {
		Trigger string   `json:"trigger"`
		Also    []string `json:"also"`
	} `json:"requireWith"`
}

// matches 是「与-或」匹配：每一组至少命中一个子串；没有组时为假。
func matches(text string, groups [][]string) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		hit := false
		for _, s := range g {
			hit = hit || strings.Contains(text, s)
		}
		if !hit {
			return false
		}
	}
	return true
}

type evalEnv struct {
	base, key, bank string
	svc             *Service
	http            *http.Client
}

func (e *evalEnv) call(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(e.base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, truncate(b.String(), 300))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func newEvalEnv(t *testing.T) *evalEnv {
	t.Helper()
	base, key, dkey := os.Getenv("ORYN_OWNERMEM_EVAL_URL"), os.Getenv("ORYN_OWNERMEM_EVAL_KEY"), os.Getenv("ORYN_OWNERMEM_EVAL_DECIDE_KEY")
	if base == "" || key == "" || dkey == "" {
		t.Skip("需要 ORYN_OWNERMEM_EVAL_URL、ORYN_OWNERMEM_EVAL_KEY、ORYN_OWNERMEM_EVAL_DECIDE_KEY")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	svc := New(Config{Q: st.Q, Box: box, HTTP: &http.Client{Timeout: time.Minute}})
	_, err = svc.PutDecideEndpoint(ctx, apigen.OwnerMemoryDecideUpdate{
		BaseURL: env("ORYN_OWNERMEM_EVAL_DECIDE_URL", DefaultDecideBaseURL),
		Model:   env("ORYN_OWNERMEM_EVAL_DECIDE_MODEL", DefaultDecideModel), APIKey: &dkey,
	})
	require.NoError(t, err)
	return &evalEnv{base: base, key: key, bank: env("ORYN_OWNERMEM_EVAL_BANK", "omem-real"), svc: svc, http: &http.Client{Timeout: 5 * time.Minute}}
}

func loadCases(t *testing.T) (cases []evalCase, topics []evalTopic) {
	t.Helper()
	raw, err := os.ReadFile("testdata/eval/cases.json")
	require.NoError(t, err)
	var f struct {
		Recall []evalCase  `json:"recall"`
		Topics []evalTopic `json:"topics"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	return f.Recall, f.Topics
}

// TestEvalRecall 对每道题：召回前 50 条（与 recall_memory 相同的请求）→ 判断模型重排分档 → 按 expect 判定。
func TestEvalRecall(t *testing.T) {
	e := newEvalEnv(t)
	cases, _ := loadCases(t)
	ctx := context.Background()
	failed := 0
	for _, c := range cases {
		bank := c.Bank
		if bank == "" {
			bank = e.bank
		}
		var resp struct {
			Results []hsFact `json:"results"`
		}
		err := e.call(ctx, http.MethodPost, "/v1/default/banks/"+url.PathEscape(bank)+"/memories/recall", map[string]any{
			"query": c.Query, "types": []string{"world", "observation"}, "max_tokens": recallTokens, "budget": "mid",
		}, &resp)
		require.NoError(t, err, c.Name)
		ok, detail := judge(c, resp.Results, e.svc.rerank(ctx, c.Query, resp.Results))
		mark := "通过"
		if !ok {
			mark, failed = "不通过", failed+1
		}
		t.Logf("[%s] %s｜%s｜%s", mark, c.Name, c.Query, detail)
	}
	if failed > 0 {
		t.Errorf("%d / %d 题不通过", failed, len(cases))
	}
}

// judge 按 expect 判定一题，返回是否通过与说明（候选数、首个相关事实的原排名与重排后排名、最高分、陷阱事实的分数）。
func judge(c evalCase, cands []hsFact, r rerankResult) (bool, string) {
	firstRel := -1
	for i, f := range cands {
		if matches(f.Text, c.Relevant) {
			firstRel = i + 1
			break
		}
	}
	top := 0.0
	if len(r.Items) > 0 {
		top = r.Items[0].Score
	}
	relRank, trapScore := -1, -1.0
	for i, it := range r.Items {
		if relRank < 0 && matches(it.Fact.Text, c.Relevant) {
			relRank = i + 1
		}
		if trapScore < 0 && matches(it.Fact.Text, c.Trap) {
			trapScore = it.Score
		}
	}
	detail := fmt.Sprintf("候选 %d，RRF 首个相关第 %d，重排后第 %d，最高分 %.2f，给出 %d 条", len(cands), firstRel, relRank, top, len(r.Items))
	if trapScore >= 0 {
		detail += fmt.Sprintf("，陷阱事实 %.2f", trapScore)
	}
	if !r.Reranked {
		return false, detail + "，判断模型不可用：" + fmt.Sprint(r.Err)
	}
	switch c.Expect {
	case "top1":
		return relRank == 1 && top >= directScore, detail
	case "noDirect":
		return r.NoDirect, detail
	case "partial":
		return r.NoDirect && relRank > 0 && r.Items[relRank-1].Partial, detail
	case "clean":
		for _, f := range cands {
			for _, bad := range c.MustNot {
				if strings.Contains(f.Text, bad) {
					return false, detail + "，候选含「" + bad + "」：" + truncate(f.Text, 120)
				}
			}
		}
		return true, detail
	}
	return false, "未知的 expect：" + c.Expect
}

// TestEvalTopics 检查 bank 里各主题当前内容不再出现负例 1–3 的写法。
func TestEvalTopics(t *testing.T) {
	e := newEvalEnv(t)
	_, topics := loadCases(t)
	ctx := context.Background()
	var resp struct {
		Items []struct {
			ID      string  `json:"id"`
			Name    string  `json:"name"`
			Content *string `json:"content"`
		} `json:"items"`
	}
	require.NoError(t, e.call(ctx, http.MethodGet, "/v1/default/banks/"+url.PathEscape(e.bank)+"/mental-models?detail=content&limit=100", nil, &resp))
	failed := 0
	for _, c := range topics {
		var problems []string
		for _, m := range resp.Items {
			if m.Content == nil {
				continue
			}
			for _, p := range c.MustNotMatch {
				if loc := regexp.MustCompile(p).FindString(*m.Content); loc != "" {
					problems = append(problems, m.Name+"：「"+loc+"」")
				}
			}
			for _, item := range strings.FieldsFunc(*m.Content, func(r rune) bool { return r == '\n' || r == '；' }) {
				for _, rw := range c.RequireWith {
					if !strings.Contains(item, rw.Trigger) {
						continue
					}
					if !matches(item, [][]string{rw.Also}) {
						problems = append(problems, m.Name+"：「"+truncate(strings.TrimSpace(item), 80)+"」缺少场景")
					}
				}
			}
		}
		mark := "通过"
		if len(problems) > 0 {
			mark, failed = "不通过", failed+1
		}
		t.Logf("[%s] %s（%d 个主题）%s", mark, c.Name, len(resp.Items), strings.Join(problems, "；"))
	}
	if failed > 0 {
		t.Errorf("%d / %d 条主题检查不通过", failed, len(topics))
	}
}
