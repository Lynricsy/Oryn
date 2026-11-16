package memory

// models.go：管理页的模型选择、嵌入与重排接口、用量（步骤 2.5 的管理接口）。用途 → 生效模型的解析沿用 relay.go 的
// selectedModel 与 deployedFamily，中转与管理页看到的是同一份结果。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// reembedProbeTimeout 是换嵌入模型前探测 Hindsight 是否仍在运行的时限。
const reembedProbeTimeout = 5 * time.Second

// CatalogModel 是中央模型目录里的一个 chat 模型（由 API 层从 modelcat 取出传入，memory 不导入 modelcat）。
type CatalogModel struct {
	ProviderKey, ModelID, Name string
}

// endpointAAD 是嵌入或重排 key 在 secrets.Box 里的 AAD 名称（与迁移 0036 的注释一致）。
func endpointAAD(kind apigen.MemoryEndpointKind) string { return "memory/endpoint/" + string(kind) + "/api-key" }

// familyOfModel 用 Config.Family 判定模型的协议类型；不支持或查不到时返回原因。
func (s *Service) familyOfModel(ctx context.Context, providerKey, modelID string) (apigen.MemoryFamily, string) {
	if s.cfg.Family == nil {
		return apigen.MemoryFamilyOpenai, ""
	}
	fam, err := s.cfg.Family(ctx, providerKey, modelID)
	if err != nil {
		return "", err.Error()
	}
	if !fam.Valid() {
		return "", "协议类型未知"
	}
	return fam, ""
}

// Models 返回五种用途的生效模型、协议类型与是否需要重启 Hindsight，以及可选模型（R16、T1、U2）。
// catalog 为 nil 表示目录不可用，此时 options 为空。
func (s *Service) Models(ctx context.Context, catalog []CatalogModel) (apigen.MemoryModels, error) {
	rows, err := s.cfg.Q.ListMemoryModels(ctx)
	if err != nil {
		return apigen.MemoryModels{}, err
	}
	own := make(map[apigen.MemoryPurpose]bool, len(rows))
	for _, r := range rows {
		own[apigen.MemoryPurpose(r.Purpose)] = true
	}
	out := apigen.MemoryModels{Items: make([]apigen.MemoryModelChoice, 0, len(Purposes)), Options: make([]apigen.MemoryModelOption, 0, len(catalog))}
	var restart []apigen.MemoryPurpose
	for _, p := range Purposes {
		item := apigen.MemoryModelChoice{Purpose: p, FollowsDefault: p != apigen.MemoryPurposeDefault && !own[p]}
		m, ok, err := s.selectedModel(ctx, p)
		if err != nil {
			return apigen.MemoryModels{}, err
		}
		fam := apigen.MemoryFamilyOpenai
		known := true
		if ok {
			pk, mid := m.ProviderKey, m.ModelID
			item.ProviderKey, item.ModelID = &pk, &mid
			if f, reason := s.familyOfModel(ctx, m.ProviderKey, m.ModelID); reason == "" {
				fam = f
			} else {
				known = false
			}
		}
		deployed, err := s.deployedFamily(ctx, p)
		if err != nil {
			return apigen.MemoryModels{}, err
		}
		item.DeployedFamily = &deployed
		if known {
			item.Family = &fam
			item.NeedsRestart = fam != deployed
		}
		if item.NeedsRestart {
			restart = append(restart, p)
		}
		out.Items = append(out.Items, item)
	}
	if len(restart) > 0 {
		if snippet, err := s.EnvSnippet(ctx, restart); err == nil {
			out.EnvSnippet = &snippet
		}
	}
	for _, c := range catalog {
		opt := apigen.MemoryModelOption{ProviderKey: c.ProviderKey, ModelID: c.ModelID, Name: c.Name}
		if fam, reason := s.familyOfModel(ctx, c.ProviderKey, c.ModelID); reason == "" {
			opt.Family = &fam
		} else {
			opt.Reason = &reason
		}
		out.Options = append(out.Options, opt)
	}
	return out, nil
}

