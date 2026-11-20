package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/modelcat"
)

// GetModelCatalog 返回中央模型目录；refresh=true 时强制重新运行 omp。
// 缓存失效由 Catalog.Get 每次比对 models.yml / config.yml 的缓存键完成，写配置的接口不需要通知它。
// 没有目录（未配置协调宿主的 omp）或运行失败且没有任何旧结果 → 503 catalog_unavailable。
func (s *Server) GetModelCatalog(w http.ResponseWriter, r *http.Request, params apigen.GetModelCatalogParams) {
	cat := s.Auth.ModelCatalog
	if cat == nil {
		WriteError(w, http.StatusServiceUnavailable, "catalog_unavailable", "未配置协调宿主的 omp，无法生成模型目录")
		return
	}
	res, err := cat.Get(r.Context(), params.Refresh != nil && *params.Refresh)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "catalog_unavailable", "模型目录不可用: "+err.Error())
		return
	}
	out := apigen.ModelCatalog{
		FetchedAt: res.FetchedAt.UnixMilli(),
		Models:    make([]apigen.CatalogModel, len(res.Models)),
		Stale:     res.Stale,
	}
	if len(res.Warnings) > 0 {
		out.Warnings = res.Warnings
	}
	for i := range res.Models {
		out.Models[i] = catalogModel(&res.Models[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// catalogModel 把目录里的一个模型转成接口形状：零值的可选字段省略。
func catalogModel(m *modelcat.Model) apigen.CatalogModel {
	out := apigen.CatalogModel{
		Provider: m.Provider, ID: m.ID, Selector: m.Selector, Name: m.Name, Kind: m.Kind,
		Reasoning: &m.Reasoning,
	}
	if m.ContextWindow > 0 {
		out.ContextWindow = &m.ContextWindow
	}
	if m.MaxTokens > 0 {
		out.MaxTokens = &m.MaxTokens
	}
	if len(m.Thinking) > 0 {
		out.Thinking = m.Thinking
	}
	if m.ThinkingDefault.Set {
		// 空串也照写：已知没有默认档位，与旧版 Core 不报告（省略）区分开
		out.ThinkingDefault = &m.ThinkingDefault.Value
	}
	if len(m.Input) > 0 {
		out.Input = m.Input
	}
	if m.PricingStatus != "" {
		out.PricingStatus = &m.PricingStatus
	}
	if c := m.Cost; c != nil {
		out.Cost = &apigen.CatalogCost{
			Input: f32(c.Input), Output: f32(c.Output), CacheRead: f32(c.CacheRead), CacheWrite: f32(c.CacheWrite),
		}
	}
	return out
}

func f32(v float64) *float32 {
	f := float32(v)
	return &f
}
