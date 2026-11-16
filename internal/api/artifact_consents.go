package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// artifact 页面能力的用户同意（M14 计划步骤 7.3）：设置页列出与收回授权，查看器的同意对话框回答请求。

// ListArtifactConsents 对应 `GET artifact-consents`：已授权的 artifact（不含回收站里的），最近授权的在前。
func (s *Server) ListArtifactConsents(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	list, err := svc.Consents(r.Context())
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ArtifactConsentList{Consents: list})
}

// AnswerArtifactConsent 对应 `POST artifact-consents/{requestId}`：第一个回答生效；请求已结束时回 404。
func (s *Server) AnswerArtifactConsent(w http.ResponseWriter, r *http.Request, key string) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactConsentAnswer
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := svc.AnswerConsent(r.Context(), key, req.Allow); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeArtifactConsents 对应 `DELETE artifact-consents/{artifactId}`：收回这个 artifact 的全部能力授权。
func (s *Server) RevokeArtifactConsents(w http.ResponseWriter, r *http.Request, key string) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	if err := svc.RevokeConsents(r.Context(), key); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