// PutModels 修改各用途的模型：未列出或不带 providerKey 的用途跟随 default，default 必须指定。
// 模型必须在中央模型目录里且协议受支持，否则 400 `unsupported_model`；目录不可用（catalog 为 nil）时 503。
// 同协议内换模型下一条中转请求即生效；协议变化由 needsRestart 提示主人重启 Hindsight。
func (s *Service) PutModels(ctx context.Context, in apigen.MemoryModelsUpdate, catalog []CatalogModel) (apigen.MemoryModels, error) {
	if catalog == nil {
		return apigen.MemoryModels{}, &Error{Status: http.StatusServiceUnavailable, Code: "catalog_unavailable", Message: "模型目录不可用，无法校验所选模型"}
	}
	inCatalog := make(map[[2]string]bool, len(catalog))
	for _, c := range catalog {
		inCatalog[[2]string{c.ProviderKey, c.ModelID}] = true
	}
	chosen := map[apigen.MemoryPurpose][2]string{}
	for _, it := range in.Items {
		if !it.Purpose.Valid() {
			return apigen.MemoryModels{}, errInvalid("未知的用途：" + string(it.Purpose))
		}
		if _, dup := chosen[it.Purpose]; dup {
			return apigen.MemoryModels{}, errInvalid("用途重复：" + string(it.Purpose))
		}
		if it.ProviderKey == nil || *it.ProviderKey == "" {
			chosen[it.Purpose] = [2]string{}
			continue
		}
		if it.ModelID == nil || *it.ModelID == "" {
			return apigen.MemoryModels{}, errInvalid("用途 " + string(it.Purpose) + " 缺少 modelId")
		}
		key := [2]string{*it.ProviderKey, *it.ModelID}
		if !inCatalog[key] {
			return apigen.MemoryModels{}, &Error{Status: http.StatusBadRequest, Code: "unsupported_model", Message: "模型不在中央模型目录里：" + key[0] + "/" + key[1]}
		}
		if _, reason := s.familyOfModel(ctx, key[0], key[1]); reason != "" {
			return apigen.MemoryModels{}, &Error{Status: http.StatusBadRequest, Code: "unsupported_model", Message: "模型不能用于 Hindsight：" + reason}
		}
		chosen[it.Purpose] = key
	}
	if chosen[apigen.MemoryPurposeDefault] == ([2]string{}) {
		return apigen.MemoryModels{}, errInvalid("default 用途必须指定模型")
	}
	now := s.cfg.Now().Unix()
	for _, p := range Purposes {
		key := chosen[p]
		if key == ([2]string{}) {
			if err := s.cfg.Q.DeleteMemoryModel(ctx, string(p)); err != nil {
				return apigen.MemoryModels{}, err
			}
			continue
		}
		if err := s.cfg.Q.UpsertMemoryModel(ctx, db.UpsertMemoryModelParams{
			Purpose: string(p), ProviderKey: key[0], ModelID: key[1], UpdatedAt: now,
		}); err != nil {
			return apigen.MemoryModels{}, err
		}
	}
	return s.Models(ctx, catalog)
}

// ConfirmDeployed 在主人按新 `.env` 重启 Hindsight 后，把各用途已部署的协议类型更新为当前选择（R16）。
func (s *Service) ConfirmDeployed(ctx context.Context, catalog []CatalogModel) (apigen.MemoryModels, error) {
	fams, err := s.families(ctx)
	if err != nil {
		return apigen.MemoryModels{}, err
	}
	raw, err := json.Marshal(fams)
	if err != nil {
		return apigen.MemoryModels{}, err
	}
	n, err := s.cfg.Q.SetMemoryDeployedFamilies(ctx, db.SetMemoryDeployedFamiliesParams{DeployedFamilies: string(raw), UpdatedAt: s.cfg.Now().Unix()})
	if err != nil {
		return apigen.MemoryModels{}, err
	}
	if n == 0 {
		return apigen.MemoryModels{}, ErrNotConfigured
	}
	return s.Models(ctx, catalog)
}

func endpointView(e db.MemoryEndpoint) *apigen.MemoryEndpoint {
	out := &apigen.MemoryEndpoint{Kind: apigen.MemoryEndpointKind(e.Kind), BaseURL: e.BaseUrl, Model: e.Model, HasKey: len(e.ApiKeyEnc) > 0}
	if e.Dimensions != nil {
		d := int(*e.Dimensions)
		out.Dimensions = &d
	}
	return out
}

// Endpoints 返回嵌入与重排的远端接口；key 只回报是否已设置。
func (s *Service) Endpoints(ctx context.Context) (apigen.MemoryEndpoints, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.MemoryEndpoints{}, err
	}
	rows, err := s.cfg.Q.ListMemoryEndpoints(ctx)
	if err != nil {
		return apigen.MemoryEndpoints{}, err
	}
	out := apigen.MemoryEndpoints{EmbedLocked: st.EmbedLocked != 0}
	for _, e := range rows {
		switch apigen.MemoryEndpointKind(e.Kind) {
		case apigen.MemoryEndpointEmbed:
			out.Embed = endpointView(e)
		case apigen.MemoryEndpointRerank:
			out.Rerank = endpointView(e)
		}
	}
	return out, nil
}

