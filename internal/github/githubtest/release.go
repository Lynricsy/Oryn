package githubtest

import (
	"fmt"
	"net/http"
	"strconv"
)

// 发布哨兵（M15 步骤 6.1）用到的只读接口：运行的 job、job 日志（与 GitHub 一样先 302 到不带鉴权的下载地址）、
// 附注 tag 对象与按 tag 读 Release。运行列表与 head_sha 过滤见 watch.go，ref 见 refs.go。

// WorkflowJob 是 Actions 运行里的一个 job（GET …/actions/runs/{id}/jobs）；Log 是 GET …/actions/jobs/{id}/logs 的正文。
type WorkflowJob struct {
	ID     int64
	RunID  int64
	RepoID int64
	Name   string
	// Status 缺省 completed。
	Status     string
	Conclusion string
	Steps      []JobStep
	Log        string
}

// JobStep 是 job 的一个步骤。
type JobStep struct {
	Number     int
	Name       string
	Conclusion string
}

// Release 是一个 GitHub Release（GET …/releases/tags/{tag}）。
type Release struct {
	ID      int64
	RepoID  int64
	TagName string
	Name    string
	Draft   bool
}

// AddJob 登记一个 job（ID 缺省自增，Status 缺省 completed）。
func (s *Server) AddJob(j WorkflowJob) *WorkflowJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.ID == 0 {
		s.seq++
		j.ID = 8000 + s.seq
	}
	if j.Status == "" {
		j.Status = "completed"
	}
	p := &j
	s.Jobs = append(s.Jobs, p)
	return p
}

// AddRelease 登记一个 Release（ID 缺省自增）。
func (s *Server) AddRelease(rel Release) *Release {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rel.ID == 0 {
		s.seq++
		rel.ID = 6000 + s.seq
	}
	p := &rel
	s.Releases = append(s.Releases, p)
	return p
}

// AddTagObject 登记一个附注 tag 对象 oid → 它指向的提交；Repo.Refs["tags/<tag>"] 设为 oid 即为附注 tag。
func (s *Server) AddTagObject(oid, commit string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.TagObjects == nil {
		s.TagObjects = map[string]string{}
	}
	s.TagObjects[oid] = commit
}

func (s *Server) releaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{id}/jobs", s.listJobs)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/jobs/{id}/logs", s.jobLogs)
	mux.HandleFunc("GET /_blobs/job-logs/{id}", s.jobLogBlob)
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/tags/{sha}", s.getTagObject)
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/tags/{tag...}", s.releaseByTag)
}

// listJobs 是一次运行的 job（按 id），分页。
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/actions/runs/"+r.PathValue("id")+"/jobs")
	if rp == nil {
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var jobs []*WorkflowJob
	for _, j := range s.Jobs {
		if j.RepoID == rp.ID && j.RunID == runID {
			jobs = append(jobs, j)
		}
	}
	lo, hi := s.paginate(w, r, len(jobs))
	items := []map[string]any{}
	for _, j := range jobs[lo:hi] {
		steps := []map[string]any{}
		for _, st := range j.Steps {
			steps = append(steps, map[string]any{"number": st.Number, "name": st.Name, "status": "completed", "conclusion": nullable(st.Conclusion)})
		}
		items = append(items, map[string]any{
			"id": j.ID, "run_id": j.RunID, "name": j.Name, "status": j.Status, "conclusion": nullable(j.Conclusion), "steps": steps,
			"html_url": fmt.Sprintf("%s/%s/actions/runs/%d/job/%d", s.baseURL(r), rp.FullName, j.RunID, j.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(jobs), "jobs": items})
}

// jobLogs 与 GitHub 一样回 302，Location 是不需要鉴权的下载地址。
func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/actions/jobs/"+r.PathValue("id")+"/logs")
	if rp == nil {
		return
	}
	if s.findJob(rp.ID, r.PathValue("id")) != nil {
		http.Redirect(w, r, s.baseURL(r)+"/_blobs/job-logs/"+r.PathValue("id"), http.StatusFound)
		return
	}
	fail(w, http.StatusNotFound, "Not Found")
}

// jobLogBlob 是日志的下载地址（真实 GitHub 是预签名 URL）。
func (s *Server) jobLogBlob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.Jobs {
		if strconv.FormatInt(j.ID, 10) == r.PathValue("id") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(j.Log))
			return
		}
	}
	fail(w, http.StatusNotFound, "Not Found")
}

// findJob 按仓库与 id 查 job。调用方持锁。
func (s *Server) findJob(repoID int64, id string) *WorkflowJob {
	for _, j := range s.Jobs {
		if j.RepoID == repoID && strconv.FormatInt(j.ID, 10) == id {
			return j
		}
	}
	return nil
}

// getTagObject 回附注 tag 对象（只给出指向的提交）。
func (s *Server) getTagObject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rp := s.readableRepo(w, r, "/git/tags/"+r.PathValue("sha")); rp == nil {
		return
	}
	commit, ok := s.TagObjects[r.PathValue("sha")]
	if !ok {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sha": r.PathValue("sha"), "object": map[string]any{"sha": commit, "type": "commit"},
	})
}

// releaseByTag 按 tag 名回 Release。与 GitHub 一样，草稿 Release 只对有 push 权限的调用者可见，这里一律可见并带 draft。
func (s *Server) releaseByTag(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/releases/tags/"+r.PathValue("tag"))
	if rp == nil {
		return
	}
	for _, rel := range s.Releases {
		if rel.RepoID == rp.ID && rel.TagName == r.PathValue("tag") {
			writeJSON(w, http.StatusOK, map[string]any{
				"id": rel.ID, "tag_name": rel.TagName, "name": nullable(rel.Name), "draft": rel.Draft,
				"html_url": s.baseURL(r) + "/" + rp.FullName + "/releases/tag/" + rel.TagName,
			})
			return
		}
	}
	fail(w, http.StatusNotFound, "Not Found")
}
