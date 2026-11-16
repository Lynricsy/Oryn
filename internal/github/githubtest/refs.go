package githubtest

import (
	"net/http"
	"strconv"
	"strings"
)

// refRoutes 是 git ref 与按 id 读仓库的只读接口（M15 步骤 5.4 的门禁查询）。
func (s *Server) refRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/ref/{ref...}", s.getRef)
	mux.HandleFunc("GET /repositories/{id}", s.getRepoByID)
}

// getRef 按 Repo.Refs（键为 heads/<分支>、tags/<tag>）回 `{ref, object{sha, type}}`：tag 的值是 TagObjects 里的附注 tag
// 对象时 type 为 tag（再经 GET …/git/tags/{sha} 剥到提交），否则为 commit（轻量 tag 与分支）。Repo.Refs 没有该 ref 时读
// GitRoot 下的裸仓库（M15 9.2：经 git smart-HTTP 推送的分支与 tag）。
func (s *Server) getRef(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if rp == nil && to != "" {
		owner, name, _ := strings.Cut(to, "/")
		rp, _ = s.lookupRepo(owner, name)
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	ref := r.PathValue("ref")
	sha, ok := rp.Refs[ref]
	typ := "commit"
	if ok {
		if _, annotated := s.TagObjects[sha]; annotated && strings.HasPrefix(ref, "tags/") {
			typ = "tag"
		}
	} else if sha, typ, ok = s.bareRef(rp.FullName, ref); !ok {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ref": "refs/" + ref, "node_id": "REF_" + ref,
		"object": map[string]any{"sha": sha, "type": typ},
	})
}

func (s *Server) getRepoByID(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var rp *Repo
	if err == nil {
		rp = s.repoByID(id)
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, s.repoJSON(r, rp))
}
