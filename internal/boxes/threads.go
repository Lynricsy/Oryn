package boxes

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ThreadTarget 把「在 box 上开线程」解析成 orb 与工作目录（executor.kind = box）。
// app 为空 = 机器本身，cwd 为 `~`；否则 app 须在这台 box 上且已有 live release，
// cwd 为 `~/apps/<app>/current`，带源码且 root 非空的 app 再接 `/<root>`。
// app 行的 root / source 只在激活成功时才变，所以它指向的目录一定存在。
func (s *Service) ThreadTarget(ctx context.Context, boxRef, app string) (orbID, cwd string, err error) {
	b, _, err := s.live(ctx, boxRef)
	if err != nil {
		return "", "", err
	}
	if b.Desired == desiredStopped {
		return "", "", errf(CodeBoxStopped, "box %s is stopped; start it first", b.Name)
	}
	if app == "" {
		return b.OrbID, "~", nil
	}
	a, err := s.st.Q.GetBoxApp(ctx, db.GetBoxAppParams{BoxID: b.ID, Name: app})
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errf(CodeAppNotFound, "app %s not found on box %s", app, b.Name)
	}
	if err != nil {
		return "", "", err
	}
	if a.LiveReleaseID == nil {
		return "", "", errf(CodeAppNotDeployed, "app %s has no live release yet", app)
	}
	cwd = "~/apps/" + a.Name + "/current"
	if a.Source && a.Root != "" {
		cwd += "/" + a.Root
	}
	return b.OrbID, cwd, nil
}
