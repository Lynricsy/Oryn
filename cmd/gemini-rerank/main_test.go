package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRerankPreservesCandidateIndices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-only-key" || r.Header.Get("Authorization") != "" {
			t.Error("判断请求未使用独立的 Google 鉴权头")
		}
		var body struct {
			Model     string `json:"model"`
			Input     string `json:"input"`
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "models/gemini-decision-1" || !strings.Contains(body.Input, "部署目录") || !strings.Contains(body.Questions["f1"].Instructions, "/root/Oryn") {
			t.Errorf("问题与候选映射错误：%+v", body)
		}
		_, _ = io.WriteString(w, `{"answers":{"f0":{"score":0.3},"f1":{"score":3},"f2":{"score":3}}}`)
	}))
	defer upstream.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"gemini-decision-1","query":"部署目录","documents":["无关","/root/Oryn","同分候选"],"top_n":2}`))
	r.Header.Set("Authorization", "Bearer test-only-key")
	w := httptest.NewRecorder()
	rerankHandler(upstream.Client(), upstream.URL)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 || out.Results[0] != (result{Index: 1, Score: 1}) || out.Results[1] != (result{Index: 2, Score: 1}) {
		t.Fatalf("排序、原始索引或 top_n 错误：%+v", out.Results)
	}
}

func TestRerankRejectsIncompleteScoresAndHidesUpstreamSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"缺少候选", http.StatusOK, `{"answers":{"f0":{"score":3}}}`},
		{"分数越界", http.StatusOK, `{"answers":{"f0":{"score":4},"f1":{"score":1}}}`},
		{"上游报错含凭据", http.StatusTooManyRequests, `{"error":"test-only-key"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			r := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"gemini-decision-1","query":"问题","documents":["甲","乙"]}`))
			r.Header.Set("Authorization", "Bearer test-only-key")
			w := httptest.NewRecorder()
			rerankHandler(upstream.Client(), upstream.URL)(w, r)
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "test-only-key") {
				t.Fatalf("不得用不完整打分伪装成功或回显密钥：HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRerankWithoutKeyNeverCallsUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("未经鉴权的请求不得调用上游")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"gemini-decision-1","query":"问题","documents":["甲"]}`))
	w := httptest.NewRecorder()
	rerankHandler(upstream.Client(), upstream.URL)(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺少密钥应返回 401，实际 %d", w.Code)
	}
}
