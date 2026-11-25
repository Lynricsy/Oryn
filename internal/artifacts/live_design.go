package artifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// DesignLive 是设计画布的实时预览：orb 里的 dev 服务（受管服务 dsn-<hash>）经 portal 打开。
// ThreadID 决定 portal 地址用哪个线程的短码（与该服务的 PUBLIC_URL 一致，vite 的 Host 白名单按它放行）。
type DesignLive struct {
	ThreadID string
	Port     int
}

// DesignServiceName 是设计画布 dev 服务的受管服务名 `dsn-<sha256(artifactId) 前 10 位 hex>`（M29 C3）。
// orbd 侧 internal/orbdrt/design.go 的 designServiceName 是同一算法，两边须一致。
func DesignServiceName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "dsn-" + hex.EncodeToString(sum[:])[:10]
}

// designLive 读 artifact 创建线程所在 orb 的服务镜像（orb_services），该 artifact 的 dev 服务在跑且有 portal 端口时
// 返回它；否则 nil。只读控制面镜像，不唤醒 orb：orb 不在 ready（冻结、停止、删除中）时镜像可能还写着 running，
// 一律按没有处理。
func (s *Service) designLive(ctx context.Context, threadID *string, id string) (*DesignLive, error) {
	if threadID == nil || *threadID == "" {
		return nil, nil
	}
	t, err := s.st.Q.GetThread(ctx, *threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Kind != threads.KindWork || t.OrbID == nil {
		return nil, nil
	}
	o, err := s.st.Q.GetOrb(ctx, *t.OrbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.State != orbs.StateReady {
		return nil, nil
	}
	rows, err := s.st.Q.ListOrbServices(ctx, *t.OrbID)
	if err != nil {
		return nil, err
	}
	name := DesignServiceName(id)
	for _, r := range rows {
		if r.Name != name {
			continue
		}
		if r.HasPortal == 0 || r.Port == nil {
			return nil, nil
		}
		var sv apigen.OrbService
		if json.Unmarshal([]byte(r.Doc), &sv) != nil || sv.Status != apigen.OrbServiceStatusRunning || sv.Port == nil {
			return nil, nil
		}
		live := &DesignLive{ThreadID: t.ID, Port: *sv.Port}
		if sv.ThreadID != nil && *sv.ThreadID != "" {
			live.ThreadID = *sv.ThreadID
		}
		return live, nil
	}
	return nil, nil
}
