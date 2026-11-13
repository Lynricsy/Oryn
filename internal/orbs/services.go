package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 受管服务与 portal（M4 §3.1）：orbd 经 services.state 上报全量服务快照，控制面只存镜像（orb_services），
// 供界面展示与冻结时的 portal 判定；公开分享（portal_shares）的真相在控制面。

// M4 的 orbd 特性名与事件种类。
const (
	FeatureServices = "services"
	FeaturePortal   = "portal"
	// EventServices 表示服务 / portal / 分享变化（From == To == 当前状态）。
	EventServices = "services"
)

// services.state 清洗限额（orb 不可信）。
const (
	maxOrbServices    = 32
	maxServiceCommand = 4096
	maxServiceLinks   = 20
	maxLinkTitle      = 80
	maxLinkPath       = 512
	maxServiceText    = 1024
	maxPublicURL      = 2048
)

var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// truncUTF8 把 s 截到至多 n 字节（不切开多字节字符），并把非法 UTF-8 换成替换字符。
func truncUTF8(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// optText 清洗可选文本字段：空串视为未设置，超长截断。
func optText(p *string, n int) *string {
	if p == nil || *p == "" {
		return nil
	}
	v := truncUTF8(*p, n)
	return &v
}

// sanitizeLinks 丢弃 path 不以 `/` 开头或超长的链接，截断标题 / 分组，至多保留 maxServiceLinks 个。
func sanitizeLinks(in []apigen.OrbPortalLink) []apigen.OrbPortalLink {
	out := make([]apigen.OrbPortalLink, 0, min(len(in), maxServiceLinks))
	for _, l := range in {
		if len(out) == maxServiceLinks {
			break
		}
		if !strings.HasPrefix(l.Path, "/") || len(l.Path) > maxLinkPath || !utf8.ValidString(l.Path) || strings.ContainsAny(l.Path, "\r\n") {
			continue
		}
		l.Title = truncUTF8(l.Title, maxLinkTitle)
		l.Folder = optText(l.Folder, maxLinkTitle)
		out = append(out, l)
	}
	return out
}

// sanitizeService 校验并清洗 orbd 上报的一项服务；不合规（名字、来源、状态、端口）时返回 false。
func sanitizeService(sv apigen.OrbService) (apigen.OrbService, bool) {
	if !serviceNameRE.MatchString(sv.Name) || !sv.Source.Valid() || !sv.Status.Valid() {
		return sv, false
	}
	if sv.Port != nil && (*sv.Port < 1 || *sv.Port > 65535) {
		return sv, false
	}
	sv.Command = truncUTF8(sv.Command, maxServiceCommand)
	sv.Cwd = optText(sv.Cwd, maxServiceText)
	sv.Error = optText(sv.Error, maxServiceText)
	sv.Health = optText(sv.Health, maxServiceText)
	if sv.Health != nil && !strings.HasPrefix(*sv.Health, "/") {
		sv.Health = nil
	}
	sv.Links = sanitizeLinks(sv.Links)
	if sv.PublicURL != nil {
		u, err := url.Parse(*sv.PublicURL)
		if len(*sv.PublicURL) > maxPublicURL || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			sv.PublicURL = nil
		}
	}
	if sv.ThreadID != nil {
		if _, ok := portalhost.Code(*sv.ThreadID); !ok {
			sv.ThreadID = nil
		}
	}
	if sv.Pid != nil && *sv.Pid <= 0 {
		sv.Pid = nil
	}
	if sv.Restarts < 0 {
		sv.Restarts = 0
	}
	return sv, true
}

// onServicesState 用 orbd 上报的全量快照替换该 orb 的服务镜像，撤掉端口已不再声明 portal 的公开分享，
// 并推送 EventServices。
func (s *Service) onServicesState(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlServicesStateParams
	if err := json.Unmarshal(params, &p); err != nil {
		slog.Warn("services.state 参数无效", "orb", orbID, "err", err)
		return
	}
	type row struct {
		sv        apigen.OrbService
		hasPortal bool
	}
	rows := make([]row, 0, min(len(p.Services), maxOrbServices))
	names := map[string]bool{}
	portalPorts := map[int]bool{}
	for _, raw := range p.Services {
		sv, ok := sanitizeService(raw)
		if !ok || names[sv.Name] {
			slog.Warn("services.state 丢弃不合规的服务", "orb", orbID, "name", truncUTF8(raw.Name, 64))
			continue
		}
		if len(rows) == maxOrbServices {
			slog.Warn("services.state 服务数超过上限，丢弃其余", "orb", orbID, "limit", maxOrbServices)
			break
		}
		names[sv.Name] = true
		// 同一端口只算第一个声明 portal 的服务（orbd 已把后来者标为 port_conflict）
		hp := len(sv.Links) > 0 && sv.Port != nil && !portalPorts[*sv.Port]
		if hp {
			portalPorts[*sv.Port] = true
		}
		rows = append(rows, row{sv, hp})
	}
	now := s.nowMs()
	var dropped int64
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		orb, err := q.GetOrb(ctx, orbID)
		if err != nil {
			return err
		}
		if orb.State == StateDeleting || orb.State == StateDeleted {
			return errOrbDeletedTx
		}
		if err := q.DeleteOrbServices(ctx, orbID); err != nil {
			return err
		}
		for _, r := range rows {
			doc, err := json.Marshal(r.sv)
			if err != nil {
				return err
			}
			var port *int64
			if r.sv.Port != nil {
				port = new(int64(*r.sv.Port))
			}
			if err := q.InsertOrbService(ctx, db.InsertOrbServiceParams{
				OrbID: orbID, Name: r.sv.Name, Port: port, HasPortal: b2i(r.hasPortal), Doc: string(doc), UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		dropped, err = q.DeleteUndeclaredPortalShares(ctx, orbID)
		return err
	})
	if errors.Is(err, errOrbDeletedTx) {
		return
	}
	if err != nil {
		slog.Warn("写入服务镜像失败", "orb", orbID, "err", err)
		return
	}
	if dropped > 0 {
		slog.Info("撤销已不再声明 portal 的端口的公开分享", "orb", orbID, "count", dropped)
	}
	s.Refresh(ctx, orbID)
}

// errOrbDeletedTx 中止对已删除 orb 的镜像写入（迟到的通知）。
var errOrbDeletedTx = errors.New("orbs: orb deleted")

// Refresh 读取 orb 并发出 EventServices（服务镜像或公开分享变化后推送 orb 更新）。
func (s *Service) Refresh(ctx context.Context, orbID string) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		slog.Warn("刷新 orb 失败", "orb", orbID, "err", err)
		return
	}
	s.emitInfo(ctx, EventServices, orb)
}

