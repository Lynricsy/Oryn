package ownermem

// settings.go：助手记忆的开关、主人的名字与别名、判断模型接口（R18）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// backfillWindow 是首次开启时回填的时间窗（D6）。
	backfillWindow = 30 * 24 * time.Hour
	// maxOwnerNames、maxOwnerNameRunes 与 OpenAPI 的 OwnerMemorySettingsUpdate 一致。
	maxOwnerNames     = 16
	maxOwnerNameRunes = 64
	// decideKeyAAD 是判断模型 key 的加密名（迁移 0040 的注释）。
	decideKeyAAD = "owner-memory/decide-key"
	// DefaultDecideBaseURL、DefaultDecideModel 是判断模型接口的默认值（计划「全局约束」）。
	DefaultDecideBaseURL = "https://generativelanguage.googleapis.com"
	DefaultDecideModel   = "gemini-decision-1"
)

var errNoRows = sql.ErrNoRows

// ownerNames 解析设置里的名字列表；格式坏了按空处理。
func ownerNames(st db.OwnerMemorySetting) []string {
	var names []string
	if err := json.Unmarshal([]byte(st.OwnerNames), &names); err != nil {
		return nil
	}
	return names
}

// normalizeNames 去首尾空白、去重（保持顺序）并校验长度。
func normalizeNames(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if utf8.RuneCountInString(n) > maxOwnerNameRunes || strings.ContainsAny(n, "\n\r") {
			return nil, errInvalid("名字或别名过长或含换行")
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) > maxOwnerNames {
		return nil, errInvalid("名字与别名最多 16 个")
	}
	return out, nil
}

// Settings 返回设置视图（判断模型的 key 只返回是否已设置）。
func (s *Service) Settings(ctx context.Context) (apigen.OwnerMemorySettings, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	names := ownerNames(st)
	if names == nil {
		names = []string{}
	}
	out := apigen.OwnerMemorySettings{Enabled: st.Enabled != 0, Available: s.Available(ctx), OwnerNames: names}
	if st.DecideBaseUrl != nil && st.DecideModel != nil {
		out.Decide = &apigen.OwnerMemoryDecideEndpoint{BaseURL: *st.DecideBaseUrl, Model: *st.DecideModel, HasKey: len(st.DecideKeyEnc) > 0}
	}
	return out, nil
}

// PutSettings 修改开关与名字。首次开启时定下回填起点（30 天前）；名字变化后下次访问 bank 时重写配置（配置哈希不同）。
func (s *Service) PutSettings(ctx context.Context, in apigen.OwnerMemorySettingsUpdate) (apigen.OwnerMemorySettings, error) {
	names, err := normalizeNames(in.OwnerNames)
	if err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	raw, _ := json.Marshal(names)
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	now := s.cfg.Now()
	enabled := int64(0)
	if in.Enabled {
		enabled = 1
	}
	if err := s.cfg.Q.UpsertOwnerMemorySettings(ctx, db.UpsertOwnerMemorySettingsParams{
		Enabled: enabled, OwnerNames: string(raw), UpdatedAt: now.UnixMilli(),
	}); err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	if in.Enabled && st.BackfillSince == nil {
		since := now.Add(-backfillWindow).UnixMilli()
		if err := s.cfg.Q.SetOwnerMemoryBackfill(ctx, &since); err != nil {
			return apigen.OwnerMemorySettings{}, err
		}
	}
	return s.Settings(ctx)
}

// PutDecideEndpoint 配置判断模型接口。远端地址必须用 https（与 M16 的地址规则相同）；apiKey 省略时保留原值。
func (s *Service) PutDecideEndpoint(ctx context.Context, in apigen.OwnerMemoryDecideUpdate) (apigen.OwnerMemorySettings, error) {
	base, err := memory.NormalizeURL(in.BaseURL)
	if err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	model := strings.TrimSpace(in.Model)
	if model == "" || strings.ContainsAny(model, "/ \n") {
		return apigen.OwnerMemorySettings{}, errInvalid("模型名无效")
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	key := st.DecideKeyEnc
	if in.APIKey != nil {
		if *in.APIKey == "" {
			key = nil
		} else if key, err = s.cfg.Box.Seal(decideKeyAAD, []byte(*in.APIKey)); err != nil {
			return apigen.OwnerMemorySettings{}, err
		}
	}
	if err := s.cfg.Q.SetOwnerMemoryDecide(ctx, db.SetOwnerMemoryDecideParams{
		DecideBaseUrl: &base, DecideModel: &model, DecideKeyEnc: key, UpdatedAt: s.cfg.Now().UnixMilli(),
	}); err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	return s.Settings(ctx)
}

// DeleteDecideEndpoint 删除判断模型接口（召回改按 RRF 顺序给出）。
func (s *Service) DeleteDecideEndpoint(ctx context.Context) (apigen.OwnerMemorySettings, error) {
	if err := s.cfg.Q.SetOwnerMemoryDecide(ctx, db.SetOwnerMemoryDecideParams{UpdatedAt: s.cfg.Now().UnixMilli()}); err != nil {
		return apigen.OwnerMemorySettings{}, err
	}
	return s.Settings(ctx)
}

// decideEndpoint 是解密后的判断模型接口；未配置（缺地址、模型或 key）时 ok 为假。
type decideEndpoint struct {
	base, model, key string
}

func (s *Service) decideEndpoint(ctx context.Context) (decideEndpoint, bool) {
	st, err := s.loadSettings(ctx)
	if err != nil || st.DecideBaseUrl == nil || st.DecideModel == nil || len(st.DecideKeyEnc) == 0 {
		return decideEndpoint{}, false
	}
	key, err := s.cfg.Box.Open(decideKeyAAD, st.DecideKeyEnc)
	if err != nil {
		return decideEndpoint{}, false
	}
	return decideEndpoint{base: *st.DecideBaseUrl, model: *st.DecideModel, key: string(key)}, true
}
