package artifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
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

// designLive 在全部 ready orb 的服务镜像里找该画布的 dev 服务（dsn-<hash>）：同项目线程 B 可以在另一台 orb 上
// design_open 接管线程 A 创建的画布，所以不能只看创建线程的 orb。
//
// 边界校验：
//   - 服务落在创建线程所在的同一台 orb 上：直接放行，threadId 取服务登记的 threadId（缺省创建线程）；
//   - 服务落在别的 orb 上（跨 orb 接管）：要求画布属于某个项目，且服务登记的线程是该 orb 上未归档、同项目的工作线程。
//
// 多台 orb 同时在跑时按 updated_at 降序取最近活跃的那台，并用实际启动线程生成 portal 地址。
func (s *Service) designLive(ctx context.Context, r db.Artifact) (*DesignLive, error) {
	rows, err := s.st.Q.ListReadyOrbServicesByName(ctx, DesignServiceName(r.ID))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	var creatorOrb string
	if r.ThreadID != nil && *r.ThreadID != "" {
		t, err := s.st.Q.GetThread(ctx, *r.ThreadID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && t.Kind == threads.KindWork && t.OrbID != nil {
			creatorOrb = *t.OrbID
		}
	}
	for _, row := range rows {
		var sv apigen.OrbService
		if json.Unmarshal([]byte(row.Doc), &sv) != nil || sv.Status != apigen.OrbServiceStatusRunning || sv.Port == nil {
			continue
		}
		tid := deref(sv.ThreadID)
		if creatorOrb != "" && row.OrbID == creatorOrb {
			if tid == "" {
				tid = *r.ThreadID
			}
			return &DesignLive{ThreadID: tid, Port: *sv.Port}, nil
		}
		if tid == "" || r.ProjectID == nil || *r.ProjectID == "" {
			continue
		}
		t, err := s.st.Q.GetThread(ctx, tid)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if t.Kind != threads.KindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != row.OrbID {
			continue
		}
		if t.ProjectID == nil || *t.ProjectID != *r.ProjectID {
			continue
		}
		return &DesignLive{ThreadID: t.ID, Port: *sv.Port}, nil
	}
	return nil, nil
}
