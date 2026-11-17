// Package geminidecision 实现工程记忆与助手记忆共用的 Gemini Decisions 评分协议。
package geminidecision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Question 是一个候选的评分题；Criteria 按从低到高的顺序给出评分档位。
type Question struct {
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

// Scores 返回与 questions 同序的期望分。任何候选缺分或越界都使整批失败，不返回部分结果。
// 错误不包含上游正文、密钥或候选记忆；调用不重试，也不允许携带密钥跟随重定向。
func Scores(ctx context.Context, client *http.Client, baseURL, key, model, input string, questions []Question) ([]float64, error) {
	wire := make(map[string]any, len(questions))
	for i, q := range questions {
		if len(q.Criteria) < 2 {
			return nil, fmt.Errorf("候选 f%d 的评分档位不足", i)
		}
		wire[fmt.Sprintf("f%d", i)] = struct {
			Type string `json:"type"`
			Question
		}{"score", q}
	}
	body, err := json.Marshal(struct {
		Model     string         `json:"model"`
		Input     string         `json:"input"`
		Questions map[string]any `json:"questions"`
	}{"models/" + strings.TrimPrefix(model, "models/"), input, wire})
	if err != nil {
		return nil, fmt.Errorf("编码判断模型请求失败")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1beta/decisions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("判断模型地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	guarded := *client
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := guarded.Do(req)
	if err != nil {
		return nil, fmt.Errorf("判断模型不可达或请求已取消")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("判断模型返回 %d", resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, fmt.Errorf("判断模型响应读取失败或过大")
	}
	var out struct {
		Answers map[string]struct {
			Score *float64 `json:"score"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("判断模型响应不是有效 JSON")
	}
	scores := make([]float64, len(questions))
	for i, q := range questions {
		score := out.Answers[fmt.Sprintf("f%d", i)].Score
		if score == nil || *score < 0 || *score > float64(len(q.Criteria)-1) {
			return nil, fmt.Errorf("判断模型应答缺少 f%d 的分数或分数越界", i)
		}
		scores[i] = *score
	}
	return scores, nil
}
