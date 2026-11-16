package memory

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// OutputLanguageLabel 是设置页显示的记忆输出语言（V1：固定简体中文）。
const OutputLanguageLabel = "简体中文"

// privateHTTPPrefixes 是允许明文 http 的地址段：私有网段（含 docker 网桥 172.17.0.0/16）、ULA 与链路本地。
var privateHTTPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

func invalidURL(msg string) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

// NormalizeURL 校验并规范化 Hindsight 根地址（去掉末尾斜杠）。远端地址必须用 https（R2）：
// 明文 http 只允许回环、`localhost`、私有网段、ULA、链路本地（含 docker 网关）这些字面地址，否则 400 `insecure_url`。
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", invalidURL("Hindsight 地址无法解析：" + err.Error())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", invalidURL("Hindsight 地址必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", invalidURL("Hindsight 地址缺少主机名")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", invalidURL("Hindsight 地址不能带用户信息、查询串或片段")
	}
	if u.Scheme == "http" && !plainHTTPAllowed(u.Hostname()) {
		return "", &Error{Status: http.StatusBadRequest, Code: "insecure_url",
			Message: "远端 Hindsight 必须用 https://；明文 http 只允许本机、私有网段或 docker 网关地址"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func plainHTTPAllowed(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	if ip.IsLoopback() {
		return true
	}
	for _, p := range privateHTTPPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Settings 返回连接设置与全局开关。
func (s *Service) Settings(ctx context.Context) (apigen.MemorySettings, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.MemorySettings{}, err
	}
	return settingsView(st), nil
}

func settingsView(st db.MemorySetting) apigen.MemorySettings {
	out := apigen.MemorySettings{
		Enabled:         st.Enabled != 0,
		HindsightURL:    st.HindsightUrl,
		KeysGenerated:   len(st.HindsightKeyEnc) > 0 && st.RelayTokenHash != nil,
		KeysGeneratedAt: st.KeysRotatedAt,
		OutputLanguage:  OutputLanguageLabel,
	}
	return out
}

// PutSettings 修改地址与全局开关；`hindsightUrl` 省略或为空表示清空。全局生效值变化时标记全部线程待重启。
func (s *Service) PutSettings(ctx context.Context, in apigen.MemorySettingsUpdate) (apigen.MemorySettings, error) {
	var u *string
	if in.HindsightURL != nil && strings.TrimSpace(*in.HindsightURL) != "" {
		norm, err := NormalizeURL(*in.HindsightURL)
		if err != nil {
			return apigen.MemorySettings{}, err
		}
		u = &norm
	}
	before := s.Enabled(ctx)
	if err := s.cfg.Q.UpsertMemorySettings(ctx, db.UpsertMemorySettingsParams{
		Enabled: boolInt(in.Enabled), HindsightUrl: u, UpdatedAt: s.cfg.Now().Unix(),
	}); err != nil {
		return apigen.MemorySettings{}, err
	}
	s.restartIfChanged(ctx, before, s.Enabled(ctx), nil)
	return s.Settings(ctx)
}

// restartIfChanged 在生效值变化时调用 Config.Restart。
func (s *Service) restartIfChanged(ctx context.Context, before, after bool, projectID *string) {
	if before != after && s.cfg.Restart != nil {
		s.cfg.Restart(ctx, projectID)
	}
}

// ListProjects 返回全部未归档项目的记忆开关；没有单独设置的项目取全局总开关。
func (s *Service) ListProjects(ctx context.Context) (apigen.MemoryProjectList, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.MemoryProjectList{}, err
	}
	archived := false
	projects, err := s.cfg.Q.ListProjects(ctx, &archived)
	if err != nil {
		return apigen.MemoryProjectList{}, err
	}
	rows, err := s.cfg.Q.ListMemoryProjects(ctx)
	if err != nil {
		return apigen.MemoryProjectList{}, err
	}
	overrides := make(map[string]bool, len(rows))
	for _, r := range rows {
		overrides[r.ProjectID] = r.Enabled != 0
	}
	out := apigen.MemoryProjectList{Items: make([]apigen.MemoryProjectToggle, 0, len(projects))}
	for _, p := range projects {
		on, overridden := overrides[p.ID]
		if !overridden {
			on = st.Enabled != 0
		}
		out.Items = append(out.Items, apigen.MemoryProjectToggle{ProjectID: p.ID, Name: p.Name, Enabled: on, Overridden: overridden})
	}
	return out, nil
}

// PutProject 设置项目的记忆开关；项目不存在时 404。项目生效值变化时标记该项目的线程待重启。
func (s *Service) PutProject(ctx context.Context, projectID string, enabled bool) (apigen.MemoryProjectToggle, error) {
	p, err := s.cfg.Q.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.MemoryProjectToggle{}, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "项目不存在"}
	}
	if err != nil {
		return apigen.MemoryProjectToggle{}, err
	}
	before := s.ProjectEnabled(ctx, p.ID)
	if err := s.cfg.Q.UpsertMemoryProject(ctx, db.UpsertMemoryProjectParams{
		ProjectID: p.ID, Enabled: boolInt(enabled), UpdatedAt: s.cfg.Now().Unix(),
	}); err != nil {
		return apigen.MemoryProjectToggle{}, err
	}
	id := p.ID
	s.restartIfChanged(ctx, before, s.ProjectEnabled(ctx, p.ID), &id)
	return apigen.MemoryProjectToggle{ProjectID: p.ID, Name: p.Name, Enabled: enabled, Overridden: true}, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
