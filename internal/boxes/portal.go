package boxes

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Lynricsy/Oryn/internal/portal"
)

// ResolveApp 把 app 主机标签解析成 portal 的拨号目标（实现 portal.Apps）：app 行 + 所属 box 的 orb。
// 没有这个 app 时 ok=false；Live = 已有成功激活过的 release。
func (s *Service) ResolveApp(ctx context.Context, name string) (portal.AppTarget, bool, error) {
	a, err := s.st.Q.GetBoxAppByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return portal.AppTarget{}, false, nil
	}
	if err != nil {
		return portal.AppTarget{}, false, err
	}
	b, err := s.st.Q.GetBox(ctx, a.BoxID)
	if errors.Is(err, sql.ErrNoRows) {
		return portal.AppTarget{}, false, nil
	}
	if err != nil {
		return portal.AppTarget{}, false, err
	}
	return portal.AppTarget{
		BoxID: b.ID, App: a.Name, OrbID: b.OrbID, Port: int(a.Port),
		Public: a.Public, Live: a.LiveReleaseID != nil,
	}, true, nil
}