// PutEndpoint 修改嵌入或重排接口（R17）。bank 已有记忆（embedLocked）时修改嵌入的 model 或 dimensions：
// 不带 reembed → 409 `embed_locked`；带 reembed → 先探测 Hindsight，仍能连上 → 409 `hindsight_running`
// （对外只有一个别名 oryn-embed，运行中切换会让新旧向量混在一起），连不上（API 已停）才接受。地址与 key 不受限制。
func (s *Service) PutEndpoint(ctx context.Context, kind apigen.MemoryEndpointKind, in apigen.MemoryEndpointUpdate) (*apigen.MemoryEndpoint, error) {
	if !kind.Valid() {
		return nil, errNotFound("未知的接口类型")
	}
	base, err := NormalizeURL(in.BaseURL)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		return nil, errInvalid("model 不能为空")
	}
	var dims *int64
	if kind == apigen.MemoryEndpointEmbed && in.Dimensions != nil {
		if *in.Dimensions < 1 {
			return nil, errInvalid("dimensions 须为正整数")
		}
		d := int64(*in.Dimensions)
		dims = &d
	}
	var cur *db.MemoryEndpoint
	rows, err := s.cfg.Q.ListMemoryEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Kind == string(kind) {
			cur = &rows[i]
		}
	}
	if kind == apigen.MemoryEndpointEmbed && cur != nil && (cur.Model != model || !sameDims(cur.Dimensions, dims)) {
		if err := s.checkReembed(ctx, in.Reembed != nil && *in.Reembed); err != nil {
			return nil, err
		}
	}
	var keyEnc []byte
	switch {
	case in.APIKey != nil && *in.APIKey != "":
		if keyEnc, err = s.cfg.Box.Seal(endpointAAD(kind), []byte(*in.APIKey)); err != nil {
			return nil, err
		}
	case in.ClearKey != nil && *in.ClearKey:
	case cur != nil:
		keyEnc = cur.ApiKeyEnc
	}
	row := db.UpsertMemoryEndpointParams{Kind: string(kind), BaseUrl: base, Model: model, Dimensions: dims, ApiKeyEnc: keyEnc, UpdatedAt: s.cfg.Now().Unix()}
	if err := s.cfg.Q.UpsertMemoryEndpoint(ctx, row); err != nil {
		return nil, err
	}
	return endpointView(db.MemoryEndpoint{Kind: row.Kind, BaseUrl: row.BaseUrl, Model: row.Model, Dimensions: row.Dimensions, ApiKeyEnc: row.ApiKeyEnc}), nil
}

func sameDims(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// checkReembed 判断能否修改嵌入模型：未锁定直接放行；锁定时只在 reembed 且 Hindsight 不可达时放行。
func (s *Service) checkReembed(ctx context.Context, reembed bool) error {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	if st.EmbedLocked == 0 {
		return nil
	}
	if !reembed {
		return &Error{Status: http.StatusConflict, Code: "embed_locked", Message: "bank 已有记忆，嵌入模型与维度只读；按 docs/deploy.md「换嵌入模型」停机后带 reembed 修改"}
	}
	h, err := s.clientFor(st)
	if errors.Is(err, ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, reembedProbeTimeout)
	defer cancel()
	if _, err := h.Version(pctx); err == nil || statusOf(err) != 0 {
		return &Error{Status: http.StatusConflict, Code: "hindsight_running", Message: "Hindsight 仍在运行：先停止 Hindsight API，再修改嵌入模型并执行 reembed（见 docs/deploy.md「换嵌入模型」）"}
	}
	return nil
}

// Usage 返回每种用途最近 24 小时与 7 天的调用次数和 token 数（R18），没有调用的用途计 0。
func (s *Service) Usage(ctx context.Context) (apigen.MemoryUsage, error) {
	now := s.cfg.Now()
	sum := func(d time.Duration) (map[string]apigen.MemoryUsageCounts, error) {
		rows, err := s.cfg.Q.SumMemoryUsageSince(ctx, now.Add(-d).Truncate(time.Hour).Unix())
		if err != nil {
			return nil, err
		}
		out := make(map[string]apigen.MemoryUsageCounts, len(rows))
		for _, r := range rows {
			out[r.Purpose] = apigen.MemoryUsageCounts{Calls: r.Calls, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens}
		}
		return out, nil
	}
	day, err := sum(24 * time.Hour)
	if err != nil {
		return apigen.MemoryUsage{}, err
	}
	week, err := sum(7 * 24 * time.Hour)
	if err != nil {
		return apigen.MemoryUsage{}, err
	}
	var out apigen.MemoryUsage
	// 顺序：五种 LLM 用途，再加嵌入与重排
	order := make([]string, 0, len(Purposes)+2)
	for _, p := range Purposes {
		order = append(order, string(p))
	}
	for _, p := range append(order, RelayEmbed, RelayRerank) {
		out.Items = append(out.Items, struct {
			Day     apigen.MemoryUsageCounts `json:"day"`
			Purpose string                   `json:"purpose"`
			Week    apigen.MemoryUsageCounts `json:"week"`
		}{Day: day[p], Purpose: p, Week: week[p]})
	}
	return out, nil
}
