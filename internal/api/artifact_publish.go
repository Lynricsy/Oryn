package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// writePublishErr 在 writeArtifactErr 之外把密钥扫描的命中映射成 409 ShareScanConflict（同线程分享）。
func writePublishErr(w http.ResponseWriter, r *http.Request, err error) {
	var se *artifacts.ScanError
	if errors.As(err, &se) {
		WriteJSON(w, http.StatusConflict, apigen.ShareScanConflict{
			Code: apigen.ShareScanConflictCodeSecretsFound, Message: "artifact contains secret-like strings", Scan: se.Scan,
		})
		return
	}
	writeArtifactErr(w, r, err)
}

// publicationState 返回 artifact 的发布状态与全部已发布版本的大小之和。
func publicationState(r *http.Request, svc *artifacts.Service, id string) (apigen.ArtifactPublicationState, error) {
	p, err := svc.Publication(r.Context(), id)
	if err != nil {
		return apigen.ArtifactPublicationState{}, err
	}
	usage, err := svc.PublicationUsage(r.Context())
	if err != nil {
		return apigen.ArtifactPublicationState{}, err
	}
	out := apigen.ArtifactPublicationState{UsageBytes: usage}
	if p != nil {
		api := p.API()
		out.Publication = &api
	}
	return out, nil
}

func writePublicationState(w http.ResponseWriter, r *http.Request, svc *artifacts.Service, id string) {
	st, err := publicationState(r, svc, id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// GetArtifactPublication 对应 `GET artifacts/{id}/publication`：未发布时 publication 为 null。
func (s *Server) GetArtifactPublication(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	writePublicationState(w, r, svc, id)
}

// PublishArtifact 对应 `PUT artifacts/{id}/publication`：不存在则创建，存在则更新给出的字段；
// 发布为 code / public 时扫描有命中且未确认 → 409 ShareScanConflict。
func (s *Server) PublishArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactPublishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := artifacts.PublishParams{Access: string(req.Access), Version: req.Version, Follow: req.Follow}
	if req.AcknowledgeFindings != nil {
		p.AcknowledgeFindings = *req.AcknowledgeFindings
	}
	if _, err := svc.Publish(r.Context(), id, p); err != nil {
		writePublishErr(w, r, err)
		return
	}
	writePublicationState(w, r, svc, id)
}

// UnpublishArtifact 对应 `DELETE artifacts/{id}/publication`：链接随即失效（没有发布时也回 204）。
func (s *Server) UnpublishArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	if err := svc.Unpublish(r.Context(), id); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateArtifactPublicationCode 对应 `POST artifacts/{id}/publication/code`：换新码并解除锁定。
func (s *Server) RotateArtifactPublicationCode(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	if _, err := svc.RotateCode(r.Context(), id); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	writePublicationState(w, r, svc, id)
}

// ListArtifactPublications 对应 `GET artifact-publications`：全部发布记录（附 artifact），最近更新的在前。
func (s *Server) ListArtifactPublications(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	list, err := svc.Publications(r.Context())
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	out := apigen.ArtifactPublicationList{Publications: make([]apigen.ArtifactPublicationListItem, 0, len(list))}
	for _, x := range list {
		out.Publications = append(out.Publications, apigen.ArtifactPublicationListItem{Artifact: x.Artifact.API(), Publication: x.Publication.API()})
	}
	WriteJSON(w, http.StatusOK, out)
}

// GrantArtifactPublication 对应 `POST artifact-publications/{slug}/grant`：所有者打开自己的私有或访问码发布页，
// 回带授权段的地址。
func (s *Server) GrantArtifactPublication(w http.ResponseWriter, r *http.Request, slug apigen.ArtifactFrameToken) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	u, err := svc.Grant(r.Context(), slug)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ArtifactOpenLink{URL: u})
}

// GetArtifactPrefs 对应 `GET settings/artifacts`；尚未初始化所有者时为缺省值（不自动发布）。
func (s *Server) GetArtifactPrefs(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Q.GetArtifactAutoPublish(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ArtifactPrefs{AutoPublish: v == 1})
}

// PutArtifactPrefs 对应 `PUT settings/artifacts`：整体替换；尚未初始化所有者 → 409。
func (s *Server) PutArtifactPrefs(w http.ResponseWriter, r *http.Request) {
	var req apigen.ArtifactPrefs
	if !decodeJSON(w, r, &req) {
		return
	}
	var v int64
	if req.AutoPublish {
		v = 1
	}
	n, err := s.Store.Q.SetArtifactAutoPublish(r.Context(), v)
	if !savedOwner(w, r, n, err) {
		return
	}
	WriteJSON(w, http.StatusOK, req)
}