// PortalService 从镜像读取在 port 上声明了 portal 的服务；没有时返回 false。
func (s *Service) PortalService(ctx context.Context, orbID string, port int) (apigen.OrbService, bool, error) {
	r, err := s.st.Q.GetOrbPortalService(ctx, db.GetOrbPortalServiceParams{OrbID: orbID, Port: int64(port)})
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.OrbService{}, false, nil
	}
	if err != nil {
		return apigen.OrbService{}, false, err
	}
	var sv apigen.OrbService
	if err := json.Unmarshal([]byte(r.Doc), &sv); err != nil {
		return apigen.OrbService{}, false, err
	}
	return sv, true, nil
}

// ServicesView 返回 orb 的服务镜像与 portal 列表：portals = 镜像里有 portal 的服务各一项，
// public 取 portal_shares 中 expires_at > now 的行。
func ServicesView(ctx context.Context, q *db.Queries, orbID string, now int64) ([]apigen.OrbService, []apigen.OrbPortal, error) {
	rows, err := q.ListOrbServices(ctx, orbID)
	if err != nil {
		return nil, nil, err
	}
	shares, err := q.ListPortalSharesByOrb(ctx, orbID)
	if err != nil {
		return nil, nil, err
	}
	public := make(map[int64]int64, len(shares))
	for _, sh := range shares {
		if sh.ExpiresAt > now {
			public[sh.Port] = sh.ExpiresAt
		}
	}
	services := make([]apigen.OrbService, 0, len(rows))
	portals := []apigen.OrbPortal{}
	for _, r := range rows {
		var sv apigen.OrbService
		if err := json.Unmarshal([]byte(r.Doc), &sv); err != nil {
			slog.Warn("服务镜像文档无效", "orb", orbID, "name", r.Name, "err", err)
			continue
		}
		if sv.Links == nil {
			sv.Links = []apigen.OrbPortalLink{}
		}
		services = append(services, sv)
		if r.HasPortal == 0 || r.Port == nil {
			continue
		}
		p := apigen.OrbPortal{Port: int(*r.Port), Service: sv.Name, Links: sv.Links}
		if exp, ok := public[*r.Port]; ok {
			p.Public = &apigen.OrbPortalPublic{ExpiresAt: exp}
		}
		portals = append(portals, p)
	}
	return services, portals, nil
}

// dropPortalState 在 orb 删除后清空其服务镜像与公开分享，并丢弃交互合并状态。
func (s *Service) dropPortalState(ctx context.Context, orbID string) {
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteOrbServices(ctx, orbID); err != nil {
			return err
		}
		return q.DeletePortalSharesByOrb(ctx, orbID)
	})
	if err != nil {
		slog.Warn("清理 orb 服务镜像与公开分享失败", "orb", orbID, "err", err)
	}
	s.mu.Lock()
	delete(s.userTouched, orbID)
	s.mu.Unlock()
}

// portalConfigure 向支持 portal 的 orbd 下发 portal 地址模板与主线程（本 orb 最早创建的线程）；失败只记日志。
func (s *Service) portalConfigure(ctx context.Context, l *orbLink) {
	params := apigen.LinkCtlPortalConfigureParams{URLTemplate: s.cfg.PortalURLTemplate}
	threads, err := s.st.Q.ListThreadsByOrb(ctx, &l.orbID)
	if err != nil {
		slog.Warn("读取 orb 线程失败", "orb", l.orbID, "err", err)
	} else if len(threads) > 0 {
		id := threads[0].ID
		params.PrimaryThreadID = &id
	}
	cctx, cancel := context.WithTimeout(ctx, portalConfigureTimeout)
	defer cancel()
	var res apigen.LinkCtlOkResult
	if err := l.ctl.Call(cctx, string(apigen.MethodPortalConfigure), params, &res); err != nil {
		slog.Warn("portal.configure 失败", "orb", l.orbID, "err", err)
	}
}
