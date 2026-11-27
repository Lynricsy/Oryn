package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/opsconf"
)

// OpsDeps 是控制面运维参数接口（设置→控制面）的依赖。
type OpsDeps struct {
	Conf *opsconf.Conf
}

// opsConf 返回运维参数覆盖层；未配置时写 501。
func (s *Server) opsConf(w http.ResponseWriter) (*opsconf.Conf, bool) {
	if s.Ops == nil || s.Ops.Conf == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "ops settings not configured")
		return nil, false
	}
	return s.Ops.Conf, true
}

// GetOpsSettings 返回运维参数的生效值、YAML 值与覆盖情况；任何已登录主体可读。
func (s *Server) GetOpsSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	c, ok := s.opsConf(w)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusOK, opsState(c.State()))
}

// PatchOpsSettings 部分覆盖运维参数：只有网页会话与 App 会话可写（设备令牌、助手不可；恢复会话已被中间件拦掉）。
// 生成的结构体分不出「没传」和 null，所以读原始 body 交给 opsconf.ParsePatch。
func (s *Server) PatchOpsSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != auth.KindWeb {
		WriteError(w, http.StatusForbidden, "forbidden", "只能在已登录的网页或 App 里修改运维参数")
		return
	}
	c, ok := s.opsConf(w)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败: "+err.Error())
		return
	}
	patch, err := opsconf.ParsePatch(body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	st, err := c.Apply(r.Context(), patch)
	if errors.Is(err, opsconf.ErrInvalid) {
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, opsState(st))
}

func opsState(st opsconf.State) apigen.OpsSettingsState {
	out := apigen.OpsSettingsState{
		Effective:  opsSettings(st.Effective),
		Yaml:       opsSettings(st.YAML),
		Overridden: st.Overridden,
	}
	if st.UpdatedAt != 0 {
		out.UpdatedAt = &st.UpdatedAt
	}
	return out
}

// opsSettings 把配置换成 API 形状：原始配置值，不做缺省换算。
func opsSettings(c config.Orynd) apigen.OpsSettings {
	var o apigen.OpsSettings
	o.Idle.AgentIdleMs = c.Idle.AgentIdle.D().Milliseconds()
	o.Idle.UserIdleMs = c.Idle.UserIdle.D().Milliseconds()
	o.Idle.StopAfterFrozenMs = c.Idle.StopAfterFrozen.D().Milliseconds()
	o.Idle.WakeOnFocus = c.Idle.WakeOnFocus
	o.StatsRetention = apigen.StatsRetention{
		SamplesMs: c.Stats.Retention.Samples.D().Milliseconds(),
		EventsMs:  c.Stats.Retention.Events.D().Milliseconds(),
		UsageMs:   c.Stats.Retention.Usage.D().Milliseconds(),
	}
	o.Repos.MaxMirrorGb = c.Repos.MaxMirrorGB
	o.Repos.MinFreeGb = c.Repos.MinFreeGB
	o.Repos.MaxLfsCacheGb = c.Repos.MaxLFSCacheGB
	o.Snapshots.MaxTotalGb = c.Snapshots.MaxTotalGB
	o.Snapshots.MaxSnapshotGb = c.Snapshots.MaxSnapshotGB
	return o
}
