package githubtest

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"

	"github.com/Lynricsy/Oryn/internal/lfs"
)

// 本文件是 Git LFS 假上游（M24 阶段 ②，③ 补上传）：POST /{owner}/{repo}.git/info/lfs/objects/batch 按 batch.md 应答，
// GET /_lfs/objects/{oid} 返回 PutLFS 存下的内容，PUT /_lfs/objects/{oid} 把请求体存下（batch upload 给出的地址）。
// 三者都记进 Calls。

var (
	lfsBatchRe  = regexp.MustCompile(`^/[^/]+/[^/]+\.git/info/lfs/objects/batch$`)
	lfsObjectRe = regexp.MustCompile(`^/_lfs/objects/([0-9a-f]{64})$`)
)

// PutLFS 存一个 LFS 对象；oid 由调用方给出，可以故意与内容不符（测损坏）。
func (s *Server) PutLFS(oid string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lfsObjects == nil {
		s.lfsObjects = map[string][]byte{}
	}
	s.lfsObjects[oid] = content
}

// LFSObject 返回存下的 LFS 对象（PutLFS 或 PUT 上传）。
func (s *Server) LFSObject(oid string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.lfsObjects[oid]
	return b, ok
}

// LFSActive 返回在途的对象请求数；LFSPeak 返回它出现过的最大值。
func (s *Server) LFSActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lfsActive
}

func (s *Server) LFSPeak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lfsPeak
}

// serveLFS 处理 LFS 端点；返回是否已处理。
func (s *Server) serveLFS(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && lfsBatchRe.MatchString(r.URL.Path):
		s.lfsBatch(w, r)
	case r.Method == http.MethodGet && lfsObjectRe.MatchString(r.URL.Path):
		s.lfsObject(w, r, lfsObjectRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodPut && lfsObjectRe.MatchString(r.URL.Path):
		s.lfsUpload(w, r, lfsObjectRe.FindStringSubmatch(r.URL.Path)[1])
	default:
		return false
	}
	return true
}

func (s *Server) lfsBatch(w http.ResponseWriter, r *http.Request) {
	var req lfs.BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		lfs.WriteError(w, http.StatusUnprocessableEntity, "invalid batch request")
		return
	}
	s.mu.Lock()
	origin := s.LFSHrefOrigin
	if origin == "" {
		origin = s.URL()
	}
	echo := s.LFSEchoAuth
	out := lfs.BatchResponse{Transfer: "basic", HashAlgo: "sha256"}
	for _, o := range req.Objects {
		res := lfs.ObjectResult{OID: o.OID, Size: o.Size}
		_, stored := s.lfsObjects[o.OID]
		a := &lfs.Action{Href: origin + "/_lfs/objects/" + o.OID, ExpiresIn: 3600}
		if auth := r.Header.Get("Authorization"); echo && auth != "" {
			a.Header = map[string]string{"Authorization": auth}
		}
		switch {
		case req.Operation == "upload" && !stored:
			res.Actions = map[string]*lfs.Action{"upload": a}
		case req.Operation == "upload":
			// 已存的对象不给 actions（batch.md：无需传输）
		case req.Operation == "download" && stored:
			res.Actions = map[string]*lfs.Action{"download": a}
		default:
			res.Error = &lfs.ObjectError{Code: http.StatusNotFound, Message: "Object does not exist"}
		}
		out.Objects = append(out.Objects, res)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", lfs.MediaType)
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) lfsObject(w http.ResponseWriter, r *http.Request, oid string) {
	s.mu.Lock()
	body, ok := s.lfsObjects[oid]
	stall := s.LFSStall
	s.lfsActive++
	s.lfsPeak = max(s.lfsPeak, s.lfsActive)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.lfsActive--
		s.mu.Unlock()
	}()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if stall {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		return
	}
	_, _ = w.Write(body)
}

// lfsUpload 存下 PUT 的请求体（不校验 sha256：测试用 PutLFS 可以故意放不符的内容，这里同理照收）。
func (s *Server) lfsUpload(w http.ResponseWriter, r *http.Request, oid string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.PutLFS(oid, body)
	w.WriteHeader(http.StatusOK)
}
